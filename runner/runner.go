package runner

import (
	"ai/agents"
	"ai/tools"
	"context"
	"fmt"
)

// AgentResponse содержит ответ модели и все выполненные вызовы функций.
type AgentResponse struct {
	Content   string
	ToolCalls []tools.ToolCall
}

// Message — нейтральное представление сообщения диалога,
// провайдеры конвертируют его в свой формат.
type Message struct {
	Role       string // "system" | "user" | "assistant" | "tool"
	Content    string
	ToolCalls  []tools.ToolCall // для assistant
	ToolName   string           // для tool
	ToolCallID string           // для tool
}

// ModelReply — ответ модели за один раунд.
type ModelReply struct {
	Content      string
	ToolCalls    []tools.ToolCall
	FinishReason string
}

// ChatProvider — провайдер, умеющий сделать ОДИН запрос к модели.
type ChatProvider interface {
	ChatOnce(ctx context.Context, agent agents.Agent, messages []Message) (*ModelReply, error)
}

// maxRounds ограничивает количество итераций выполнения инструментов,
// чтобы защититься от бесконечного цикла "модель -> инструмент".
const maxRounds = 12

// Generate выполняет агентский цикл: отправляет диалог модели, исполняет
// запрошенные инструменты, возвращает результат модели обратно в историю
// и повторяет, пока модель не завершит ответ (нет tool_calls).
func Generate(ctx context.Context, provider ChatProvider, agent agents.Agent) (*AgentResponse, error) {
	var messages []Message
	for _, m := range agent.GetAgentMemoryMessages(nil) {
		messages = append(messages, Message{Role: "system", Content: m.Message})
	}
	for _, m := range agent.GetMessages() {
		messages = append(messages, Message{Role: "user", Content: m.Message})
	}

	var allToolCalls []tools.ToolCall
	content := ""

	for round := 0; round < maxRounds; round++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("контекст отменён до раунда %d: %w", round+1, err)
		}

		reply, err := provider.ChatOnce(ctx, agent, messages)
		if err != nil {
			return nil, err
		}

		if len(reply.ToolCalls) == 0 {
			content = reply.Content
			Debugf("RUNNER: раунд %d: модель завершила (finish_reason=%q), content=%q",
				round+1, reply.FinishReason, Truncate(content, 300))
			return &AgentResponse{Content: content, ToolCalls: allToolCalls}, nil
		}

		Debugf("RUNNER: раунд %d: модель запросила %d вызова(ов), finish_reason=%q",
			round+1, len(reply.ToolCalls), reply.FinishReason)

		messages = append(messages, Message{Role: "assistant", Content: reply.Content, ToolCalls: reply.ToolCalls})
		allToolCalls = append(allToolCalls, reply.ToolCalls...)

		for _, tc := range reply.ToolCalls {
			Debugf("RUNNER: выполняю инструмент %q args=%s", tc.Name, Truncate(tc.Arguments, 500))

			args, err := tools.ParseArguments(tc.Arguments)
			if err != nil {
				return nil, fmt.Errorf("разбор аргументов инструмента %s: %w", tc.Name, err)
			}

			result, err := agent.CallFunction(tc.Name, args)
			if err != nil {
				Debugf("RUNNER: инструмент %q вернул ошибку: %v", tc.Name, err)
				return nil, fmt.Errorf("выполнение инструмента %s: %w", tc.Name, err)
			}

			Debugf("RUNNER: результат инструмента %q: %s", tc.Name, Truncate(string(result), 500))
			messages = append(messages, Message{
				Role:       "tool",
				ToolName:   tc.Name,
				ToolCallID: tc.ID,
				Content:    string(result),
			})
		}
	}

	Debugf("RUNNER: достигнут лимит раундов (%d), возвращаю частичный результат", maxRounds)
	return &AgentResponse{Content: content, ToolCalls: allToolCalls}, nil
}