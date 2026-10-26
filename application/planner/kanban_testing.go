package planner

import (
	"ai/domain"
	"context"
	"fmt"
	"strings"
)

// phaseComplete: эпик становится «выполнен» только после завершения всех его
// задач.
func (k *KanbanRunner) phaseComplete(ctx context.Context) (bool, error) {
	epics, err := k.store.ListEpics(ctx)
	if err != nil {
		return false, err
	}
	progress := false
	for _, epic := range epics {
		if epic.Status.Terminal() {
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
			advanced := false
			for _, tgt := range []domain.Status{
				domain.StatusBusinessReview, domain.StatusSystemAnalysis, domain.StatusDone,
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
				if err := k.finalizeEpicTokens(ctx, epic.TaskID); err != nil {
					k.log.Warnf("[учёт токенов] эпик %s: %v", epic.TaskID, err)
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

// noteEpicProgress переводит эпик по цепочке статусов (его задачи начали
// выполняться).
func (k *KanbanRunner) noteEpicProgress(ctx context.Context, epicID string) {
	epic, err := k.store.GetEpic(ctx, epicID)
	if err != nil || epic.Status.Terminal() {
		return
	}
	for _, tgt := range []domain.Status{
		domain.StatusBusinessReview, domain.StatusSystemAnalysis,
	} {
		if err := k.store.SetEpicStatus(ctx, epicID, tgt); err != nil {
			if _, ok := err.(*domain.StatusError); ok {
				continue
			}
			k.log.Detailf("[эпик %s] продвижение статуса: %v", epicID, err)
			return
		}
	}
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
