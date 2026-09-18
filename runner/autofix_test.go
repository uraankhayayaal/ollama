// Тесты Ф-2 в runner: авто-самоисправление после раундов с мутациями.
// Fake-агент реализует AutoFixer и отдаёт сценарные диагностики; сеть и
// реальные чекеры не используются.

package runner

import (
	"context"
	"strings"
	"testing"

	"ai/tools"
)

// runRecordingGenerate запускает Generate с записывающим провайдером.
func runRecordingGenerate(t *testing.T, agent *autoFixAgent, provider *recordingProvider) *AgentResponse {
	t.Helper()
	resp, err := Generate(context.Background(), provider, agent)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return resp
}

// autoFixAgent — fakeAgent + интерфейс AutoFixer. Диагностики отдаются по
// порядку вызовов (последний сценарий повторяется), как fakeChatProvider.
type autoFixAgent struct {
	fakeAgent
	diags   [][]string
	hasMut  []bool
	afCalls int
}

func (a *autoFixAgent) LspAutoFix() ([]string, bool) {
	idx := a.afCalls
	a.afCalls++
	if len(a.diags) == 0 {
		return nil, false
	}
	if idx >= len(a.diags) {
		idx = len(a.diags) - 1
	}
	return a.diags[idx], a.hasMut[idx]
}

// Скрытый промпт авто-лечения подмешивается после раунда с мутацией, а при
// последующем чистом раунде цикл завершается штатно.
func TestAutoFixInjectsHiddenPrompt(t *testing.T) {
	agent := &autoFixAgent{
		diags:  [][]string{{"main.go:3:9: undefined: x"}, nil},
		hasMut: []bool{true, true},
	}
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{
		replies: []*ModelReply{
			{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: "{}"}}, FinishReason: "tool_calls"},
			{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: "{}"}}, FinishReason: "tool_calls"},
			{Content: "готово", FinishReason: "stop"},
		},
	}}

	resp := runRecordingGenerate(t, agent, provider)
	if resp.Content != "готово" {
		t.Fatalf("got %q", resp.Content)
	}
	// Ищем скрытую подсказку в истории, отправленной модели.
	found := false
	for _, batch := range provider.received {
		for _, m := range batch {
			if m.Role == "user" && strings.Contains(m.Content, "вызвала ошибки компиляции") {
				found = true
				if !strings.Contains(m.Content, "main.go:3:9: undefined: x") {
					t.Fatalf("подсказка без точной строки: %q", m.Content)
				}
				if !strings.Contains(m.Content, "Итерация 1/") {
					t.Fatalf("подсказка без номера итерации: %q", m.Content)
				}
			}
		}
	}
	if !found {
		t.Fatalf("скрытый промпт авто-лечения не найден в истории: %#v", resp.Messages)
	}
	if agent.afCalls != 2 {
		t.Fatalf("ожидали 2 вызова LspAutoFix (по раунду), got %d", agent.afCalls)
	}
}

// Раунд без мутаций (только чтения) не подмешивает подсказку авто-лечения.
func TestAutoFixSkippedWithoutMutation(t *testing.T) {
	agent := &autoFixAgent{diags: [][]string{}, hasMut: []bool{}}
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{replies: []*ModelReply{
		{ToolCalls: []tools.ToolCall{{Name: "ReadFiles", Arguments: "{}"}}, FinishReason: "tool_calls"},
		{Content: "готово", FinishReason: "stop"},
	}}}
	_ = runRecordingGenerate(t, agent, provider)
	for _, batch := range provider.received {
		for _, m := range batch {
			if strings.Contains(m.Content, "вызвала ошибки компиляции") {
				t.Fatalf("без мутаций подсказка не нужна: %q", m.Content)
			}
		}
	}
}

