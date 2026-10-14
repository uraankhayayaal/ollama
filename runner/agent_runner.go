package runner

import (
	"ai/agents"
	"ai/application/usecases"
	"ai/domain"
	"context"
)

// agentRunnerAdapter — адаптер локальной функции Generate к порту
// usecases.AgentRunner: провайдеры моделей вызывают полный агентский цикл
// через usecases.RunAgent и не зависят от пакета runner напрямую.
type agentRunnerAdapter struct{}

func init() { usecases.SetAgentRunner(agentRunnerAdapter{}) }

func (agentRunnerAdapter) Generate(ctx context.Context, provider usecases.ChatProvider, agent agents.Agent) (*domain.AgentResponse, error) {
	return Generate(ctx, provider, agent)
}
