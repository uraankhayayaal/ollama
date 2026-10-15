package runner

import (
	"ai/application/usecases"
	"ai/domain"
	"ai/domain/services"
	"context"
)

// agentRunnerAdapter — адаптер локальной функции Generate к порту
// services.AgentRunner: application-слой (usecases.RunAgent) вызывает полный
// агентский цикл через порт и не зависит от пакета runner напрямую.
type agentRunnerAdapter struct{}

func init() { usecases.SetAgentRunner(agentRunnerAdapter{}) }

func (agentRunnerAdapter) Generate(ctx context.Context, provider services.ChatProvider, agent services.Agent) (*domain.AgentResponse, error) {
	return Generate(ctx, provider, agent)
}
