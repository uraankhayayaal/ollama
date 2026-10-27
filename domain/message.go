package domain

import (
	"context"
	"fmt"
)

// Message — нейтральное сообщение диалога (без привязки к провайдеру).
type Message struct {
	Role       string       `json:"role"`
	Content    string       `json:"content"`
	ToolCalls  []ToolCall   `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
	ToolName   string       `json:"tool_name,omitempty"`
}

// ToolCall — вызов инструмента моделью.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ModelReply — ответ модели на раунд.
type ModelReply struct {
	Content      string     `json:"content"`
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"`
	FinishReason string     `json:"finish_reason,omitempty"`
	Usage        *Usage     `json:"usage,omitempty"`
	Truncated    bool       `json:"truncated,omitempty"`
	Rounds       int        `json:"rounds,omitempty"`
}

// Usage — фактическое потребление токенов раунда (от провайдера).
type Usage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	OutputTPS    float64 `json:"output_tps,omitempty"`
}

// AgentResponse — результат выполнения агентского цикла.
type AgentResponse struct {
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	Truncated  bool       `json:"truncated,omitempty"`
	Rounds     int        `json:"rounds,omitempty"`
	Messages   []Message  `json:"messages,omitempty"`
	Looped     bool       `json:"looped,omitempty"`
	LoopReason string     `json:"loop_reason,omitempty"`
}

// StopReason возвращает причину остановки генерации.
func (r *AgentResponse) StopReason() string {
	if r.LoopReason != "" {
		return "зацикливание"
	}
	if r.Truncated {
		return "limit"
	}
	return "stop"
}

// LoopError возвращает ошибку, если агент попал в петлю.
func (r *AgentResponse) LoopError(taskName string) error {
	if !r.Looped {
		return nil
	}
	return fmt.Errorf("агент попал в петлю: %s", r.LoopReason)
}

// RoundState — состояние раунда агента.
type RoundState struct {
	Round     int      `json:"round"`
	ToolCalls []string `json:"tool_calls,omitempty"`
	Touched   []string `json:"touched,omitempty"`
	Committed bool     `json:"committed"`
}

// HeavyModelFor возвращает, нужна ли тяжёлая модель.
func HeavyModelFor(ctx context.Context) bool {
	return false
}
