package planner

import (
	"ai/board"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// runBoardOnly — цикл запуска по доске (кнопка «Продолжить»): новых эпиков не
// создаётся (архитектор пропускается), но существующие эпики декомпозируются
// лидами, а задачи исполняются; когда брать в работу нечего — режим ожидания
// до появления работы на доске. Бюджет раундов ограничивает продуктивную
// работу, но не ожидание: после простоя счётчик обнуляется, поэтому «ждать
// работу» можно бесконечно, а «крутиться без результата» — нет.
func (k *KanbanRunner) runBoardOnly(ctx context.Context) error {
	round := 0
	for {
		round++
		if round > maxKanbanRounds {
			return fmt.Errorf("исчерпан бюджет Kanban-раундов (%d), работа по доске не завершена, доска: %s",
				maxKanbanRounds, k.boardSummary(ctx))
		}
		// Watchdog по heartbeat (Ф-6, 4.1): после «в режиме ожидания» на доске
		// могли остаться задачи «в работе» — их тоже нужно вернуть в очередь.
		if n, err := k.recoverStuckTasks(ctx); err != nil {
			return err
		} else if n > 0 {
			k.status("[Kanban] зависших «в работе» задач возвращено в очередь: %d", n)
		}
		progress, err := k.runPhases(ctx)
		if err != nil {
			return err
		}
		if progress {
			continue
		}

		k.log.Infof("[Kanban] нет эпиков/задач, которые можно взять в работу — режим ожидания (доска: %s)",
			k.boardSummary(ctx))
		k.setStandby(true)
		if err := k.waitForWork(ctx); err != nil {
			return err
		}
		k.setStandby(false)
		k.log.Infof("[Kanban] на доске появилась работа — продолжаю")
		round = 0
	}
}

// runPhases исполняет один Kanban-цикл и сообщает, была ли сделана работа.
// В board-only режиме пропускается только фаза архитектора (новые эпики не
// создаются); эпики, уже лежащие на доске, декомпозируются лидами в задачи и
// исполняются — иначе эпик без задач никогда бы не был взят в работу.
func (k *KanbanRunner) runPhases(ctx context.Context) (bool, error) {
	progress := false

	if !k.boardOnly {
		// Архитектура: эпики -> затвор HITL «утвердить эпики» (перед
		// декомпозицией лидами). Затвор срабатывает только в раунде, где
		// архитектор что-то опубликовал (phaseArchitect вернул true) — в
		// последующих раундах эпики уже есть, и повторного подтверждения
		// не требуется.
		p, err := k.phaseArchitect(ctx)
		if err != nil {
			return false, err
		}
		progress = progress || p
		if k.gate != nil && p {
			if err := k.waitEpics(ctx); err != nil {
				return false, err
			}
		}
	}

	// Ревизия эпиков-черновиков (Ф-8): эпики с RequiresReview=true проверяются
	// Системным архитектором в режиме ревизии ПЕРЕД декомпозицией лидами.
	// Работает в обоих режимах (обычном и board-only): чат-ассистент создаёт
	// черновики на доске в любом из них. Фаза снимает флаг RequiresReview —
	// и только после этого лид может декомпозировать эпик.
	p, err := k.phaseArchitectReview(ctx)
	if err != nil {
		return false, err
	}
	progress = progress || p

	// Декомпозиция лидами: в board-only обрабатываются эпики, уже лежащие на
	// доске (созданные чатом/вручную, но ещё без задач), а также их ревизия.
	p, err = k.phaseLeads(ctx)
	if err != nil {
		return false, err
	}
	progress = progress || p

	// Готовность к работе -> затвор HITL «утвердить задачи» (перед
	// раздачей специалистам). Срабатывает в раунде, где появились новые
	// готовые задачи; уже подтверждённые потоки в следующих раундах
	// исполняются без повторного подтверждения.
	p, err = k.phaseReady(ctx)
	if err != nil {
		return false, err
	}
	progress = progress || p
	if k.gate != nil && p {
		if err := k.waitReadyTasks(ctx); err != nil {
			return false, err
		}
	}

	// Исполнение специалистами.
	p, err = k.phaseExecute(ctx)
	if err != nil {
		return false, err
	}
	progress = progress || p

	// Фаза «На тестирование»: задачи в testing доводит тестировщик/специалист.
	p, err = k.phaseTesting(ctx)
	if err != nil {
		return false, err
	}
	progress = progress || p

	// Багрепорты и финализация эпиков.
	p, err = k.phaseBugs(ctx)
	if err != nil {
		return false, err
	}
	progress = progress || p

	p, err = k.phaseComplete(ctx)
	if err != nil {
		return false, err
	}

	// Ф-3: проход по доске, фиксирующий факт расхода токенов по всем
	// терминальным единицам. Основные точки фиксации — конец phaseExecute и
	// phaseComplete; проход закрывает переходы, сделанные помимо оркестратора
	// (специалист, чат-ассистент, человек в Web UI). Идемпотентен, ошибки учёта
	// не роняют цикл.
	k.finalizeTokens(ctx)

	return progress || p, nil
}

// finishIfAllDone завершает оркестрацию, если все эпики и задачи выполнены:
// meta-задача проекта переводится в «выполнена». Возвращает true, когда задача
// пользователя решена.
func (k *KanbanRunner) finishIfAllDone(ctx context.Context) (bool, error) {
	done, err := k.store.AllDone(ctx)
	if err != nil {
		return false, err
	}
	if !done {
		return false, nil
	}
	if meta, gerr := k.store.GetMeta(ctx); gerr == nil && meta != nil {
		meta.Status = board.StatusDone
		_ = k.store.SaveMeta(ctx, meta)
	}
	k.log.Infof("[доска] задача пользователя решена: все эпики и задачи выполнены")
	return true, nil
}

// waitForWork опрашивает доску в режиме ожидания: возврат, как только на ней
// появляется работа, которую можно взять (или отменён контекст — остановка).
// Опрос раз в standbyPoll дополнен событийным Wake: сервер будит runner по
// board_changed, так что новая работа подхватывается сразу, а не с задержкой.
func (k *KanbanRunner) waitForWork(ctx context.Context) error {
	ticker := time.NewTicker(standbyPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-k.wake:
			ok, err := k.hasWork(ctx)
			if err != nil {
				return err
			}
			if ok {
				return nil
			}
		case <-ticker.C:
			ok, err := k.hasWork(ctx)
			if err != nil {
				return err
			}
			if ok {
				return nil
			}
		}
	}
}

