package models

import (
	"ai/agents"
	"context"
	"encoding/json"
	"log"
	"os"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/ollama"
)

type OllamaProvider struct {
	client *ollama.LLM
	model  string
}

func NewOllamaProvider(model string) (*OllamaProvider, error) {
	llmModelName := os.Getenv("LLM")

	client, err := ollama.New(
		ollama.WithServerURL("http://localhost:11434"),
		ollama.WithModel(llmModelName),
	)
	if err != nil {
		log.Fatalf("Ошибка инициализации Ollama: %v", err)
	}

	return &OllamaProvider{client: client, model: model}, nil
}

func (o *OllamaProvider) Generate(ctx context.Context, agent agents.Agent) (*AgentResponse, error) {
	// Перевод tools
	var ollamaTools []llms.Tool
	for _, t := range agent.GetTools() {
		ollamaTools = append(ollamaTools, llms.Tool{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
				Strict:      true,
			},
		})
	}

	// Перевод prompt
	var messages []llms.MessageContent
	var userMessage string
	for _, m := range agent.GetMessages() {
		switch m.Type {
		case agents.MessageTypeAI:
			messages = append(messages, llms.TextParts(llms.ChatMessageTypeAI, m.Message))
		case agents.MessageTypeFunction:
			messages = append(messages, llms.TextParts(llms.ChatMessageTypeFunction, m.Message))
		case agents.MessageTypeTool:
			messages = append(messages, llms.TextParts(llms.ChatMessageTypeTool, m.Message))
		case agents.MessageTypeGeneric:
			messages = append(messages, llms.TextParts(llms.ChatMessageTypeGeneric, m.Message))
		case agents.MessageTypeSystem:
			messages = append(messages, llms.TextParts(llms.ChatMessageTypeSystem, m.Message))
		case agents.MessageTypeHuman:
			messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, m.Message))
			userMessage = userMessage + "; " + m.Message
		}
	}

	// Выполнение запроса
	for {
		resp, err := o.client.GenerateContent(ctx, messages, llms.WithTools(ollamaTools), llms.WithToolChoice("required"))
		if err != nil {
			log.Fatalf("Ошибка генерации контента моделью Qwen: %v", err)
			return nil, err
		}

		choice := resp.Choices[0]

		// Если модель решила продолжить текстом и больше не вызывает функции, завершаем работу
		if len(choice.ToolCalls) == 0 {
			return &AgentResponse{Content: choice.Content}, nil
		}

		// Добавляем ответ модели в историю сообщений
		messages = append(messages, llms.MessageContent{
			Role:  llms.ChatMessageTypeAI,
			Parts: []llms.ContentPart{llms.TextPart(choice.Content)},
		})

		// Обрабатываем каждый вызов инструмента от LLM
		for _, toolCall := range choice.ToolCalls {
			functionName := toolCall.FunctionCall.Name
			var functionArgs map[string]any
			if err := json.Unmarshal([]byte(toolCall.FunctionCall.Arguments), &functionArgs); err != nil {
				log.Fatalf("Ошибка при разборе аргументов: %v", err)
				return nil, err
			}

			var resultJSON []byte

			resultJSON, err := agent.CallFunction(functionName, functionArgs)
			if err != nil {
				log.Fatalf("Ошибка при выполнении инструмента: %v", err)
				return nil, err
			}

			// Возвращаем результат выполнения инструмента обратно в контекст LLM
			messages = append(messages, llms.MessageContent{
				Role: llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{
					llms.ToolCallResponse{
						ToolCallID: toolCall.ID,
						Name:       toolCall.FunctionCall.Name,
						Content:    string(resultJSON),
					},
				},
			})
		}
	}
}
