package planner

import (
	"ai/agents"
	"ai/agents/architect"
	"ai/agents/backendlead"
	"ai/agents/developer"
	"ai/agents/devops"
	"ai/agents/devopslead"
	"ai/agents/frontendlead"
	"ai/agents/qaengineer"
	"ai/agents/qalead"
	"ai/board"
	"ai/logging"
	"ai/models"
	"ai/tools"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// maxKanbanRounds — максимум Kanban-циклов за один запуск. Цикл выполняет один
// этап: архитектура → декомпозиция лидом → раздача задач специалистам →
// завершение эпиков. Пока задача не решена (все эпики и задачи = done),
// циклы повторяются. Лимит защищает от зацикливания при отменённых записях.
const maxKanbanRounds = 100

// KanbanRunner реализует организационный паттерн «Kanban / Shared Task Board»:
// задача пользователя → эпики (Системный архитектор) → декомпозиция лидами →
// выполнение специалистами → все эпики и задачи выполнены (AllDone) = задача
// решена. Общая доска (эпики, задачи, статусы) хранится в Redis.
//
// Статусы эпиков и задач единые: новая -> в анализе -> готова к работе ->
// в работе -> выполнена (или отменена). Переходы валидирует board.ValidateTransition.
//
// Лиды направлений (frontendlead/backendlead/devopslead/qalead) НЕ пишут код и
// НЕ запускают консольные команды: они исследуют существующий код (List/ReadFiles),
// декомпозируют эпик на задачи для подчинённых (publication через общую доску
// BoardCreateTask/BoardUpdateTask/BoardDeleteTask) и ревизуют задачи при
// изменении содержимого эпика.
type KanbanRunner struct {
	provider models.LLMProvider
	store    *board.Store
	gate     HumanGate
	// boardOnly — режим запуска по доске (кнопка «Продолжить»): новая работа
	// НЕ придумывается (фаза архитектора пропускается — новых эпиков нет), в
	// работу берутся только записи, которые уже есть на доске. Эпики без задач
	// декомпозируются лидами в задачи и исполняются.
	boardOnly bool
	// onStandby — нотификатор режима ожидания: true — работы на доске нет
	// (ожидание), false — работа появилась (цикл возобновлён). Устанавливается
	// сервером для трансляции статуса сессии; nil в консольном режиме.
	onStandby func(bool)
	// outputDir — переключатель рабочей директории специалиста (Ф-3): функция
	// (project, taskID) → каталог работы. Для git-проектов это постоянный
	// worktree ветки задачи (создаётся серверным хуком на статусе in_progress),
	// и правки агента падают в ВЕТКУ ЗАДАЧИ — авто-коммит на done соберёт именно
	// их. nil — общая проектная копия temp/<проект>.
	outputDir func(project, taskID string) string
	// wake — канал событийного побуждения из standby (см. Wake): буфер 1
	// поглощает сигналы, когда runner не ждёт работу.
	wake chan struct{}
	// log — лог проекта (logs/<проект>.log). Устанавливается в Run, когда имя
	// проекта известно; до этого nil, и сообщения идут в файл по умолчанию
	// (нулевой получатель logging.Logger допустим — проверки не нужны).
	log *logging.Logger
	// rag — клиент векторной памяти для фаз архитектора (Ф-1/Ф-4): CodeSearch/
	// RagIndexStatus и блок «релевантный код» в промпте. nil — архитектор
	// работает без RAG (degrade, как раньше).
	rag tools.RAGSearcher
	// architectExtras — серверные мосты-инструменты для архитектора (Ф-4/Р-5):
	// например AskUser для уточняющего вопроса до публикации бэклога.
	// nil/пустой — автономный режим (консоль), архитектор работает без них.
	architectExtras []tools.Tool
}

// standbyPoll — период опроса доски в режиме ожидания: раз в 5 с runner
// проверяет, не появились ли эпики/задачи, которые можно взять в работу.
const standbyPoll = 5 * time.Second

// GateDecision — решение человека по HITL-затвору.
type GateDecision struct {
	// Approved — подтверждено. false означает «переделать»: публикация
	// отменяется, доска очищается, фаза запускается заново.
	Approved bool
	// Reason — комментарий человека (причина отклонения / правки).
	Reason string
}

// HumanGate — интерфейс подтверждения человеком. Устанавливается сервером
// Web UI; в консольном режиме (nil) KanbanRunner работает полностью
// автономно, как раньше. Затворы БЛОКИРУЮТ runner до решения человека —
// пока канал ожидания открыт, пользователь правит доску (редактирование,
// статусы, удаление), а runner при продолжении перечитает её заново.
type HumanGate interface {
	// Epics — подтверждение эпиков, только что опубликованных Системным
	// архитектором, перед декомпозицией лидами.
	Epics(ctx context.Context, epics []*board.Epic) (GateDecision, error)
	// Tasks — подтверждение задач, готовых к работе, перед раздачей
	// специалистам.
	Tasks(ctx context.Context, tasks []*board.Task) (GateDecision, error)
}

// SetGate устанавливает HITL-затвор. nil возвращает автономный режим.
func (k *KanbanRunner) SetGate(g HumanGate) { k.gate = g }

// SetBoardOnly включает/выключает режим «только доска»: новые эпики не
// создаются, а берётся в работу то, что уже есть на доске (эпики без задач
// декомпозируются лидами); при отсутствии работы — режим ожидания (см.
// SetStandbyNotifier).
func (k *KanbanRunner) SetBoardOnly(v bool) { k.boardOnly = v }

// SetStandbyNotifier задаёт нотификатор режима ожидания: fn(true) — работы на
// доске нет (ожидание), fn(false) — работа появилась (цикл возобновлён).
func (k *KanbanRunner) SetStandbyNotifier(fn func(bool)) { k.onStandby = fn }

// SetOutputDir задаёт функцию выбора рабочей директории специалиста по
// (project, taskID) (Ф-3): worktree ветки задачи для git, temp/<проект> —
// стандартно. nil возвращает поведение по умолчанию.
func (k *KanbanRunner) SetOutputDir(fn func(project, taskID string) string) { k.outputDir = fn }

// SetRAG подключает клиент векторной памяти к фазам архитектора (Ф-1/Ф-6):
// CodeSearch/RagIndexStatus и блок «релевантный код» в промпте начинают
// работать по индексу. nil — архитектор работает без RAG (degrade). Клиент
// ленивый и nil-safe: недоступный Qdrant/эмбеддинги не роняют фазу.
func (k *KanbanRunner) SetRAG(r tools.RAGSearcher) *KanbanRunner {
	k.rag = r
	return k
}

// SetArchitectExtras дополняет набор инструментов архитектора мостами вне
// общего реестра (Ф-4/Р-5) — например серверным AskUser для уточняющего
// вопроса до публикации бэклога. nil/пустой список возвращает автономный
// режим (консоль). Применяется в фазе архитектора, ревизии черновиков и
// экспертизы багрепортов.
func (k *KanbanRunner) SetArchitectExtras(extra ...tools.Tool) *KanbanRunner {
	k.architectExtras = append([]tools.Tool{}, extra...)
	return k
}

// prepareArchitect применяет к архитектору RAG и серверные extras (Ф-4):
// CodeSearch/RagIndexStatus начинают искать по индексу, мост AskUser
// добавляется в набор инструментов. Повторный вызов безопасен (Set.Add не
// дублирует уже присутствующие имена, SetRAG пересобирает набор из реестра).
func (k *KanbanRunner) prepareArchitect(a *architect.Architect) *architect.Architect {
	if k.rag != nil {
		a.SetRAG(k.rag)
	}
	if len(k.architectExtras) > 0 {
		a.Tools.Add(k.architectExtras...)
	}
	return a
}

// Wake побуждает runner, ожидающий работу на доске (standby), немедленно
// перепроверить её (Ф-2, PLAN-dashboard-events) — вместо ожидания следующего
// 5-секундного тика опроса. Сервер зовёт его по событию board_changed из
// другого процесса-компонента (REST-правка доски, созданный чатом эпик).
// Безопасен из любых горутин; вне ожидания — no-op (буфер 1 поглощает сигнал).
func (k *KanbanRunner) Wake() {
	select {
	case k.wake <- struct{}{}:
	default:
	}
}

// NewKanbanRunner создаёт Kanban-оркестратор поверх хранилища доски.
func NewKanbanRunner(provider models.LLMProvider, store *board.Store) *KanbanRunner {
	return &KanbanRunner{provider: provider, store: store, wake: make(chan struct{}, 1)}
}

// waitEpics — HITL-затвор «утвердить эпики»: блокирует до решения человека.
// При отклонении эпики (и их задачи) удаляются — следующая итерация
// перезапустит архитектора.
func (k *KanbanRunner) waitEpics(ctx context.Context) error {
	epics, err := k.store.ListEpics(ctx)
	if err != nil {
		return err
	}
	if len(epics) == 0 {
		return nil
	}
	dec, err := k.gate.Epics(ctx, epics)
	if err != nil {
		return fmt.Errorf("затвор «утвердить эпики»: %w", err)
	}
	if dec.Approved {
		k.log.Infof("[HITL] эпики утверждены (%d): %s", len(epics), epicList(epics))
		return nil
	}
	k.log.Infof("[HITL] эпики отклонены (%s), переделываем", truncateText(dec.Reason, 120))
	return k.rework(ctx, epics)
}

// waitReadyTasks — HITL-затвор «утвердить задачи»: блокирует до решения
// человека. При отклонении задачи не исполняются: эпики, в которых они
// живут, удаляются (каскадом), и весь путь повторяется.
func (k *KanbanRunner) waitReadyTasks(ctx context.Context) error {
	ready, err := k.readyTasks(ctx)
	if err != nil {
		return err
	}
	if len(ready) == 0 {
		return nil
	}
	dec, err := k.gate.Tasks(ctx, ready)
	if err != nil {
		return fmt.Errorf("затвор «утвердить задачи»: %w", err)
	}
	if dec.Approved {
		k.log.Infof("[HITL] задачи утверждены (%d)", len(ready))
		return nil
	}
	k.log.Infof("[HITL] задачи отклонены (%s), переделываем эпики", truncateText(dec.Reason, 120))
	epicIDs := map[string]struct{}{}
	for _, t := range ready {
		epicIDs[t.EpicID] = struct{}{}
	}
	var epics []*board.Epic
	for id := range epicIDs {
		e, err := k.store.GetEpic(ctx, id)
		if err != nil {
			if errors.Is(err, board.ErrNotFound) {
				continue
			}
			return err
		}
		epics = append(epics, e)
	}
	return k.rework(ctx, epics)
}

// rework удаляет эпики вместе с их задачами (каскадом). Эпики, чьи задачи
// уже взяты в работу, удалить нельзя — вернётся первая такая ошибка, чтобы
// человек вмешался вручную.
func (k *KanbanRunner) rework(ctx context.Context, epics []*board.Epic) error {
	var firstErr error
	deleted := 0
	for _, e := range epics {
		if err := k.store.DeleteEpic(ctx, e.TaskID); err != nil {
			if errors.Is(err, board.ErrNotFound) {
				continue
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("переделка: эпик %s: %w", e.TaskID, err)
			}
			continue
		}
		deleted++
	}
	k.log.Infof("[HITL] переделка: удалено эпиков: %d, проблем: %v", deleted, firstErr)
	return firstErr
}

// Run исполняет Kanban-оркестрацию до решения задачи пользователя.
// В board-only режиме (SetBoardOnly) новые эпики не создаются: цикл берёт в
// работу только записи, которые уже лежат на доске (эпики без задач
// декомпозируются лидами), а при отсутствии работы переходит в режим ожидания
// (см. runBoardOnly).
func (k *KanbanRunner) Run(ctx context.Context, projectName, taskText string) error {
	// Все сообщения ранера — в лог своего проекта: в режиме serve один процесс
	// ведёт несколько проектов, и общий файл смешал бы их строки.
	k.log = logging.For(projectName)

	if k.boardOnly {
		// Запуск по доске: новых записей не создаём (в т.ч. meta), существующую
		// meta-задачу держим «в работе».
		if err := k.touchMeta(ctx); err != nil {
			return err
		}
	} else if err := k.ensureMeta(ctx, projectName, taskText); err != nil {
		return err
	}

	// Задачи, зависшие «в работе» после остановленной/прерванной сессии,
	// возвращаем в «готова к работе». Иначе их никто не исполняет (phaseExecute
	// берёт только готовые), а незавершённая задача блокирует и pipeline
	// (phaseLeads: очередь лидов не продвигается), и финализацию эпиков
	// (phaseComplete) — цикл падал бы с «нет прогресса». В этом раунде других
	// in_progress-задач ещё нет, поэтому сброс безопасен.
	if n, err := k.resetStaleInProgress(ctx); err != nil {
		return err
	} else if n > 0 {
		k.log.Infof("[Kanban] зависших «в работе» задач сброшено в «готова к работе»: %d", n)
	}

	if k.boardOnly {
		return k.runBoardOnly(ctx)
	}

	for round := 1; round <= maxKanbanRounds; round++ {
		// Задача решена: все эпики и все задачи успешно выполнены.
		done, err := k.finishIfAllDone(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}

		progress, err := k.runPhases(ctx)
		if err != nil {
			return err
		}

		// Ни один этап цикла не сделал работу: на доске остались только
		// отменённые записи или неразрешимые зависимости — дальше бессмысленно.
		if !progress {
			return fmt.Errorf("Kanban-цикл %d: нет прогресса (остались отменённые/заблокированные эпики и задачи), доска: %s",
				round, k.boardSummary(ctx))
		}
	}

	return fmt.Errorf("исчерпан бюджет Kanban-раундов (%d), задача не решена", maxKanbanRounds)
}

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
		if e.Status.Terminal() || e.Status == board.StatusPaused {
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
// и не находится на паузе/в отмене/выполнен. Приостановленные и отменённые
// эпики «замораживают» свои задачи — оркестратор их не трогает (код/ветки
// остаются на месте, позже можно возобновить). Отсутствующий эпик — «нет
// работы» (задача-сирота без родителя исполняться не должна).
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
	case board.StatusPaused, board.StatusCancelled, board.StatusDone:
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

// phaseArchitect: если эпиков на доске ещё нет — запускает Системного
// архитектора. Эпики публикуются его вызовом submit_architecture_backlog.
func (k *KanbanRunner) phaseArchitect(ctx context.Context) (bool, error) {
	epics, err := k.store.ListEpics(ctx)
	if err != nil {
		return false, err
	}
	if len(epics) > 0 {
		return false, nil
	}

	meta, err := k.store.GetMeta(ctx)
	if err != nil {
		return false, err
	}

	arch := architect.NewArchitectWithStore(k.store.Project(), meta.Task, k.store)
	arch = k.prepareArchitect(arch)
	resp, err := k.provider.Generate(ctx, arch)
	if err != nil {
		return false, fmt.Errorf("фаза архитектора: %w", err)
	}
	if resp != nil && resp.Truncated {
		return false, fmt.Errorf("фаза архитектора: цикл остановлен по лимиту раундов")
	}

	epics, err = k.store.ListEpics(ctx)
	if err != nil {
		return false, err
	}
	if len(epics) == 0 {
		return false, fmt.Errorf("фаза архитектора: модель не опубликовала эпики (submit_architecture_backlog не вызван)")
	}
	k.log.Infof("[Системный архитектор] опубликованы эпики: %s", epicList(epics))
	return true, nil
}

// phaseArchitectReview — Ф-8: ревизия эпиков-черновиков (requires_review=true)
// Системным архитектором в режиме ревизора. Черновики, созданные чатом/вручную,
// декомпозиции лидами не подлежат (nextLeadEpic их пропускает), поэтому именно
// архитектор приводит их к стандарту бэклога и снимает флаг — только после этого
// лид получает эпик. Если на доске черновиков нет — фаза простаивает.
// Работает в обоих режимах (обычном и board-only): в board-only чат уже мог
// наложить на доску эпики, требующие ревизии.
func (k *KanbanRunner) phaseArchitectReview(ctx context.Context) (bool, error) {
	epics, err := k.store.ListEpics(ctx)
	if err != nil {
		return false, err
	}
	var drafts []*board.Epic
	for _, epic := range epics {
		// Черновики не трогаем только в терминальных/приостановленных состояниях
		// (пауза замораживает ревизию, Ф-8): в остальных состояниях ревизия
		// обязательна, пока флаг не снят.
		if !epic.RequiresReview || epic.Status.Terminal() || epic.Status == board.StatusPaused {
			continue
		}
		drafts = append(drafts, epic)
	}
	if len(drafts) == 0 {
		return false, nil
	}

	reviewer := architect.NewArchitectWithStore(k.store.Project(), k.epicReviewPrompt(k.store.Project(), drafts), k.store).AsReviewer()
	reviewer = k.prepareArchitect(reviewer)
	resp, err := k.provider.Generate(ctx, reviewer)
	if err != nil {
		return false, fmt.Errorf("фаза ревизии эпиков: %w", err)
	}
	if resp != nil && resp.Truncated {
		return false, fmt.Errorf("фаза ревизии эпиков: цикл остановлен по лимиту раундов")
	}
	k.log.Infof("[Системный архитектор] ревизия %d эпиков-черновиков", len(drafts))

	// Ревизия успешна: снимаем флаг и синхронизируем ревизию лида с ревизией
	// эпика, чтобы лид сразу принял утверждённый эпик к декомпозиции без
	// повторной «ресинхронизации» (LeadSyncedRev == Revision).
	for _, d := range drafts {
		d.RequiresReview = false
		d.LeadSyncedRev = d.Revision
		if err := k.store.SaveEpic(ctx, d); err != nil {
			return false, fmt.Errorf("эпик %s: снятие ревизии: %w", d.TaskID, err)
		}
	}
	return true, nil
}

// epicReviewPrompt формирует задание ревизии эпиков-черновиков (Ф-8): архитектор
// приводит каждый черновик к стандарту бэклога (ТЗ, стек, роли, глубина
// декомпозиции, смежные модули).
func (k *KanbanRunner) epicReviewPrompt(project string, drafts []*board.Epic) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Проведи ревизию %d эпиков-черновиков на доске проекта %q. Это черновики, созданные чат-ассистентом или вручную: каждому требуется твоя экспертиза перед декомпозицией лидами.\n\n", len(drafts), project)
	for _, epic := range drafts {
		fmt.Fprintf(&b, "- %s «%s» (статус %s, ревизия %d): %s\n",
			epic.TaskID, truncateText(epic.Title, 80), epic.Status.Label(), epic.Revision, truncateText(epic.Description, 400))
	}
	b.WriteString("\nДля каждого эпика: проверь корректность ТЗ, соответствие фактическому стеку проекта (DetectStack), глубину декомпозиции и затронутые смежные модули; скорректируй эпик инструментами доски (BoardUpdateEpic и др.).")
	return b.String()
}

// phaseLeads: декомпозиция и ревизия эпиков лидами направлений. Работа идёт
// по очереди (сериализация): лид декомпозирует следующий эпик только после
// завершения текущей волны задач — лиды не проектируют всё подряд на пустой
// проект. Порядок очереди: инфраструктура (DevOps) -> приложение
// (backend/frontend) -> тестирование (QA Last); внутри фазы — sequence_order,
// затем task_id. Лид работает инструментами доски (BoardCreateTask и др.);
// при недоступности доски — JSON-декомпозицией (fallback). После ревизии
// LeadSyncedRev синхронизируется с ревизией эпика (пересмотр задач при
// изменении контрактов архитектором, Revision > LeadSyncedRev).
func (k *KanbanRunner) phaseLeads(ctx context.Context) (bool, error) {
	epics, err := k.store.ListEpics(ctx)
	if err != nil {
		return false, err
	}

	// Пока на доске есть незавершённые задачи (новая / в анализе / готова к
	// работе / в работе), следующий эпик лиду не выдаётся: сначала должен
	// выполниться уже запланированный объём работы.
	idle, err := k.pipelineIdle(ctx)
	if err != nil {
		return false, err
	}
	if !idle {
		return false, nil
	}

	// Следующий эпик очереди лидов, которому нужна декомпозиция/ревизия.
	epic, needDecompose, needResync, err := k.nextLeadEpic(epics)
	if err != nil {
		return false, err
	}
	if epic == nil {
		return false, nil
	}

	lead, err := k.leadFor(epic)
	if err != nil {
		return false, fmt.Errorf("фаза лидов, эпик %s: %w", epic.TaskID, err)
	}
	if sb, ok := lead.(interface{ SetBoardStore(*board.Store) }); ok {
		sb.SetBoardStore(k.store)
	}
	// Декомпозиция/ревизия всегда требует публикации задач на доске:
	// лид обязан создать/обновить/удалить задачи для подчинённых.
	if tp, ok := lead.(interface{ SetTaskPublishing(bool) }); ok {
		tp.SetTaskPublishing(true)
	}

	// «В анализе»: для новой декомпозиции обязателен; при ревизии эпик может
	// находиться дальше по цепочке — некритично (переход в анализ не требуется).
	if err := k.store.SetEpicStatus(ctx, epic.TaskID, board.StatusAnalysis); err != nil {
		if _, ok := err.(*board.StatusError); !ok {
			return false, fmt.Errorf("эпик %s: перевод в «в анализе»: %w", epic.TaskID, err)
		}
	}
	k.log.Infof("[%s] декомпозиция/ревизия эпика %s (%s) (нужна ревизия: %v)",
		leadName(epic), epic.TaskID, truncateText(epic.Title, 60), needResync)

	resp, err := k.provider.Generate(ctx, lead)
	if err != nil {
		return false, fmt.Errorf("декомпозиция эпика %s: %w", epic.TaskID, err)
	}
	if resp != nil && resp.Truncated {
		// Лимит раундов агентского цикла не должен ронять весь запуск, если
		// лид уже опубликовал на доске частичную (но рабочую) декомпозицию:
		// продолжаем, задачи передадутся специалистам после затвора.
		tasks, terr := k.store.TasksByEpic(ctx, epic.TaskID)
		if terr != nil {
			return false, fmt.Errorf("декомпозиция эпика %s: цикл остановлен по лимиту раундов: %w", epic.TaskID, terr)
		}
		if len(tasks) == 0 {
			return false, fmt.Errorf("декомпозиция эпика %s: цикл остановлен по лимиту раундов (задачи не созданы)", epic.TaskID)
		}
		k.log.Infof("[эпик %s] лимит раундов лида, но опубликовано задач: %d — продолжаем с частичной декомпозицией", epic.TaskID, len(tasks))
	}

	// Инструментный путь: лид мог создать/изменить задачи прямо на доске
	// (BoardCreateTask/BoardUpdateTask/BoardDeleteTask). Задачи уже в эпике.
	if !needDecompose {
		// Ревизия: доводим до «готова к работе», задачи мог обновить лид.
		// LeadSyncedRev синхронизируем ниже.
	} else {
		// Падение на JSON-декомпозицию, если лид не создал задачи
		// инструментами доски (fallback для тестов и standalone-режима).
		tasks, err := k.store.TasksByEpic(ctx, epic.TaskID)
		if err != nil {
			return false, err
		}
		if len(tasks) == 0 {
			dec, err := board.UnmarshalTasks(resp.Content)
			if err != nil {
				return false, fmt.Errorf("декомпозиция эпика %s: лид не создал задач ни доской, ни JSON: %w", epic.TaskID, err)
			}
			if len(dec) == 0 {
				return false, fmt.Errorf("декомпозиция эпика %s: лид вернул пустой список tasks", epic.TaskID)
			}
			for i := range dec {
				ts := dec[i]
				if err := k.store.CreateTask(ctx, &board.Task{TaskSpec: ts, EpicID: epic.TaskID, Assignee: assignee(ts)}); err != nil {
					return false, fmt.Errorf("эпик %s: создание задачи %s: %w", epic.TaskID, ts.TaskID, err)
				}
			}
			k.log.Infof("[эпик %s] декомпозирован на %d задач (JSON-fallback)", epic.TaskID, len(dec))
		} else {
			k.log.Infof("[эпик %s] декомпозирован на %d задач (инструменты доски)", epic.TaskID, len(tasks))
		}
	}

	// Синхронизация ревизии: с этой ревизии доски лид больше не обязан
	// пересматривать задачи, пока архитектор не изменит эпик снова.
	synced, err := k.store.GetEpic(ctx, epic.TaskID)
	if err != nil {
		return false, fmt.Errorf("эпик %s: чтение после декомпозиции: %w", epic.TaskID, err)
	}
	synced.LeadSyncedRev = synced.Revision
	if err := k.store.SaveEpic(ctx, synced); err != nil {
		return false, fmt.Errorf("эпик %s: сохранение ревизии: %w", epic.TaskID, err)
	}

	if !needDecompose && needResync {
		// Ревизия: статус «в работе» уже не откатываем; доводим эпик к работе,
		// если он стоит «в анализе» или «готова к работе».
	} else if !needResync {
		if err := k.store.SetEpicStatus(ctx, epic.TaskID, board.StatusReady); err != nil {
			if _, ok := err.(*board.StatusError); !ok {
				return false, fmt.Errorf("эпик %s: перевод в «готов к работе»: %w", epic.TaskID, err)
			}
		}
	}
	k.log.Infof("[эпик %s] готов (ревизия %d, синхронизировано лидом %d)", epic.TaskID, synced.Revision, synced.LeadSyncedRev)
	// «Список обновлённых задач»: после декомпозиции/ревизии лида печатаем
	// в консоль актуальный состав задач эпика, чтобы человек видел итог.
	published, listErr := k.store.TasksByEpic(ctx, synced.TaskID)
	if listErr != nil {
		return false, fmt.Errorf("эпик %s: чтение задач после декомпозиции: %w", synced.TaskID, listErr)
	}
	printLeadTasks(k.log, synced, published)
	return true, nil
}

// printLeadTasks выводит в консоль и лог проекта текущие задачи эпика после
// работы лида: ID, статус, исполнитель, заголовок и приоритет/параллельность.
func printLeadTasks(log *logging.Logger, epic *board.Epic, tasks []*board.Task) {
	log.Infof("[Список обновлённых задач] эпик %s (%s) — %d задач:", epic.TaskID, truncateText(epic.Title, 60), len(tasks))
	for _, t := range tasks {
		log.Infof("  - %s [%s] -> %s | %s (порядок %d, параллельно: %v)",
			t.TaskID, t.Status.Label(), t.Assignee, truncateText(t.Title, 60), t.SequenceOrder.Int(), t.CanRunParallel.Bool())
	}
}

// pipelineIdle сообщает, что на доске нет незавершённых задач (все предыдущие
// декомпозиции выполнены, отменены или приостановлены). Только в этом состоянии
// лиду выдаётся следующая декомпозиция: сначала выполняется уже запланированный
// объём работы. Задачи «на паузе» (эпик приостановлен) не блокируют очередь —
// пауза эпика не должна замораживать декомпозицию остальных эпиков.
func (k *KanbanRunner) pipelineIdle(ctx context.Context) (bool, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return false, err
	}
	for _, t := range tasks {
		switch t.Status {
		case board.StatusDone, board.StatusCancelled, board.StatusPaused:
			continue
		default:
			return false, nil
		}
	}
	return true, nil
}

