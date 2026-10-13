package planner

import (
	"ai/domain"
	"ai/domain/services"
	"context"
	"errors"
	"fmt"
)

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
		if t.Status != domain.StatusNew {
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
		if err := k.store.SetTaskStatus(ctx, t.TaskID, domain.StatusAnalysis); err != nil {
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
		if t.Status != domain.StatusAnalysis {
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
		if err := k.store.SetTaskStatus(ctx, t.TaskID, domain.StatusReady); err != nil {
			return false, fmt.Errorf("задача %s: в анализе -> готова к работе: %w", t.TaskID, err)
		}
		k.log.Infof("[задача %s] в анализе → готова к работе", t.TaskID)
		progress = true
	}
	return progress, nil
}

// readyTasks возвращает задачи «готова к работе» (эпик не остановлен/не
// отменён), отсортированные по порядку (sequence_order) и ID.
func (k *KanbanRunner) readyTasks(ctx context.Context) ([]*domain.Task, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	var ready []*domain.Task
	for _, t := range tasks {
		if t.Status != domain.StatusReady {
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
