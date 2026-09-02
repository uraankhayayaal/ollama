package models

import (
	"ai/agents"
	"ai/runner"
	"ai/tools"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/shared"
	"github.com/openai/openai-go/shared/constant"
)

// maxTokensOut для Yandex: 1000 токенов не хватало, из-за чего аргументы
// WriteFiles обрезались и доходил только 1 файл. Значение можно задать
// через переменную окружения YANDEX_MAX_TOKENS.
func maxTokensOut() int {
	if n := os.Getenv("YANDEX_MAX_TOKENS"); n != "" {
		return atoiDefault(n, 4000)
	}
	return 4000
}

func atoiDefault(s string, def int) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
	}
	if n == 0 {
		return def
	}
	return n
}

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
	return runner.Generate(ctx, y, agent)
}

// ChatOnce выполняет один запрос к YandexGPT.
func (y *AlisaProvider) ChatOnce(ctx context.Context, agent agents.Agent, msgs []runner.Message) (*runner.ModelReply, error) {
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
	runner.Debugf("YANDEX: запрос к модели %q (сообщений: %d, инструментов: %d)", y.model, len(messages), len(oaTools))
	for i, m := range messages {
		runner.Debugf("YANDEX: messages[%d] role=%q content=%q", i, messageRole(m), runner.Truncate(messageContent(m), 300))
	}
	for _, t := range oaTools {
		runner.Debugf("YANDEX: tool=%q", t.Function.Name)
	}

	response, err := y.client.Chat.Completions.New(
		ctx,
		openai.ChatCompletionNewParams{
			Model:               y.model,
			Messages:            messages,
			Tools:               oaTools,
			ToolChoice:          toolChoice,
			MaxCompletionTokens: openai.Int(int64(maxTokensOut())),

			// ОТКЛЮЧАЕМ THINKING: Передаем "none" для подавления рассуждений,
			// чтобы модель сразу генерировала ответ и не тратила контекст.
			ReasoningEffort: openai.ReasoningEffort("none"),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("Ошибка при выполнении запроса: %w", err)
	}

	// Пустой ответ модели — защита от паники ниже
	if len(response.Choices) == 0 {
		runner.DebugJSON("YANDEX: пустой срез Choices, полный ответ", response)
		return nil, fmt.Errorf("Модель не вернула ни одного ответа")
	}

	// Ответ модели
	message := response.Choices[0].Message
	runner.Debugf(
		"YANDEX: ответ choice[0] finish_reason=%q content=%q tool_calls=%d function_call=%q",
		response.Choices[0].FinishReason, runner.Truncate(message.Content, 300),
		len(message.ToolCalls), message.FunctionCall.Name,
	)
	runner.DebugCheckEmpty("yandex", string(response.Choices[0].FinishReason), message.Content, len(message.ToolCalls), response)

	// Вызов запрошенных моделью функций.
	// Yandex отвечает устаревшим полем function_call (одиночный вызов),
	// а не массивом tool_calls — поддерживаем оба варианта.
	var toolCalls []tools.ToolCall
	for _, toolCall := range message.ToolCalls {
		runner.Debugf("YANDEX: tool_call %q args=%s", toolCall.Function.Name, runner.Truncate(toolCall.Function.Arguments, 300))
		toolCalls = append(toolCalls, tools.ToolCall{
			ID:        toolCall.ID,
			Name:      toolCall.Function.Name,
			Arguments: toolCall.Function.Arguments,
		})
	}

	if len(toolCalls) == 0 && message.FunctionCall.Name != "" {
		runner.Debugf("YANDEX: tool_call через function_call %q args=%s",
			message.FunctionCall.Name, runner.Truncate(message.FunctionCall.Arguments, 300))
		toolCalls = append(toolCalls, tools.ToolCall{
			ID:        fmt.Sprintf("call_%s", message.FunctionCall.Name),
			Name:      message.FunctionCall.Name,
			Arguments: message.FunctionCall.Arguments,
		})
	}

	return &runner.ModelReply{
		Content:      message.Content,
		ToolCalls:    toolCalls,
		FinishReason: string(response.Choices[0].FinishReason),
	}, nil
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

// hasToolResult сообщает, выполнился ли уже хотя бы один инструмент
// (в истории есть сообщение роли "tool"). Используется, чтобы форсировать
// tool_choice только на самом первом (или ещё не выполнившем инструмент) шаге.
func hasToolResult(msgs []runner.Message) bool {
	for _, m := range msgs {
		if m.Role == "tool" {
			return true
		}
	}
	return false
}

// messageContent достаёт текстовое содержимое сообщения из union-типа OpenAI.
func messageContent(m openai.ChatCompletionMessageParamUnion) string {
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Sprintf("<marshal error: %v>", err)
	}

	var v struct {
		Role    json.RawMessage `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(b, &v); err != nil || v.Content == nil {
		return ""
	}

	var s string
	if json.Unmarshal(v.Content, &s) == nil {
		return s
	}
	return string(v.Content)
}

// messageRole достаёт роль сообщения из union-типа OpenAI.
func messageRole(m openai.ChatCompletionMessageParamUnion) string {
	b, err := json.Marshal(m)
	if err != nil {
		return "<unknown>"
	}

	var v struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return "<unknown>"
	}
	return v.Role
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