// resetStaleInProgress возвращает задачи, зависшие в статусе «в работе» после
// остановленной/прерванной сессии, обратно в «готова к работе», чтобы их снова
// выдал phaseExecute. Используется прямая запись статуса (SaveTask в обход
// ValidateTransition): перевод «в работе» → «готова к работе» конечным автоматом
// не предусмотрен (только → «выполнена»/«отменена»), но для восстановления он
// необходим. Вызывается один раз в начале Run — своего in_progress в тот момент
// ещё нет.
func (k *KanbanRunner) resetStaleInProgress(ctx context.Context) (int, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return 0, err
	}
	reset := 0
	for _, t := range tasks {
		if t.Status != board.StatusInProgress {
			continue
		}
		t.Status = board.StatusReady
		if err := k.store.SaveTask(ctx, t); err != nil {
			return 0, fmt.Errorf("задача %s: сброс «в работе» → «готова к работе»: %w", t.TaskID, err)
		}
		k.log.Infof("[задача %s] перезапуск: «в работе» → «готова к работе»", t.TaskID)
		reset++
	}
	return reset, nil
}

// epicPhase классифицирует эпик по фазе разработки (аналог taskPhase для задач):
// инфраструктура (DevOps) -> приложение (backend/frontend) -> тестирование (QA).
func epicPhase(e *board.Epic) string {
	switch {
	case isRole(e.AssignedRole, "qa", "тест", "testing"):
		return "test"
	case isRole(e.AssignedRole, "devops", "инфра", "infra", "sre", "docker", "k8s", "ci"):
		return "infra"
	default:
		return "app"
	}
}

