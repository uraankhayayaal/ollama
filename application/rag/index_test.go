// Тесты сбора и полной индексации каталога проекта (application/rag/index.go).
package rag

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	infrarag "ai/infrastructure/rag"
)

// TestCollectIndexItemsHermetic — сбор IndexItem'ов из списка файлов: путь,
// scope (ScopeForPath) и содержимое; нечитаемые файлы пропускаются.
func TestCollectIndexItemsHermetic(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "server/api.go", "package main\n")
	writeTestFile(t, dir, "web/index.ts", "const x = 1\n")

	items, err := CollectIndexItems(context.Background(), dir, func(dir string) ([]string, error) {
		return []string{"server/api.go", "web/index.ts", "gone.go"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2 (gone.go не читается и пропускается)", len(items))
	}
	byPath := map[string]infrarag.IndexItem{}
	for _, it := range items {
		byPath[it.Path] = it
	}
	if byPath["server/api.go"].Scope != "server" || byPath["server/api.go"].Content != "package main\n" {
		t.Fatalf("server/api.go неправильно собран: %+v", byPath["server/api.go"])
	}
	if byPath["web/index.ts"].Scope != "web" {
		t.Fatalf("web/index.ts scope = %q, want web", byPath["web/index.ts"].Scope)
	}
}

// fakeProjectIndexer — hermetic-индексатор (реализует Indexer): записывает
// вызовы EnsureCollection/IndexProject, без сети.
type fakeProjectIndexer struct {
	mu      sync.Mutex
	ensured int
	calls   []fakeIndexCall
	indexed bool // BranchIndexed: индекс ветки уже актуален
}

type fakeIndexCall struct {
	project string
	items   []infrarag.IndexItem
	opts    infrarag.IndexOptions
}

func (f *fakeProjectIndexer) EnsureCollection(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured++
	return 8, nil
}

func (f *fakeProjectIndexer) IndexProject(_ context.Context, project string, items []infrarag.IndexItem, opts infrarag.IndexOptions) (*infrarag.IndexResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeIndexCall{project: project, items: items, opts: opts})
	return &infrarag.IndexResult{Files: len(items), Chunks: len(items) * 2}, nil
}

func (f *fakeProjectIndexer) BranchIndexed(context.Context, string, string, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.indexed, nil
}

// TestIndexProjectRAGHermetic — IndexProjectRAG подготавливает коллекцию,
// собирает файлы реального каталога и вызывает IndexProject по имени проекта.
// Повторный прогон успешен (на уровне IndexClient идемпотентен: старые точки
// удаляются до загрузки) — фоновый вызов не «накапливает» состояние.
func TestIndexProjectRAGHermetic(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "main.go", "package main\n")
	writeTestFile(t, dir, "server/handler.go", "package server\n")

	idx := &fakeProjectIndexer{}
	opts := infrarag.IndexOptions{Branch: infrarag.MainBranch, CommitSHA: "c0ffee"}
	res, err := IndexProjectRAG(context.Background(), idx, "proj-lg", dir, opts)
	if err != nil {
		t.Fatalf("первый прогон: %v", err)
	}
	if res.Files != 2 {
		t.Fatalf("Files = %d, want 2", res.Files)
	}
	if res.Chunks != 4 {
		t.Fatalf("Chunks = %d, want 4", res.Chunks)
	}

	// Повторный прогон: успешен и пишет те же данные (идемпотентность вызова).
	res2, err := IndexProjectRAG(context.Background(), idx, "proj-lg", dir, opts)
	if err != nil || res2.Files != 2 {
		t.Fatalf("повторный прогон: err=%v Files=%d", err, res2.Files)
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if len(idx.calls) != 2 {
		t.Fatalf("IndexProject вызван %d раз(а), want 2", len(idx.calls))
	}
	for i, c := range idx.calls {
		if c.project != "proj-lg" {
			t.Fatalf("вызов %d: проект %q, want proj-lg", i, c.project)
		}
		if len(c.items) != 2 {
			t.Fatalf("вызов %d: files %d, want 2", i, len(c.items))
		}
		if c.opts != opts {
			t.Fatalf("вызов %d: опции индексации %+v, want %+v", i, c.opts, opts)
		}
	}
	if idx.ensured != 2 {
		t.Fatalf("EnsureCollection вызван %d раз(а), want 2", idx.ensured)
	}
}

// TestBranchIndexUpToDate — пустой коммит → проверка не нужна (false, nil);
// иначе отдаётся результат BranchIndexed.
func TestBranchIndexUpToDate(t *testing.T) {
	idx := &fakeProjectIndexer{indexed: true}
	done, err := BranchIndexUpToDate(context.Background(), idx, "p", "main", "abc")
	if err != nil || !done {
		t.Fatalf("BranchIndexUpToDate = %v, %v; want true, nil", done, err)
	}
	done, err = BranchIndexUpToDate(context.Background(), idx, "p", "main", "")
	if err != nil || done {
		t.Fatalf("пустой коммит = %v, %v; want false, nil", done, err)
	}
}

// writeTestFile создаёт файл (и родительские каталоги) в директории.
func writeTestFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