// Число подряд итераций авто-лечения ограничено LSP_MAX_FIX_ROUNDS: после
// лимита новые скрытые промпты не добавляются, даже если ошибки остались.
func TestAutoFixRespectsMaxRounds(t *testing.T) {
	t.Setenv("LSP_MAX_FIX_ROUNDS", "2")
	agent := &autoFixAgent{
		diags:  [][]string{{"a.go:1:1: e1"}, {"a.go:2:2: e2"}, {"a.go:3:3: e3"}, {"a.go:4:4: e4"}},
		hasMut: []bool{true, true, true, true},
	}
	// Модель всегда пишет файл (мутация) и никогда не завершает — цикл до лимита
	// раундов; но раундов авто-лечения должно быть не больше 2.
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{replies: []*ModelReply{
		{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: "{}"}}, FinishReason: "tool_calls"},
	}}}
	resp := runRecordingGenerate(t, agent, provider)

	// Подсказки накапливаются в истории, поэтому считаем их в ФИНАЛЬНОМ
	// снимке (recording-провайдер копирует историю на каждом запросе).
	count := 0
	for _, m := range resp.Messages {
		if m.Role == "user" && strings.Contains(m.Content, "вызвала ошибки компиляции") {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("ожидали 2 подсказки авто-лечения (лимит), got %d", count)
	}
}

// Повторные (идентичные) диагностики между итерациями не подмешиваются
// повторно — токен-бюджет (Ф-4).
func TestAutoFixDeduplicatesRepeatedDiags(t *testing.T) {
	agent := &autoFixAgent{
		diags: [][]string{
			{"a.go:1:1: boom", "b.go:2:2: bang"},
			{"a.go:1:1: boom", "b.go:2:2: bang"},
			{"a.go:1:1: boom", "c.go:3:3: new"},
		},
		hasMut: []bool{true, true, true, true},
	}
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{replies: []*ModelReply{
		{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: "{}"}}, FinishReason: "tool_calls"},
	}}}
	resp := runRecordingGenerate(t, agent, provider)

	var prompts []string
	for _, m := range resp.Messages {
		if m.Role == "user" && strings.Contains(m.Content, "вызвала ошибки компиляции") {
			prompts = append(prompts, m.Content)
		}
	}
	if len(prompts) != 2 {
		t.Fatalf("ожидали 2 подсказки (раунд без новых ошибок пропускается), got %d: %#v", len(prompts), prompts)
	}
	if n := strings.Count(strings.Join(prompts, "\n"), "a.go:1:1: boom"); n != 1 {
		t.Fatalf("повторная диагностика показана %d раз(а), ожидали 1", n)
	}
	if n := strings.Count(strings.Join(prompts, "\n"), "c.go:3:3: new"); n != 1 {
		t.Fatalf("новая диагностика не показана ровно один раз: %d", n)
	}
}

// filterNewDiags возвращает только ещё не отправленные строки, сохраняя порядок.
func TestFilterNewDiags(t *testing.T) {
	sent := map[string]bool{"a": true, "b": true}
	got := filterNewDiags([]string{"a", "c", "b", "d"}, sent)
	if len(got) != 2 || got[0] != "c" || got[1] != "d" {
		t.Fatalf("filterNewDiags = %#v", got)
	}
	if len(filterNewDiags(nil, sent)) != 0 {
		t.Fatal("nil-вход должен дать пусто")
	}
	if got := filterNewDiags([]string{"x"}, nil); len(got) != 1 || got[0] != "x" {
		t.Fatalf("без sent всё считается новым: %#v", got)
	}
}

// LSP_AUTO_FIX=0 полностью выключает авто-лечение.
func TestAutoFixDisabledByEnv(t *testing.T) {
	t.Setenv("LSP_AUTO_FIX", "0")
	agent := &autoFixAgent{
		diags:  [][]string{{"a.go:1:1: boom"}},
		hasMut: []bool{true},
	}
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{
		replies: []*ModelReply{
			{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: "{}"}}, FinishReason: "tool_calls"},
			{Content: "готово", FinishReason: "stop"},
		},
	}}
	_ = runRecordingGenerate(t, agent, provider)
	if agent.afCalls != 0 {
		t.Fatalf("при LSP_AUTO_FIX=0 LspAutoFix не должен вызываться, got %d", agent.afCalls)
	}
	for _, batch := range provider.received {
		for _, m := range batch {
			if strings.Contains(m.Content, "вызвала ошибки компиляции") {
				t.Fatalf("при выключенной фиче подсказки быть не должно: %q", m.Content)
			}
		}
	}
}