// epicPhaseRank — приоритет фазы эпика в очереди лидов: инфраструктура раньше
// приложения, тестирование (в т.ч. qalead) — после всех задач разработки.
func epicPhaseRank(phase string) int {
	switch phase {
	case "infra":
		return 0
	case "app":
		return 1
	default: // test
		return 2
	}
}

// epicLess — приоритет эпиков в очереди лидов: сначала фаза разработки
// (инфраструктура -> приложение -> тестирование, QA Last), затем
// sequence_order и, для детерминизма, task_id.
func epicLess(a, b *board.Epic) bool {
	ar, br := epicPhaseRank(epicPhase(a)), epicPhaseRank(epicPhase(b))
	if ar != br {
		return ar < br
	}
	ao, bo := a.SequenceOrder.Int(), b.SequenceOrder.Int()
	if ao != bo {
		return ao < bo
	}
	return a.TaskID < b.TaskID
}

// nextLeadEpic выбирает следующий эпик для декомпозиции/ревизии лидом по
// приоритету очереди: инфраструктура -> приложение -> тестирование (QA Last),
// внутри фазы — sequence_order, затем task_id. Возвращает (nil, false, false),
// если ни одному эпику декомпозиция/ревизия не требуется.
func (k *KanbanRunner) nextLeadEpic(epics []*board.Epic) (*board.Epic, bool, bool, error) {
	var best *board.Epic
	bestDecompose := false
	for _, epic := range epics {
		// Терминальные и приостановленные эпики не трогаем: пауза замораживает
		// и декомпозицию, и ревизию лидом (код остаётся в своей ветке).
		if epic.Status.Terminal() || epic.Status == board.StatusPaused {
			continue
		}
		// Черновик (requires_review=true, Ф-8) не декомпозируется лидом, пока
		// Системный архитектор не утвердил его ревизией: лид получает эпик
		// только после снятия флага. Иначе черновик чата разнёсся бы на задачи
		// без экспертизы архитектора.
		if epic.RequiresReview {
			continue
		}
		needDecompose := len(epic.Tasks) == 0
		needResync := !needDecompose && epic.Revision > epic.LeadSyncedRev
		if !needDecompose && !needResync {
			continue
		}
		if best == nil || epicLess(epic, best) {
			best = epic
			bestDecompose = needDecompose
		}
	}
	if best == nil {
		return nil, false, false, nil
	}
	return best, bestDecompose, !bestDecompose, nil
}

