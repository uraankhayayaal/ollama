// Тесты Manager: singleflight запуска сервера, кэш живых клиентов, эвикция
// простоя. Реальные процессы языкового сервера не запускаются — lspStart подменяется.

package lspclient

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai/stackdetect"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

// newFakeClient создаёт живой Client поверх in-memory потока (без процесса).
// Клиент «живой»: conn.Done не закрыт, диалог не нужен.
func newFakeClient(t *testing.T) *Client {
	t.Helper()
	dir := writeProject(t)
	left, right := jsonrpc2.NewChannelStreamPair(0)
	srv := &fakeServer{root: dir}
	_, sconn, sclient := protocol.NewServer(context.Background(), srv, right)
	srv.client = sclient
	t.Cleanup(func() { sconn.Close() })
	_, cconn, cserver := protocol.NewClient(context.Background(), clientHandler{store: newDiagStore()}, left)
	return &Client{
		dir:    dir,
		conn:   cconn,
		server: cserver,
		cancel: func() { _ = cconn.Close() }, // отмечает Alive=false
		diags:  newDiagStore(),
		open:   make(map[string]openDoc),
	}
}

func TestManagerSingleflightStart(t *testing.T) {
	var startCount atomic.Int32
	old := lspStart
	lspStart = func(_ context.Context, cfg Config) (*Client, error) {
		startCount.Add(1)
		time.Sleep(60 * time.Millisecond) // имитация старта gopls
		return newFakeClient(t), nil
	}
	t.Cleanup(func() { lspStart = old })

	mgr := NewManager()
	ctx := context.Background()
	key := "test-singleflight"
	const N = 5
	var wg sync.WaitGroup
	wg.Add(N)
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			c, err := mgr.Client(ctx, "/tmp/"+key, stackdetect.KindGo)
			if err != nil {
				errs <- err
				return
			}
			if c == nil {
				t.Error("клиент nil")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	if startCount.Load() != 1 {
		t.Fatalf("ожидался 1 запуск, получили %d", startCount.Load())
	}
}

func TestManagerReturnsExistingLiveClient(t *testing.T) {
	c := newFakeClient(t)
	mgr := NewManager()
	mgr.mu.Lock()
	mgr.clients["go|/tmp/x"] = c
	mgr.lastUsed["go|/tmp/x"] = time.Now().Add(-time.Minute) // old lastUsed
	mgr.mu.Unlock()

	got, err := mgr.Client(context.Background(), "/tmp/x", stackdetect.KindGo)
	if err != nil {
		t.Fatal(err)
	}
	if got != c {
		t.Fatal("должен вернуть тот же объект клиента")
	}
	mgr.mu.Lock()
	lu := mgr.lastUsed["go|/tmp/x"]
	mgr.mu.Unlock()
	if time.Since(lu) > time.Second {
		t.Fatal("lastUsed должен обновиться на текущий момент")
	}
}

func TestManagerEvictsIdleClients(t *testing.T) {
	c := newFakeClient(t)
	mgr := NewManager()
	key := "go|/tmp/evict"
	mgr.mu.Lock()
	mgr.clients[key] = c
	mgr.lastUsed[key] = time.Now().Add(-2 * time.Hour)
	mgr.mu.Unlock()

	closed := mgr.reapIdle(30 * time.Minute)
	if closed != 1 {
		t.Fatalf("ожидалась 1 эвикция, got %d", closed)
	}
	mgr.mu.Lock()
	_, exists := mgr.clients[key]
	mgr.mu.Unlock()
	if exists {
		t.Fatal("клиент должен был быть удалён после idle")
	}
}

func TestManagerCloseIdlePreservesRecent(t *testing.T) {
	fresh := newFakeClient(t)
	stale := newFakeClient(t)
	mgr := NewManager()
	mgr.mu.Lock()
	mgr.clients["go|/tmp/fresh"] = fresh
	mgr.lastUsed["go|/tmp/fresh"] = time.Now()
	mgr.clients["go|/tmp/stale"] = stale
	mgr.lastUsed["go|/tmp/stale"] = time.Now().Add(-2 * time.Hour)
	mgr.mu.Unlock()

	closed := mgr.CloseIdle(30 * time.Minute)
	if closed != 1 {
		t.Fatalf("ожидалась 1 эвикция, got %d", closed)
	}
	mgr.mu.Lock()
	_, exists := mgr.clients["go|/tmp/fresh"]
	mgr.mu.Unlock()
	if !exists {
		t.Fatal("свежий клиент не должен удаляться")
	}
}

func TestManagerClose(t *testing.T) {
	c1 := newFakeClient(t)
	c2 := newFakeClient(t)
	mgr := NewManager()
	mgr.mu.Lock()
	mgr.clients["a"] = c1
	mgr.clients["b"] = c2
	mgr.lastUsed["a"] = time.Now()
	mgr.lastUsed["b"] = time.Now()
	mgr.mu.Unlock()

	mgr.Close()
	mgr.mu.Lock()
	n := len(mgr.clients)
	lu := len(mgr.lastUsed)
	mgr.mu.Unlock()
	if n != 0 || lu != 0 {
		t.Fatalf("после Close: clients=%d, lastUsed=%d — должно быть 0", n, lu)
	}
}

// maybeReapIdle вызывается в Client() после попадания живого клиента; при
// выключенной эвикции (LSP_IDLE_TTL=0) ничего не удаляется.
func TestManagerNoReapWhenTTLZero(t *testing.T) {
	t.Setenv("LSP_IDLE_TTL", "0")
	c := newFakeClient(t)
	mgr := NewManager()
	mgr.mu.Lock()
	mgr.clients["go|/tmp/noreap"] = c
	mgr.lastUsed["go|/tmp/noreap"] = time.Now().Add(-time.Hour)
	mgr.lastReap = time.Time{} // заставляем maybeReap пройти проверку reapInterval
	mgr.mu.Unlock()

	mgr.maybeReapIdle()
	mgr.mu.Lock()
	_, exists := mgr.clients["go|/tmp/noreap"]
	mgr.mu.Unlock()
	if !exists {
		t.Fatal("при LSP_IDLE_TTL=0 клиент не должен удаляться")
	}
}
