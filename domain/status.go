package domain

import "fmt"

// Status — статус эпика или задачи на Kanban-доске. Порядок колонок:
// new → analysis → ready → human_help → in_progress → testing → done →
// cancelled. Конечный автомат переходов — ValidateTransition.
type Status string

const (
	StatusNew        Status = "new"
	StatusAnalysis   Status = "analysis"
	StatusReady      Status = "ready"
	StatusHumanHelp  Status = "human_help"
	StatusInProgress Status = "in_progress"
	StatusTesting    Status = "testing"
	StatusDone       Status = "done"
	StatusCancelled  Status = "cancelled"
	// StatusPaused — устаревший статус (заменён на human_help), оставлен для
	// совместимости разбора старых снимков доски.
	StatusPaused Status = "paused"
)

// Label возвращает человекочитаемое название статуса (для логов/UI).
func (s Status) Label() string {
	switch s {
	case StatusNew:
		return "новая"
	case StatusAnalysis:
		return "в анализе"
	case StatusReady:
		return "готова к работе"
	case StatusHumanHelp:
		return "помощь человека"
	case StatusInProgress:
		return "в работе"
	case StatusTesting:
		return "на тестирование"
	case StatusDone:
		return "выполнена"
	case StatusCancelled:
		return "отменена"
	case StatusPaused:
		return "пауза"
	default:
		return string(s)
	}
}

// Terminal сообщает, является ли статус завершающим (дальнейшие переходы
// запрещены). «Помощь человека» — боковая ветка, не терминальна.
func (s Status) Terminal() bool {
	return s == StatusDone || s == StatusCancelled
}

// Valid проверяет, что статус известен системе.
func (s Status) Valid() bool {
	switch s {
	case StatusNew, StatusAnalysis, StatusReady, StatusHumanHelp,
		StatusInProgress, StatusTesting, StatusDone, StatusCancelled, StatusPaused:
		return true
	}
	return false
}

// ValidateTransition проверяет допустимость перехода from -> to по конечному
// автомату статусов Kanban-доски:
//
//	новая           -> в анализе, помощь человеку, отменена
//	в анализе       -> готова к работе, помощь человеку, отменена
//	готова к работе -> в работе, помощь человеку, отменена
//	помощь человека -> готова к работе, в работе, отменена
//	в работе        -> готова к работе, на тестирование, помощь человеку,
//	                   выполнена, отменена
//	на тестирование -> в работе, выполнена, помощь человеку, отмена
//	(терминальные: выполнена/отменена переходов не имеют)
//
// «Помощь человека» — боковая ветка: вход из любого нетерминального статуса,
// выход только обратно в работу. Отмена доступна из любого нетерминального
// статуса. Одинаковый статус не считается переходом (идемпотентность).
func ValidateTransition(from, to Status) error {
	if from == to {
		return nil
	}
	if !from.Valid() || !to.Valid() {
		return &StatusError{From: from, To: to, Reason: "неизвестный статус"}
	}
	if !from.Terminal() && (to == StatusHumanHelp || to == StatusCancelled) {
		return nil
	}
	switch from {
	case StatusNew:
		if to == StatusAnalysis {
			return nil
		}
	case StatusAnalysis:
		if to == StatusReady {
			return nil
		}
	case StatusReady:
		if to == StatusInProgress {
			return nil
		}
	case StatusHumanHelp:
		if to == StatusReady || to == StatusInProgress {
			return nil
		}
	case StatusInProgress:
		if to == StatusReady || to == StatusTesting || to == StatusDone {
			return nil
		}
	case StatusTesting:
		if to == StatusInProgress || to == StatusDone {
			return nil
		}
	}
	return &StatusError{From: from, To: to, Reason: "переход не разрешён конечным автоматом"}
}

// StatusError — ошибка некорректного перехода статуса.
type StatusError struct {
	From   Status
	To     Status
	Reason string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("переход статуса %s -> %s запрещён: %s", e.From.Label(), e.To.Label(), e.Reason)
}

// StatusCounts — распределение записей доски по статусам.
type StatusCounts struct {
	Total int
	By    map[Status]int
}