// phaseReady: задачи, у которых выполнены все зависимости и фаза-гейт
// (инфраструктура → приложение → тестирование), переводятся «новая» ->
// «в анализе» -> «готова к работе» (готова к раздаче специалистам).
func (k *KanbanRunner) phaseReady(ctx context.Context) (bool, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return false, err
	}

	// «Новая»: зависимости и фаза-порядок выполнены — «в анализе».
	progress := false
	for _, t := range tasks {
		if t.Status != board.StatusNew {
			continue
		}
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
		if !ok {
			continue
		}
		if err := k.store.SetTaskStatus(ctx, t.TaskID, board.StatusAnalysis); err != nil {
			return false, fmt.Errorf("задача %s: новая -> в анализе: %w", t.TaskID, err)
		}
		k.log.Infof("[задача %s] новая → в анализе", t.TaskID)
		progress = true
	}

	// «В анализе»: фаза-гейт пройден и готова к раздаче — «готова к работе».
	// Перечитаем доску: первый цикл только что перевёл «новые» в «в анализе».
	tasks, err = k.store.ListTasks(ctx)
	if err != nil {
		return false, err
	}
	for _, t := range tasks {
		if t.Status != board.StatusAnalysis {
			continue
		}
		ok, err := k.epicWorkable(ctx, t.EpicID)
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
		if !ok {
			continue
		}
		if err := k.store.SetTaskStatus(ctx, t.TaskID, board.StatusReady); err != nil {
			return false, fmt.Errorf("задача %s: в анализе -> готова к работе: %w", t.TaskID, err)
		}
		k.log.Infof("[задача %s] в анализе → готова к работе", t.TaskID)
		progress = true
	}
	return progress, nil
}