// hasWork сообщает, есть ли на доске что взять в работу БЕЗ создания новых
// записей: готовая/работающая задача, задача, которую phaseReady способен
// продвинуть (зависимости и фаза-гейт пройдены), эпик, которому нужна
// декомпозиция/ревизия лида или который готов к финализации, либо багрепорт,
// требующий триажа/экспертизы.
func (k *KanbanRunner) hasWork(ctx context.Context) (bool, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return false, err
	}
	for _, t := range tasks {
		// «Помощь человека» и «на тестирование» работой не считаются:
		// human_help ждёт ручного выхода, testing возьмёт phaseTesting
		// (этап 7 плана) — пока фазы нет, такие статусы не должны ни
		// будить runner, ни давать «прогресс» циклу (иначе — кручение
		// без результата).
		switch t.Status {
		case board.StatusReady, board.StatusInProgress:
			ok, err := k.epicWorkable(ctx, t.EpicID)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		case board.StatusNew, board.StatusAnalysis:
			ok, err := k.epicWorkable(ctx, t.EpicID)
			if err != nil {
				return false, err
			}
			if !ok {
				continue
			}
			ok, err = k.depsDone(ctx, t)
			if err != nil {
				return false, err
			}
			if !ok {
				continue
			}
			ok, err = k.phasePrereqSatisfied(ctx, t)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
	}

	epics, err := k.store.ListEpics(ctx)
	if err != nil {
		return false, err
	}
	for _, e := range epics {
		if e.Status.Terminal() || e.Status == board.StatusHumanHelp {
			continue
		}
		// Эпик без задач (или с необработанной ревизией) — работа для лида:
		// в board-only он декомпозируется, после чего задачи пойдут в исполнение.
		if len(e.Tasks) == 0 || e.Revision > e.LeadSyncedRev {
			return true, nil
		}
		ok, err := k.epicTasksDone(ctx, e)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}

	bugs, err := k.store.ListBugReports(ctx)
	if err != nil {
		return false, err
	}
	for _, b := range bugs {
		if b.Status == board.BugStatusNew || b.Status == board.BugStatusConfirmed {
			return true, nil
		}
	}
	return false, nil
}

