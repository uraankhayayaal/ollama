package services

import (
	"ai/domain"
	"context"
)

// LLMClient — порт обращения к модели. Реализуется провайдерами (Ollama/
// OpenAI) в инфраструктуре; application-слой зависит только от этого
// интерфейса и нейтрального domain.ChatRequest (без agents.Agent и tools).
type LLMClient interface {
	ChatOnce(ctx context.Context, req domain.ChatRequest) (*domain.ModelReply, error)
	ChatStream(ctx context.Context, req domain.ChatRequest, onChunk func(domain.StreamChunk)) (*domain.ModelReply, error)
	ModelLimits() domain.ModelLimits
}

// ChatProvider — минимальная способность провайдера: один запрос к модели.
// Провайдер может не уметь стриминг и не сообщать лимиты, поэтому способности
// вынесены в отдельные интерфейсы (проверяются type-assertion).
type ChatProvider interface {
	ChatOnce(ctx context.Context, req domain.ChatRequest) (*domain.ModelReply, error)
}

// StreamChatProvider — опциональное потоковое расширение ChatProvider: отдаёт
// текст ответа по кускам через onChunk.
type StreamChatProvider interface {
	ChatStream(ctx context.Context, req domain.ChatRequest, onChunk func(domain.StreamChunk)) (*domain.ModelReply, error)
}

// ModelLimitsProvider — опциональный интерфейс провайдера: сообщает лимиты
// модели до запроса (для упреждающего сжатия контекста).
type ModelLimitsProvider interface {
	ModelLimits() domain.ModelLimits
}
