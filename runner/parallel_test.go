package runner

import (
	"ai/tools"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// parallelCallRec — запись одного выполненного вызова инструмента.
type parallelCallRec struct {
	name string
	idx  int
}

// parallelRecordingAgent — псевдо-агент, записывающий фактическую параллельность
// выполнения инструментов (максимум одновременно выполняющихся вызовов) и
// порядок завершения. Задержка вызова берётся из delays[name], затем delay.
type parallelRecordingAgent struct {
	*fakeAgent
	mu        sync.Mutex
	delay     time.Duration
	delays    map[string]time.Duration
	results   map[string][]byte
	errs      map[string]error
	active    int
	maxActive int
	callSeq   int
	calls     []parallelCallRec
}

func (a *parallelRecordingAgent) CallFunction(name string, _ map[string]any) ([]byte, error) {
	a.mu.Lock()
	a.active++
	if a.active > a.maxActive {
		a.maxActive = a.active
	}
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		a.active--
		a.calls = append(a.calls, parallelCallRec{name: name, idx: a.callSeq})
		a.callSeq++
		a.mu.Unlock()
	}()

	d := a.delay
	if d0, ok := a.delays[name]; ok {
		d = d0
	}
	if d > 0 {
		time.Sleep(d)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if err, ok := a.errs[name]; ok {
		return nil, err
	}
	if res, ok := a.results[name]; ok {
		return res, nil
	}
	return []byte(`{"status":"success"}`), nil
}

// toolMessages возвращает имена tool-сообщений из истории в порядке следования
// (роль модели была-ассистентом, поэтому порядок = порядок вызова).
func toolMessages(resp *AgentResponse) []string {
	var out []string
	for _, m := range resp.Messages {
		if m.Role == "tool" {
			out = append(out, m.ToolName)
		}
	}
	return out
}

// Несколько read-only вызовов одного раунда выполняются ПАРАЛЛЕЛЬНО:
// задержки перекрываются (maxActive >= 2), а суммарное время заметно меньше
// последовательного исполнения.
func TestParallelToolCallsRunConcurrently(t *testing.T) {
	agent := &parallelRecordingAgent{
		fakeAgent: &fakeAgent{},
		delay:     50 * time.Millisecond,
		results: map[string][]byte{
			"List":      []byte(`{"status":"success"}`),
			"ReadFiles": []byte(`{"status":"success"}`),
			"ReadMap":   []byte(`{"status":"success"}`),
		},
	}
	provider := &fakeChatProvider{
		replies: []*ModelReply{
			{ToolCalls: []tools.ToolCall{
				{ID: "r1", Name: "ReadFiles", Arguments: `{"filenames":["a.go"]}`},
				{ID: "l1", Name: "List", Arguments: `{}`},
				{ID: "r2", Name: "ReadFiles", Arguments: `{"filenames":["b.go"]}`},
				{ID: "m1", Name: "ReadMap", Arguments: `{"filenames":["c.go"]}`},
			}, FinishReason: "tool_calls"},
			{Content: "готово", FinishReason: "stop"},
		},
	}

	start := time.Now()
	resp := testGenerate(t, agent, provider)
	elapsed := time.Since(start)

	if resp.Content != "готово" {
		t.Fatalf("ожидали завершение текстом, got %q", resp.Content)
	}
	if len(resp.ToolCalls) != 4 {
		t.Fatalf("ожидали 4 вызова инструментов, got %d", len(resp.ToolCalls))
	}
	// Ключевая проверка: вызовы реально перекрывались (не выполнялись по очереди).
	if agent.maxActive < 2 {
		t.Fatalf("ожидали параллельность (maxActive>=2), got %d", agent.maxActive)
	}
	// Санитарная проверка: суммарное время < времени 4 последовательных вызовов
	// (4*50мс + проскакивания). Параллельная волна укладывается в ~50мс.
	if elapsed >= 4*time.Duration(50)*time.Millisecond {
		t.Fatalf("вызовы выполнялись слишком долго (%v) — параллельность не сработала", elapsed)
	}
}

// Порядок tool-сообщений в истории = порядку запрошенных моделью вызовов,
// даже когда read-only вызовы выполняются параллельно и завершаются в другом
// порядке, а мутирующий инструмент идёт строго на своём месте.
func TestParallelToolCallsPreserveOrder(t *testing.T) {
	agent := &parallelRecordingAgent{
		fakeAgent: &fakeAgent{},
		delay:     30 * time.Millisecond,
		delays: map[string]time.Duration{
			// Пишущий инструмент медленный (выполняется на своём месте), чтения
			// быстрые и идут параллельной волной — их результаты готовы заранее.
			"WriteFiles": 80 * time.Millisecond,
			"ReadFiles":  5 * time.Millisecond,
			"List":       10 * time.Millisecond,
		},
		results: map[string][]byte{
			"ReadFiles":  []byte(`{"status":"success"}`),
			"WriteFiles": []byte(`{"status":"success"}`),
		},
	}
	provider := &fakeChatProvider{
		replies: []*ModelReply{
			{ToolCalls: []tools.ToolCall{
				{ID: "w", Name: "WriteFiles", Arguments: `{"files":[]}`},
				{ID: "r", Name: "ReadFiles", Arguments: `{"filenames":["a.go"]}`},
				{ID: "l", Name: "List", Arguments: `{}`},
			}, FinishReason: "tool_calls"},
			{Content: "готово", FinishReason: "stop"},
		},
	}

	resp := testGenerate(t, agent, provider)

	want := []string{"WriteFiles", "ReadFiles", "List"}
	got := toolMessages(resp)
	if len(got) != len(want) {
		t.Fatalf("ожидали %d tool-сообщений, got %#v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("порядок tool-сообщений нарушен: хотим %v, got %v", want, got)
		}
	}
}

// При PARALLEL_TOOL_CALLS=0 read-only вызовы выполняются последовательно,
// как и раньше (флаг-выключатель для отладки/безопасности).
func TestParallelToolCallsDisabledByEnv(t *testing.T) {
	t.Setenv("PARALLEL_TOOL_CALLS", "0")

	agent := &parallelRecordingAgent{
		fakeAgent: &fakeAgent{},
		delay:     20 * time.Millisecond,
		results: map[string][]byte{
			"ReadFiles": []byte(`{"status":"success"}`),
			"List":      []byte(`{"status":"success"}`),
		},
	}
	provider := &fakeChatProvider{
		replies: []*ModelReply{
			{ToolCalls: []tools.ToolCall{
				{ID: "r1", Name: "ReadFiles", Arguments: `{"filenames":["a.go"]}`},
				{ID: "r2", Name: "ReadFiles", Arguments: `{"filenames":["b.go"]}`},
				{ID: "l", Name: "List", Arguments: `{}`},
			}, FinishReason: "tool_calls"},
			{Content: "готово", FinishReason: "stop"},
		},
	}

	resp := testGenerate(t, agent, provider)

	if agent.maxActive != 1 {
		t.Fatalf("при PARALLEL_TOOL_CALLS=0 ожидали последовательность (maxActive=1), got %d", agent.maxActive)
	}
	if resp.Content != "готово" {
		t.Fatalf("ожидали завершение текстом, got %q", resp.Content)
	}
	if len(resp.ToolCalls) != 3 {
		t.Fatalf("ожидали 3 вызова, got %d", len(resp.ToolCalls))
	}
}

// Жёсткая ошибка read-only вызова (Go-ошибка, а не JSON "status":"error")
// останавливает цикл даже при параллельном выполнении волны.
func TestParallelToolCallsErrorSurfaces(t *testing.T) {
	agent := &parallelRecordingAgent{
		fakeAgent: &fakeAgent{},
		delay:     10 * time.Millisecond,
		errs:      map[string]error{"ReadFiles": fmt.Errorf("чтение не удалось")},
		results:   map[string][]byte{"List": []byte(`{"status":"success"}`)},
	}
	provider := &fakeChatProvider{
		replies: []*ModelReply{
			{ToolCalls: []tools.ToolCall{
				{ID: "r", Name: "ReadFiles", Arguments: `{"filenames":["a.go"]}`},
				{ID: "l", Name: "List", Arguments: `{}`},
			}, FinishReason: "tool_calls"},
		},
	}

	_, err := Generate(context.Background(), provider, agent)
	if err == nil {
		t.Fatal("ожидали ошибку при провале read-only вызова в параллельной волне")
	}
	if !strings.Contains(err.Error(), "ReadFiles") {
		t.Fatalf("ошибка должна упоминать инструмент, got %v", err)
	}
}
