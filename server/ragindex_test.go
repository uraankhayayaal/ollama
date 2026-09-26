// Тесты фоновой индексации RAG (Ф-5, Р-2): безопасный мост IndexBackground,
// сбор файлов проекта и полная индексация через фейковый индексатор.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ai/chat"
	"ai/rag"
)

// TestIndexBackgroundBridgeSafe — безопасный мост: выполняется БЕЗ
// подтверждения (не деструктивный), вызывает ActionsBackend и возвращает
// status=success.
func TestIndexBackgroundBridgeSafe(t *testing.T) {
	b := &fakeActions{}
	out, err := newIndexBackgroundTool(b).Execute(map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["status"] != "success" {
		t.Fatalf("status = %v, want success", m["status"])
	}
	if !b.indexBg {
		t.Fatal("IndexBackground должен запускать фоновую индексацию без подтверждения")
	}
}

// TestIndexBackgroundBridgeError — ошибка фоновой индексации возвращается
// мостом как status=error (модель продолжить работу, не прерывая цикл).
func TestIndexBackgroundBridgeError(t *testing.T) {
	b := &fakeActions{indexErr: errors.New("Qdrant недоступен")}
	out, _ := newIndexBackgroundTool(b).Execute(map[string]any{})
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["status"] != "error" {
		t.Fatalf("status = %v, want error (сообщение = %v)", m["status"], m["message"])
	}
}

// TestCollectIndexItemsHermetic — сбор IndexItem'ов из списка файлов: путь,
// scope (ScopeForPath) и содержимое; нечитаемые файлы пропускаются.
func TestCollectIndexItemsHermetic(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "server/api.go", "package main\n")
	writeTestFile(t, dir, "web/index.ts", "const x = 1\n")

	items, err := collectIndexItems(context.Background(), dir, func(dir string) ([]string, error) {
		return []string{"server/api.go", "web/index.ts", "gone.go"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2 (gone.go не читается и пропускается)", len(items))
	}
	byPath := map[string]rag.IndexItem{}
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

// fakeProjectIndexer — hermetic-индексатор (реализует projectIndexerCloser):
// записывает вызовы EnsureCollection/IndexProject, без сети.
type fakeProjectIndexer struct {
	mu      sync.Mutex
	ensured int
	calls   []fakeIndexCall
}

type fakeIndexCall struct {
	project string
	items   []rag.IndexItem
}

func (f *fakeProjectIndexer) EnsureCollection(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured++
	return 8, nil
}

func (f *fakeProjectIndexer) IndexProject(_ context.Context, project string, items []rag.IndexItem) (*rag.IndexResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeIndexCall{project: project, items: items})
	return &rag.IndexResult{Files: len(items), Chunks: len(items) * 2}, nil
}

func (f *fakeProjectIndexer) Close() error { return nil }

// TestIndexProjectRAGHermetic — indexProjectRAG подготавливает коллекцию,
// собирает файлы реального каталога и вызывает IndexProject по имени проекта.
// Повторный прогон успешен (на уровне IndexClient идемпотентен: старые точки
// удаляются до загрузки) — фоновый вызов не «накапливает» состояние.
func TestIndexProjectRAGHermetic(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "main.go", "package main\n")
	writeTestFile(t, dir, "server/handler.go", "package server\n")

	idx := &fakeProjectIndexer{}
	res, err := indexProjectRAG(context.Background(), idx, "proj-lg", dir)
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
	res2, err := indexProjectRAG(context.Background(), idx, "proj-lg", dir)
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
	}
	if idx.ensured != 2 {
		t.Fatalf("EnsureCollection вызван %d раз(а), want 2", idx.ensured)
	}
}

// blockingIndexer — индексатор, блокирующийся в IndexProject до release:
// проверяем, что Session.IndexBackground не блокирует вызов и что повторный
// запуск при идущей индексации отклоняется.
type blockingIndexer struct {
	launched chan struct{}
	release  chan struct{}
	closed   chan struct{}
	// closeOnce — Close идемпотентен: фабрика buildProjectIndexer в тесте отдаёт
	// один и тот же индексатор на каждый вызов, а IndexBackground честно закрывает
	// свежеоткрытый клиент и при отклонении повторного запуска. Без once второй
	// Close паниковал бы «close of closed channel».
	closeOnce sync.Once
}

func newBlockingIndexer() *blockingIndexer {
	return &blockingIndexer{
		launched: make(chan struct{}),
		release:  make(chan struct{}),
		closed:   make(chan struct{}),
	}
}

func (i *blockingIndexer) EnsureCollection(context.Context) (int, error) { return 8, nil }

func (i *blockingIndexer) IndexProject(_ context.Context, p string, _ []rag.IndexItem) (*rag.IndexResult, error) {
	close(i.launched)
	<-i.release
	return &rag.IndexResult{Files: 0, Chunks: 0}, nil
}

func (i *blockingIndexer) Close() error {
	i.closeOnce.Do(func() { close(i.closed) })
	return nil
}

