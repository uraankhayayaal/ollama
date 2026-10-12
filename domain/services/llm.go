package services

import (
	"ai/domain"
	"context"
)

type LLMClient interface {
	ChatOnce(ctx context.Context, agentRole domain.AgentRole, messages []domain.Message) (*domain.ModelReply, error)
	ChatStream(ctx context.Context, agentRole domain.AgentRole, messages []domain.Message, onChunk func(domain.StreamChunk)) (*domain.ModelReply, error)
	ModelLimits() domain.ModelLimits
}
