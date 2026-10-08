package planner

import (
	"ai/agents/architect"
	"ai/board"
	"ai/logging"
	"ai/tokens"
	"context"
	"fmt"
	"strings"
)

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
	// 4.1: резолвер worktree ветки эпика. Вызывается архитектором ВНУТРИ
	// submitBacklog — после публикации бэклога, когда эпики уже на доске и их
	// ветки созданы хуком EpicCreatedHook. Основной (первый по порядку) эпик
	// получает структуру проекта; без worktree (консоль/не-git) архитектор
	// получит note и структуру не напишет.
	arch.SetEpicWorkdir(func() (string, string) {
		if k.epicOutputDir == nil {
			return "", ""
		}
		eps, err := k.store.ListEpics(ctx)
		if err != nil || len(eps) == 0 {
			return "", ""
		}
		primary := eps[0]
		for _, e := range eps[1:] {
			if epicLess(e, primary) {
				primary = e
			}
		}
		dir := k.epicOutputDir(k.store.Project(), primary.TaskID)
		if dir == "" {
			return "", ""
		}
		return dir, primary.GitBranch
	})
	resp, err := k.generate(ctx, tokens.ScopeArchitecture, arch)
	if err != nil {
		return false, k.architectFail(ctx, fmt.Errorf("фаза архитектора: %w", err))
	}
	if err := resp.LoopError("фаза архитектора"); err != nil {
		return false, k.architectFail(ctx, err)
	}
	if resp != nil && resp.Truncated {
		return false, k.architectFail(ctx, fmt.Errorf("фаза архитектора: цикл остановлен по лимиту раундов"))
	}

	epics, err = k.store.ListEpics(ctx)
	if err != nil {
		return false, k.architectFail(ctx, err)
	}
	if len(epics) == 0 {
		return false, k.architectFail(ctx, fmt.Errorf("фаза архитектора: модель не опубликовала эпики (submit_architecture_backlog не вызван)"))
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
		// Черновики не трогаем только в терминальных/остановленных
		// состояниях («помощь человека» замораживает ревизию, Ф-8): в
		// остальных состояниях ревизия обязательна, пока флаг не снят.
		if !epic.RequiresReview || epic.Status.Terminal() || epic.Status == board.StatusHumanHelp {
			continue
		}
		drafts = append(drafts, epic)
	}
	if len(drafts) == 0 {
		return false, nil
	}

	reviewer := architect.NewArchitectWithStore(k.store.Project(), k.epicReviewPrompt(k.store.Project(), drafts), k.store).AsReviewer()
	reviewer = k.prepareArchitect(reviewer)
	resp, err := k.generate(ctx, tokens.ScopeArchitecture, reviewer)
	if err != nil {
		return false, fmt.Errorf("фаза ревизии эпиков: %w", err)
	}
	if err := resp.LoopError("фаза ревизии эпиков"); err != nil {
		return false, err
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
	// выполниться уже запланированный объём работы. Исключение — взаимная
	// блокировка: задача ждёт готовности ЭПИКА-зависимости, а этот эпик ещё не
	// разобран. Без исключения цикл вставал: DOL-01 ждал `done` эпика ARCH-01,
	// а разобрать ARCH-01 нельзя, пока DOL-01 не выполнена → «Kanban-цикл N: нет
	// прогресса». В этом случае лиду выдаётся именно эпик-зависимость
	// (см. blockingDependencyEpic), порядок «по одному эпику за раз» сохраняется.
	idle, err := k.pipelineIdle(ctx)
	if err != nil {
		return false, err
	}

	var epic *board.Epic
	var needDecompose, needResync bool
	if idle {
		// Следующий эпик очереди лидов, которому нужна декомпозиция/ревизия.
		epic, needDecompose, needResync, err = k.nextLeadEpic(epics)
		if err != nil {
			return false, err
		}
	} else {
		epic, err = k.blockingDependencyEpic(ctx, epics)
		if err != nil {
			return false, err
		}
		if epic != nil {
			needDecompose, needResync = true, false
			k.log.Infof("[Kanban] на доске есть незавершённые задачи, но они ждут эпик %s "+
				"(зависимость ещё не разобрана) — разбираю его, чтобы снять блокировку", epic.TaskID)
		}
	}
	if epic == nil {
		return false, nil
	}

	lead, err := k.leadFor(epic)
	if err != nil {
		return false, k.leadFail(ctx, epic, fmt.Errorf("фаза лидов, эпик %s: %w", epic.TaskID, err))
	}
	if sb, ok := lead.(interface{ SetBoardStore(*board.Store) }); ok {
		sb.SetBoardStore(k.store)
	}
	// Декомпозиция/ревизия всегда требует публикации задач на доске:
	// лид обязан создать/обновить/удалить задачи для подчинённых.
	if tp, ok := lead.(interface{ SetTaskPublishing(bool) }); ok {
		tp.SetTaskPublishing(true)
	}
	// 5.2: резолвер worktree ветки эпика — лид пишет скелетон в ветку своего
	// эпика, а не в общий temp/<проект>. Резолвер лениво пересоздаёт worktree
	// (мог быть удалён drop-хуком после прошлой фазы), поэтому вызывается на
	// каждой итерации. Без worktree (консоль/не-git) резолвер вернёт "" — лид
	// работает в обычной директории проекта и скелетон не пишет (см. промпт).
	if k.epicOutputDir != nil {
		if sd, ok := lead.(interface{ SetOutputDir(string) }); ok {
			if dir := k.epicOutputDir(k.store.Project(), epic.TaskID); dir != "" {
				sd.SetOutputDir(dir)
			}
		}
	}

	// «В анализе»: для новой декомпозиции обязателен; при ревизии эпик может
	// находиться дальше по цепочке — некритично (переход в анализ не требуется).
	if err := k.store.SetEpicStatus(ctx, epic.TaskID, board.StatusAnalysis); err != nil {
		if _, ok := err.(*board.StatusError); !ok {
			return false, k.leadFail(ctx, epic, fmt.Errorf("эпик %s: перевод в «в анализе»: %w", epic.TaskID, err))
		}
	}
	k.log.Infof("[%s] декомпозиция/ревизия эпика %s (%s) (нужна ревизия: %v)",
		leadName(epic), epic.TaskID, truncateText(epic.Title, 60), needResync)

	resp, err := k.generate(ctx, tokens.ScopeEpic(epic.TaskID), lead)
	if err != nil {
		return false, k.leadFail(ctx, epic, fmt.Errorf("декомпозиция эпика %s: %w", epic.TaskID, err))
	}
	if stop := resp.StopReason(); stop != "" {
		// Ни лимит раундов, ни зацикливание лида не должны ронять весь запуск,
		// если он уже опубликовал на доске частичную (но рабочую) декомпозицию:
		// продолжаем, задачи передадутся специалистам после затвора. Задач нет —
		// эпик пуст, работа не выполнена: эпик уходит в human_help (5.4).
		tasks, terr := k.store.TasksByEpic(ctx, epic.TaskID)
		if terr != nil {
			return false, fmt.Errorf("декомпозиция эпика %s: цикл прерван (%s): %w", epic.TaskID, stop, terr)
		}
		if len(tasks) == 0 {
			return false, k.leadFail(ctx, epic, fmt.Errorf("декомпозиция эпика %s: цикл прерван (%s), задачи не созданы", epic.TaskID, stop))
		}
		k.log.Infof("[эпик %s] цикл лида прерван (%s), но опубликовано задач: %d — продолжаем с частичной декомпозицией", epic.TaskID, stop, len(tasks))
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
				return false, k.leadFail(ctx, epic, fmt.Errorf("декомпозиция эпика %s: лид не создал задач ни доской, ни JSON: %w", epic.TaskID, err))
			}
			if len(dec) == 0 {
				return false, k.leadFail(ctx, epic, fmt.Errorf("декомпозиция эпика %s: лид вернул пустой список tasks", epic.TaskID))
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

// blockingDependencyEpic ищет эпик, который блокирует уже запланированные
// задачи: у незавершённой задачи в dependencies стоит ID эпика, который ещё не
// декомпозирован лидом (len(epic.Tasks)==0), не ждёт ревизии архитектора
// (RequiresReview=false) и не терминален/не остановлен. Такой эпик разбирается
// вне очереди `pipelineIdle` — иначе задача и эпик блокируют друг друга.
//
// Возвращает nil, если блокирующих эпиков нет: тогда поведение phaseLeads
// прежнее (следующий эпик выдаётся только на «пустой» доске).
func (k *KanbanRunner) blockingDependencyEpic(ctx context.Context, epics []*board.Epic) (*board.Epic, error) {
	byID := make(map[string]*board.Epic, len(epics))
	for _, e := range epics {
		byID[e.TaskID] = e
	}
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	var best *board.Epic
	for _, t := range tasks {
		switch t.Status {
		case board.StatusDone, board.StatusCancelled, board.StatusHumanHelp:
			continue
		}
		for _, dep := range t.Dependencies {
			depEpic, ok := byID[dep]
			if !ok || depEpic.Status.Terminal() || depEpic.Status == board.StatusHumanHelp {
				continue
			}
			if depEpic.RequiresReview || len(depEpic.Tasks) > 0 {
				continue
			}
			if best == nil || epicLess(depEpic, best) {
				best = depEpic
			}
		}
	}
	return best, nil
}

// nextLeadEpic выбирает следующий эпик для декомпозиции/ревизии лидом по
// приоритету очереди: инфраструктура -> приложение -> тестирование (QA Last),
// внутри фазы — sequence_order, затем task_id. Возвращает (nil, false, false),
// если ни одному эпику декомпозиция/ревизия не требуется.
func (k *KanbanRunner) nextLeadEpic(epics []*board.Epic) (*board.Epic, bool, bool, error) {
	var best *board.Epic
	bestDecompose := false
	for _, epic := range epics {
		// Терминальные и остановленные эпики не трогаем: «помощь человека»
		// замораживает и декомпозицию, и ревизию лидом (код остаётся в
		// своей ветке).
		if epic.Status.Terminal() || epic.Status == board.StatusHumanHelp {
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