// TestSessionIndexBackground — Session.IndexBackground: запускает горутину
// (возврат без блокировки), повторный вызов при идущей индексации отклоняется,
// по завершении публикуется status-сообщение в чат.
func TestSessionIndexBackground(t *testing.T) {
	orig := buildProjectIndexer
	defer func() { buildProjectIndexer = orig }()

	srv, _, _ := newTestServer(t)
	registerTestDir(t, srv, "proj-rag-bg")
	sess, _, err := srv.getOrCreate("proj-rag-bg")
	if err != nil {
		t.Fatal(err)
	}

	bi := newBlockingIndexer()
	buildProjectIndexer = func() (projectIndexerCloser, error) { return bi, nil }

	start := time.Now()
	if err := sess.IndexBackground(context.Background()); err != nil {
		t.Fatalf("IndexBackground: %v", err)
	}
	// Возврат без блокировки: горутина доходит до IndexProject быстро.
	select {
	case <-bi.launched:
	case <-time.After(2 * time.Second):
		t.Fatal("IndexProject не наступил (горутина не добралась до индексации)")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("IndexBackground заблокировал цикл на %v", elapsed)
	}

	// Повторный запуск при идущей индексации — ошибка.
	if err := sess.IndexBackground(context.Background()); err == nil {
		t.Error("повторный IndexBackground при идущей индексации должен отклоняться")
	}

	// Разрешаем завершить индексацию: ждём status-отчёт в чате.
	close(bi.release)
	if m := waitChatRole(t, sess, chat.RoleStatus, 3*time.Second); !containsCase(m.Content, "RAG-индекс") {
		t.Fatalf("нет status-отчёта об индексации, последний: %q", m.Content)
	}
	select {
	case <-bi.closed:
	default:
		t.Error("клиент RAG фоновой индексации не закрыт после завершения")
	}
}

// TestSessionIndexBackgroundCallerCtxIgnored — регрессия: фоновая индексация
// НЕ наследует отменённый контекст вызывающего (REST-запрос/раунд агента
// завершается до первого embed-вызова). Даже если ctx вызова уже
// отменён при запуске, индексация доходит до IndexProject с живым ctx.
func TestSessionIndexBackgroundCallerCtxIgnored(t *testing.T) {
	orig := buildProjectIndexer
	defer func() { buildProjectIndexer = orig }()

	srv, _, _ := newTestServer(t)
	registerTestDir(t, srv, "proj-rag-cancel")
	sess, _, err := srv.getOrCreate("proj-rag-cancel")
	if err != nil {
		t.Fatal(err)
	}

	idx := &fakeProjectIndexer{}
	buildProjectIndexer = func() (projectIndexerCloser, error) { return idx, nil }

	canceled, cancel := context.WithCancel(context.Background())
	cancel() // контекст вызывающего уже мёртв (как r.Context() после ответа).
	if err := sess.IndexBackground(canceled); err != nil {
		t.Fatalf("IndexBackground с отменённым ctx вызывающего: %v", err)
	}

	if m := waitChatRole(t, sess, chat.RoleStatus, 3*time.Second); !containsCase(m.Content, "RAG-индекс") {
		t.Fatalf("индексация не завершилась со status-отчётом, последний: %q", m.Content)
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if len(idx.calls) != 1 {
		t.Fatalf("IndexProject вызван %d раз(а), want 1 (ctx вызывающего отменён до запуска)", len(idx.calls))
	}
}

// TestRESTProjectIndex — кнопка «Индекс RAG» (опциональный нюанс Ф-5):
// POST /api/projects/{id}/index запускает фоновую индексацию (горутину),
// отвечает 200 {ok,message}; по завершении — status-сообщение в чат.
func TestRESTProjectIndex(t *testing.T) {
	orig := buildProjectIndexer
	defer func() { buildProjectIndexer = orig }()

	srv, handler, _ := newTestServer(t)
	dir := registerTestDir(t, srv, "proj-rag-rest")
	writeTestFile(t, dir, "main.go", "package main\n")

	idx := &fakeProjectIndexer{}
	buildProjectIndexer = func() (projectIndexerCloser, error) { return idx, nil }

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/proj-rag-rest/index", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST index: %d, body: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["ok"] != true {
		t.Fatalf("ответ = %+v", out)
	}

	// Индексация ушла в горутину: ждём финальный status-отчёт в чате.
	sess, _, err := srv.getOrCreate("proj-rag-rest")
	if err != nil {
		t.Fatal(err)
	}
	if m := waitChatRole(t, sess, chat.RoleStatus, 3*time.Second); !containsCase(m.Content, "RAG-индекс") {
		t.Fatalf("нет status-отчёта об индексации, последний: %q", m.Content)
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if len(idx.calls) != 1 || idx.calls[0].project != "proj-rag-rest" {
		t.Fatalf("IndexProject вызван с %+v, want 1 вызов по proj-rag-rest", idx.calls)
	}
}

// TestRESTProjectIndexRAGUnavailable — недоступный клиент RAG: 503, тело с
// сообщением (UI показывает ошибку, доска не ломается).
func TestRESTProjectIndexRAGUnavailable(t *testing.T) {
	orig := buildProjectIndexer
	defer func() { buildProjectIndexer = orig }()
	buildProjectIndexer = func() (projectIndexerCloser, error) {
		return nil, errors.New("клиент RAG не создан (проверь QDRANT_ADDR и EMBEDDING_MODEL)")
	}

	srv, handler, _ := newTestServer(t)
	registerTestDir(t, srv, "proj-rag-rest-err")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/proj-rag-rest-err/index", strings.NewReader(`{}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST index без RAG: %d, want 503 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "QDRANT_ADDR") {
		t.Fatalf("тело 503 не объясняет причину: %s", rec.Body.String())
	}
}

// containsCase — подстрока без учёта регистра.
func containsCase(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
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
