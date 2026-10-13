package planner

// Hermetic-тесты контекста RAG планировщика (Ф-4): подмешивание блока
// «релевантный код по задаче» в карту проекта, фолбэк на обычную карту без
// RAG (nil/ошибка/выключен/проекта нет), лимиты maxMapChars не превышаются;
// scope-маппинг шага плана → фильтр области Qdrant (ragScopeFromScope) и
// контекст по шагу (stepRAGContext). Сеть не используется: поиск — fake.

import (
	"ai/projects"
	"ai/rag"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeRAGSearcher — управляемая реализация RAGSearcher для тестов.
type fakeRAGSearcher struct {
	results []rag.SearchResult
	err     error
	last    rag.SearchParams
}

func (f *fakeRAGSearcher) Search(_ context.Context, p rag.SearchParams) ([]rag.SearchResult, error) {
	f.last = p
	if f.err != nil {
		return nil, f.err
	}
	return f.results, nil
}

// newRAGProject создаёт реальный проект в temp/ с одним индекс-файлом и
// возвращает его имя (удаляется по завершении теста).
func newRAGProject(t *testing.T) string {
	t.Helper()
	name := "testrag_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	dir := projects.ProjectDir(name)
	if err := os.MkdirAll(filepath.Join(dir, "server", "auth"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server", "auth", "token.go"), []byte("package auth\n\nfunc ValidateToken() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return name
}

// Без RAG (nil-поисковик) — карта ровно как BuildProjectMap (фолбэк).
func TestBuildProjectMapRAGFallbackNil(t *testing.T) {
	name := newRAGProject(t)
	want := BuildProjectMap(name)
	if got := BuildProjectMapRAG(name, "где валидируется токен", nil); got != want {
		t.Fatalf("без поисковика ожидали обычную карту:\n=== want ===\n%s\n=== got ===\n%s", want, got)
	}
}

// Ошибка поиска (Qdrant недоступен) — тоже фолбэк на обычную карту.
func TestBuildProjectMapRAGFallbackError(t *testing.T) {
	name := newRAGProject(t)
	fake := &fakeRAGSearcher{err: &rag.UnavailableError{Err: errors.New("connection refused")}}
	got := BuildProjectMapRAG(name, "где валидируется токен", fake)
	want := BuildProjectMap(name)
	if got != want {
		t.Fatalf("при ошибке поиска ожидали обычную карту:\n=== want ===\n%s\n=== got ===\n%s", want, got)
	}
}

// Нет результатов — фолбэк на обычную карту, без блока.
func TestBuildProjectMapRAGNoResults(t *testing.T) {
	name := newRAGProject(t)
	fake := &fakeRAGSearcher{results: []rag.SearchResult{}}
	got := BuildProjectMapRAG(name, "чего-то несуществующего", fake)
	if strings.Contains(got, "Релевантный код") {
		t.Fatalf("пустые результаты не должны давать блок:\n%s", got)
	}
}

// Успешный поиск — карта содержит блок «релевантный код» с координатами и
// сниппетом; параметры поиска — фильтр по проекту без scope.
func TestBuildProjectMapRAGAppendsBlock(t *testing.T) {
	name := newRAGProject(t)
	fake := &fakeRAGSearcher{results: []rag.SearchResult{
		{File: "server/auth/token.go", StartLine: 3, EndLine: 4, Score: 0.91, Snippet: "func ValidateToken() {}"},
	}}
	m := BuildProjectMapRAG(name, "где валидируется токен сессии", fake)

	if !strings.Contains(m, "Релевантный код по задаче") {
		t.Fatalf("карта не содержит блок релевантного кода:\n%s", m)
	}
	for _, want := range []string{"server/auth/token.go:3-4", "score 0.91", "ValidateToken"} {
		if !strings.Contains(m, want) {
			t.Errorf("блок не содержит %q:\n%s", want, m)
		}
	}
	// Поиск ограничен проектом, scope пуст (весь проект), лимит — maxMapChars.
	if fake.last.Project != name {
		t.Fatalf("проект: got %q, want %q", fake.last.Project, name)
	}
	if fake.last.Scope != "" {
		t.Fatalf("scope глобальной задачи должен быть пустым, got %q", fake.last.Scope)
	}
	if fake.last.MaxTotal != maxMapChars {
		t.Fatalf("MaxTotal: got %d, want %d (в рамках maxMapChars)", fake.last.MaxTotal, maxMapChars)
	}
}

// Проект не создан — пометка «не существует» без обращения к поиску.
func TestBuildProjectMapRAGMissingProject(t *testing.T) {
	fake := &fakeRAGSearcher{results: []rag.SearchResult{{File: "a.go", Snippet: "x"}}}
	name := "testrag_missing_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	m := BuildProjectMapRAG(name, "задача", fake)
	if !strings.Contains(m, "ещё не существует") {
		t.Fatalf("для отсутствующего проекта ожидали пометку, got:\n%s", m)
	}
	if strings.Contains(m, "Релевантный код") {
		t.Fatalf("для отсутствующего проекта не должно быть RAG-блока:\n%s", m)
	}
	if len(fake.last.Query) > 0 {
		t.Fatalf("поиск не должен вызываться для отсутствующего проекта")
	}
}

// Пустая задача — без блока и без обращения к поиску.
func TestBuildProjectMapRAGEmptyTask(t *testing.T) {
	name := newRAGProject(t)
	fake := &fakeRAGSearcher{results: []rag.SearchResult{{File: "a.go", Snippet: "x"}}}
	m := BuildProjectMapRAG(name, "   ", fake)
	if strings.Contains(m, "Релевантный код") {
		t.Fatalf("пустая задача не должна давать RAG-блок:\n%s", m)
	}
	if len(fake.last.Query) > 0 {
		t.Fatalf("поиск не должен вызываться при пустой задаче")
	}
}

// Выключенный RAG_PLANNER_CONTEXT — обычная карта даже при успешном поиске.
func TestBuildProjectMapRAGDisabled(t *testing.T) {
	t.Setenv("RAG_PLANNER_CONTEXT", "0")
	name := newRAGProject(t)
	want := BuildProjectMap(name)
	fake := &fakeRAGSearcher{results: []rag.SearchResult{{File: "a.go", Snippet: "x"}}}
	if got := BuildProjectMapRAG(name, "задача", fake); got != want {
		t.Fatalf("при RAG_PLANNER_CONTEXT=0 ожидали обычную карту: got %q, want %q", got, want)
	}
}

// Лимиты: суммарный объём блока не превышает maxMapChars, хвост обрезан.
func TestBuildProjectMapRAGLimits(t *testing.T) {
	t.Setenv("RAG_MAX_RESULTS", "5")
	name := newRAGProject(t)
	long := strings.Repeat("abcdefghij", 600) // ~6000 символов на чанк
	fake := &fakeRAGSearcher{results: []rag.SearchResult{
		{File: "a.go", StartLine: 1, EndLine: 2, Score: 0.9, Snippet: long},
		{File: "b.go", StartLine: 1, EndLine: 2, Score: 0.8, Snippet: long},
		{File: "c.go", StartLine: 1, EndLine: 2, Score: 0.7, Snippet: long},
	}}
	m := BuildProjectMapRAG(name, "задача", fake)
	if !strings.Contains(m, "обрезан по лимиту") {
		t.Fatalf("ожидали пометку об обрезке блока:\n%.400s", m)
	}
	// Блок (с заголовком) в рамках maxMapChars + запас на пометку обрезки.
	idx := strings.Index(m, "Релевантный код")
	if idx < 0 {
		t.Fatalf("блок «релевантный код» не найден")
	}
	if got := len(m) - idx; got > maxMapChars+64 {
		t.Fatalf("блок разросся за лимит: %d символов (лимит %d)", got, maxMapChars)
	}
}

// Scope-маппинг шага плана → фильтр области Qdrant.
func TestRAGScopeFromScope(t *testing.T) {
	cases := []struct {
		name  string
		scope []string
		want  string
	}{
		{"пустой scope", nil, ""},
		{"только пробелы", []string{"  ", ""}, ""},
		{"корень", []string{"."}, ""},
		{"файлы одного каталога", []string{"server/internal/auth/", "server/go.mod"}, "server"},
		{"одна директория frontend", []string{"frontend/src/App.tsx"}, "frontend"},
		{"файлы в корне проекта", []string{"README.md"}, "root"},
		{"nested internal", []string{"internal/order/model.go"}, "internal"},
		{"разнородные сегменты", []string{"server/a.go", "frontend/b.tsx"}, ""},
		{"root + подкаталог", []string{"README.md", "server/a.go"}, ""},
		{"с пробелами вокруг", []string{"  server/auth/  "}, "server"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ragScopeFromScope(tc.scope); got != tc.want {
				t.Fatalf("ragScopeFromScope(%v): got %q, want %q", tc.scope, got, tc.want)
			}
		})
	}
}

// stepRAGContext: успешный поиск — блок с координатами, scope проброшен из
// области шага; nil-поисковик / ошибка / выключенный — пусто.
func TestStepRAGContext(t *testing.T) {
	name := newRAGProject(t)
	fake := &fakeRAGSearcher{results: []rag.SearchResult{
		{File: "server/auth/token.go", StartLine: 3, EndLine: 4, Score: 0.9, Snippet: "func ValidateToken() {}"},
	}}
	step := &Step{ID: "s1", Prompt: "добавь валидацию токена в auth", Scope: []string{"server/auth/"}}

	blk := stepRAGContext(context.Background(), name, step, fake)
	if blk == "" {
		t.Fatalf("ожидали блок контекста по шагу")
	}
	if !strings.Contains(blk, "server/auth/token.go:3-4") {
		t.Fatalf("блок не содержит координаты чанка:\n%s", blk)
	}
	if fake.last.Project != name || fake.last.Scope != "server" {
		t.Fatalf("параметры поиска: got %q/%q, want %q/%q", fake.last.Project, fake.last.Scope, name, "server")
	}
	if fake.last.Query != step.Prompt {
		t.Fatalf("query: got %q, want %q", fake.last.Query, step.Prompt)
	}

	if got := stepRAGContext(context.Background(), name, step, nil); got != "" {
		t.Fatalf("без поисковика — пусто, got %q", got)
	}
	if got := stepRAGContext(context.Background(), name, step, &fakeRAGSearcher{err: errors.New("q")}); got != "" {
		t.Fatalf("при ошибке поиска — пусто, got %q", got)
	}
	t.Setenv("RAG_PLANNER_CONTEXT", "off")
	if got := stepRAGContext(context.Background(), name, step, fake); got != "" {
		t.Fatalf("при выключенном контексте — пусто, got %q", got)
	}
}

// stepRAGContext с пустым промптом берёт описание шага как запрос.
func TestStepRAGContextFallsBackToDescription(t *testing.T) {
	name := newRAGProject(t)
	fake := &fakeRAGSearcher{results: []rag.SearchResult{{File: "a.go", Snippet: "x"}}}
	step := &Step{ID: "s1", Description: "валидация токена", Scope: []string{"server/"}}
	if blk := stepRAGContext(context.Background(), name, step, fake); blk == "" {
		t.Fatalf("ожидали блок по описанию шага")
	}
	if fake.last.Query != "валидация токена" {
		t.Fatalf("query по описанию: got %q", fake.last.Query)
	}
}