// phaseExecute: раздаёт готовые задачи специалистам. Правило «одна задача на
// одного специалиста»: за цикл каждый специалист (assignee) получает не более
// одной задачи. Задача: готова к работе -> в работе; после успешного цикла
// агента -> выполнена.
func (k *KanbanRunner) phaseExecute(ctx context.Context) (bool, error) {
	ready, err := k.readyTasks(ctx)
	if err != nil {
		return false, err
	}
	if len(ready) == 0 {
		return false, nil
	}

	busy := map[string]bool{}
	progress := false
	for _, t := range ready {
		// Одна задача на одного специалиста за цикл.
		if busy[t.Assignee] {
			k.log.Detailf("[задача %s] ждёт: специалист %s уже занят в этом цикле", t.TaskID, t.Assignee)
			continue
		}
		busy[t.Assignee] = true

		if err := k.store.SetTaskStatus(ctx, t.TaskID, board.StatusInProgress); err != nil {
			return false, fmt.Errorf("задача %s: готова к работе -> в работе: %w", t.TaskID, err)
		}
		// Эпик, в котором появилась работающая задача, тоже уходит «в работу».
		k.noteEpicProgress(ctx, t.EpicID)

		specialist, err := k.specialistFor(t)
		if err != nil {
			return false, fmt.Errorf("задача %s: %w", t.TaskID, err)
		}
		// Специалисты получают доступ к доске: смена статуса своей задачи,
		// QA-инженер — публикация багрепортов (BoardCreateBugReport).
		if sb, ok := specialist.(interface{ SetBoardStore(*board.Store) }); ok {
			sb.SetBoardStore(k.store)
		}
		// Специалист git-проекта работает в своём worktree ветки задачи (Ф-3):
		// правки падают в ветку, авто-коммит на done подхватит их. В остальных
		// случаях остаётся стандартный OutputDir (temp/<проект>).
		if k.outputDir != nil {
			if dir := k.outputDir(k.store.Project(), t.TaskID); dir != "" {
				if so, ok := specialist.(interface{ SetOutputDir(string) }); ok {
					so.SetOutputDir(dir)
					k.log.Infof("[задача %s] рабочая директория → %s", t.TaskID, dir)
				}
			}
		}

		k.log.Infof("[задача %s] специалист %s выполняет: %s",
			t.TaskID, t.Assignee, truncateText(t.Title, 60))
		resp, err := k.provider.Generate(ctx, specialist)
		if err != nil {
			return false, fmt.Errorf("задача %s: %w", t.TaskID, err)
		}
		if resp != nil && resp.Truncated {
			return false, fmt.Errorf("задача %s: цикл остановлен по лимиту раундов", t.TaskID)
		}

		// Разработчик сам переводит задачу в done по завершении работы
		// (BoardSetTaskStatus). Проверяем фактический статус и, если агент
		// не сменил его, — страхуем fallback-ом оркестратора, чтобы задача
		// не зависла и канал не зацикливался.
		current, err := k.store.GetTask(ctx, t.TaskID)
		if err != nil {
			return false, fmt.Errorf("задача %s: чтение статуса после работы: %w", t.TaskID, err)
		}
		if current.Status == board.StatusDone {
			k.log.Infof("[задача %s] в работе → выполнена (агент подтвердил сам)", t.TaskID)
		} else {
			if err := k.store.SetTaskStatus(ctx, t.TaskID, board.StatusDone); err != nil {
				return false, fmt.Errorf("задача %s: в работе -> выполнена: %w", t.TaskID, err)
			}
			k.log.Infof("[задача %s] в работе → выполнена (fallback: агент не сменил статус)", t.TaskID)
		}
		progress = true
	}
	return progress, nil
}

