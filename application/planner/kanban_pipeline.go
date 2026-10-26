package planner

import (
	"ai/domain"
	"ai/domain/services"
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
		// Watchdog по heartbeat: после «в режиме ожидания» на доске
		// могли остаться задачи «в работе» — их тоже нужно вернуть в очередь.
		if n, err := k.recoverStuckTasks(ctx); err != nil {
			return err
		} else if n > 0 {
			k.status("[Kanban] зависших задач возвращено в очередь: %d", n)
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

	// Бизнес-ревью эпиков: бизнес-аналитик уточняет требования,
	// добавляет User Stories и Acceptance Criteria, переводит в system_analysis.
	{
		p, err := k.phaseBusinessReview(ctx)
		if err != nil {
			return false, err
		}
		progress = progress || p
	}

	// Декомпозиция лидами.
	{
		p, err := k.phaseLeads(ctx)
		if err != nil {
			return false, err
		}
		progress = progress || p
	}

	// Готовность к работе -> затвор HITL «утвердить задачи» (перед
	// раздачей специалистами).
	{
		p, err := k.phaseReady(ctx)
		if err != nil {
			return false, err
		}
		progress = progress || p
		if k.gate != nil && p {
			if err := k.waitReadyTasks(ctx); err != nil {
				return false, err
			}
		}
	}

	// Исполнение специалистами.
	{
		p, err := k.phaseExecute(ctx)
		if err != nil {
			return false, err
		}
		progress = progress || p
	}

	// Финализация эпиков.
	{
		p, err := k.phaseComplete(ctx)
		if err != nil {
			return false, err
		}
		progress = progress || p
	}

	// Ф-3: проход по доске, фиксирующий факт расхода токенов по всем
	// терминальным единицам. Идемпотентен, ошибки учёта не роняют цикл.
	k.finalizeTokens(ctx)

	return progress, nil
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
		meta.Status = domain.StatusDone
		_ = k.store.SaveMeta(ctx, meta)
	}
	k.log.Infof("[доска] задача пользователя решена: все эпики и задачи выполнены")
	return true, nil
}

// waitForWork опрашивает доску в режиме ожидания: возврат, как только на ней
// появляется работа, которую можно взять (или отменён контекст — остановка).
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
// записей: активная задача, эпик, которому нужна декомпозиция или финализация.
func (k *KanbanRunner) hasWork(ctx context.Context) (bool, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return false, err
	}
	for _, t := range tasks {
		switch t.Status {
		case domain.StatusInfra, domain.StatusTestCases, domain.StatusDevelopment:
			ok, err := k.epicWorkable(ctx, t.EpicID)
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
		if e.Status.Terminal() {
			continue
		}
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
	return false, nil
}

// epicWorkable сообщает, можно ли брать в работу задачи эпика: эпик существует,
// не отменён и не выполнен. Отсутствующий эпик — задача может работать
// автономно (нет родителя).
func (k *KanbanRunner) epicWorkable(ctx context.Context, epicID string) (bool, error) {
	if epicID == "" {
		return true, nil
	}
	e, err := k.store.GetEpic(ctx, epicID)
	if err != nil {
		if errors.Is(err, services.ErrNotFound) {
			return true, nil
		}
		return false, err
	}
	switch e.Status {
	case domain.StatusCancelled, domain.StatusDone:
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
	if errors.Is(err, services.ErrNotFound) {
		meta = &domain.Meta{ProjectName: projectName, Task: taskText, Status: domain.StatusDraft}
		if err := k.store.SaveMeta(ctx, meta); err != nil {
			return err
		}
		k.log.Infof("[доска] проект %q создан: %s", projectName, truncateText(taskText, 80))
	}
	if meta.Status != domain.StatusDone {
		meta.Status = domain.StatusSystemAnalysis
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
		if errors.Is(err, services.ErrNotFound) {
			return nil
		}
		return err
	}
	if meta == nil || meta.Status.Terminal() || meta.Status == domain.StatusSystemAnalysis {
		return nil
	}
	meta.Status = domain.StatusSystemAnalysis
	if err := k.store.SaveMeta(ctx, meta); err != nil {
		return err
	}
	k.log.Infof("[доска] meta-задача проекта %q в работе", k.store.Project())
	return nil
}

// staleTaskMinDefault — сколько ждать пульса задачи, прежде чем считать прогон
// брошенным. Heartbeat обновляет агент каждый раунд (Ф-6, этап 2), поэтому
// порог — «заметно дольше одного раунда», а не точное время. Перекрывается
// переменной KANBAN_STALE_TASK_MIN; 0 отключает порог.
const staleTaskMinDefault = 30

func staleTaskAfter() time.Duration {
	if v := strings.TrimSpace(os.Getenv("KANBAN_STALE_TASK_MIN")); v != "" {
		if mins, err := strconv.Atoi(v); err == nil && mins >= 0 {
			return time.Duration(mins) * time.Minute
		}
	}
	return staleTaskMinDefault * time.Minute
}

// recoverStuckTasks возвращает в очередь задачи, зависшие в активной фазе.
// Иначе их никто не исполнит, а незавершённая задача блокирует финализацию
// эпиков — цикл падал бы с «нет прогресса».
func (k *KanbanRunner) recoverStuckTasks(ctx context.Context) (int, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return 0, err
	}
	stale := staleTaskAfter()
	reset := 0
	for _, t := range tasks {
		if t.Status != domain.StatusDevelopment && t.Status != domain.StatusTestCases && t.Status != domain.StatusInfra {
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
				k.log.Detailf("[задача %s] активна, пульс %s назад — не трогаем (живой прогон)",
					t.TaskID, time.Since(age).Truncate(time.Second))
				continue
			default:
				reason = fmt.Sprintf("пульс %s назад", time.Since(age).Truncate(time.Second))
			}
		}
		// Строго линейный FSM — возвратов назад нет. Сбрасываем heartbeat
		// и agent_state, чтобы следующий цикл подхватил задачу.
		if err := k.store.PatchTask(ctx, t.TaskID, func(task *domain.Task) error {
			task.HeartbeatAt = ""
			task.AgentState = ""
			return nil
		}); err != nil {
			k.log.Warnf("[задача %s] очистка heartbeat: %v", t.TaskID, err)
			continue
		}
		k.status("[задача %s] возврат в очередь: «%s», %s", t.TaskID, t.Status.Label(), reason)
		reset++
	}
	return reset, nil
}
