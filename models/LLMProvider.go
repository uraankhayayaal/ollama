package models

import (
	"ai/agents"
	"ai/runner"
	"context"
)

// LLMProvider — единый интерфейс для моделей
type LLMProvider interface {
	Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error)
	GetEmbedded(ctx context.Context) ([][]float64, error)
	GetModelName(ctx context.Context) string
}

