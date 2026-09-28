// Тесты Ф-5 в runner: переиндексация RAG после раундов с мутациями и helper
// ReindexFiles. Fake-агент изображает очередь touched и записывает вызовы
// ReindexTouched; сеть и Qdrant не используются.

package runner

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ai/rag"
	"ai/tools"
)

// reindexAgent — autoFixAgent + Reindexer: записывает переданные списки.
type reindexAgent struct {
	autoFixAgent
	rtCalls   int
	rtTouched [][]string
}

func (a *reindexAgent) ReindexTouched(touched []string) (int, error) {
	a.rtCalls++
	a.rtTouched = append(a.rtTouched, append([]string(nil), touched...))
	return len(touched), nil
}

// Мутационный раунд при RAG_AUTO_REINDEX=1 → переиндексация затронутых файлов;
// при выключенном LSP_AUTO_FIX LSP-хук не мешается (промпты не подмешиваются).
func TestReindexAfterMutation(t *testing.T) {
	t.Setenv("RAG_AUTO_REINDEX", "1")
	t.Setenv("LSP_AUTO_FIX", "0")
	agent := &reindexAgent{autoFixAgent: autoFixAgent{hasMut: []bool{true}}}
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{replies: []*ModelReply{
		{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: "{}"}}, FinishReason: "tool_calls"},
		{Content: "готово", FinishReason: "stop"},
	}}}
	_ = runRecordingGenerate(t, agent, provider)
	if agent.rtCalls != 1 {
		t.Fatalf("ожидали 1 вызов ReindexTouched, got %d", agent.rtCalls)
	}
	if !slices.Equal(agent.rtTouched[0], []string{"main.go"}) {
		t.Fatalf("ReindexTouched получил %#v, ожидали [main.go]", agent.rtTouched[0])
	}
	if agent.afCalls != 0 {
		t.Fatalf("при LSP_AUTO_FIX=0 LspCheckFiles не должен вызываться, got %d", agent.afCalls)
	}
}

// Без RAG_AUTO_REINDEX (по умолчанию 0) переиндексация не запускается.
func TestReindexDisabledByEnv(t *testing.T) {
	t.Setenv("LSP_AUTO_FIX", "0")
	agent := &reindexAgent{autoFixAgent: autoFixAgent{hasMut: []bool{true}}}
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{replies: []*ModelReply{
		{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: "{}"}}, FinishReason: "tool_calls"},
		{Content: "готово", FinishReason: "stop"},
	}}}
	_ = runRecordingGenerate(t, agent, provider)
	if agent.rtCalls != 0 {
		t.Fatalf("по умолчанию ReindexTouched не должен вызываться, got %d", agent.rtCalls)
	}
}

// Один дренаж очереди за раунд: и переиндексация, и LSP-хук получают один и
// тот же список затронутых файлов, без потерь и дублей между вызовами.
func TestReindexAndLspShareDrainedTouched(t *testing.T) {
	t.Setenv("RAG_AUTO_REINDEX", "1")
	agent := &reindexAgent{autoFixAgent: autoFixAgent{
		diags:  [][]string{nil, nil},
		hasMut: []bool{true, true},
	}}
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{replies: []*ModelReply{
		{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: "{}"}}, FinishReason: "tool_calls"},
		{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: "{}"}}, FinishReason: "tool_calls"},
		{Content: "готово", FinishReason: "stop"},
	}}}
	_ = runRecordingGenerate(t, agent, provider)
	if agent.afCalls != 2 || agent.rtCalls != 2 {
		t.Fatalf("afCalls=%d rtCalls=%d, ожидали 2/2", agent.afCalls, agent.rtCalls)
	}
	for i, got := range agent.rtTouched {
		if !slices.Equal(got, []string{"main.go"}) {
			t.Fatalf("ReindexTouched[%d] = %#v, ожидали [main.go]", i, got)
		}
	}
}

// fakeReindexClient — записывающий ReindexClient для helper ReindexFiles.
type fakeReindexClient struct {
	indexed []string // "project|rel|scope|branch|commit"
	deleted []string // "project|rel|branch|commit"
	opts    []rag.IndexOptions
	calls   int
	failAt  int // номер обращения к индексу (1-based), которое вернёт ошибку; 0 — без сбоев
}

