package domain

import "fmt"

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
	StatusPaused     Status = "paused"
)

func (s Status) Terminal() bool {
	switch s {
	case StatusDone, StatusCancelled, StatusHumanHelp:
		return true
	default:
		return false
	}
}

func (s Status) Valid() bool {
	switch s {
	case StatusNew, StatusAnalysis, StatusReady, StatusHumanHelp,
		StatusInProgress, StatusTesting, StatusDone, StatusCancelled, StatusPaused:
		return true
	default:
		return false
	}
}

func (s Status) Label() string {
	switch s {
	case StatusNew:
		return "Новая"
	case StatusAnalysis:
		return "Анализ"
	case StatusReady:
		return "Готова"
	case StatusHumanHelp:
		return "Нужен человек"
	case StatusInProgress:
		return "В работе"
	case StatusTesting:
		return "Тестирование"
	case StatusDone:
		return "Выполнено"
	case StatusCancelled:
		return "Отменена"
	case StatusPaused:
		return "Пауза"
	default:
		return string(s)
	}
}

type StatusError struct {
	From   Status
	To     Status
	Reason string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("недопустимый переход статуса: %s -> %s (%s)", e.From, e.To, e.Reason)
}
