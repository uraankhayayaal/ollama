package models

import (
	"ai/agents"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/ollama/ollama/api"
)

type OllamaProvider struct {
	client *api.Client
	model  string
}

func NewOllamaProvider(model string) (*OllamaProvider, error) {
	llmModelName := os.Getenv("OLLAMA_MODEL")

	// 1. Создаем клиент Ollama (по умолчанию подключается к http://127.0.0.1:11434)
	client, err := api.ClientFromEnvironment()
	if err != nil {
		log.Fatalf("Ошибка инициализации клиента: %v", err)
	}

	return &OllamaProvider{client: client, model: llmModelName}, nil
}

func (o *OllamaProvider) Generate(ctx context.Context, agent agents.Agent) (*AgentResponse, error) {
	// Перевод tools
	ollamaTools := agent.GetToolsForOllama()

	// Перевод prompt
	var messages []api.Message
	for _, m := range agent.GetMessages() {
		switch m.Type {
		case agents.MessageTypeAI:
			messages = append(messages, api.Message{
				Role:    "ai",
				Content: m.Message,
			})
		case agents.MessageTypeFunction:
			messages = append(messages, api.Message{
				Role:    "function",
				Content: m.Message,
			})
		case agents.MessageTypeTool:
			messages = append(messages, api.Message{
				Role:    "tool",
				Content: m.Message,
			})
		case agents.MessageTypeGeneric, agents.MessageTypeSystem:
			messages = append(messages, api.Message{
				Role:    "system",
				Content: m.Message,
			})
		case agents.MessageTypeHuman:
			messages = append(messages, api.Message{
				Role:    "user",
				Content: m.Message,
			})
		}
	}
	messages = append(messages, api.Message{
		Role:    "system",
		Content: "Используй перечисленные инструменты",
	})

	// Флаг для отключения стриминга (false гарантирует атомарный ответ)
	stream := false

	// 4. Отправляем запрос к модели (например, llama3 или qwen2.5)
	req := &api.ChatRequest{
		Model:    o.model, // Убедитесь, что модель на вашем ПК поддерживает Tools
		Messages: messages,
		Tools:    ollamaTools,
		Stream:   &stream,
	}

	// Функция-колбэк для обработки ответа
	var Result string
	var responseError error
	err := o.client.Chat(ctx, req, func(resp api.ChatResponse) error {
		// 5. Проверяем, хочет ли модель вызвать инструмент
		if len(resp.Message.ToolCalls) > 0 {
			for _, tc := range resp.Message.ToolCalls {
				functionName := tc.Function.Name
				// var functionArgs map[string]any
				// Аргументы приходят в виде map[string]interface{}
				// Для удобства переведем их в JSON и распарсим в нашу структуру
				argsBytes, err := json.Marshal(tc.Function.Arguments)
				if err != nil {
					responseError = fmt.Errorf("ошибка маршалинга аргументов: %w", err)
					return nil
				}
				var functionArgs map[string]any
				if err := json.Unmarshal(argsBytes, &functionArgs); err != nil {
					responseError = fmt.Errorf("ошибка парсинга аргументов: %w", err)
					return nil
				}

				var resultJSON []byte

				resultJSON, err = agent.CallFunction(functionName, functionArgs)
				if err != nil {
					log.Fatalf("Ошибка при выполнении инструмента: %v", err)
					return err
				}

				fmt.Printf("-> Результат выполнения функции: %s\n", string(resultJSON))
				Result = string(resultJSON)
			}
		} else {
			// Если модель ответила обычным текстом
			fmt.Printf("Текстовый ответ модели: %s\n", resp.Message.Content)
			Result = resp.Message.Content
		}

		return nil
	})

	if err != nil {
		log.Fatalf("Ошибка выполнения Chat: %v", err)
	}
	if responseError != nil {
		log.Fatalf("Ошибка внутри колбэка: %v", responseError)
	}

	return &AgentResponse{Content: Result}, nil
}
