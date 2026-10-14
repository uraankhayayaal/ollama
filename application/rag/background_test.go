// Тесты оркестратора фоновой индексации (application/rag/background.go):
// single-flight по слоту, запуск в горутине, отчёты в IndexSink.
package rag

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	infrarag "ai/infrastructure/rag"
)

// recordingSink — IndexSink, запоминающий отчёты.
type recordingSink struct {
	mu     sync.Mutex
	info   []string
	warn   []string
	status []string
}

func (r *recordingSink) Info(m string)   { r.mu.Lock(); r.info = append(r.info, m); r.mu.Unlock() }
func (r *recordingSink) Warn(m string)   { r.mu.Lock(); r.warn = append(r.warn, m); r.mu.Unlock() }
func (r *recordingSink) Status(m string) { r.mu.Lock(); r.status = append(r.status, m); r.mu.Unlock() }

func (r *recordingSink) hasStatus(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.status {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// fakeCloser считает Close.
type fakeCloser struct{ closed atomic.Int32 }

func (c *fakeCloser) Close() error { c.closed.Add(1); return nil }

// bgBlockingIndexer блокируется в IndexProject до release.
type bgBlockingIndexer struct {
	launched chan struct{}
	release  chan struct{}
	once     sync.Once
}

func newBGBlockingIndexer() *bgBlockingIndexer {
	return &bgBlockingIndexer{launched: make(chan struct{}), release: make(chan struct{})}
}

func (i *bgBlockingIndexer) EnsureCollection(context.Context) (int, error) { return 8, nil }
func (i *bgBlockingIndexer) IndexProject(_ context.Context, _ string, _ []infrarag.IndexItem, _ infrarag.IndexOptions) (*infrarag.IndexResult, error) {
	i.once.Do(func() { close(i.launched) })
	<-i.release
	return &infrarag.IndexResult{Files: 1, Chunks: 2}, nil
}
func (i *bgBlockingIndexer) BranchIndexed(context.Context, string, string, string) (bool, error) {
	return false, nil
}

func TestIndexSlotKey(t *testing.T) {
	if got := indexSlotKey("note", "main"); got != "branch:main" {
		t.Fatalf("слот ветки = %q", got)
	}
	if got := indexSlotKey(" src/x ", ""); got != "src:src/x" {
		t.Fatalf("слот источника = %q", got)
	}
}

func TestBackgroundIndexerSingleFlight(t *testing.T) {
	sink := &recordingSink{}
	bi := newBGBlockingIndexer()
	closer := &fakeCloser{}
	b := NewBackgroundIndexer("proj", sink, func() (Indexer, io.Closer, error) {
		return bi, closer, nil
	})
	run := func(ctx context.Context, idx Indexer) (*infrarag.IndexResult, string, bool, error) {
		res, err := idx.IndexProject(ctx, "proj", nil, infrarag.IndexOptions{Branch: "main"})
		return res, "main", false, err
	}

	if err := b.Start("note", "main", run); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-bi.launched:
	case <-time.After(2 * time.Second):
		t.Fatal("прогон не добрался до IndexProject")
	}
	// Повторный запуск для той же ветки отклоняется, а свежий клиент закрыт.
	if err := b.Start("note", "main", run); err == nil {
		t.Fatal("повторный запуск той же ветки должен отклоняться")
	}
	if got := closer.closed.Load(); got != 1 {
		t.Fatalf("closer закрытий после отказа = %d, want 1", got)
	}

	close(bi.release)
	b.Wait()
	if got := closer.closed.Load(); got != 2 {
		t.Fatalf("closer закрытий всего = %d, want 2", got)
	}
	if !sink.hasStatus("RAG-индекс проекта обновлён") {
		t.Fatalf("нет финального отчёта: %+v", sink.status)
	}
}

func TestBackgroundIndexerSkipped(t *testing.T) {
	sink := &recordingSink{}
	b := NewBackgroundIndexer("proj", sink, func() (Indexer, io.Closer, error) {
		return &fakeProjectIndexer{}, &fakeCloser{}, nil
	})
	run := func(context.Context, Indexer) (*infrarag.IndexResult, string, bool, error) {
		return nil, "main", true, nil
	}
	if err := b.Start("ветка main", "main", run); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b.Wait()
	if !sink.hasStatus("RAG-индекс проекта актуален") {
		t.Fatalf("нет отчёта об актуальном индексе: %+v", sink.status)
	}
}

func TestBackgroundIndexerError(t *testing.T) {
	sink := &recordingSink{}
	b := NewBackgroundIndexer("proj", sink, func() (Indexer, io.Closer, error) {
		return &fakeProjectIndexer{}, &fakeCloser{}, nil
	})
	run := func(context.Context, Indexer) (*infrarag.IndexResult, string, bool, error) {
		return nil, "", false, errors.New("qdrant недоступен")
	}
	if err := b.Start("note", "main", run); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b.Wait()
	if !sink.hasStatus("Фоновая индексация RAG не удалась") {
		t.Fatalf("нет отчёта об ошибке: %+v", sink.status)
	}
}

func TestBackgroundIndexerFactoryError(t *testing.T) {
	sink := &recordingSink{}
	b := NewBackgroundIndexer("proj", sink, func() (Indexer, io.Closer, error) {
		return nil, nil, errors.New("RAG не создан")
	})
	err := b.Start("note", "main", func(context.Context, Indexer) (*infrarag.IndexResult, string, bool, error) {
		t.Fatal("прогон не должен запускаться при ошибке фабрики")
		return nil, "", false, nil
	})
	if err == nil || err.Error() != "RAG не создан" {
		t.Fatalf("err = %v, want ошибку фабрики", err)
	}
	if len(sink.status) != 0 {
		t.Fatalf("при ошибке фабрики отчётов быть не должно: %+v", sink.status)
	}
}

func TestBackgroundIndexerParallelBranches(t *testing.T) {
	sink := &recordingSink{}
	i1 := newBGBlockingIndexer()
	i2 := newBGBlockingIndexer()
	var n int
	var mu sync.Mutex
	b := NewBackgroundIndexer("proj", sink, func() (Indexer, io.Closer, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return i1, &fakeCloser{}, nil
		}
		return i2, &fakeCloser{}, nil
	})
	run := func(ctx context.Context, idx Indexer) (*infrarag.IndexResult, string, bool, error) {
		res, err := idx.IndexProject(ctx, "proj", nil, infrarag.IndexOptions{})
		return res, "", false, err
	}
	if err := b.Start("a", "branchA", run); err != nil {
		t.Fatalf("Start A: %v", err)
	}
	if err := b.Start("b", "branchB", run); err != nil {
		t.Fatalf("Start B: %v", err)
	}
	close(i1.release)
	close(i2.release)
	b.Wait()
	if len(sink.status) < 2 {
		t.Fatalf("ожидались два стартовых отчёта: %+v", sink.status)
	}
}