// phaseBugs: конвейер багрепортов. QA-специалисты публикуют багрепорты
// (BoardCreateBugReport, статус new) во время выполнения задач. QA Lead
// отсеивает «нейрослоп» (new -> slop) и подтверждает реальные проблемы (new ->
// confirmed). Системный архитектор в режиме экспертизы выносит вердикт по
// подтверждённым багрепортам (confirmed -> fix|feature|wont_fix), при вердикте
// fix создавая эпик исправления.
func (k *KanbanRunner) phaseBugs(ctx context.Context) (bool, error) {
	bugs, err := k.store.ListBugReports(ctx)
	if err != nil {
		return false, err
	}
	progress := false

	var newBugs, confirmedBugs []*board.BugReport
	for _, b := range bugs {
		switch b.Status {
		case board.BugStatusNew:
			newBugs = append(newBugs, b)
		case board.BugStatusConfirmed:
			confirmedBugs = append(confirmedBugs, b)
		}
	}

	// Триаж QA Lead: подтвердить или отсечь «нейрослоп».
	if len(newBugs) > 0 {
		qa := qalead.NewQALead(k.store.Project(), k.bugTriagePrompt(k.store.Project(), newBugs))
		if sb, ok := any(qa).(interface{ SetBoardStore(*board.Store) }); ok {
			sb.SetBoardStore(k.store)
		}
		// Триаж багрепортов — не декомпозиция: QA Lead работает статусами
		// багрепортов (BoardSetBugStatus), публиковать задачи не обязан.
		if tp, ok := any(qa).(interface{ SetTaskPublishing(bool) }); ok {
			tp.SetTaskPublishing(false)
		}
		resp, err := k.provider.Generate(ctx, qa)
		if err != nil {
			return false, fmt.Errorf("фаза триажа багрепортов: %w", err)
		}
		if resp != nil && resp.Truncated {
			return false, fmt.Errorf("фаза триажа багрепортов: цикл остановлен по лимиту раундов")
		}
		k.log.Infof("[QA Lead] триаж %d багрепортов", len(newBugs))
		progress = true
	}

	// Экспертиза Системного архитектора: вердикт по подтверждённым багрепортам.
	if len(confirmedBugs) > 0 {
		arch := architect.NewArchitectWithStore(k.store.Project(),
			k.bugExpertPrompt(k.store.Project(), confirmedBugs), k.store).AsBugExpert()
		arch = k.prepareArchitect(arch)
		resp, err := k.provider.Generate(ctx, arch)
		if err != nil {
			return false, fmt.Errorf("фаза экспертизы багрепортов: %w", err)
		}
		if resp != nil && resp.Truncated {
			return false, fmt.Errorf("фаза экспертизы багрепортов: цикл остановлен по лимиту раундов")
		}
		k.log.Infof("[Системный архитектор] экспертиза %d багрепортов", len(confirmedBugs))
		progress = true
	}

	return progress, nil
}

// phaseComplete: эпик становится «выполнен» только после завершения всех его
// задач (в работе -> выполнена).
func (k *KanbanRunner) phaseComplete(ctx context.Context) (bool, error) {
	epics, err := k.store.ListEpics(ctx)
	if err != nil {
		return false, err
	}
	progress := false
	for _, epic := range epics {
		if epic.Status == board.StatusDone || epic.Status.Terminal() {
			continue
		}
		// Приостановленный эпик не финализируем: «на паузе» — временное
		// состояние, завершение дождётся возобновления.
		if epic.Status == board.StatusPaused {
			continue
		}
		if len(epic.Tasks) == 0 {
			continue
		}
		allDone, err := k.epicTasksDone(ctx, epic)
		if err != nil {
			return false, err
		}
		if allDone {
			// «Доводим» эпик до «выполнена» из любого не-терминального статуса
			// по цепочке new -> analysis -> ready -> in_progress -> done. Новая
			// задача продвигает последующие; статусы менеджер переместил вручную
			// (resume) — по цепочке с текущего места.
			advanced := false
			for _, tgt := range []board.Status{
				board.StatusAnalysis, board.StatusReady,
				board.StatusInProgress, board.StatusDone,
			} {
				if err := k.store.SetEpicStatus(ctx, epic.TaskID, tgt); err == nil {
					advanced = true
					continue
				} else if _, ok := err.(*board.StatusError); ok {
					continue
				} else {
					return false, fmt.Errorf("эпик %s: продвижение статуса: %w", epic.TaskID, err)
				}
			}
			if advanced {
				k.log.Infof("[эпик %s] выполнен: %s", epic.TaskID, truncateText(epic.Title, 60))
				progress = true
				// Багрепорты, направленные на эпик исправления, закрываются
				// (fix -> fixed): исправление поставлено и проверено.
				if n, err := k.store.MarkBugsFixedForEpic(ctx, epic.TaskID); err != nil {
					return false, fmt.Errorf("эпик %s: закрытие багрепортов: %w", epic.TaskID, err)
				} else if n > 0 {
					k.log.Infof("[эпик %s] закрыты багрепорты: %d", epic.TaskID, n)
					progress = true
				}
			}
		}
	}
	return progress, nil
}

// epicTasksDone сообщает, что все задачи эпика выполнены (и их хотя бы одна):
// признак того, что эпик можно финализировать.
func (k *KanbanRunner) epicTasksDone(ctx context.Context, epic *board.Epic) (bool, error) {
	if len(epic.Tasks) == 0 {
		return false, nil
	}
	for _, taskID := range epic.Tasks {
		t, err := k.store.GetTask(ctx, taskID)
		if err != nil {
			return false, fmt.Errorf("эпик %s: чтение задачи %s: %w", epic.TaskID, taskID, err)
		}
		if t.Status != board.StatusDone {
			return false, nil
		}
	}
	return true, nil
}

// noteEpicProgress переводит эпик в «в работе», если он в статусе
// «готова к работе» (а его задачи начали выполняться).
func (k *KanbanRunner) noteEpicProgress(ctx context.Context, epicID string) {
	epic, err := k.store.GetEpic(ctx, epicID)
	if err != nil || epic.Status != board.StatusReady {
		return
	}
	if err := k.store.SetEpicStatus(ctx, epicID, board.StatusInProgress); err != nil {
		k.log.Detailf("[эпик %s] в работу: %v", epicID, err)
	}
}

