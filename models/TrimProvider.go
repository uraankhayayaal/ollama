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
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/shared/constant"
)

type TrimProvider struct {
	client openai.Client
	model  string
}

func NewTrimProvider() (*TrimProvider, error) {
	// Get API key from environment variable
	apiKey := os.Getenv("TRIM_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("TRIM_API_KEY environment variable is not set")
	}

	// Use the specified URL from environment variable or default to the provided URL
	trimURL := os.Getenv("TRIM_HOST")
	if trimURL == "" {
		trimURL = "https://vllm-app.rc.itops.su/v1"
	}

	// Create client with Bearer token authentication
	client := openai.NewClient(
		option.WithAPIKey(apiKey),
		option.WithBaseURL(trimURL),
		option.WithHeader("Authorization", "Bearer "+apiKey),
	)

	return &TrimProvider{client: client, model: "/models/T-pro-it-1.0"}, nil
}

func (t *TrimProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	return runner.Generate(ctx, t, agent)
}

// ChatOnce выполняет один запрос к модели Trim.
func (t *TrimProvider) ChatOnce(ctx context.Context, agent agents.Agent, msgs []runner.Message) (*runner.ModelReply, error) {
	// Перевод tools
	var oaTools []openai.ChatCompletionToolParam
	for _, t := range agent.GetTools() {
		oaTools = append(oaTools, openai.ChatCompletionToolParam{
			Function: openai.FunctionDefinitionParam{
				Name:        t.Name,
				Description: openai.String(t.Description),
				Parameters:  t.Parameters,
				Strict:      openai.Bool(true), // Рекомендуется включать для точного следования схеме
			},
		})
	}

	// Перевод нейтральных сообщений в формат OpenAI
	var messages []openai.ChatCompletionMessageParamUnion
	for _, m := range msgs {
		switch m.Role {
		case "system":
			messages = append(messages, openai.SystemMessage(m.Content))

		case "user":
			messages = append(messages, openai.UserMessage(m.Content))

		case "tool":
			messages = append(messages, openai.ToolMessage(m.Content, m.ToolCallID))

		case "assistant":
			am := openai.ChatCompletionAssistantMessageParam{
				Role: constant.Assistant("assistant"),
				Content: openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: param.NewOpt(m.Content),
				},
			}
			for _, tc := range m.ToolCalls {
				am.ToolCalls = append(am.ToolCalls, openai.ChatCompletionMessageToolCallParam{
					ID:   tc.ID,
					Type: constant.Function("function"),
					Function: openai.ChatCompletionMessageToolCallFunctionParam{
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				})
			}
			messages = append(messages, openai.ChatCompletionMessageParamUnion{OfAssistant: &am})
		}
	}

	// tool_choice: если агент требует обязательный инструмент и он ещё не был
	// вызван (нет сообщений роли "tool"), форсируем вызов инструмента
	// (tool_choice="required"), чтобы модель не отвечала текстом вместо кода.
	toolChoice := openai.ChatCompletionToolChoiceOptionUnionParam{
		OfAuto: openai.String("auto"),
	}
	if req, ok := agent.(runner.ToolRequiringAgent); ok {
		if name, yes := req.RequiredToolFirstRound(); yes && name != "" && !hasToolResult(msgs) {
			toolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{
				OfChatCompletionNamedToolChoice: &openai.ChatCompletionNamedToolChoiceParam{
					Type: constant.Function("function"),
					Function: openai.ChatCompletionNamedToolChoiceFunctionParam{
						Name: name,
					},
				},
			}
		}
	}

	// Выполнение запроса
	runner.Debugf("TRIM: запрос к модели %q (сообщений: %d, инструментов: %d)", t.model, len(messages), len(oaTools))
	for i, m := range messages {
		runner.Debugf("TRIM: messages[%d] role=%q content=%q", i, messageRole(m), runner.Truncate(messageContent(m), 300))
	}
	for _, t := range oaTools {
		runner.Debugf("TRIM: tool=%q", t.Function.Name)
	}

	response, err := t.client.Chat.Completions.New(
		ctx,
		openai.ChatCompletionNewParams{
			Model:               t.model,
			Messages:            messages,
			Tools:               oaTools,
			ToolChoice:          toolChoice,
			MaxCompletionTokens: openai.Int(4000),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("Ошибка при выполнении запроса: %w", err)
	}

	// Пустой ответ модели — защита от паники ниже
	if len(response.Choices) == 0 {
		runner.DebugJSON("TRIM: пустой срез Choices, полный ответ", response)
		return nil, fmt.Errorf("Модель не вернула ни одного ответа")
	}

	// Ответ модели
	message := response.Choices[0].Message
	runner.Debugf(
		"TRIM: ответ choice[0] finish_reason=%q content=%q tool_calls=%d function_call=%q",
		response.Choices[0].FinishReason, runner.Truncate(message.Content, 300),
		len(message.ToolCalls), message.FunctionCall.Name,
	)
	runner.DebugCheckEmpty("trim", string(response.Choices[0].FinishReason), message.Content, len(message.ToolCalls), response)

	// Вызов запрошенных моделью функций.
	var toolCalls []tools.ToolCall
	for _, toolCall := range message.ToolCalls {
		runner.Debugf("TRIM: tool_call %q args=%s", toolCall.Function.Name, runner.Truncate(toolCall.Function.Arguments, 300))
		toolCalls = append(toolCalls, tools.ToolCall{
			ID:        toolCall.ID,
			Name:      toolCall.Function.Name,
			Arguments: toolCall.Function.Arguments,
		})
	}

	return &runner.ModelReply{
		Content:      message.Content,
		ToolCalls:    toolCalls,
		FinishReason: string(response.Choices[0].FinishReason),
	}, nil
}

func (t *TrimProvider) GetEmbedded(ctx context.Context) ([][]float64, error) {
	req := openai.EmbeddingNewParams{
		Model: t.model,
		Input: openai.EmbeddingNewParamsInputUnion{
			OfString: openai.String("Язык программирования Go идеально подходит для микросервисов."),
		},
	}

	resp, err := t.client.Embeddings.New(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("Ошибка генерации вектора: %v", err)
	}

	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("Срез пуст!")
	}

	return convertTrimMatrix(resp.Data), nil
}

func (t *TrimProvider) GetModelName(ctx context.Context) string {
	return t.model
}

func convertTrimMatrix(embeddings []openai.Embedding) [][]float64 {
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
