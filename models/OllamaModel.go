package models

import (
	"ai/agents"
	"ai/runner"
	"ai/tools"
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/ollama/ollama/api"
)

type OllamaProvider struct {
	client *api.Client
	model  string
}

func NewOllamaProvider(model string) (*OllamaProvider, error) {
	// 1. Создаем клиент Ollama (по умолчанию подключается к http://127.0.0.1:11434)
	client, err := api.ClientFromEnvironment()
	if err != nil {
		log.Fatalf("Ошибка инициализации клиента: %v", err)
	}

	return &OllamaProvider{client: client, model: model}, nil
}

func (o *OllamaProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	return runner.Generate(ctx, o, agent)
}

// ChatOnce выполняет один запрос к модели Ollama.
func (o *OllamaProvider) ChatOnce(ctx context.Context, agent agents.Agent, msgs []runner.Message) (*runner.ModelReply, error) {
	ollamaTools := agent.GetToolsForOllama()

	// Перевод нейтральных сообщений в формат Ollama
	var messages []api.Message
	for _, m := range msgs {
		switch m.Role {
		case "tool":
			messages = append(messages, api.Message{
				Role:       "tool",
				Content:    m.Content,
				ToolName:   m.ToolName,
				ToolCallID: m.ToolCallID,
			})
		default:
			am := api.Message{Role: m.Role, Content: m.Content}
			if m.Role == "assistant" && len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					argsMap, err := tools.ParseArguments(tc.Arguments)
					if err != nil {
						return nil, fmt.Errorf("разбор аргументов истории вызова %s: %w", tc.Name, err)
					}
					args := api.NewToolCallFunctionArguments()
					for k, v := range argsMap {
						args.Set(k, v)
					}
					am.ToolCalls = append(am.ToolCalls, api.ToolCall{
						ID: tc.ID,
						Function: api.ToolCallFunction{
							Name:      tc.Name,
							Arguments: args,
						},
					})
				}
			}
			messages = append(messages, am)
		}
	}

	// Флаг для отключения стриминга (false гарантирует атомарный ответ)
	stream := false

	req := &api.ChatRequest{
		Model:    o.model,
		Messages: messages,
		Tools:    ollamaTools,
		Stream:   &stream,
	}

	runner.Debugf("OLLAMA: запрос к модели %q (сообщений: %d, инструментов: %d)", o.model, len(messages), len(ollamaTools))
	for i, m := range messages {
		runner.Debugf("OLLAMA: messages[%d] role=%q content=%q tool_calls=%d", i, m.Role, runner.Truncate(m.Content, 300), len(m.ToolCalls))
	}
	for _, t := range ollamaTools {
		runner.Debugf("OLLAMA: tool=%q", t.Function.Name)
	}

	var content string
	var toolCalls []tools.ToolCall
	var doneReason string

	err := o.client.Chat(ctx, req, func(resp api.ChatResponse) error {
		content = resp.Message.Content
		doneReason = resp.DoneReason

		if len(resp.Message.ToolCalls) > 0 {
			for i, tc := range resp.Message.ToolCalls {
				argsBytes, err := json.Marshal(tc.Function.Arguments)
				if err != nil {
					return fmt.Errorf("ошибка маршалинга аргументов: %w", err)
				}

				// Некоторые модели не заполняют id — генерируем сами,
				// чтобы tool-результат корректно стыковался с вызовом.
				id := tc.ID
				if id == "" {
					id = fmt.Sprintf("call_%d", i+1)
				}

				toolCalls = append(toolCalls, tools.ToolCall{ID: id, Name: tc.Function.Name, Arguments: string(argsBytes)})
				runner.Debugf("OLLAMA: tool_call %q (id=%q) args=%s", tc.Function.Name, id, string(argsBytes))
			}
		}

		runner.Debugf(
			"OLLAMA: сырой ответ done=%v done_reason=%q content=%q thinking=%q",
			resp.Done, resp.DoneReason, runner.Truncate(resp.Message.Content, 300), runner.Truncate(resp.Message.Thinking, 300),
		)
		runner.DebugCheckEmpty("ollama", resp.DoneReason, resp.Message.Content, len(resp.Message.ToolCalls), resp)

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("Ошибка выполнения Chat: %v", err)
	}

	return &runner.ModelReply{Content: content, ToolCalls: toolCalls, FinishReason: doneReason}, nil
}
