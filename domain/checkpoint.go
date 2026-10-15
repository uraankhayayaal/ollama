package domain

import (
	"encoding/json"
	"errors"
)

// Типы чекпоинта плана (Этап 8, H5c): состояние выполнения плана сохраняется
// application-слоем (planner) через порт, а конкретное хранилище (Redis) —
// инфраструктура (checkpoint). Домен владеет снимком и статусами, чтобы
// application не зависел от инфраструктурного пакета.
//
// Статусы — нетипизированные строковые константы (как в хранилище): их кладут
// в map[string]string без конвертаций.
const (
	CheckpointPending = "pending"
	CheckpointRunning = "running"
	CheckpointDone    = "done"
	CheckpointFailed  = "failed"
	CheckpointSkipped = "skipped"
)

// ErrCheckpointNotFound — чекпоинт для ключа не найден.
var ErrCheckpointNotFound = errors.New("чекпоинт не найден")

// CheckpointSnapshot — состояние выполнения плана в момент контрольной точки.
type CheckpointSnapshot struct {
	// ProjectName — имя проекта, к которому относится план.
	ProjectName string `json:"project_name"`
	// Summary — короткое описание плана (для логов/UI).
	Summary string `json:"summary"`
	// PlanJSON — план (сериализованный в JSON), как его вернул планировщик.
	PlanJSON json.RawMessage `json:"plan"`
	// Completed — шаги, успешно завершённые к моменту контрольной точки.
	Completed map[string]bool `json:"completed"`
	// Statuses — детальный статус каждого шага (pending/running/done/failed).
	Statuses map[string]string `json:"statuses"`
	// Rounds — количество раундов агентского цикла, потраченных на каждый шаг
	// до момента остановки (для resume после лимита раундов).
	Rounds map[string]int `json:"rounds,omitempty"`
	// Conversations — история диалога агента незавершённого шага, сериализованная
	// в JSON (массив runner.Message). Позволяет при resume продолжить агентский
	// цикл с потраченных раундов, а не начинать шаг заново.
	Conversations map[string]json.RawMessage `json:"conversations,omitempty"`
	// Metrics — детальные метрики выполнения шагов (стадии): каждый шаг — карта
	// ключ→значение (например вердикт приёмки, число изменений контракта, число
	// раундов, список изменённых файлов). Чистая телеметрия для анализа, на
	// resume не влияет.
	Metrics map[string]map[string]any `json:"metrics,omitempty"`
	// Waves — шаги, сгруппированные по волнам параллельности (для resume).
	Waves [][]string `json:"waves,omitempty"`
	// UpdatedAt — время последнего обновления чекпоинта (RFC3339).
	UpdatedAt string `json:"updated_at"`
}
