package services

import (
	"ai/domain"
	"context"
)

// Agent — доменный порт агента, запускаемого в агентском цикле: промпты,
// доступные инструменты и диспетчер вызовов. Конкретные агенты (agents/*)
// структурно удовлетворяют ему (за счёт алиасов domain.AgentMessage/
// domain.ToolDefinition), поэтому application-слой не зависит от пакета agents.
type Agent interface {
	GetSystemMessages(text []domain.AgentMessage) []domain.AgentMessage
	GetUserMessages() []domain.AgentMessage
	GetTools() []domain.ToolDefinition
	CallFunction(functionName string, functionArgs map[string]any) ([]byte, error)
}

// AgentRunner — порт полного агентского цикла: диалог + инструменты для
// конкретного провайдера и агента. Реализуется инфраструктурой (runner).
type AgentRunner interface {
	Generate(ctx context.Context, provider ChatProvider, agent Agent) (*domain.AgentResponse, error)
}

// AgentCycle — порт полного агентского цикла для конкретного агента (без
// указания провайдера): оркестратор, чат-ассистент и CLI зависят от него, а не
// от конкретного провайдера модели. Реализуется application-слоем
// (usecases.FullCycle) поверх ChatProvider и AgentRunner.
type AgentCycle interface {
	Generate(ctx context.Context, agent Agent) (*domain.AgentResponse, error)
}
