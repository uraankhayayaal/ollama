package planner

import (
	"ai/domain"
	"ai/domain/services"
	"context"
	"errors"
	"fmt"
)

// phaseReady: задачи в активном статусе (infra/test_cases/development), у которых
// эпик не остановлен, готовы к исполнению. Функция возвращает true, если есть
// готовые к выполнению задачи (phaseExecute возьмёт их в работу).
func (k *KanbanRunner) phaseReady(ctx context.Context) (bool, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return false, err
	}
	for _, t := range tasks {
		if t.Status != domain.StatusInfra && t.Status != domain.StatusTestCases && t.Status != domain.StatusDevelopment {
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
		return true, nil
	}
	return false, nil
}

// readyTasks возвращает задачи в активном статусе (infra/test_cases/development),
// у которых эпик не остановлен/не отменён, отсортированные по порядку (sequence_order) и ID.
func (k *KanbanRunner) readyTasks(ctx context.Context) ([]*domain.Task, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	var ready []*domain.Task
	for _, t := range tasks {
		if t.Status != domain.StatusInfra && t.Status != domain.StatusTestCases && t.Status != domain.StatusDevelopment {
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
func taskPhase(t *domain.Task) string {
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
func (k *KanbanRunner) phasePrereqSatisfied(ctx context.Context, t *domain.Task) (bool, error) {
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
		if other.Status == domain.StatusCancelled {
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
		if required && other.Status != domain.StatusDone {
			return false, nil
		}
	}
	return true, nil
}

// depsDone проверяет, что все зависимости задачи (задачи/эпики доски)
// уже выполнены. При несуществующей зависимости возвращает ошибку доски.
func (k *KanbanRunner) depsDone(ctx context.Context, t *domain.Task) (bool, error) {
	if len(t.Dependencies) == 0 {
		return true, nil
	}
	for _, dep := range t.Dependencies {
		if task, err := k.store.GetTask(ctx, dep); err == nil {
			if task.Status != domain.StatusDone {
				return false, nil
			}
			continue
		} else if !errors.Is(err, services.ErrNotFound) {
			return false, err
		}
		if epic, err := k.store.GetEpic(ctx, dep); err == nil {
			if epic.Status != domain.StatusDone {
				return false, nil
			}
			continue
		} else if !errors.Is(err, services.ErrNotFound) {
			return false, err
		}
		return false, fmt.Errorf("задача %s: зависимость %q не существует на доске", t.TaskID, dep)
	}
	return true, nil
}
