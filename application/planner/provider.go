package planner

import (
	"ai/agents"
	"ai/domain"
	"context"
)

// LLMProvider — порт полного агентского цикла для оркестратора плана.
// Реализуются инфраструктурные провайдеры (Ollama/OpenAI/Layered); planner
// зависит только от этого интерфейса, а не от пакета llm.
type LLMProvider interface {
	Generate(ctx context.Context, agent agents.Agent) (*domain.AgentResponse, error)
}
