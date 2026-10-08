package planner

import (
	"ai/agents/architect"
	"ai/agents/qaengineer"
	"ai/board"
	"ai/models"
	"ai/tokens"
	"context"
	"fmt"
	"strings"
)

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
		qa := qaengineer.NewQAEngineer(k.store.Project(), k.bugTriagePrompt(k.store.Project(), newBugs))
		if sb, ok := any(qa).(interface{ SetBoardStore(*board.Store) }); ok {
			sb.SetBoardStore(k.store)
		}
		// Триаж багрепортов — не декомпозиция: QA Lead работает статусами
		// багрепортов (BoardSetBugStatus), публиковать задачи не обязан.
		if tp, ok := any(qa).(interface{ SetTaskPublishing(bool) }); ok {
			tp.SetTaskPublishing(false)
		}
		resp, err := k.generate(ctx, tokens.ScopeBugs, qa)
		if err != nil {
			return false, fmt.Errorf("фаза триажа багрепортов: %w", err)
		}
		if err := resp.LoopError("фаза триажа багрепортов"); err != nil {
			return false, err
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
		resp, err := k.generate(ctx, tokens.ScopeBugs, arch)
		if err != nil {
			return false, fmt.Errorf("фаза экспертизы багрепортов: %w", err)
		}
		if err := resp.LoopError("фаза экспертизы багрепортов"); err != nil {
			return false, err
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
		// Остановленный эпик не финализируем: «помощь человека» — временное
		// состояние, завершение дождётся выхода (вручную).
		if epic.Status == board.StatusHumanHelp {
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
			// по цепочке new -> analysis -> ready -> in_progress -> done (из
			// testing цепочка заходит через in_progress; human_help фаза выше
			// пропускает). Новая задача продвигает последующие; статусы
			// менеджер переместил вручную (resume) — по цепочке с текущего
			// места.
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
				// Ф-3: факт расхода токенов эпика (задачи + лид + доля
				// архитектора) фиксируется сразу после выполнения.
				if err := k.finalizeEpicTokens(ctx, epic.TaskID); err != nil {
					k.log.Warnf("[учёт токенов] эпик %s: %v", epic.TaskID, err)
				}
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

// phaseTesting доводит задачи в статусе "на тестирование" (StatusTesting)
// до "выполнено". QA (или назначенный специалист) берёт задачу из testing,
// доводит до done, либо возвращает замечаниями в работу/human_help.
func (k *KanbanRunner) phaseTesting(ctx context.Context) (bool, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return false, err
	}
	progress := false
	busy := map[string]bool{}
	for _, t := range tasks {
		if t.Status != board.StatusTesting {
			continue
		}
		if busy[t.Assignee] {
			continue
		}
		busy[t.Assignee] = true
		if err := k.store.SetTaskStatus(ctx, t.TaskID, board.StatusInProgress); err != nil {
			return false, fmt.Errorf("задача %s: testing -> in_progress: %w", t.TaskID, err)
		}
		k.noteEpicProgress(ctx, t.EpicID)
		specialist, err := k.specialistFor(t)
		if err != nil {
			return false, fmt.Errorf("задача %s: %w", t.TaskID, err)
		}
		if sb, ok := specialist.(interface{ SetBoardStore(*board.Store) }); ok {
			sb.SetBoardStore(k.store)
		}
		if k.outputDir != nil {
			if dir := k.outputDir(k.store.Project(), t.TaskID); dir != "" {
				if so, ok := specialist.(interface{ SetOutputDir(string) }); ok {
					so.SetOutputDir(dir)
				}
			}
		}
		if k.store != nil {
			if sn, ok := specialist.(interface{ SetProjectName(string) }); ok {
				sn.SetProjectName(k.store.Project())
			}
			if ti, ok := specialist.(interface{ SetTaskID(string) }); ok {
				ti.SetTaskID(t.TaskID)
			}
		}
		taskCtx := board.NewInjectionContext(ctx, t.Injections)
		taskCtx = board.NewCommentContext(taskCtx, t.Comments)
		if k.store != nil {
			taskCtx = board.WithInjectionSource(taskCtx, func(ctx context.Context) ([]board.Injection, error) {
				fresh, err := k.store.GetTask(ctx, t.TaskID)
				if err != nil {
					return nil, err
				}
				if fresh == nil {
					return nil, fmt.Errorf("задача %s не найдена", t.TaskID)
				}
				return fresh.Injections, nil
			})
			taskCtx = board.WithCommentSource(taskCtx, func(ctx context.Context) (board.Comments, error) {
				fresh, err := k.store.GetTask(ctx, t.TaskID)
				if err != nil {
					return nil, err
				}
				if fresh == nil {
					return nil, fmt.Errorf("задача %s не найдена", t.TaskID)
				}
				return fresh.Comments, nil
			})
		}
		taskCtx = board.WithInjectionScope(taskCtx, board.InjectionScope{Project: k.store.Project(), TaskID: t.TaskID, Role: t.Assignee})
		if t.ModelTier == board.ModelTierLarge {
			taskCtx = models.WithHeavyModel(taskCtx)
		}
		k.markActive(t.TaskID)
		resp, genErr := k.generate(taskCtx, tokens.ScopeTask(t.TaskID), specialist)
		k.unmarkActive(t.TaskID)
		if genErr != nil {
			return false, fmt.Errorf("задача %s: %w", t.TaskID, genErr)
		}
		if resp != nil && resp.Looped {
			done, err := k.escalateLoop(ctx, t, resp.LoopReason)
			if err != nil {
				return false, err
			}
			progress = progress || done
			continue
		}
		if resp != nil && resp.Truncated {
			return false, fmt.Errorf("задача %s: цикл остановлен по лимиту раундов", t.TaskID)
		}
		current, err := k.store.GetTask(ctx, t.TaskID)
		if err != nil {
			return false, fmt.Errorf("задача %s: чтение статуса после работы: %w", t.TaskID, err)
		}
		switch current.Status {
		case board.StatusDone:
			if err := k.finalizeTaskTokens(ctx, t.TaskID); err != nil {
				k.log.Warnf("[учёт токенов] задача %s: %v", t.TaskID, err)
			}
			progress = true
			continue
		case board.StatusHumanHelp, board.StatusCancelled, board.StatusTesting:
			progress = true
			continue
		default:
			progress = true
			continue
		}
	}
	return progress, nil
}

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
