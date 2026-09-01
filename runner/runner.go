package runner

import (
	"ai/agents"
	"ai/tools"
	"fmt"
)

// AgentResponse содержит ответ модели и запрос на вызов функции
type AgentResponse struct {
	Content   string
	ToolCalls []tools.ToolCall
}

// RunToolCalls выполняет запрошенные моделью инструменты через агента
// и собирает итоговый ответ. Провайдеры делегируют сюда общую логику
// исполнения, чтобы не дублировать её.
func RunToolCalls(agent agents.Agent, toolCalls []tools.ToolCall, content string) (*AgentResponse, error) {
	for _, tc := range toolCalls {
		args, err := tools.ParseArguments(tc.Arguments)
		if err != nil {
			return nil, fmt.Errorf("разбор аргументов инструмента %s: %w", tc.Name, err)
		}

		if _, err := agent.CallFunction(tc.Name, args); err != nil {
			return nil, fmt.Errorf("выполнение инструмента %s: %w", tc.Name, err)
		}
	}

	return &AgentResponse{Content: content, ToolCalls: toolCalls}, nil
}
