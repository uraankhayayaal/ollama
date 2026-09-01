package models

import (
	"ai/agents"
	"ai/runner"
	"ai/tools"
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

func (o *OllamaProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	// Перевод tools
	ollamaTools := agent.GetToolsForOllama()

	// Перевод prompt
	var messages []api.Message

	// Системное сообщение
	memoryMessages := agent.GetAgentMemoryMessages([]agents.Message{})
	for _, m := range memoryMessages {
		messages = append(messages, api.Message{
			Role:    "system",
			Content: m.Message,
		})
	}

	// Пользвоательское сообщение
	for _, m := range agent.GetMessages() {
		messages = append(messages, api.Message{
			Role:    "user",
			Content: m.Message,
		})
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
	var content string
	var toolCalls []tools.ToolCall
	err := o.client.Chat(ctx, req, func(resp api.ChatResponse) error {
		// 5. Проверяем, хочет ли модель вызвать инструмент
		if len(resp.Message.ToolCalls) > 0 {
			for _, tc := range resp.Message.ToolCalls {
				argsBytes, err := json.Marshal(tc.Function.Arguments)
				if err != nil {
					return fmt.Errorf("ошибка маршалинга аргументов: %w", err)
				}

				toolCalls = append(toolCalls, tools.ToolCall{Name: tc.Function.Name, Arguments: string(argsBytes)})
			}
		} else {
			// Если модель ответила обычным текстом
			fmt.Printf("Текстовый ответ модели: %s\n", resp.Message.Content)
			content = resp.Message.Content
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("Ошибка выполнения Chat: %v", err)
	}

	return runner.RunToolCalls(agent, toolCalls, content)
}

func (o *OllamaProvider) GetEmbedded(ctx context.Context) ([][]float64, error) {
	req := &api.EmbedRequest{
		Model: o.model,
		Input: "Язык программирования Go идеально подходит для микросервисов.",
	}

	resp, err := o.client.Embed(ctx, req)
	if err != nil {
		log.Fatalf("Ошибка генерации вектора: %v", err)
	}

	if len(resp.Embeddings) == 0 {
		return nil, fmt.Errorf("Срез пуст!")
	}

	return convertOllamaMatrix(resp.Embeddings), nil
}

func (o *OllamaProvider) GetModelName(ctx context.Context) string {
	return o.model
}

func convertOllamaMatrix(input [][]float32) [][]float64 {
	if input == nil {
		return nil
	}

	// 1. Выделяем память под внешнюю матрицу
	result := make([][]float64, len(input))

	for i, row := range input {
		if row == nil {
			continue
		}
		// 2. Выделяем память под внутреннюю строку (точного размера)
		result[i] = make([]float64, len(row))

		// 3. Копируем элементы с приведением типа
		for j, val := range row {
			result[i][j] = float64(val)
		}
	}

	return result
}
