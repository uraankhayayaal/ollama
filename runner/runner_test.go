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

func (f *fakeAgent) GetUserMessages() []agents.Message {
	return []agents.Message{{Type: agents.MessageTypeHuman, Message: "напиши код"}}
}
func (f *fakeAgent) GetSystemMessages(text []agents.Message) []agents.Message {
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

// Модель исчерпала лимит токенов генерации (finish_reason="length") и вернула
// пустой ответ без инструментов: ответ должен быть помечен Truncated, чтобы
// вызывающий код отличал «модель закончила» от «модель обрезалась».
func TestGenerateLengthMarksTruncated(t *testing.T) {
	agent := &fakeAgent{requiredTool: "WriteFiles"}
	// Модель всегда обрезается по лимиту и ничего не вызывает.
	provider := &fakeChatProvider{
		replies: []*ModelReply{{Content: "", FinishReason: "length"}},
	}

	resp := testGenerate(t, agent, provider)

	if !resp.Truncated {
		t.Fatal("ответ с finish_reason=length должен быть помечен Truncated")
	}
	if resp.Content != "" {
		t.Fatalf("содержимое ожидалось пустым, got %q", resp.Content)
	}
	// Запросов: изначальный + requiredRetries подсказок (пока не вызовет
	// инструмент) — как и при текстовом игноре.
	expect := 1 + requiredRetries
	if provider.calls != expect {
		t.Fatalf("ожидали %d запросов, got %d", expect, provider.calls)
	}
}

// recordingProvider записывает сообщения, которые получает модель, дополнительно
// к поведению fakeChatProvider.
type recordingProvider struct {
	fakeChatProvider
	received [][]Message
}

func (r *recordingProvider) ChatOnce(ctx context.Context, agent agents.Agent, messages []Message) (*ModelReply, error) {
	cp := make([]Message, len(messages))
	copy(cp, messages)
	r.received = append(r.received, cp)
	return r.fakeChatProvider.ChatOnce(ctx, agent, messages)
}

// После лимита раундов Generate должен вернуть полную историю диалога и
// количество потраченных раундов, чтобы вызывающий код мог сохранить их
// в чекпоинт для resume.
func TestGenerateRoundLimitExposesHistory(t *testing.T) {
	agent := &fakeAgent{}
	// Модель на каждый вызов просит инструмент — цикл упрётся в лимит раундов.
	provider := &fakeChatProvider{
		replies: []*ModelReply{{
			Content:      "",
			FinishReason: "tool_calls",
			ToolCalls:    []tools.ToolCall{{ID: "c1", Name: "WriteFiles", Arguments: "{}"}},
		}},
	}

	resp := testGenerate(t, agent, provider)

	if !resp.Truncated {
		t.Fatal("ожидали Truncated после исчерпания лимита раундов")
	}
	if resp.Rounds != maxRounds() {
		t.Fatalf("ожидали Rounds=%d, got %d", maxRounds(), resp.Rounds)
	}
	if len(resp.Messages) < 2 {
		t.Fatalf("история должна содержать system+user (+ раунды), got %d", len(resp.Messages))
	}
	if resp.Messages[0].Role != "system" || resp.Messages[1].Role != "user" {
		t.Fatalf("история должна начинаться с system/user, got %#v", resp.Messages[:2])
	}
}

// С WithResumeState цикл продолжает сохранённый диалог с раунда Rounds+1:
// история resume передаётся модели как есть (без повторного добавления
// system/user), а ответ помечает правильный номер раунда.
func TestGenerateResumeContinuesConversation(t *testing.T) {
	agent := &fakeAgent{}
	saved := []Message{
		{Role: "system", Content: "ты агент"},
		{Role: "user", Content: "напиши код"},
		{Role: "assistant", Content: "вызываю инструмент", ToolCalls: []tools.ToolCall{{ID: "c1", Name: "WriteFiles", Arguments: "{}"}}},
		{Role: "tool", ToolName: "WriteFiles", ToolCallID: "c1", Content: "ок"},
	}
	resume := &ResumeState{Messages: saved, Rounds: 12}

	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{
		replies: []*ModelReply{{Content: "готово", FinishReason: "stop"}},
	}}

	ctx := WithResumeState(context.Background(), resume)
	resp, err := Generate(ctx, provider, agent)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if provider.calls != 1 {
		t.Fatalf("ожидали 1 запрос после resume, got %d", provider.calls)
	}
	// История resume передаётся как есть, без повторного добавления system/user.
	got := provider.received[0]
	if len(got) != len(saved) {
		t.Fatalf("ожидали %d сообщений (история resume), got %d: %#v", len(saved), len(got), got)
	}
	for i := range saved {
		if got[i].Role != saved[i].Role || got[i].Content != saved[i].Content {
			t.Fatalf("сообщение %d не совпадает: %#v vs %#v", i, got[i], saved[i])
		}
	}

	if resp.Truncated {
		t.Fatal("resume не должен завершиться Truncated")
	}
	if resp.Rounds != 13 {
		t.Fatalf("ожидали Rounds=13 (12 прошлых + 1 новый), got %d", resp.Rounds)
	}
	if resp.Content != "готово" {
		t.Fatalf("ожидали content=готово, got %q", resp.Content)
	}
	last := resp.Messages[len(resp.Messages)-1]
	if last.Role != "assistant" || last.Content != "готово" {
		t.Fatalf("история должна включать финальный ответ модели, got %#v", last)
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
