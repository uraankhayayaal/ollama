package planner

import (
	"ai/domain"
	"ai/domain/services"
	"ai/shared/tokens"
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

	var newBugs, confirmedBugs []*domain.BugReport
	for _, b := range bugs {
		switch b.Status {
		case domain.BugStatusNew:
			newBugs = append(newBugs, b)
		case domain.BugStatusConfirmed:
			confirmedBugs = append(confirmedBugs, b)
		}
	}

	// Триаж QA Lead: подтвердить или отсечь «нейрослоп».
	if len(newBugs) > 0 {
		qa := agentFactory().Lead(k.store.Project(), "qa", k.bugTriagePrompt(k.store.Project(), newBugs))
		if sb, ok := any(qa).(interface {
			SetBoardStore(services.BoardStore)
		}); ok {
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
		arch := agentFactory().ArchitectBugExpert(k.store.Project(),
			k.bugExpertPrompt(k.store.Project(), confirmedBugs), k.store)
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
		if epic.Status == domain.StatusDone || epic.Status.Terminal() {
			continue
		}
		// Остановленный эпик не финализируем: «помощь человека» — временное
		// состояние, завершение дождётся выхода (вручную).
		if epic.Status == domain.StatusHumanHelp {
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
			for _, tgt := range []domain.Status{
				domain.StatusAnalysis, domain.StatusReady,
				domain.StatusInProgress, domain.StatusDone,
			} {
				if err := k.store.SetEpicStatus(ctx, epic.TaskID, tgt); err == nil {
					advanced = true
					continue
				} else if _, ok := err.(*domain.StatusError); ok {
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
func (k *KanbanRunner) epicTasksDone(ctx context.Context, epic *domain.Epic) (bool, error) {
	if len(epic.Tasks) == 0 {
		return false, nil
	}
	for _, taskID := range epic.Tasks {
		t, err := k.store.GetTask(ctx, taskID)
		if err != nil {
			return false, fmt.Errorf("эпик %s: чтение задачи %s: %w", epic.TaskID, taskID, err)
		}
		if t.Status != domain.StatusDone {
			return false, nil
		}
	}
	return true, nil
}

// noteEpicProgress переводит эпик в «в работе», если он в статусе
// «готова к работе» (а его задачи начали выполняться).
func (k *KanbanRunner) noteEpicProgress(ctx context.Context, epicID string) {
	epic, err := k.store.GetEpic(ctx, epicID)
	if err != nil || epic.Status != domain.StatusReady {
		return
	}
	if err := k.store.SetEpicStatus(ctx, epicID, domain.StatusInProgress); err != nil {
		k.log.Detailf("[эпик %s] в работу: %v", epicID, err)
	}
}

// phaseTesting доводит задачи в статусе "на тестирование" (StatusTesting)
// до "выполнено". QA (или назначенный специалист) берёт задачу из testing,
// доводит до done, либо возвращает замечаниями в работу/human_help.
//
// Фаза обрабатывает остаток очереди приёмки (задачи, оставшиеся в testing с
// прошлого цикла). Свежезавершённые разработчиком задачи тестируются сразу в
// phaseExecute — это не копит их в «На тестировании» и не пускает зависимые
// задачи в работу до влития предыдущей в эпик.
func (k *KanbanRunner) phaseTesting(ctx context.Context) (bool, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return false, err
	}
	progress := false
	busy := map[string]bool{}
	for _, t := range tasks {
		if t.Status != domain.StatusTesting {
			continue
		}
		if busy[t.Assignee] {
			continue
		}
		busy[t.Assignee] = true
		done, err := k.testTask(ctx, t)
		if err != nil {
			return false, err
		}
		progress = progress || done
	}
	return progress, nil
}

// testTask доводит одну задачу в статусе "на тестирование" (StatusTesting) до
// "выполнено": QA (или назначенный специалист) берёт задачу из testing, доводит
// до done, либо возвращает замечаниями в работу/human_help. Возвращает true,
// если работа продвинулась.
func (k *KanbanRunner) testTask(ctx context.Context, t *domain.Task) (bool, error) {
	// Задача остаётся в testing на время работы тестировщика: переход в
	// in_progress означал бы «зависла» для recoverStuckTasks, и при пустом
	// ответе тестировщика задача вернулась бы в ready — цикл вместо
	// приёмки. Статус меняет сам тестировщик (done/in_progress/human_help).
	k.noteEpicProgress(ctx, t.EpicID)
	// Тестировщик перехватывает задачу после разработчика (Этап 7):
	// назначаем QA-инженера независимо от Assignee задачи.
	var specialist services.Agent = agentFactory().Specialist(k.store.Project(), "qa", k.taskPrompt(t))
	if sb, ok := specialist.(interface {
		SetBoardStore(services.BoardStore)
	}); ok {
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
	taskCtx := domain.NewInjectionContext(ctx, t.Injections)
	taskCtx = domain.NewCommentContext(taskCtx, t.Comments)
	if k.store != nil {
		taskCtx = domain.WithInjectionSource(taskCtx, func(ctx context.Context) ([]domain.Injection, error) {
			fresh, err := k.store.GetTask(ctx, t.TaskID)
			if err != nil {
				return nil, err
			}
			if fresh == nil {
				return nil, fmt.Errorf("задача %s не найдена", t.TaskID)
			}
			return fresh.Injections, nil
		})
		taskCtx = domain.WithCommentSource(taskCtx, func(ctx context.Context) (domain.Comments, error) {
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
	taskCtx = domain.WithInjectionScope(taskCtx, domain.InjectionScope{Project: k.store.Project(), TaskID: t.TaskID, Role: t.Assignee})
	if t.ModelTier == domain.ModelTierLarge {
		taskCtx = domain.WithHeavyModel(taskCtx)
	}
	k.markActive(t.TaskID)
	resp, genErr := k.generate(taskCtx, tokens.ScopeTask(t.TaskID), specialist)
	k.unmarkActive(t.TaskID)
	if genErr != nil {
		return false, fmt.Errorf("задача %s: %w", t.TaskID, genErr)
	}
	if resp != nil && resp.Looped {
		return k.escalateLoop(ctx, t, resp.LoopReason)
	}
	if resp != nil && resp.Truncated {
		return false, fmt.Errorf("задача %s: цикл остановлен по лимиту раундов", t.TaskID)
	}
	current, err := k.store.GetTask(ctx, t.TaskID)
	if err != nil {
		return false, fmt.Errorf("задача %s: чтение статуса после работы: %w", t.TaskID, err)
	}
	switch current.Status {
	case domain.StatusDone:
		if err := k.finalizeTaskTokens(ctx, t.TaskID); err != nil {
			k.log.Warnf("[учёт токенов] задача %s: %v", t.TaskID, err)
		}
		return true, nil
	case domain.StatusHumanHelp, domain.StatusCancelled:
		return true, nil
	case domain.StatusTesting:
		// Тестировщик не завершил ра운д: статус остался «на тестирование»
		// (гард отклонил done «phantom done» — в ветке нет коммитов, прогон
		// оборвался, модель ответила текстом). Раньше раунд считался
		// «прогрессом» 和 петля QA↔testing крутилась до 100 раундов, сжигая
		// токены без результата (инцидент FEL-04). Теперь это «работа не
		// сделана»: эскалация в рамках бюджета автономии, при исчерпании —
		// human_help с причиной (см. usecases.Escalate).
		//
		// Особый случай: если задача осталась в testing после того, как
		// тестировщик попытался её проверить, но не смог перевести в done
		// (например, из-за отсутствия golangci-lint или других ограничений
		// окружения), — это не повод для бесконечной эскалации. Переводим
		// задачу в human_help с причиной.
		if current.Attempts > 3 {
			k.setStopReason("Задача зависла в testing: QA-агент не смог завершить приёмку")
			if _, err := k.store.AddTaskComment(ctx, t.TaskID, domain.Comment{
				Author: "system",
				Type:   domain.CommentTypeSystem,
				Body:   "Задача остановлена: QA-агент несколько раз не смог перевести её в done. Возможно, из-за ограничений окружения (golangci-lint недоступен, Docker не запущен) или из-за того, что проверки не пройдены. Требуется ручное вмешательство.",
			}); err != nil {
				k.log.Warnf("[доска] задача %s: комментарий: %v", t.TaskID, err)
			}
			if err := k.store.SetTaskStatus(ctx, t.TaskID, domain.StatusHumanHelp); err != nil {
				k.log.Warnf("[доска] задача %s: статус human_help: %v", t.TaskID, err)
			}
			return true, nil
		}
		done, err := k.escalate(ctx, current, escalationDiag{
			Event:  "работа не сделана",
			Detail: "тестирование не подтвердило результат: задача осталась в «на тестирование» после прогона тестировщика",
			Action: "Сделай раунд по существу — внеси правки в проект (файлы в worktree задачи) и зафиксируй их коммутом. Если задача невыполнима (нужного кода, зависимостей или доступа в проекте нет) — переведи её в human_help инструментом BoardSetTaskStatus с обязательной причиной (reason).",
		})
		if err != nil {
			return false, fmt.Errorf("задача %s: эскалация незавершённого тестирования: %w", t.TaskID, err)
		}
		return done, nil
	case domain.StatusInProgress:
		// Проверим, не происходит ли цикл testing → in_progress
		if !k.checkTestingLoop(ctx, t) {
			// Если цикл обнаружен, останавливаем задачу
			k.setStopReason("Обнаружен цикл между разработчиком и тестировщиком")
			if _, err := k.store.AddTaskComment(ctx, t.TaskID, domain.Comment{
				Author: "system",
				Type:   domain.CommentTypeSystem,
				Body:   "Задача остановлена из-за циклического возврата между разработчиком и тестировщиком. Повторяющиеся замечания не привели к результату. Требуется ручное вмешательство.",
			}); err != nil {
				k.log.Warnf("[доска] цикл задачи %s: комментарий: %v", t.TaskID, err)
			}
			if err := k.store.SetTaskStatus(ctx, t.TaskID, domain.StatusHumanHelp); err != nil {
				k.log.Warnf("[доска] цикл задачи %s: статус human_help: %v", t.TaskID, err)
			}
			return true, nil
		}
		// Если возврат в работу был, то добавим комментарий
		if _, err := k.store.AddTaskComment(ctx, t.TaskID, domain.Comment{
			Author: "qa",
			Type:   domain.CommentTypeQA,
			Body:   "Тестирование показало необходимость доработки. Задача возвращена разработчику для исправления.",
		}); err != nil {
			k.log.Warnf("[доска] тестирование задачи %s: комментарий: %v", t.TaskID, err)
		}
		return true, nil
	default:
		// Если тестировщик оставил задачу с другим статусом (например, в работе)
		// добавим комментарий
		if _, err := k.store.AddTaskComment(ctx, t.TaskID, domain.Comment{
			Author: "qa",
			Type:   domain.CommentTypeQA,
			Body:   "Проведено тестирование. Результаты переданы разработчику для доработки.",
		}); err != nil {
			k.log.Warnf("[доска] тестирование задачи %s: комментарий: %v", t.TaskID, err)
		}
		return true, nil
	}
}

// checkTestingLoop проверяет, не происходит ли цикл testing → in_progress
// с повторяющимися замечаниями. Возвращает false, если цикл обнаружен.
func (k *KanbanRunner) checkTestingLoop(ctx context.Context, task *domain.Task) bool {
	// Если количество попыток больше лимита (3), считаем это циклом
	if task.Attempts > 3 {
		k.log.Infof("[цикл] задача %s достигла лимита попыток возврата (%d), остановка", task.TaskID, task.Attempts)
		return false
	}

	return true
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
