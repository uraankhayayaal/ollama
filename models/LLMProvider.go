package models

import (
	"ai/agents"
	"ai/tools"
	"context"
)

// LLMProvider — единый интерфейс для моделей
type LLMProvider interface {
	Generate(ctx context.Context, agent agents.Agent) (*AgentResponse, error)
}

// AgentResponse содержит ответ модели и запрос на вызов функции
type AgentResponse struct {
	Content   string
	ToolCalls []tools.ToolCall
}
