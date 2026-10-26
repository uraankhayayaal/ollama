package planner

import (
	"ai/domain"
	"ai/shared/logging"
	"ai/shared/tokens"
	"context"
	"fmt"
)

// phaseArchitectDecompose — Ф-5 из плана: архитектор разбивает эпики в
// system_analysis на задачи. Задачи создаются через BoardCreateTask со
// статусами infra, test_cases, development и назначенными ролями. Эпик
// остаётся в system_analysis — перейдёт в done автоматически, когда все
// задачи выполнены.
func (k *KanbanRunner) phaseArchitectDecompose(ctx context.Context) (bool, error) {
	epics, err := k.store.ListEpics(ctx)
	if err != nil {
		return false, err
	}

	progress := false
	for _, e := range epics {
		if e.Status != domain.StatusSystemAnalysis {
			continue
		}

		// Проверяем, есть ли ещё задачи для этого эпика.
		tasks, err := k.store.TasksByEpic(ctx, e.TaskID)
		if err != nil {
			return false, fmt.Errorf("эпик %s: чтение задач: %w", e.TaskID, err)
		}

		if len(tasks) > 0 {
			k.log.Infof("[эпик %s] уже есть %d задач — пропускаем декомпозицию", e.TaskID, len(tasks))
			continue
		}

		k.log.Infof("[эпик %s] декомпозиция архитектором: %s", e.TaskID, truncateText(e.Title, 60))

		arch := agentFactory().Architect(k.store.Project(), k.architectDecomposePrompt(e), k.store)
		arch = k.prepareArchitect(arch)

		resp, err := k.generate(ctx, tokens.ScopeEpic(e.TaskID), arch)
		if err != nil {
			return false, fmt.Errorf("эпик %s: декомпозиция архитектором: %w", e.TaskID, err)
		}
		if err := resp.LoopError("декомпозиция эпика " + e.TaskID); err != nil {
			return false, err
		}
		if resp != nil && resp.Truncated {
			return false, fmt.Errorf("эпик %s: декомпозиция остановлена по лимиту раундов", e.TaskID)
		}

		// Проверяем, что агент создал задачи.
		freshTasks, err := k.store.TasksByEpic(ctx, e.TaskID)
		if err != nil {
			return false, fmt.Errorf("эпик %s: чтение задач после декомпозиции: %w", e.TaskID, err)
		}

		if len(freshTasks) == 0 {
			// Fallback: JSON-декомпозиция из ответа.
			dec, err := domain.UnmarshalTasks(resp.Content)
			if err != nil || len(dec) == 0 {
				return false, fmt.Errorf("эпик %s: архитектор не создал задач (доска и JSON-fallback: %w)", e.TaskID, err)
			}

			for i := range dec {
				ts := dec[i]
				if err := k.store.CreateTask(ctx, &domain.Task{
					TaskSpec: ts,
					EpicID:   e.TaskID,
					Assignee: assignee(ts),
				}); err != nil {
					return false, fmt.Errorf("эпик %s: создание задачи %s: %w", e.TaskID, ts.TaskID, err)
				}
			}
			freshTasks, err = k.store.TasksByEpic(ctx, e.TaskID)
			if err != nil {
				return false, err
			}
			k.log.Infof("[эпик %s] декомпозирован на %d задач (JSON-fallback)", e.TaskID, len(freshTasks))
		} else {
			k.log.Infof("[эпик %s] декомпозирован на %d задач (инструменты доски)", e.TaskID, len(freshTasks))
		}

		printArchitectTasks(k.log, e, freshTasks)
		progress = true
	}

	return progress, nil
}

// architectDecomposePrompt формирует задание для архитектора: разбить эпик
// на задачи со статусами infra → test_cases → development.
func (k *KanbanRunner) architectDecomposePrompt(epic *domain.Epic) string {
	return fmt.Sprintf(`Разбьёп эпик на задачи для специалистов. Каждая задача — один чёткий кусок работы.

Проект: %s
Эпик: %s — %s
Описание: %s

Правила декомпозиции:
1. Создай задачи инструментами доски (BoardCreateTask).
2. Каждая задача должна иметь:
   - task_id: уникальный ID (например T-01)
   - title: название
   - description: подробное описание с контрактами
   - epic_id: %s
   - status: один из [infra, test_cases, development]
   - assigned_role: роль исполнителя
   - sequence_order: порядок (1, 2, 3...)
   - can_run_parallel: параллельность
   - dependencies: зависимости (другие task_id или epic_id)

3. Статусы задач и роли:
   - infra + DevOps Engineer: инфраструктура, Docker, Makefile
   - test_cases + QA Engineer: e2e-тесты до кода, stub-комментарии
   - development + Developer (Backend/Frontend): реализация кода

4. Порядок: сначала infra, потом test_cases, потом development. Зависимости: test_cases зависит от infra, development зависит от test_cases.

5. Опиши каждую задачу детально: контракты, сигнатуры, API, структуры.

НЕ создавай эпики — только задачи в рамках этого эпика.`,
		k.store.Project(), epic.TaskID, epic.Title, epic.Description, epic.TaskID)
}

// printArchitectTasks выводит в консоль и лог проекта текущие задачи эпика после
// работы архитектора: ID, статус, исполнитель, заголовок и приоритет/параллельность.
func printArchitectTasks(log *logging.Logger, epic *domain.Epic, tasks []*domain.Task) {
	log.Infof("[Список задач] эпик %s (%s) — %d задач:", epic.TaskID, truncateText(epic.Title, 60), len(tasks))
	for _, t := range tasks {
		log.Infof("  - %s [%s] -> %s | %s (порядок %d, параллельно: %v)",
			t.TaskID, t.Status.Label(), t.Assignee, truncateText(t.Title, 60), t.SequenceOrder.Int(), t.CanRunParallel.Bool())
	}
}
