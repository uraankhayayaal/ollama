package domain

import "fmt"

// EntityType — тип записи на доске: эпик или задача.
// Каждая сущность имеет свой конечный автомат переходов (ValidateTransition).
type EntityType string

const (
	EntityTypeEpic EntityType = "epic"
	EntityTypeTask EntityType = "task"
)

// Status — статус эпика или задачи на Kanban-доске.
//
// Эпики: draft → business_review → system_analysis → done (автоматически, когда все задачи done)
// Задачи: infra → test_cases → development → done (строго линейно)
// Конечный автомат переходов — ValidateTransition.
type Status string

const (
	// Эпики
	StatusDraft          Status = "draft"
	StatusBusinessReview Status = "business_review"
	StatusSystemAnalysis Status = "system_analysis"
	// Задачи (Test-First)
	StatusInfra       Status = "infra"
	StatusTestCases   Status = "test_cases"
	StatusDevelopment Status = "development"
	// Общие
	StatusDone      Status = "done"
	StatusCancelled Status = "cancelled"
)

// Label возвращает человекочитаемое название статуса (для логов/UI).
func (s Status) Label() string {
	switch s {
	case StatusDraft:
		return "черновик"
	case StatusBusinessReview:
		return "бизнес-ревью"
	case StatusSystemAnalysis:
		return "системный анализ"
	case StatusInfra:
		return "инфраструктура"
	case StatusTestCases:
		return "написание тестов"
	case StatusDevelopment:
		return "разработка"
	case StatusDone:
		return "выполнена"
	case StatusCancelled:
		return "отменена"
	default:
		return string(s)
	}
}

// Terminal сообщает, является ли статус завершающим (дальнейшие переходы
// запрещены).
func (s Status) Terminal() bool {
	return s == StatusDone || s == StatusCancelled
}

// Valid проверяет, что статус известен системе.
func (s Status) Valid() bool {
	switch s {
	case StatusDraft, StatusBusinessReview, StatusSystemAnalysis,
		StatusInfra, StatusTestCases, StatusDevelopment,
		StatusDone, StatusCancelled:
		return true
	}
	return false
}

// ValidateTransition проверяет допустимость перехода from -> to по конечному
// автомату статусов Kanban-доски.
//
// Эпики:
//
//	draft            -> business_review, cancelled
//	business_review  -> system_analysis, draft, cancelled
//	system_analysis  -> done, cancelled
//	(терминальные: done/cancelled переходов не имеют)
//
// Задачи (строго линейно):
//
//	infra        -> test_cases, cancelled
//	test_cases   -> development, cancelled
//	development  -> done, cancelled
//	(терминальные: done/cancelled переходов не имеют)
//
// Переходов назад нет. Отмена доступна из любого нетерминального статуса.
// Одинаковый статус не считается переходом (идемпотентность).
func ValidateTransition(from, to Status, entityType EntityType) error {
	if from == to {
		return nil
	}
	if !from.Valid() || !to.Valid() {
		return &StatusError{From: from, To: to, EntityType: entityType, Reason: "неизвестный статус"}
	}
	if !from.Terminal() && to == StatusCancelled {
		return nil
	}
	switch entityType {
	case EntityTypeEpic:
		return validateEpic(from, to)
	case EntityTypeTask:
		return validateTask(from, to)
	default:
		return &StatusError{From: from, To: to, EntityType: entityType, Reason: "неизвестный тип сущности"}
	}
}

func validateEpic(from, to Status) error {
	switch from {
	case StatusDraft:
		if to == StatusBusinessReview {
			return nil
		}
	case StatusBusinessReview:
		if to == StatusSystemAnalysis || to == StatusDraft {
			return nil
		}
	case StatusSystemAnalysis:
		if to == StatusDone {
			return nil
		}
	}
	return &StatusError{From: from, To: to, EntityType: EntityTypeEpic, Reason: "переход не разрешён конечным автоматом эпиков"}
}

func validateTask(from, to Status) error {
	switch from {
	case StatusDraft:
		if to == StatusInfra || to == StatusCancelled {
			return nil
		}
	case StatusInfra:
		if to == StatusTestCases || to == StatusCancelled {
			return nil
		}
	case StatusTestCases:
		if to == StatusDevelopment || to == StatusCancelled {
			return nil
		}
	case StatusDevelopment:
		if to == StatusDone || to == StatusCancelled {
			return nil
		}
	}
	return &StatusError{From: from, To: to, EntityType: EntityTypeTask, Reason: "переход не разрешён конечным автоматом задач"}
}

// StatusError — ошибка некорректного перехода статуса.
type StatusError struct {
	From       Status
	To         Status
	EntityType EntityType
	Reason     string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("переход статуса %s (%s) -> %s запрещён: %s", e.From.Label(), e.EntityType, e.To.Label(), e.Reason)
}

// StatusCounts — распределение записей доски по статусам.
type StatusCounts struct {
	Total int
	By    map[Status]int
}
