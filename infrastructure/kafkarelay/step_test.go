package kafkarelay

import (
	"testing"
)

func TestAgentStepValidate(t *testing.T) {
	ok := AgentStep{
		RunID:        "123e4567-e89b-12d3-a456-426614174000",
		StepNumber:   1,
		Thought:      "test",
		ToolCalled:   "",
		Status:       StepStatusSuccess,
		LatencyMs:    10,
		TokensUsed:   20,
		CostUSD:      0.001,
		HarnessName:  "h",
		AgentVersion: "v",
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("ожидался валидный шаг: %v", err)
	}

	bad := ok
	bad.RunID = "not-a-uuid"
	if err := bad.Validate(); err == nil {
		t.Fatalf("ожидалась ошибка для не-UUID")
	}

	bad = ok
	bad.StepNumber = 0
	if err := bad.Validate(); err == nil {
		t.Fatalf("ожидалась ошибка для StepNumber < 1")
	}

	bad = ok
	bad.Status = "unknown"
	if err := bad.Validate(); err == nil {
		t.Fatalf("ожидалась ошибка для статуса")
	}

	bad = ok
	bad.LatencyMs = -1
	if err := bad.Validate(); err == nil {
		t.Fatalf("ожидалась ошибка для отрицательных")
	}
}
