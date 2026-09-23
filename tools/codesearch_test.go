// Hermetic-тесты CodeSearch: формат ответа, лимиты RAG_MAX_RESULTS/
// RAG_READ_MAX_TOTAL и degrade (skipped) при отсутствии/недоступности RAG.
// Сеть не используется: поиск заменён fake-реализацией RAGSearcher.

package tools

import (
	"ai/rag"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeSearcher — управляемая реализация RAGSearcher для тестов.
type fakeSearcher struct {
	pingErr     error
	results     []rag.SearchResult
	searchErr   error
	projectInfo rag.ProjectInfo
	infoErr     error
	last        rag.SearchParams
}

func (f *fakeSearcher) Ping(context.Context) error { return f.pingErr }

func (f *fakeSearcher) Search(_ context.Context, p rag.SearchParams) ([]rag.SearchResult, error) {
	f.last = p
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.results, nil
}

func (f *fakeSearcher) ProjectInfo(_ context.Context, _ string) (rag.ProjectInfo, error) {
	return f.projectInfo, f.infoErr
}

func newCodeSearchTool(searcher RAGSearcher) *codeSearchTool {
	return &codeSearchTool{ops: &FileOps{OutputDir: "/tmp/temp/demo"}, searcher: searcher}
}

// Успешный поиск: формат {"status":"success","results":[...]} с полями
// file/start_line/end_line/score/snippet.
func TestCodeSearchSuccessFormat(t *testing.T) {
	fake := &fakeSearcher{results: []rag.SearchResult{
		{File: "server/auth/token.go", StartLine: 12, EndLine: 26, Score: 0.86, Snippet: "func ValidateToken(...) {...}"},
		{File: "server/auth/session.go", StartLine: 4, EndLine: 9, Score: 0.72, Snippet: "func NewSession(...) {...}"},
	}}
	tool := newCodeSearchTool(fake)

	out, err := tool.Execute(map[string]any{"query": "где валидируется токен сессии", "scope": "server"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var m map[string]any
	if json.Unmarshal(out, &m) != nil {
		t.Fatalf("не JSON: %s", out)
	}
	if m["status"] != "success" {
		t.Fatalf("status: got %v, want success", m["status"])
	}
	results, ok := m["results"].([]any)
	if !ok || len(results) != 2 {
		t.Fatalf("results: got %#v", m["results"])
	}
	r, _ := results[0].(map[string]any)
	if r["file"] != "server/auth/token.go" || r["start_line"] != float64(12) ||
		r["end_line"] != float64(26) || r["score"] != float64(0.86) ||
		!strings.Contains(r["snippet"].(string), "ValidateToken") {
		t.Fatalf("первый результат: %#v", r)
	}

	// Параметры поиска: проект из OutputDir, query и scope проброшены.
	if fake.last.Project != "demo" {
		t.Fatalf("проект: got %q, want demo", fake.last.Project)
	}
	if fake.last.Query != "где валидируется токен сессии" || fake.last.Scope != "server" {
		t.Fatalf("query/scope: got %q/%q", fake.last.Query, fake.last.Scope)
	}
}

// Пустой список результатов — success с пустым массивом и подсказкой.
func TestCodeSearchNoResults(t *testing.T) {
	tool := newCodeSearchTool(&fakeSearcher{})
	out, err := tool.Execute(map[string]any{"query": "несуществующее"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var m map[string]any
	if json.Unmarshal(out, &m) != nil {
		t.Fatalf("не JSON: %s", out)
	}
	if m["status"] != "success" {
		t.Fatalf("status: got %v, want success", m["status"])
	}
	if arr, ok := m["results"].([]any); !ok || len(arr) != 0 {
		t.Fatalf("пустые результаты должны быть []: %#v", m["results"])
	}
}

// Пустой query — ошибка с подсказкой, без обращения к поиску.
func TestCodeSearchEmptyQuery(t *testing.T) {
	tool := newCodeSearchTool(&fakeSearcher{})
	out, err := tool.Execute(map[string]any{"query": "  "})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var m map[string]any
	if json.Unmarshal(out, &m) != nil || m["status"] != "error" {
		t.Fatalf("должен быть error, got %s", out)
	}
	if fake := tool.searcher.(*fakeSearcher); fake.last.Query != "" {
		t.Fatal("поиск не должен вызываться при пустом query")
	}
}

// Без клиента RAG (deps.RAG == nil) — skipped с подсказкой ReadMap/ReadFiles.
func TestCodeSearchSkippedWhenRAGNil(t *testing.T) {
	tool := newCodeSearchTool(nil)
	out, err := tool.Execute(map[string]any{"query": "тест"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var m map[string]any
	if json.Unmarshal(out, &m) != nil || m["status"] != "skipped" {
		t.Fatalf("должен быть skipped, got %s", out)
	}
	if msg, _ := m["message"].(string); !strings.Contains(msg, "ReadMap/ReadFiles") {
		t.Fatalf("подсказка должна упоминать ReadMap/ReadFiles: %s", msg)
	}
}

// Недоступный Qdrant (Ping → UnavailableError) — skipped, не падает.
func TestCodeSearchSkippedWhenQdrantDown(t *testing.T) {
	tool := newCodeSearchTool(&fakeSearcher{pingErr: &rag.UnavailableError{Err: errors.New("connection refused")}})
	out, err := tool.Execute(map[string]any{"query": "тест"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var m map[string]any
	if json.Unmarshal(out, &m) != nil || m["status"] != "skipped" {
		t.Fatalf("должен быть skipped, got %s", out)
	}
}

// Qdrant выпал уже на этапе поиска (Search → UnavailableError) — тоже skipped.
func TestCodeSearchSkippedWhenSearchUnavailable(t *testing.T) {
	tool := newCodeSearchTool(&fakeSearcher{searchErr: &rag.UnavailableError{Err: errors.New("connection refused")}})
	out, err := tool.Execute(map[string]any{"query": "тест"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var m map[string]any
	if json.Unmarshal(out, &m) != nil || m["status"] != "skipped" {
		t.Fatalf("должен быть skipped, got %s", out)
	}
}

// Прочие ошибки поиска (не недоступность) — status error.
func TestCodeSearchInternalError(t *testing.T) {
	tool := newCodeSearchTool(&fakeSearcher{searchErr: errors.New("битый индекс")})
	out, err := tool.Execute(map[string]any{"query": "тест"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var m map[string]any
	if json.Unmarshal(out, &m) != nil || m["status"] != "error" {
		t.Fatalf("должен быть error, got %s", out)
	}
}

// Проект берётся из OutputDir: temp/<имя> → <имя>; пустой OutputDir — skipped.
func TestCodeSearchProjectFromOutputDir(t *testing.T) {
	if got := projectFromOutputDir(&FileOps{OutputDir: "/tmp/temp/billingService"}); got != "billingService" {
		t.Fatalf("проект: got %q, want billingService", got)
	}
	if got := projectFromOutputDir(nil); got != "" {
		t.Fatalf("нет Ops: got %q, want ''", got)
	}
	if got := projectFromOutputDir(&FileOps{OutputDir: ""}); got != "" {
		t.Fatalf("пустой OutputDir: got %q, want ''", got)
	}

	tool := &codeSearchTool{ops: &FileOps{OutputDir: ""}, searcher: &fakeSearcher{}}
	out, _ := tool.Execute(map[string]any{"query": "тест"})
	var m map[string]any
	if json.Unmarshal(out, &m) != nil || m["status"] != "skipped" {
		t.Fatalf("без OutputDir должен быть skipped, got %s", out)
	}
}

// Лимит выдачи управляется RAG_MAX_RESULTS и пробрасывается в поиск.
func TestCodeSearchLimitsFromEnv(t *testing.T) {
	t.Setenv("RAG_MAX_RESULTS", "5")
	t.Setenv("RAG_READ_MAX_TOTAL", "1000")
	defer t.Setenv("RAG_MAX_RESULTS", "")
	defer t.Setenv("RAG_READ_MAX_TOTAL", "")

	fake := &fakeSearcher{results: []rag.SearchResult{{File: "a.go", Snippet: "x"}}}
	tool := newCodeSearchTool(fake)
	if _, err := tool.Execute(map[string]any{"query": "тест"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fake.last.Limit != 5 || fake.last.MaxTotal != 1000 {
		t.Fatalf("лимиты: got %d/%d, want 5/1000", fake.last.Limit, fake.last.MaxTotal)
	}
}