// epicWorkable сообщает, можно ли брать в работу задачи эпика: эпик существует
// и не находится на «помощи человека»/в отмене/выполнен. Остановленные и
// отменённые эпики «замораживают» свои задачи — оркестратор их не трогает
// (код/ветки остаются на месте, позже можно возобновить). Отсутствующий эпик —
// «нет работы» (задача-сирота без родителя исполняться не должна).
func (k *KanbanRunner) epicWorkable(ctx context.Context, epicID string) (bool, error) {
	if epicID == "" {
		return false, nil
	}
	e, err := k.store.GetEpic(ctx, epicID)
	if err != nil {
		if errors.Is(err, board.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	switch e.Status {
	case board.StatusHumanHelp, board.StatusCancelled, board.StatusDone:
		return false, nil
	}
	return true, nil
}

// setStandby сообщает серверу о входе (true) / выходе (false) из режима
// ожидания. Нотификатор может быть не установлен (консольный режим, тесты).
func (k *KanbanRunner) setStandby(v bool) {
	if k.onStandby != nil {
		k.onStandby(v)
	}
}

// ensureMeta создаёт метаданные доски (исходная задача пользователя), если их
// ещё нет, и переводит статус решения в «в работе».
func (k *KanbanRunner) ensureMeta(ctx context.Context, projectName, taskText string) error {
	meta, err := k.store.GetMeta(ctx)
	if errors.Is(err, board.ErrNotFound) {
		meta = &board.Meta{ProjectName: projectName, Task: taskText, Status: board.StatusNew}
		if err := k.store.SaveMeta(ctx, meta); err != nil {
			return err
		}
		k.log.Infof("[доска] проект %q создан: %s", projectName, truncateText(taskText, 80))
	}
	if meta.Status != board.StatusDone {
		meta.Status = board.StatusInProgress
		return k.store.SaveMeta(ctx, meta)
	}
	return nil
}

// touchMeta — meta-задача для board-only режима: существующая запись
// переводится в «в работе», новая НЕ создаётся (запуск по кнопке работает
// только с тем, что уже лежит на доске).
func (k *KanbanRunner) touchMeta(ctx context.Context) error {
	meta, err := k.store.GetMeta(ctx)
	if err != nil {
		if errors.Is(err, board.ErrNotFound) {
			return nil
		}
		return err
	}
	if meta == nil || meta.Status.Terminal() || meta.Status == board.StatusInProgress {
		return nil
	}
	meta.Status = board.StatusInProgress
	if err := k.store.SaveMeta(ctx, meta); err != nil {
		return err
	}
	k.log.Infof("[доска] meta-задача проекта %q в работе", k.store.Project())
	return nil
}

// pipelineIdle сообщает, что на доске нет незавершённых задач (все предыдущие
// декомпозиции выполнены, отменены или остановлены). Только в этом состоянии
// лиду выдаётся следующая декомпозиция: сначала выполняется уже запланированный
// объём работы. Задачи на «помощи человека» (эпик остановлен) не блокируют
// очередь — остановка эпика не должна замораживать декомпозицию остальных
// эпиков. «На тестирование» — активная работа: конвейер лидов пока не свободен.
func (k *KanbanRunner) pipelineIdle(ctx context.Context) (bool, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return false, err
	}
	for _, t := range tasks {
		switch t.Status {
		case board.StatusDone, board.StatusCancelled, board.StatusHumanHelp:
			continue
		default:
			return false, nil
		}
	}
	return true, nil
}

// staleTaskMinDefault — сколько ждать пульса задачи, прежде чем считать прогон
// брошенным. Heartbeat обновляет агент каждый раунд (Ф-6, этап 2), поэтому
// порог — «заметно дольше одного раунда», а не точное время. Перекрывается
// переменной KANBAN_STALE_TASK_MIN; 0 отключает порог (тогда брошенной
// считается любая задача вне active).
const staleTaskMinDefault = 30

func staleTaskAfter() time.Duration {
	if v := strings.TrimSpace(os.Getenv("KANBAN_STALE_TASK_MIN")); v != "" {
		if mins, err := strconv.Atoi(v); err == nil && mins >= 0 {
			return time.Duration(mins) * time.Minute
		}
	}
	return staleTaskMinDefault * time.Minute
}

// recoverStuckTasks возвращает в «готова к работе» задачи, зависшие «в работе».
// Иначе их никто не исполнит (phaseExecute берёт только готовые), а
// незавершённая задача блокирует и pipeline (очередь лидов), и финализацию
// эпиков — цикл падал бы с «нет прогресса».
//
// Решение принимается по двум сигналам, а не «сбросить всё подряд» (Ф-6,
// этап 4.1):
//
//   - задача, которую выполняет ЭТОТ раннер (k.active), жива по определению;
//   - задача со СВЕЖИМ heartbeat принадлежит живому прогону (другой процесс
//     оркестрации, ещё работающий агент) — её не трогаем. Прежний код
//     полагался на «в этом раунде своих in_progress ещё нет», из-за чего два
//     запуска одного проекта мешали друг другу.
//
// Всё остальное — брошено: пульс старше порога либо его нет вовсе (задача
// старше State Tracking или агент не отчитывался). Переход «в работе» →
// «готова к работе» теперь разрешён конечным автоматом (см. board/entity.go),
// поэтому статус идёт через SetTaskStatus, а не в обход валидации.
//
// «Помощь человека» и «на тестирование» сюда НЕ попадают: human_help снимается
// только вручную (иначе авто-цикл заменил бы собой ручной разбор), testing
// ждёт тестировщика.
func (k *KanbanRunner) recoverStuckTasks(ctx context.Context) (int, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return 0, err
	}
	stale := staleTaskAfter()
	reset := 0
	for _, t := range tasks {
		if t.Status != board.StatusInProgress {
			continue
		}
		if k.isActive(t.TaskID) {
			continue
		}
		reason := "пульса не было"
		if hb := strings.TrimSpace(t.HeartbeatAt); hb != "" {
			age, err := time.Parse(time.RFC3339, hb)
			switch {
			case err != nil:
				reason = fmt.Sprintf("нечитаемый пульс %q", hb)
			case stale > 0 && time.Since(age) < stale:
				k.log.Detailf("[задача %s] «в работе», пульс %s назад — не трогаем (живой прогон)",
					t.TaskID, time.Since(age).Truncate(time.Second))
				continue
			default:
				reason = fmt.Sprintf("пульс %s назад", time.Since(age).Truncate(time.Second))
			}
		}
		if err := k.store.SetTaskStatus(ctx, t.TaskID, board.StatusReady); err != nil {
			return 0, fmt.Errorf("задача %s: возврат «в работе» → «готова к работе»: %w", t.TaskID, err)
		}
		k.status("[задача %s] возврат в очередь: была «в работе», но %s", t.TaskID, reason)
		reset++
	}
	return reset, nil
}
