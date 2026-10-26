package planner

import (
	"ai/domain"
	"ai/domain/services"
	"ai/shared/tokens"
	"context"
	"fmt"
)

// phaseBusinessReview — Ф-4: бизнес-аналитик уточняет требования эпика.
// Берёт эпики в статусе draft (сразу после архитектора) или business_review,
// запускает агента для каждого. Агент уточняет требования и переводит эпик в
// system_analysis через BoardSetEpicStatus.
func (k *KanbanRunner) phaseBusinessReview(ctx context.Context) (bool, error) {
	epics, err := k.store.ListEpics(ctx)
	if err != nil {
		return false, err
	}

	progress := false
	for _, e := range epics {
		switch e.Status {
		case domain.StatusDraft:
			// Эпики, только что созданные архитектором: автоматически
			// переходим к бизнес-ревью.
			if err := k.store.SetEpicStatus(ctx, e.TaskID, domain.StatusBusinessReview); err != nil {
				return false, fmt.Errorf("эпик %s: draft → business_review: %w", e.TaskID, err)
			}
			e.Status = domain.StatusBusinessReview
			k.log.Infof("[эпик %s] draft → business_review (авто-переход)", e.TaskID)

		case domain.StatusBusinessReview:
			// nothing — агент возьмёт в работу ниже

		default:
			continue
		}

		// Бизнес-аналитик уточняет требования эпика.
		k.log.Infof("[эпик %s] бизнес-ревью: %s", e.TaskID, truncateText(e.Title, 60))

		ba := k.businessAnalystFor(e)
		// SetBoardStore может быть не у всех агентов — проверяем тип.
		if setter, ok := ba.(interface{ SetBoardStore(services.BoardStore) }); ok {
			setter.SetBoardStore(k.store)
		}

		resp, err := k.generate(ctx, tokens.ScopeEpic(e.TaskID), ba)
		if err != nil {
			return false, fmt.Errorf("эпик %s: бизнес-ревью: %w", e.TaskID, err)
		}
		if err := resp.LoopError("бизнес-ревью эпика " + e.TaskID); err != nil {
			return false, err
		}
		if resp != nil && resp.Truncated {
			return false, fmt.Errorf("эпик %s: бизнес-ревью остановлен по лимиту раундов", e.TaskID)
		}

		// Проверяем, что агент сменил статус эпика.
		fresh, err := k.store.GetEpic(ctx, e.TaskID)
		if err != nil {
			return false, fmt.Errorf("эпик %s: чтение статуса после бизнес-ревью: %w", e.TaskID, err)
		}

		switch fresh.Status {
		case domain.StatusSystemAnalysis:
			k.log.Infof("[эпик %s] бизнес-ревью → system_analysis (агент подтвердил сам)", e.TaskID)
			progress = true
		case domain.StatusCancelled:
			k.log.Warnf("[эпик %s] бизнес-аналитик отменил эпик", e.TaskID)
			progress = true
		default:
			// Агент не сменил статус — логируем, но не роняем: следующий раунд
			// повторит бизнес-ревью для этого эпика.
			k.log.Warnf("[эпик %s] бизнес-аналитик не сменил статус (остался %s) — повторим на следующем раунде",
				e.TaskID, fresh.Status.Label())
		}
	}

	return progress, nil
}

// businessAnalystFor создаёт агента бизнес-аналитика для эпика.
func (k *KanbanRunner) businessAnalystFor(epic *domain.Epic) services.Agent {
	prompt := k.businessReviewPrompt(epic)
	return agentFactory().BusinessAnalyst(k.store.Project(), prompt, k.store)
}

// businessReviewPrompt формирует задание для бизнес-аналитика.
func (k *KanbanRunner) businessReviewPrompt(epic *domain.Epic) string {
	return fmt.Sprintf(`Уточни требования эпика:
ID: %s
Заголовок: %s
Описание: %s

Задачи:
- Изучи текущее описание эпика.
- Дополни бизнес-контекстом: зачем нужно, какую проблему решает, для кого.
- Добавь пользовательские сценарии (User Stories).
- Добавь приёмочные критерии (Acceptance Criteria): конкретные и проверяемые.
- Дополни нефункциональными требованиями при необходимости.
- Сохрани обновлённое описание через BoardUpdateEpic.
- Переведи эпик в system_analysis через BoardSetEpicStatus.
- Если эпик неактуален — переведи в cancelled.

НЕ пиши код и НЕ создавай задачи — это сделает архитектор.`,
		epic.TaskID, epic.Title, epic.Description)
}
