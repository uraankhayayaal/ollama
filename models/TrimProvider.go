package models

import (
	"ai/agents"
	"ai/runner"
	"ai/tools"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type TrimProvider struct {
	client  *http.Client
	baseURL string
	apiKey  string
	model   string
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatResponse struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content   string         `json:"content"`
			ToolCalls []chatToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

// trimMaxTokens — лимит выходных токенов для Trim. Задаётся переменной
// окружения TRIM_MAX_TOKENS.
func trimMaxTokens() int {
	if n := os.Getenv("TRIM_MAX_TOKENS"); n != "" {
		return atoiDefault(n, 4000)
	}
	return 4000
}

func NewTrimProvider() (*TrimProvider, error) {
	apiKey := os.Getenv("TRIM_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("TRIM_API_KEY environment variable is not set")
	}

	trimURL := os.Getenv("TRIM_HOST")
	if trimURL == "" {
		trimURL = "https://vllm-app.rc.itops.su/v1"
	}

	model := os.Getenv("TRIM_MODEL")
	if model == "" {
		model = "/models/T-pro-it-1.0"
	}

	return &TrimProvider{
		client:  &http.Client{Timeout: 5 * time.Minute},
		baseURL: strings.TrimSuffix(trimURL, "/"),
		apiKey:  apiKey,
		model:   model,
	}, nil
}

func (t *TrimProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	return runner.Generate(ctx, t, agent)
}

// ChatOnce выполняет один запрос к модели Trim через обычный HTTP.
func (t *TrimProvider) ChatOnce(ctx context.Context, agent agents.Agent, msgs []runner.Message) (*runner.ModelReply, error) {
	// forceTool — обязательный инструмент первого раунда: если агент требует
	// вызвать конкретный инструмент и он ещё не был вызван (нет сообщений роли
	// "tool"), передаём tools и форсируем его через tool_choice (named function).
	// В остальных раундах tools не передаём совсем: сервер требует флаг
	// --enable-auto-tool-choice, чтобы модель сама выбирала инструменты,
	// поэтому свободного выбора здесь нет — модель завершает цикл текстом.
	forceTool := ""
	if req, ok := agent.(runner.ToolRequiringAgent); ok {
		if name, yes := req.RequiredToolFirstRound(); yes && name != "" && !hasToolResult(msgs) {
			forceTool = name
		}
	}

	var toolsParam []map[string]any
	if forceTool != "" {
		for _, tool := range agent.GetTools() {
			toolsParam = append(toolsParam, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        tool.Name,
					"description": tool.Description,
					"parameters":  tool.Parameters,
					"strict":      true,
				},
			})
		}
	}

	// Перевод нейтральных сообщений в формат OpenAI
	var messages []chatMessage
	for _, m := range msgs {
		switch m.Role {
		case "system":
			messages = append(messages, chatMessage{Role: "system", Content: m.Content})
		case "user":
			messages = append(messages, chatMessage{Role: "user", Content: m.Content})
		case "tool":
			messages = append(messages, chatMessage{Role: "tool", Content: m.Content, ToolCallID: m.ToolCallID})
		case "assistant":
			am := chatMessage{Role: "assistant", Content: m.Content}
			for _, tc := range m.ToolCalls {
				am.ToolCalls = append(am.ToolCalls, chatToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: tc.Name, Arguments: tc.Arguments},
				})
			}
			messages = append(messages, am)
		}
	}

	// Выполнение запроса
	runner.Debugf("TRIM: запрос к модели %q (сообщений: %d, инструментов: %d, force_tool=%q)",
		t.model, len(messages), len(toolsParam), forceTool)
	for i, m := range messages {
		runner.Debugf("TRIM: messages[%d] role=%q content=%q", i, m.Role, runner.Truncate(m.Content, 300))
	}
	if len(toolsParam) > 0 {
		for _, tool := range toolsParam {
			runner.Debugf("TRIM: tool=%q", tool["function"].(map[string]any)["name"])
		}
	}

	req := map[string]any{
		"model":                 t.model,
		"messages":              messages,
		"max_completion_tokens": trimMaxTokens(),
	}
	if forceTool != "" {
		req["tools"] = toolsParam
		req["tool_choice"] = map[string]any{
			"type":     "function",
			"function": map[string]any{"name": forceTool},
		}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("Ошибка сериализации запроса: %w", err)
	}

	var chat chatResponse
	if err := t.do(ctx, "/chat/completions", body, &chat); err != nil {
		return nil, err
	}

	// Пустой ответ модели — защита от паники ниже
	if len(chat.Choices) == 0 {
		runner.DebugJSON("TRIM: пустой срез Choices, полный ответ", chat)
		return nil, fmt.Errorf("Модель не вернула ни одного ответа")
	}

	// Ответ модели
	choice := chat.Choices[0]
	message := choice.Message
	runner.Debugf(
		"TRIM: ответ choice[0] finish_reason=%q content=%q tool_calls=%d",
		choice.FinishReason, runner.Truncate(message.Content, 300),
		len(message.ToolCalls),
	)
	runner.DebugCheckEmpty("trim", choice.FinishReason, message.Content, len(message.ToolCalls), chat)

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
		FinishReason: choice.FinishReason,
	}, nil
}

func (t *TrimProvider) GetModelName(ctx context.Context) string {
	return t.model
}

// do выполняет POST-запрос к указанному пути с JSON-телом и разбирает ответ.
func (t *TrimProvider) do(ctx context.Context, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("Ошибка создания запроса: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.apiKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("Ошибка при выполнении запроса: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("Ошибка чтения ответа: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Сервер вернул статус %d: %s", resp.StatusCode, string(respBody))
	}

	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("Ошибка разбора ответа: %w", err)
	}

	return nil
}
