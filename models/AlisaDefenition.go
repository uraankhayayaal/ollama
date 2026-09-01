package models

import (
	"ai/agents"
	"ai/runner"
	"ai/tools"
	"context"
	"fmt"
	"os"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

const MAX_TOKEN_COUNT = 1000

type AlisaProvider struct {
	client openai.Client
	model  string // например, "yandexgpt/latest"
}

func NewAlisaProvider() *AlisaProvider {
	yandexAPIKey := os.Getenv("YANDEX_API_KEY")
	yandexFolderID := os.Getenv("YANDEX_FOLDER_ID")
	yandexModel := os.Getenv("YANDEX_MODEL")

	client := openai.NewClient(
		option.WithAPIKey(yandexAPIKey),
		option.WithBaseURL("https://ai.api.cloud.yandex.net/v1"),
		option.WithHeader("OpenAI-Project", yandexFolderID),
	)
	return &AlisaProvider{client: client, model: fmt.Sprintf("gpt://%s/%s", yandexFolderID, yandexModel)}
}

func (y *AlisaProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	// Перевод tools
	var oaTools []openai.ChatCompletionToolParam
	for _, t := range agent.GetTools() {
		oaTools = append(oaTools, openai.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        t.Name,
				Description: openai.String(t.Description),
				Parameters:  t.Parameters,
				Strict:      openai.Bool(true), // Рекомендуется включать для точного следования схеме
			},
		})
	}

	// Перевод prompt
	var messages []openai.ChatCompletionMessageParamUnion

	// Системное сообщение
	for _, m := range agent.GetAgentMemoryMessages([]agents.Message{}) {
		messages = append(messages, openai.SystemMessage(m.Message))
	}

	// Пользвоательское сообщение
	for _, m := range agent.GetMessages() {
		messages = append(messages, openai.UserMessage(m.Message))
	}

	// Выполнение запроса
	response, err := y.client.Chat.Completions.New(
		context.Background(),
		openai.ChatCompletionNewParams{
			Model:    y.model,
			Messages: messages,
			Tools:    oaTools,
			ToolChoice: openai.ChatCompletionToolChoiceOptionUnionParam{
				OfAuto: openai.String("auto"),
			},
			MaxCompletionTokens: openai.Int(MAX_TOKEN_COUNT),

			// ОТКЛЮЧАЕМ THINKING: Передаем "none" для подавления рассуждений,
			// чтобы модель сразу генерировала ответ и не тратила контекст.
			// ReasoningEffort: openai.ReasoningEffort("none"),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("Ошибка при выполнении запроса: %w", err)
	}

	// Пустой ответ модели — защита от паники ниже
	if len(response.Choices) == 0 {
		return nil, fmt.Errorf("Модель не вернула ни одного ответа")
	}

	// Ответ модели
	message := response.Choices[0].Message

	// Вызов запрошенных моделью функций
	var toolCalls []tools.ToolCall
	for _, toolCall := range message.ToolCalls {
		toolCalls = append(toolCalls, tools.ToolCall{
			Name:      toolCall.Function.Name,
			Arguments: toolCall.Function.Arguments,
		})
	}

	return runner.RunToolCalls(agent, toolCalls, message.Content)
}

func (y *AlisaProvider) GetEmbedded(ctx context.Context) ([][]float64, error) {
	req := openai.EmbeddingNewParams{
		Model: y.model,
		Input: openai.EmbeddingNewParamsInputUnion{
			OfString: openai.String("Язык программирования Go идеально подходит для микросервисов."),
		},
	}

	resp, err := y.client.Embeddings.New(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("Ошибка генерации вектора: %v", err)
	}

	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("Срез пуст!")
	}

	return convertAlisaMatrix(resp.Data), nil
}

func (y *AlisaProvider) GetModelName(ctx context.Context) string {
	return y.model
}

func convertAlisaMatrix(embeddings []openai.Embedding) [][]float64 {
	if len(embeddings) == 0 {
		return nil
	}

	result := make([][]float64, len(embeddings))

	for i, embedding := range embeddings {
		if embedding.Embedding == nil {
			continue
		}

		result[i] = embedding.Embedding
	}

	return result
}
