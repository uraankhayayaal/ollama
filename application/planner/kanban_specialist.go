package planner

import (
	"ai/domain"
	"ai/domain/services"
	"ai/shared/tokens"
	"context"
	"fmt"
)

// statusPair defines a phase that executes tasks in `from` status and
// auto-advances them to `to` after successful completion.
type statusPair struct {
	from domain.Status
	to   domain.Status
}

// executeTasksForStatus — общий цикл: берёт задачи заданного статуса,
// раздаёт специалистам (одна задача на специалиста за цикл), генерирует
// работу, эскалирует при зацикливании и завершает при успехе.
// После успешного завершения задача автоматически продвигается к nextStatus.
// Возвращает true, если хотя бы одна задача продвинута.
func (k *KanbanRunner) executeTasksForStatus(ctx context.Context, status domain.Status, nextStatus domain.Status) (bool, error) {
	tasks, err := k.tasksForStatus(ctx, status)
	if err != nil {
		return false, err
	}
	if len(tasks) == 0 {
		return false, nil
	}

	busy := map[string]bool{}
	progress := false
	for _, t := range tasks {
		// Одна задача на одного специалиста за цикл.
		if busy[t.Assignee] {
			k.log.Detailf("[задача %s] ждёт: специалист %s уже занят в этом цикле", t.TaskID, t.Assignee)
			continue
		}
		busy[t.Assignee] = true

		// Эпик, в котором появилась работающая задача, тоже уходит «в работу».
		k.noteEpicProgress(ctx, t.EpicID)

		specialist, err := k.specialistFor(t)
		if err != nil {
			return false, fmt.Errorf("задача %s: %w", t.TaskID, err)
		}
		// Специалисты получают доступ к доске: смена статуса своей задачи.
		if sb, ok := specialist.(interface {
			SetBoardStore(services.BoardStore)
		}); ok {
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
		// Имя проекта для RAG — всегда до worktree: имя каталога задачи
		// (.wt-task-<проект>-<id>) в индексе не совпадает с именем проекта,
		// и без явного значения CodeSearch идёт по пустому «проекту».
		if k.store != nil {
			if sn, ok := specialist.(interface{ SetProjectName(string) }); ok {
				sn.SetProjectName(k.store.Project())
			}
		}
		// Ф-6 (этап 2): id своей задачи — агенту нужен, чтобы писать
		// состояние раунда (State Tracking) в нужную запись доски.
		if ti, ok := specialist.(interface{ SetTaskID(string) }); ok {
			ti.SetTaskID(t.TaskID)
		}

		k.log.Infof("[задача %s] специалист %s выполняет: %s",
			t.TaskID, t.Assignee, truncateText(t.Title, 60))
		// Промпт-инъекции задачи: снапшот на старте + ЖИВОЙ источник, который
		// перечитывает задачу перед каждым запросом к модели. Благодаря живому
		// источнику правка инъекций (REST/инструмент доски) видна модели со
		// следующего раунда, даже если цикл уже идёт. Также передаём комментарии
		// в контекст для будущей доставки в модель.
		//
		// Контекст собирается в taskCtx, а не в ctx: присваивание в ctx внутри
		// цикла по ready оставляло бы контекст предыдущей задачи следующей.
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
		taskCtx = domain.WithInjectionScope(taskCtx, domain.InjectionScope{
			Project: k.store.Project(),
			TaskID:  t.TaskID,
			Role:    t.Assignee,
		})
		// Требование «эта задача тяжёлая» из доски (Ф-6, этап 4.3): смена модели
		// переживает рестарт сервера, потому что живёт в записи задачи, а не
		// внутри одного вызова Generate.
		if t.ModelTier == domain.ModelTierLarge {
			taskCtx = domain.WithHeavyModel(taskCtx)
			k.log.Detailf("[задача %s] модель: LARGE (требование задачи)", t.TaskID)
		}

		k.markActive(t.TaskID)
		chatName := agentChatNameForKanban(specialist, t.Assignee)
		if rep := services.ReporterFromContext(taskCtx); rep != nil {
			taskCtx = services.WithReporter(taskCtx, rep.WithAgent(chatName))
		}
		resp, genErr := k.generate(taskCtx, tokens.ScopeTask(t.TaskID), specialist)
		k.unmarkActive(t.TaskID)
		if genErr != nil {
			return false, fmt.Errorf("задача %s: %w", t.TaskID, genErr)
		}
		// Специалист зациклился: разрыв петли не помог, модель не поняла свою
		// ошибку и повторяет то же самое. Задачу НЕ помечаем выполненной, но и
		// запуск НЕ роняем (Ф-6, этап 4.4): эскалируем — сильная модель плюс
		// инъекция с диагнозом — и возвращаем задачу в очередь на новый прогон.
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

		// Читаем актуальный статус задачи после работы агента.
		current, err := k.store.GetTask(ctx, t.TaskID)
		if err != nil {
			return false, fmt.Errorf("задача %s: чтение статуса после работы: %w", t.TaskID, err)
		}

		// Агент сам продвинул статус (например, до nextStatus или done).
		// Уважаем его решение.
		if current.Status == nextStatus || current.Status == domain.StatusDone {
			k.log.Infof("[задача %s] в работе → %s (агент подтвердил сам)", t.TaskID, current.Status.Label())
			if current.Status == domain.StatusDone {
				if err := k.finalizeTaskTokens(ctx, t.TaskID); err != nil {
					k.log.Warnf("[учёт токенов] задача %s: %v", t.TaskID, err)
				}
			}
			progress = true
			continue
		}

		// Специалист сам снял задачу с работы (cancelled).
		if current.Status == domain.StatusCancelled {
			reason := truncateText(current.PauseReason, 400)
			if reason == "" {
				reason = "причина не указана"
			}
			k.log.Warnf("[задача %s] специалист снял с работы (cancelled): %s", t.TaskID, reason)
			k.status("[задача %s] специалист снял с работу: %s", t.TaskID, reason)
			progress = true
			continue
		}

		// Агент не продвинул статус: возможно, работа не сделана или агент
		// ожидает. Продвигаем задачу автоматически к следующему статусу —
		// оркестратор выступает fallback-ом, чтобы задача не зависла.
		if err := k.store.SetTaskStatus(ctx, t.TaskID, nextStatus); err != nil {
			_ = err // StatusError — не наш переход, эскалируем
			if _, escErr := k.escalate(ctx, current, escalationDiag{
				Event:  "работа не сделана",
				Detail: "агент не продвинул статус задачи",
				Action: "Сделай работу по существу — внеси правки в проект (файлы в worktree задачи) и зафиксируй их коммитом. Если задача невыполнима — переведи её в cancelled с обязательной причиной.",
			}); escErr != nil {
				return false, fmt.Errorf("задача %s: эскалация пустой работы: %w", t.TaskID, escErr)
			}
		} else {
			k.log.Infof("[задача %s] статус: %s → %s (авто-переход оркестратора)",
				t.TaskID, status.Label(), nextStatus.Label())
		}
		progress = true
	}
	return progress, nil
}

// tasksForStatus возвращает задачи заданного статуса, у которых эпик не
// остановлен, отсортированные по порядку (sequence_order) и ID.
func (k *KanbanRunner) tasksForStatus(ctx context.Context, status domain.Status) ([]*domain.Task, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	var ready []*domain.Task
	for _, t := range tasks {
		if t.Status != status {
			continue
		}
		ok, err := k.epicWorkable(ctx, t.EpicID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		ok, err = k.depsDone(ctx, t)
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

// phaseInfra — итерация 6 плана: DevOps-фаза. Берёт задачи со статусом `infra`,
// выполняет (Docker, Makefile, build-команды), продвигает к `test_cases`.
func (k *KanbanRunner) phaseInfra(ctx context.Context) (bool, error) {
	return k.executeTasksForStatus(ctx, domain.StatusInfra, domain.StatusTestCases)
}

// phaseTestCases — итерация 7 плана: QA-фаза (Test-First). Берёт задачи
// со статусом `test_cases`, выполняет (e2e-тесты, stub-комментарии),
// продвигает к `development`.
func (k *KanbanRunner) phaseTestCases(ctx context.Context) (bool, error) {
	return k.executeTasksForStatus(ctx, domain.StatusTestCases, domain.StatusDevelopment)
}

// phaseDevelopment — итерация 8 плана: Developer-фаза. Берёт задачи со
// статусом `development`, выполняет (реализация кода), продвигает к `done`.
func (k *KanbanRunner) phaseDevelopment(ctx context.Context) (bool, error) {
	return k.executeTasksForStatus(ctx, domain.StatusDevelopment, domain.StatusDone)
}

// phaseExecute — Legacy: используется тестами kanban_injections_test.go.
// В runPhases заменён тремя специализированными фазами (phaseInfra,
// phaseTestCases, phaseDevelopment) — см. итерации 6–8.
