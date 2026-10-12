package services

import (
	"ai/domain"
	"context"
)

// LLMClient — порт обращения к модели. Реализуется провайдерами (Ollama/
// OpenAI/Layered) в инфраструктуре; application-слой зависит только от этого
// интерфейса и нейтрального domain.ChatRequest (без agents.Agent и tools).
type LLMClient interface {
	ChatOnce(ctx context.Context, req domain.ChatRequest) (*domain.ModelReply, error)
	ChatStream(ctx context.Context, req domain.ChatRequest, onChunk func(domain.StreamChunk)) (*domain.ModelReply, error)
	ModelLimits() domain.ModelLimits
}
