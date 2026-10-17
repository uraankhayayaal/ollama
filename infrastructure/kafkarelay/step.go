package kafkarelay

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// uuidPattern — формат UUID, который требует приёмщик (backend/internal/domain).
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// StepStatus — статус шага в терминах приёмщика.
type StepStatus string

const (
	StepStatusSuccess StepStatus = "success"
	StepStatusError   StepStatus = "error"
)

// AgentStep — контракт сообщения в топик agent-harness-metrics. Зеркало
// backend/internal/domain.AgentStep приёмщика: поля и правила валидации
// обязаны совпадать, иначе сообщения будут падать в DLQ на стороне дашборда.
type AgentStep struct {
	RunID        string     `json:"run_id"`
	HarnessName  string     `json:"harness_name"`
	AgentVersion string     `json:"agent_version"`
	StepNumber   int        `json:"step_number"`
	Thought      string     `json:"thought"`
	ToolCalled   string     `json:"tool_called"`
	Status       StepStatus `json:"status"`
	LatencyMs    int        `json:"latency_ms"`
	TokensUsed   int        `json:"tokens_used"`
	CostUSD      float64    `json:"cost_usd"`
}

// Validate повторяет правила приёмщика: строгий UUID run_id, порядковый номер
// с 1, известный статус, неотрицательные числа.
func (s AgentStep) Validate() error {
	if !uuidPattern.MatchString(s.RunID) {
		return fmt.Errorf("run_id должен быть UUID, получено %q", s.RunID)
	}
	if s.StepNumber < 1 {
		return errors.New("step_number должен быть >= 1")
	}
	if s.Status != StepStatusSuccess && s.Status != StepStatusError {
		return fmt.Errorf("неизвестный status: %q", s.Status)
	}
	if s.LatencyMs < 0 || s.TokensUsed < 0 || s.CostUSD < 0 {
		return errors.New("latency_ms, tokens_used и cost_usd не могут быть отрицательными")
	}
	return nil
}

// marshalStep — JSON-сериализация сообщения (payload Kafka).
func marshalStep(s AgentStep) ([]byte, error) {
	return json.Marshal(s)
}
