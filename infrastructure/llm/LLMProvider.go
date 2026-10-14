package llm

import (
	"ai/agents"
	"ai/domain"
	"context"
)

// LLMProvider — единый интерфейс для моделей
type LLMProvider interface {
	Generate(ctx context.Context, agent agents.Agent) (*domain.AgentResponse, error)
}
