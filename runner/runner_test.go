package runner

import (
	"ai/agents"
	"ai/tools"
	"context"
	"testing"

	"github.com/ollama/ollama/api"
)

// fakeAgent — минимальная реализация agents.Agent для тестов.
type fakeAgent struct {
	requiredTool string
}

func (f *fakeAgent) GetMessages() []agents.Message {
	return []agents.Message{{Type: agents.MessageTypeHuman, Message: "напиши код"}}
}
func (f *fakeAgent) GetAgentMemoryMessages(text []agents.Message) []agents.Message {
	return []agents.Message{{Type: agents.MessageTypeSystem, Message: "ты агент"}}
}
func (f *fakeAgent) GetTools() []tools.ToolDefinition {
	return []tools.ToolDefinition{{Name: "WriteFiles", Description: "писать файлы"}}
}
func (f *fakeAgent) GetToolsForOllama() []api.Tool { return nil }
func (f *fakeAgent) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	return []byte("ok"), nil
}
func (f *fakeAgent) RequiredToolFirstRound() (string, bool) {
	if f.requiredTool == "" {
		return "", false
	}
	return f.requiredTool, true
}

// fakeChatProvider отдаёт ответы по порядку; если ответы закончились —
// повторяет последний. Счётчик calls растёт на каждый запрос.
type fakeChatProvider struct {
	replies []*ModelReply
	calls   int
}

func (f *fakeChatProvider) ChatOnce(ctx context.Context, agent agents.Agent, messages []Message) (*ModelReply, error) {
	idx := f.calls
	if idx >= len(f.replies) {
		idx = len(f.replies) - 1
	}
	f.calls++
	if idx < 0 || idx >= len(f.replies) {
		return &ModelReply{Content: "", FinishReason: "stop"}, nil
	}
	return f.replies[idx], nil
}

func testGenerate(t *testing.T, agent agents.Agent, provider *fakeChatProvider) *AgentResponse {
	t.Helper()
	resp, err := Generate(context.Background(), provider, agent)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return resp
}

// Модель без обязательного инструмента: первый текстовый ответ завершает цикл.
func TestGenerateEndsOnTextWithoutRequiredTool(t *testing.T) {
	agent := &fakeAgent{} // requiredTool == "", интерфейс не активен
	provider := &fakeChatProvider{
		replies: []*ModelReply{{Content: "текст", FinishReason: "stop"}},
	}

	resp := testGenerate(t, agent, provider)
	if resp.Content != "текст" {
		t.Fatalf("ожидали завершение с текстом, got %q", resp.Content)
	}
	if provider.calls != 1 {
		t.Fatalf("ожидали 1 запрос, got %d", provider.calls)
	}
}

// Обязательный инструмент: если модель не вызвала его текстом первый раз,
// раннер подсказывает и повторяет (не завершает цикл преждевременно).
func TestGenerateNudgesRequiredTool(t *testing.T) {
	agent := &fakeAgent{requiredTool: "WriteFiles"}
	provider := &fakeChatProvider{
		replies: []*ModelReply{
			{Content: "текст без вызова", FinishReason: "stop"},                                              // раунд 1: игнор инструмента
			{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: "{}"}}, FinishReason: "tool_calls"}, // раунд 1 (ретрай): вызвал WriteFiles
			{Content: "готово", FinishReason: "stop"},                                                        // раунд 2: завершил
		},
	}

	resp := testGenerate(t, agent, provider)

	// Запросов: 1-й (текст) + ретрай (WriteFiles) + финал ("готово") = 3.
	if provider.calls != 3 {
		t.Fatalf("ожидали 3 запроса (ретрай после подсказки + завершение), got %d", provider.calls)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "WriteFiles" {
		t.Fatalf("ожидали вызов WriteFiles, got %#v", resp.ToolCalls)
	}
	if resp.Content != "готово" {
		t.Fatalf("ожидали финальный текст после вызова инструмента, got %q", resp.Content)
	}
}

// Модель упорно не вызывает инструмент: после requiredRetries подсказок
// цикл завершается с текстовым ответом (не зацикливается вечно).
func TestGenerateGivesUpRequiredToolAfterRetries(t *testing.T) {
	agent := &fakeAgent{requiredTool: "WriteFiles"}
	// Модель всегда отвечает текстом, никогда не вызывает инструмент.
	provider := &fakeChatProvider{
		replies: []*ModelReply{{Content: "текст", FinishReason: "stop"}},
	}

	resp := testGenerate(t, agent, provider)

	// Запросов: изначальный + requiredRetries подсказок.
	expect := 1 + requiredRetries
	if provider.calls != expect {
		t.Fatalf("ожидали %d запросов, got %d", expect, provider.calls)
	}
	if resp.Content != "текст" {
		t.Fatalf("после исчерпания ретраев должен вернуться последний текст, got %q", resp.Content)
	}
}
