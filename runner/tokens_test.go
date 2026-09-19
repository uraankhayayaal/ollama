package runner

import (
	"ai/agents"
	"ai/runevents"
	"ai/tools"
	"context"
	"testing"
)

func TestEstimateTextTokens(t *testing.T) {
	cases := []struct {
		name string
		in   string
		min  int
	}{
		{"пусто", "", 0},
		{"ascii короткий", "hello world", 2},
		{"ascii длинный", "the quick brown fox jumps over the lazy dog", 6},
		{"кириллица", "привет мир, это тест счётчика токенов проекта", 8},
		{"код", "func main() { fmt.Println(\"hello\") }", 6},
		{"пробелы", "   \n\t ", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EstimateTextTokens(tc.in)
			if tc.min == 0 {
				if got != 0 {
					t.Fatalf("EstimateToken(%q) = %d, want 0", tc.in, got)
				}
				return
			}
			if got < tc.min {
				t.Fatalf("EstimateToken(%q) = %d, want >= %d", tc.in, got, tc.min)
			}
		})
	}
}

func TestEstimateUsageCountsHistoryAndReply(t *testing.T) {
	messages := []Message{
		{Role: "system", Content: "система"},
		{Role: "user", Content: "задача"},
		{Role: "assistant", ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: `{"file":"a.go"}`}}},
		{Role: "tool", ToolName: "WriteFiles", Content: "ok"},
	}
	reply := &ModelReply{
		Content:   "готово",
		ToolCalls: []tools.ToolCall{{Name: "List", Arguments: `{"path":"."}`}},
	}
	in, out := EstimateUsage(messages, reply)
	if in <= 0 {
		t.Fatalf("вход = %d, want > 0", in)
	}
	if out <= 0 {
		t.Fatalf("выход = %d, want > 0", out)
	}
	// Вход должен учитывать контент и аргументы tool_calls истории.
	if in < EstimateTextTokens("задача") {
		t.Fatalf("вход %d меньше только user-сообщения %d", in, EstimateTextTokens("задача"))
	}
}

// TestGenerateReportsTokens проверяет, что цикл раннера транслирует событие
// токенов в репортёр (Web UI) на каждый раунд модели.
func TestGenerateReportsTokens(t *testing.T) {
	agent := &fakeAgent{}
	provider := &fakeChatProvider{
		replies: []*ModelReply{
			{Content: "один", FinishReason: "stop"},
		},
	}

	ch := make(chan runevents.Event, 4)
	sink := func(ev runevents.Event) { ch <- ev }
	router := runevents.NewRouter(sink)
	ctx := runevents.WithReporter(context.Background(), router)

	resp, err := Generate(ctx, provider, agent)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Content != "один" {
		t.Fatalf("content = %q, want один", resp.Content)
	}

	found := false
	for i := 0; i < 2; i++ {
		ev := <-ch
		if ev.Type != runevents.TypeTokenCount {
			continue
		}
		found = true
		if ev.In <= 0 || ev.Out <= 0 {
			t.Fatalf("событие токенов = %+v, want оба счётчика > 0", ev)
		}
	}
	if !found {
		t.Fatal("раннер не отправил событие TypeTokenCount")
	}
}

// TestGeneratePrefersProviderUsage проверяет, что фактический usage провайдера
// (ModelReply.Usage) приоритетнее эвристической оценки.
func TestGeneratePrefersProviderUsage(t *testing.T) {
	agent := &fakeAgent{}
	provider := &fakeChatProvider{
		replies: []*ModelReply{
			{Content: "ответ", FinishReason: "stop", Usage: &Usage{InputTokens: 777, OutputTokens: 42}},
		},
	}

	ch := make(chan runevents.Event, 4)
	router := runevents.NewRouter(func(ev runevents.Event) { ch <- ev })
	ctx := runevents.WithReporter(context.Background(), router)

	if _, err := Generate(ctx, provider, agent); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for i := 0; i < 2; i++ {
		ev := <-ch
		if ev.Type != runevents.TypeTokenCount {
			continue
		}
		if ev.In != 777 || ev.Out != 42 {
			t.Fatalf("токены = %d/%d, want 777/42 (usage провайдера)", ev.In, ev.Out)
		}
		return
	}
	t.Fatal("событие TypeTokenCount не пришло")
}

var _ agents.Agent = (*fakeAgent)(nil)