// readyTasks возвращает задачи «готова к работе» (эпик не приостановлен/не
// отменён), отсортированные по порядку (sequence_order) и ID.
func (k *KanbanRunner) readyTasks(ctx context.Context) ([]*board.Task, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	var ready []*board.Task
	for _, t := range tasks {
		if t.Status != board.StatusReady {
			continue
		}
		ok, err := k.epicWorkable(ctx, t.EpicID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		ready = append(ready, t)
	}
	sortTasks(ready)
	return ready, nil
}

// taskPhase классифицирует задачу по фазе разработки по её assigned_role:
// инфраструктура (DevOps), тестирование (QA) или приложение (остальное).
// Используется для фаза-гейтинга «инфраструктура → приложение → тестирование».
func taskPhase(t *board.Task) string {
	switch {
	case isRole(t.AssignedRole, "qa", "тест", "testing"):
		return "test"
	case isRole(t.AssignedRole, "devops", "инфра", "infra", "sre", "docker", "k8s", "ci"):
		return "infra"
	default:
		return "app"
	}
}

// phasePrereqSatisfied проверяет фаза-порядок в рамках эпика задачи:
// прикладные задачи ждут завершения инфраструктурных задач эпика, тестовые —
// завершения всех НЕ тестовых задач эпика. Отменённые задачи считаются
// «прошедшими», инфраструктурные задачи гейта не имеют.
func (k *KanbanRunner) phasePrereqSatisfied(ctx context.Context, t *board.Task) (bool, error) {
	phase := taskPhase(t)
	if phase == "infra" {
		return true, nil
	}
	epic, err := k.store.GetEpic(ctx, t.EpicID)
	if err != nil {
		return false, err
	}
	for _, taskID := range epic.Tasks {
		if taskID == t.TaskID {
			continue
		}
		other, err := k.store.GetTask(ctx, taskID)
		if err != nil {
			return false, err
		}
		if other.Status == board.StatusCancelled {
			continue
		}
		otherPhase := taskPhase(other)
		var required bool
		switch phase {
		case "app":
			required = otherPhase == "infra"
		case "test":
			required = otherPhase != "test"
		}
		if required && other.Status != board.StatusDone {
			return false, nil
		}
	}
	return true, nil
}

// depsDone проверяет, что все зависимости задачи (задачи/эпики доски)
// уже выполнены. При несуществующей зависимости возвращает ошибку доски.
func (k *KanbanRunner) depsDone(ctx context.Context, t *board.Task) (bool, error) {
	if len(t.Dependencies) == 0 {
		return true, nil
	}
	for _, dep := range t.Dependencies {
		if task, err := k.store.GetTask(ctx, dep); err == nil {
			if task.Status != board.StatusDone {
				return false, nil
			}
			continue
		} else if !errors.Is(err, board.ErrNotFound) {
			return false, err
		}
		if epic, err := k.store.GetEpic(ctx, dep); err == nil {
			if epic.Status != board.StatusDone {
				return false, nil
			}
			continue
		} else if !errors.Is(err, board.ErrNotFound) {
			return false, err
		}
		return false, fmt.Errorf("задача %s: зависимость %q не существует на доске", t.TaskID, dep)
	}
	return true, nil
}

// boardSummary собирает краткую сводку доски для логов и ошибок. Устойчиво к
// отмене контекста/ошибкам хранилища: если счётчики не прочитались (например,
// контекст отменён прямо во время ожидания работы), сводка показывает нули.
func (k *KanbanRunner) boardSummary(ctx context.Context) string {
	ec, _ := k.store.EpicCounts(ctx)
	tc, _ := k.store.TaskCounts(ctx)
	if ec == nil {
		ec = &board.StatusCounts{By: map[board.Status]int{}}
	}
	if tc == nil {
		tc = &board.StatusCounts{By: map[board.Status]int{}}
	}
	return fmt.Sprintf("эпиков: %d (выполнено %d, отменено %d), задач: %d (выполнено %d, отменено %d)",
		ec.Total, ec.By[board.StatusDone], ec.By[board.StatusCancelled],
		tc.Total, tc.By[board.StatusDone], tc.By[board.StatusCancelled])
}

// leadFor создаёт агента-лида направления по assigned_role эпика и собирает
// промпт декомпозиции. Лиды не пишут код и не запускают команды.
func (k *KanbanRunner) leadFor(epic *board.Epic) (agents.Agent, error) {
	prompt := k.leadPrompt(epic)
	project := k.store.Project()
	switch {
	case isRole(epic.AssignedRole, "qa", "тест", "testing"):
		return qalead.NewQALead(project, prompt), nil
	case isRole(epic.AssignedRole, "devops", "инфра", "infra", "dev", "sre", "ci"):
		return devopslead.NewDevopsLead(project, prompt), nil
	case isRole(epic.AssignedRole, "front", "react", "ui", "client", "фронт"):
		return frontendlead.NewFrontendLead(project, prompt), nil
	default: // backend
		return backendlead.NewBackendLead(project, prompt), nil
	}
}

// bugTriagePrompt формирует задание QA Lead на триаж багрепортов (new):
// подтвердить (BoardSetBugStatus, confirmed) или отсеять «нейрослоп» (slop).
func (k *KanbanRunner) bugTriagePrompt(project string, bugs []*board.BugReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Проведи триаж %d багрепортов QA-специалистов проекта %q с доски.\n\n", len(bugs), project)
	for _, bug := range bugs {
		fmt.Fprintf(&b, "- %s (статус new): %s\n  Репортёр: %s, задача %s, эпик %s\n  %s\n",
			bug.BugID, bug.Title, bug.ReporterRole, bug.TaskID, bug.EpicID, truncateText(bug.Description, 400))
	}
	b.WriteString("\nДля каждого багрепорта вызови BoardSetBugStatus: реальная, воспроизводимая проблема — status=confirmed; надуманное, «нейрослоп», дубль или жалоба без фактов — status=slop.\n")
	b.WriteString("Ответь текстом, что триаж выполнен.")
	return b.String()
}

// bugExpertPrompt формирует задание архитектора (режим экспертизы) по
// подтверждённым багрепортам: вынести вердикт BoardReviewBugReport
// (fix|feature|wont_fix) и при fix создать эпик исправления.
func (k *KanbanRunner) bugExpertPrompt(project string, bugs []*board.BugReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Системный архитектор проекта %q: QA Lead подтвердил %d багрепортов, требуется твоя экспертиза.\n\n", project, len(bugs))
	for i, bug := range bugs {
		fmt.Fprintf(&b, "%d) %s — %s\n   Репортёр %s, задача %s, эпик %s.\n   %s\n",
			i+1, bug.BugID, bug.Title, bug.ReporterRole, bug.TaskID, bug.EpicID, truncateText(bug.Description, 600))
	}
	b.WriteString("\nДля каждого багрепорта вызови BoardReviewBugReport с вердиктом: fix (создай эпик исправления), feature (это фича), wont_fix (не исправляем). Параметры эпика исправления укажи в том же вызове (epic_task_id, epic_title, epic_description, epic_assigned_role, epic_sequence_order).")
	return b.String()
}

// leadPrompt формирует задание лиду: эпик, архитектурная сводка. Лид публикует
// задачи инструментами доски (если они доступны), иначе — JSON-декомпозицией.
func (k *KanbanRunner) leadPrompt(epic *board.Epic) string {
	var b strings.Builder
	b.WriteString("Декомпозируй эпик из бэклога Системного архитектора на задачи для рядовых специалистов.\n\n")
	fmt.Fprintf(&b, "Проект: %s\n", k.store.Project())
	fmt.Fprintf(&b, "Эпик: %s — %s\n", epic.TaskID, epic.Title)
	if epic.Summary != "" {
		fmt.Fprintf(&b, "Архитектурная сводка:\n%s\n\n", epic.Summary)
	}
	fmt.Fprintf(&b, "Описание эпика:\n%s\n\n", epic.Description)
	b.WriteString("Публикация задач:\n")
	b.WriteString("- Если у тебя есть инструменты доски (BoardCreateTask и др.) — создавай задачи ими (полный контракт в description, assigned_role, sequence_order, dependencies). Обновляй и удаляй задачи через BoardUpdateTask/BoardDeleteTask (кроме взятых в работу).\n")
	b.WriteString("- Если инструментов доски нет — верни строго JSON-декомпозицию (без markdown-обёрток) по схеме:\n")
	b.WriteString("{\n")
	b.WriteString(`  "lead_summary": "краткое техническое описание модуля",` + "\n")
	b.WriteString(`  "tasks": [` + "\n")
	b.WriteString("    {\n")
	b.WriteString(`      "task_id": "уникальный ID (например T-01)",` + "\n")
	b.WriteString(`      "title": "название задачи",` + "\n")
	b.WriteString(`      "description": "детальное техническое описание задачи с готовым контрактом взаимодействия, который ты спроектировал",` + "\n")
	b.WriteString(`      "assigned_role": "роль специалиста (например Senior Go Developer / React Developer / QA Engineer / DevOps Engineer)",` + "\n")
	b.WriteString("      \"sequence_order\": 1,\n")
	b.WriteString(`      "can_run_parallel": true,` + "\n")
	b.WriteString(`      "dependencies": [` + "\n")
	b.WriteString("      ]\n")
	b.WriteString("    }\n")
	b.WriteString("  ]\n")
	b.WriteString("}\n\n")
	b.WriteString("ДЕТАЛИЗАЦИЯ КОНТРАКТОВ (обязательно для каждой задачи): описание задачи обязано содержать ПОЛНЫЙ контракт, по которому специалист пишет код без догадок: точные типы/структуры (поля с типами), публичные сигнатуры функций/методов/интерфейсов (имя, параметры с типами, возвращаемые значения), API-контракты (метод, путь, схема запроса/ответа, коды ошибок), схему БД, манифесты с конкретными значениями. Зафиксируй контракт в description — специалист не меняет публичные сигнатуры и схемы.\n\n")
	b.WriteString("Правила:\n")
	b.WriteString("- Ты НЕ пишешь код и НЕ запускаешь команды: только проектируешь контракты и раздаёшь задачи.\n")
	b.WriteString("- Порядок разработки: сначала инфраструктура, затем приложение, затем тестирование — проставляй sequence_order и зависимости так, чтобы этот порядок соблюдался (тестовые задачи зависят от прикладных, прикладные — от инфраструктурных).\n")
	b.WriteString("- Контракты взаимодействия дублируй в описание каждой связанной задачи (единый источник истины).\n")
	b.WriteString("- Чётко проставь sequence_order и dependencies: какие задачи параллельны (can_run_parallel: true), какие блокируют друг друга.\n")
	return b.String()
}

// specialistFor создаёт агента-специалиста для задачи: QA/DevOps или
// разработчик (backend/frontend). Разработчик-агент создаёт недостающую
// структуру подпроекта с нуля и дорабатывает существующий код; выбор
// специализации (frontend/backend) идёт по роли задачи (маркеры фронтенда)
// либо по умолчанию — backend.
func (k *KanbanRunner) specialistFor(t *board.Task) (agents.Agent, error) {
	return specialistForRole(k.store.Project(), t.AssignedRole, k.taskPrompt(t)), nil
}

// specialistForRole создаёт агента-специалиста по роли задачи: QA/DevOps или
// разработчик (backend/frontend). Общий для Kanban- и plan-исполнителей:
// выбор специализации (QA/DevOps/frontend/backend) идёт по роли задачи.
func specialistForRole(project, role, prompt string) agents.Agent {
	switch {
	case isRole(role, "qa", "тест", "testing"):
		return qaengineer.NewQAEngineer(project, prompt)
	case isRole(role, "devops", "инфра", "infra", "sre", "docker", "k8s", "ci"):
		return devops.NewDevops(project, prompt)
	}

	// Разработка кода: задача с фронтенд-маркерами отдаётся Frontend-агенту,
	// всё остальное (включая смешанные/backend/универсальные задачи) — Backend-
	// агенту. Оба агента работают в temp/<project> в корне модуля и сами
	// создают недостающую структуру подпроекта.
	if isRole(role, "front", "react", "ui", "client", "фронт", "js", "ts", "vue") {
		return developer.NewFrontendDeveloper(project, prompt)
	}
	return developer.NewBackendDeveloper(project, prompt)
}

// taskPrompt формирует задание специалисту по задаче с Kanban-доски.
func (k *KanbanRunner) taskPrompt(t *board.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Ты — специалист (%s), выполняешь задачу с общей Kanban-доски проекта %q.\n\n", t.AssignedRole, k.store.Project())
	fmt.Fprintf(&b, "Задача на доске: %s — %s\n\n", t.TaskID, t.Title)
	b.WriteString("Постановка задачи:\n")
	b.WriteString(t.Description)
	b.WriteString("\n\n")
	b.WriteString("Правила:\n")
	b.WriteString("- Работай в своей выходной директории (OutputDir): учи структуру через List, читай контракты через ReadFiles.\n")
	b.WriteString("- Выполни задачу, прогони сборку и проверки через Run, доведи до зелёного состояния.\n")
	b.WriteString("- Не выходи за пределы своей части монорепозитория (роль задана промптом).\n")
	fmt.Fprintf(&b, "- Если у тебя есть инструменты доски: идентификатор задачи %s. Ты можешь читать её контракт (BoardGetTask); когда работа полностью выполнена (сборка и проверки зелёные) — ОБЯЗАТЕЛЬНО переведи задачу в статус done вызовом BoardSetTaskStatus.\n", t.TaskID)
	b.WriteString("- Если ты QA-инженер и нашёл дефект по контракту — оформи багрепорт инструментом BoardCreateBugReport (статус new), его разберут QA Lead и архитектор.")
	return b.String()
}

// assignee возвращает ключ специалиста для задачи: присваиваем роль из
// assigned_role (одна задача — один специалист).
func assignee(ts board.TaskSpec) string {
	if strings.TrimSpace(ts.AssignedRole) == "" {
		return "developer"
	}
	return ts.AssignedRole
}

// isRole проверяет, содержит ли роль (в нижнем регистре) один из маркеров.
func isRole(role string, markers ...string) bool {
	role = strings.ToLower(role)
	for _, m := range markers {
		if strings.Contains(role, m) {
			return true
		}
	}
	return false
}

// sortTasks сортирует задачи по sequence_order, затем по TaskID (детерминизм).
func sortTasks(tasks []*board.Task) {
	for i := 1; i < len(tasks); i++ {
		for j := i; j > 0; j-- {
			a, b := tasks[j-1], tasks[j]
			if a.SequenceOrder.Int() < b.SequenceOrder.Int() ||
				(a.SequenceOrder.Int() == b.SequenceOrder.Int() && a.TaskID <= b.TaskID) {
				continue
			}
			tasks[j-1], tasks[j] = tasks[j], tasks[j-1]
		}
	}
}

// epicList форматирует список эпиков для логов.
func epicList(epics []*board.Epic) string {
	ids := make([]string, 0, len(epics))
	for _, e := range epics {
		ids = append(ids, e.TaskID+" ("+truncateText(e.Title, 40)+")")
	}
	return strings.Join(ids, ", ")
}

// leadName описывает лида направления для логов.
func leadName(epic *board.Epic) string {
	switch {
	case isRole(epic.AssignedRole, "qa", "тест", "testing"):
		return "QA Lead"
	case isRole(epic.AssignedRole, "devops", "инфра", "infra", "dev", "sre", "ci"):
		return "DevOps Lead"
	case isRole(epic.AssignedRole, "front", "react", "ui", "client", "фронт"):
		return "Frontend Lead"
	default:
		return "Backend Lead"
	}
}

// truncateText обрезает длинный текст для логов.
func truncateText(s string, n int) string {
	if len(s) <= n {
		return strings.TrimSpace(s)
	}
	trimmed := strings.TrimSpace(s)
	if len(trimmed) <= n {
		return trimmed
	}
	return trimmed[:n] + "…"
}