func (f *fakeReindexClient) toc() error {
	f.calls++
	if f.failAt > 0 && f.calls == f.failAt {
		return context.DeadlineExceeded
	}
	return nil
}

func (f *fakeReindexClient) IndexFile(_ context.Context, projectName, relPath, scope, _ string, opts rag.IndexOptions) (int, error) {
	f.indexed = append(f.indexed, strings.Join([]string{projectName, relPath, scope, opts.Branch, opts.CommitSHA}, "|"))
	f.opts = append(f.opts, opts)
	if err := f.toc(); err != nil {
		return 0, err
	}
	return 1, nil
}

func (f *fakeReindexClient) DeleteFile(_ context.Context, projectName, relPath string, opts rag.IndexOptions) error {
	f.deleted = append(f.deleted, projectName+"|"+relPath+"|"+opts.Branch+"|"+opts.CommitSHA)
	f.opts = append(f.opts, opts)
	return f.toc()
}

// ReindexFiles: существующие файлы перечитываются и индексируются (scope от
// scopeOf), удалённые мутацией — чистятся из индекса; nil-клиент и пустой
// список — тихий ноль.
func TestReindexFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("plain"), 0644); err != nil {
		t.Fatal(err)
	}
	cl := &fakeReindexClient{}
	scopeOf := func(rel string) string {
		if strings.HasSuffix(rel, ".go") {
			return "server"
		}
		return ""
	}
	// Версия индексации задаётся вызывающей стороной (ветка worktree агента +
	// коммит) и прокидывается в каждое обращение к индексу (Р-7).
	opts := rag.IndexOptions{Branch: "ai/task/T-01", CommitSHA: "abc123"}
	n, err := ReindexFiles(context.Background(), cl, scopeOf, dir, "p", []string{"a.go", "b.txt", "gone.go"}, opts)
	if err != nil {
		t.Fatalf("ReindexFiles: %v", err)
	}
	if n != 3 {
		t.Fatalf("переиндексировано %d, ожидали 3", n)
	}
	if len(cl.indexed) != 2 || cl.indexed[0] != "p|a.go|server|ai/task/T-01|abc123" ||
		cl.indexed[1] != "p|b.txt||ai/task/T-01|abc123" {
		t.Fatalf("indexed = %#v", cl.indexed)
	}
	if len(cl.deleted) != 1 || cl.deleted[0] != "p|gone.go|ai/task/T-01|abc123" {
		t.Fatalf("deleted = %#v", cl.deleted)
	}
	if len(cl.opts) != 3 {
		t.Fatalf("обращений к индексу: %d, ожидали 3", len(cl.opts))
	}
	for _, got := range cl.opts {
		if got != opts {
			t.Fatalf("опции индексации не прокинуты: %+v", got)
		}
	}

	if n, _ := ReindexFiles(context.Background(), nil, scopeOf, dir, "p", []string{"a.go"}, opts); n != 0 {
		t.Fatalf("nil-клиент должен давать 0, got %d", n)
	}
	if n, _ := ReindexFiles(context.Background(), cl, scopeOf, dir, "p", nil, opts); n != 0 {
		t.Fatalf("пустой список должен давать 0, got %d", n)
	}
}

// Первая ошибка индекса останавливает переиндексацию (degrade): возвращается
// частичный результат и ошибка — раннер логирует, генерация не падает.
func TestReindexFilesStopsOnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cl := &fakeReindexClient{failAt: 2}
	n, err := ReindexFiles(context.Background(), cl, nil, dir, "p", []string{"a.go", "b.go"}, rag.IndexOptions{Branch: "main"})
	if err == nil {
		t.Fatalf("ожидали ошибку индексации, got nil (n=%d)", n)
	}
	if n != 1 {
		t.Fatalf("частичный результат = %d, ожидали 1 (a.go успел, b.go упал)", n)
	}
	if len(cl.indexed) != 2 {
		t.Fatalf("indexed вызовов = %d, ожидали 2 (запись до ошибки)", len(cl.indexed))
	}
}
