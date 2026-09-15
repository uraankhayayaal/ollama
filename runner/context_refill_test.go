package runner

import (
	"ai/agents"
	"ai/tools"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// supplierAgent — агент, который одновременно реализует ContextSupplier через
// встроенный *tools.FileOps (FetchContext) и наследует поведение fakeAgent.
type supplierAgent struct {
	*fakeAgent
	*tools.FileOps
}

// TestNeedContextTargets проверяет извлечение целей дозаправки из маркеров
// NEED_*: дедупликацию и поддержку диапазонов строк.
func TestNeedContextTargets(t *testing.T) {
	content := "мне нужен код:\nNEED_CONTEXT: internal/store.go\nNEED_SIGNATURE internal/service.go:10-20\nNEED_FILE: internal/store.go\nи ещё NEED_CONTEXT:  frontend/api.ts 20-45"
	got := NeedContextTargets(content)
	want := []string{"internal/store.go", "internal/service.go:10-20", "frontend/api.ts 20-45"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NeedContextTargets = %#v, want %#v", got, want)
	}
	if got := NeedContextTargets("без маркеров"); len(got) != 0 {
		t.Fatalf("ожидали 0 целей, got %#v", got)
	}
}

// TestContextRefillMessage проверяет детерминированность сообщения дозаправки
// (сортировка целей по алфавиту).
func TestContextRefillMessage(t *testing.T) {
	msg := contextRefillMessage(map[string]string{
		"b.txt": "В",
		"a.txt": "А",
	})
	if strings.Index(msg, "--- a.txt ---") > strings.Index(msg, "--- b.txt ---") {
		t.Fatalf("цели должны идти по алфавиту, got:\n%s", msg)
	}
	if !strings.Contains(msg, "НЕ повторяй маркеры NEED_*.") {
		t.Fatalf("сообщение должно запрещать повтор маркеров, got:\n%s", msg)
	}
}

// Маркеры NEED_* в финальном тексте модели: runner дозаправляет контекст
// (карта кода через ContextSupplier) и продолжает диалог, а не завершает
// цикл с галлюцинирующими сигнатурами.
func TestGenerateRefillsContextOnNeedMarkers(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "svc.go"), []byte("package svc\n\nfunc Do(a int) int {\n\treturn a + 1\n}\n"), 0644)

	agent := &supplierAgent{
		fakeAgent: &fakeAgent{},
		FileOps:   &tools.FileOps{OutputDir: dir},
	}
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{
		replies: []*ModelReply{
			{Content: "нужна сигнатура: NEED_CONTEXT: svc.go", FinishReason: "stop"}, // раунд 1: дозаправка
			{Content: "готово", FinishReason: "stop"},                                // раунд 2: завершил
		},
	}}

	resp, err := Generate(context.Background(), provider, agent)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if provider.calls != 2 {
		t.Fatalf("ожидали 2 запроса (дозаправка + финал), got %d", provider.calls)
	}
	if resp.Content != "готово" {
		t.Fatalf("ожидали финальный текст, got %q", resp.Content)
	}
	// Дозаправленный контекст должен уйти модели в следующем запросе.
	second := provider.received[1]
	joined := ""
	for _, m := range second {
		joined += m.Content
	}
	if !strings.Contains(joined, "Система дозаправила контекст") {
		t.Fatalf("следующий запрос должен содержать сообщение дозаправки, got: %#v", second)
	}
	if !strings.Contains(joined, "--- svc.go ---") || !strings.Contains(joined, "func Do(a int) int") {
		t.Fatalf("дозаправка должна включать карту svc.go, got: %q", joined)
	}
}

// Модель упорно просит контекст маркерами NEED_*: после maxNeedRefills
// дозаправок runner принимает ответ как финальный и не зацикливается.
func TestGenerateLimitsNeedRefills(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "svc.go"), []byte("package svc\n"), 0644)

	agent := &supplierAgent{
		fakeAgent: &fakeAgent{},
		FileOps:   &tools.FileOps{OutputDir: dir},
	}
	provider := &fakeChatProvider{
		replies: []*ModelReply{
			{Content: "NEED_CONTEXT: svc.go", FinishReason: "stop"},
		},
	}

	resp := testGenerate(t, agent, provider)

	expect := 1 + maxNeedRefills
	if provider.calls != expect {
		t.Fatalf("ожидали %d запросов (1 + %d дозаправок), got %d", expect, maxNeedRefills, provider.calls)
	}
	if !strings.Contains(resp.Content, "NEED_CONTEXT: svc.go") {
		t.Fatalf("после исчерпания лимита возвращается последний ответ модели, got %q", resp.Content)
	}
	// Дозаправки должны попасть в историю диалога.
	refills := 0
	for _, m := range resp.Messages {
		if m.Role == "user" && strings.Contains(m.Content, "Система дозаправила контекст") {
			refills++
		}
	}
	if refills != maxNeedRefills {
		t.Fatalf("ожидали %d сообщений дозаправки в истории, got %d", maxNeedRefills, refills)
	}
}

// Агент без ContextSupplier: маркеры NEED_* не приводят к дозаправке, текст
// принимается как финальный ответ (падение до старого поведения).
func TestGenerateNoSupplierIgnoresNeedMarkers(t *testing.T) {
	agent := &fakeAgent{requiredTool: ""}
	provider := &fakeChatProvider{
		replies: []*ModelReply{{Content: "NEED_CONTEXT: svc.go", FinishReason: "stop"}},
	}

	resp := testGenerate(t, agent, provider)

	if provider.calls != 1 {
		t.Fatalf("ожидали 1 запрос (дозаправки нет), got %d", provider.calls)
	}
	if resp.Content != "NEED_CONTEXT: svc.go" {
		t.Fatalf("маркеры без supplier должны сохраняться в ответе, got %q", resp.Content)
	}
}

// Усечённый ответ (finish_reason=length) не должен дозаправляться: маркер
// NEED_* в нём может быть оборван на полуслове и приведёт лишь к мусору.
func TestGenerateNoRefillOnTruncated(t *testing.T) {
	dir := t.TempDir()
	agent := &supplierAgent{
		fakeAgent: &fakeAgent{},
		FileOps:   &tools.FileOps{OutputDir: dir},
	}
	provider := &fakeChatProvider{
		replies: []*ModelReply{
			{Content: "NEED_CONTEXT: svc.go", FinishReason: "length"},
		},
	}

	resp := testGenerate(t, agent, provider)

	if provider.calls != 1 {
		t.Fatalf("усечённый ответ без дозаправки завершает цикл одним запросом, got %d", provider.calls)
	}
	if !resp.Truncated {
		t.Fatal("ответ должен остаться Truncated")
	}
	for _, m := range resp.Messages {
		if strings.Contains(m.Content, "Система дозаправила контекст") {
			t.Fatalf("усечённый ответ не должен дозаправляться, got: %#v", m)
		}
	}
}

// Дозаправка не мешает обязательным инструментам: гарантирует компиляцию
// supplierAgent как agents.Agent и ContextSupplier одновременно.
var (
	_ agents.Agent      = (*supplierAgent)(nil)
	_ ContextSupplier   = (*supplierAgent)(nil)
)