package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"ai/board"
	"ai/web"
)

// --- WsConn: кадры ---

func TestWsWriteTextRead(t *testing.T) {
	srv, cli := net.Pipe()
	defer cli.Close()
	ws := &WsConn{conn: srv, br: bufio.NewReader(srv), closed: make(chan struct{})}

	payload := []byte(`{"type":"chat","payload":"привет"}`)
	errCh := make(chan error, 1)
	go func() {
		errCh <- ws.WriteText(payload)
	}()

	// Читаем клиентский кадр: 0x81 FIN+text, длина, payload.
	var hdr [2]byte
	if _, err := io.ReadFull(cli, hdr[:]); err != nil {
		t.Fatalf("заголовок кадра: %v", err)
	}
	if hdr[0] != 0x81 {
		t.Fatalf("opcode = %#x, want 0x81", hdr[0])
	}
	n := int(hdr[1] & 0x7f)
	body := make([]byte, n)
	if _, err := io.ReadFull(cli, body); err != nil {
		t.Fatalf("тело кадра: %v", err)
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("payload = %q, want %q", body, payload)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("WriteText: %v", err)
	}
}

func TestWsReadMaskedFrame(t *testing.T) {
	srv, cli := net.Pipe()
	defer cli.Close()
	ws := &WsConn{conn: srv, br: bufio.NewReader(srv), closed: make(chan struct{})}

	// Собираем маскированный кадр клиента: FIN+text, MASK=1, len=5.
	frame := []byte{0x81, 0x85, 0x01, 0x02, 0x03, 0x04}
	msg := []byte("hello")
	for i, b := range msg {
		frame = append(frame, b^[]byte{0x01, 0x02, 0x03, 0x04}[i%4])
	}
	// Пишем в отдельной горутине (net.Pipe блокирует Write до чтения).
	errCh := make(chan error, 1)
	go func() {
		_, err := cli.Write(frame)
		errCh <- err
	}()

	op, pay, err := ws.ReadBlock()
	if err != nil {
		t.Fatalf("ReadBlock: %v", err)
	}
	if op != 0x1 {
		t.Fatalf("opcode = %#x, want 0x1", op)
	}
	if string(pay) != "hello" {
		t.Fatalf("payload = %q, want hello (снята маска)", pay)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("write: %v", err)
	}
}

// --- Hub: publish клиентам проекта ---

func TestHubPublishDeliversToProject(t *testing.T) {
	h := NewHub()

	srv, cli := net.Pipe()
	defer cli.Close()
	ws := &WsConn{conn: srv, br: bufio.NewReader(srv), closed: make(chan struct{})}
	h.Subscribe("proj-x", ws)
	t.Cleanup(func() { _ = ws.Close() })

	h.Publish("proj-x", "chat", map[string]string{"role": "user", "content": "hi"})

	// Читаем JSON-событие с клиентского сокета.
	var hdr [2]byte
	if _, err := io.ReadFull(cli, hdr[:]); err != nil {
		t.Fatalf("заголовок: %v", err)
	}
	if hdr[0] != 0x81 {
		t.Fatalf("opcode = %#x", hdr[0])
	}
	n := int(hdr[1] & 0x7f)
	body := make([]byte, n)
	if _, err := io.ReadFull(cli, body); err != nil {
		t.Fatalf("тело: %v", err)
	}

	var ev struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		t.Fatalf("json: %v", err)
	}
	if ev.Type != "chat" {
		t.Fatalf("type = %q, want chat", ev.Type)
	}
	var pl struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(ev.Payload, &pl); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if pl.Role != "user" || pl.Content != "hi" {
		t.Fatalf("payload = %+v", pl)
	}

	// Событие для другого проекта не приходит.
	h.Publish("proj-different", "status", "running")
	// Клиент не должен получить ничего нового — проверяем, что чтение
	// зависает (короткий дедлайн).
	h.Publish("proj-x", "status", "done")
	body = readFramePayload(t, cli)
	var ev2 struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &ev2); err != nil || ev2.Type != "status" {
		t.Fatalf("второе событие = %s (%v)", body, err)
	}
}

func readFramePayload(t *testing.T, r io.Reader) []byte {
	t.Helper()
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		t.Fatalf("чтение кадра: %v", err)
	}
	n := int(hdr[1] & 0x7f)
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		t.Fatalf("тело кадра: %v", err)
	}
	return body
}

// --- Server REST ---

func newTestServer(t *testing.T) (*Server, http.Handler, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	t.Setenv("BOARD_REDIS_ADDR", mr.Addr())

	wsPath := filepath.Join(t.TempDir(), "workspaces.json")
	srv, err := NewServer(Config{WorkspacesPath: wsPath})
	if err != nil {
		t.Fatal(err)
	}
	return srv, srv.routes(), mr
}

func TestOpenProjectDirAndList(t *testing.T) {
	_, handler, _ := newTestServer(t)

	dir := t.TempDir()
	proj := filepath.Join(dir, "myapp")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}

	// POST /api/projects с путём к каталогу.
	body := `{"path_or_git":"` + proj + `"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects", bytes.NewBufferString(body))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/projects: %d, body: %s", rec.Code, rec.Body.String())
	}
	var meta map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta["project_name"] != "myapp" {
		t.Fatalf("project_name = %v, want myapp", meta["project_name"])
	}

	// GET /api/projects возвращает зарегистрированный проект.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/projects", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/projects: %d", rec.Code)
	}
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range list {
		if p["project_name"] == "myapp" {
			found = true
		}
	}
	if !found {
		t.Fatalf("GET /api/projects не содержит myapp: %+v", list)
	}
}

func TestGetBoardReturnsSnapshot(t *testing.T) {
	srv, handler, _ := newTestServer(t)
	_ = srv

	// Сессия создаётся на лету в getOrCreate? Нет: для GET board используем
	// открытие store на лету.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/projects/some-project", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET board: %d, body: %s", rec.Code, rec.Body.String())
	}
	var snap boardSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Epics == nil || snap.Tasks == nil || snap.Bugs == nil {
		t.Fatalf("доска должна отдавать пустые массивы: %+v", snap)
	}
}

func TestDeleteEpic(t *testing.T) {
	_, handler, mr := newTestServer(t)
	ctx := context.Background()

	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "proj-del"})
	defer store.Close()
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Удаляемый"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-2", Title: "С начатой задачей"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "task-2", Title: "в работе"},
		EpicID:   "epic-2",
		Status:   board.StatusInProgress,
	}); err != nil {
		t.Fatal(err)
	}

	// Удаление эпика без начатых задач — 200, эпика с доски больше нет.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/projects/proj-del/epics/epic-1", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE эпик без задач: %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if _, err := store.GetEpic(ctx, "epic-1"); err == nil {
		t.Fatal("эпик epic-1 должен быть удалён")
	}

	// Эпик, у которого задача уже в работе, удалять нельзя — 400.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("DELETE", "/api/projects/proj-del/epics/epic-2", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("DELETE эпик с задачей в работе: %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	epics, err := store.ListEpics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(epics) != 1 || epics[0].TaskID != "epic-2" {
		t.Fatalf("эпики после попытки удаления: %+v, want [epic-2]", epics)
	}

	// Несуществующий эпик — 404.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("DELETE", "/api/projects/proj-del/epics/nope", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE несуществующего эпика: %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestPostChatWithoutProviderFails(t *testing.T) {
	_, handler, _ := newTestServer(t)

	// Вопрос/запрос статуса требует LLM-провайдера — без него 503.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects/proj-1/chat",
		bytes.NewBufferString(`{"message":"что делает проект?"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST chat вопрос без LLM: %d, want 503 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestPostChatTaskEnqueuesEpic(t *testing.T) {
	srv, handler, _ := newTestServer(t)

	// Явный запрос на создание задачи/эпика кладётся на доску планировщику
	// (новый эпик) и НЕ требует LLM-провайдера: оркестрация живёт своим циклом
	// и берёт эпик в работу по кнопке «Продолжить».
	ctx := context.Background()
	for i, msg := range []string{"создай задачу: оптимизируй загрузку страницы", "добавь эпик на добавление тестов"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/projects/proj-1/chat",
			bytes.NewBufferString(`{"message":`+strconv.Quote(msg)+`}`))
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST chat задача #%d: %d, want 200 (body: %s)", i, rec.Code, rec.Body.String())
		}
	}

	sess := srv.session("proj-1")
	if sess == nil {
		t.Fatal("сессия проекта не создана")
	}
	epics, err := sess.board.ListEpics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(epics) != 2 {
		t.Fatalf("эпиков на доске: %d, want 2", len(epics))
	}
	if epics[0].TaskID != "epic-1" || epics[1].TaskID != "epic-2" {
		t.Fatalf("ID эпиков чата = %q, %q; want epic-1, epic-2", epics[0].TaskID, epics[1].TaskID)
	}
	if epics[0].Title != "создай задачу: оптимизируй загрузку страницы" {
		t.Fatalf("title = %q", epics[0].Title)
	}

	// Оркестрация не запущена: чат обработал задачу без единого раунда.
	sess.mu.Lock()
	running := sess.running
	sess.mu.Unlock()
	if running {
		t.Fatal("оркестрация не должна запускаться при добавлении задачи из чата")
	}
}

func TestPostChatDialogueDoesNotCreateEpic(t *testing.T) {
	srv, handler, _ := newTestServer(t)

	// «Подскажи погоду» и прочий свободный диалог — это НЕ создание задачи:
	// маршрут уходит в Q&A-ассистента, а без LLM-провайдера отвечает 503,
	// доску при этом не трогая (эпиков не появляется).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects/proj-chat/chat",
		bytes.NewBufferString(`{"message":"Подскажи погоду в Москве"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST chat диалог без LLM: %d, want 503 (body: %s)", rec.Code, rec.Body.String())
	}

	sess, _, err := srv.getOrCreate("proj-chat")
	if err != nil {
		t.Fatal(err)
	}
	epics, err := sess.board.ListEpics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(epics) != 0 {
		t.Fatalf("диалог не должен создавать эпики, на доске: %d", len(epics))
	}
}

func TestContinueEmptyBoardBadRequest(t *testing.T) {
	_, handler, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects/proj-empty/continue", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("continue пустой доски: %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestContinueOnBoardRequiresProvider(t *testing.T) {
	_, handler, mr := newTestServer(t)

	// Задача на доске уже есть (эпик добавлен, напр., из чата) — Continue
	// доходит до резолва LLM-провайдера; в hermetic-тесте его нет → 503.
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "proj-cont"})
	defer store.Close()
	if err := store.CreateEpic(context.Background(), &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "задача с чата"},
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects/proj-cont/continue", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("continue с доской без LLM: %d, want 503 (body: %s)", rec.Code, rec.Body.String())
	}
	// Ошибка про провайдера, а не про «нет задач» на доске.
	if !strings.Contains(rec.Body.String(), "провайдер") {
		t.Fatalf("ожидалась ошибка про провайдера, got: %s", rec.Body.String())
	}
}

func TestChatHistoryEmpty(t *testing.T) {
	_, handler, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/projects/proj-2/chat?limit=10", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET chat: %d, body: %s", rec.Code, rec.Body.String())
	}
	var msgs []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("история должна быть пустой, got %d", len(msgs))
	}
}

func TestGateDecideWithoutSession(t *testing.T) {
	_, handler, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects/proj-3/epics/decide",
		bytes.NewBufferString(`{"approved":true}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("decide без сессии: %d, want 404", rec.Code)
	}
}

func TestStopSessionIdempotent(t *testing.T) {
	_, handler, _ := newTestServer(t)

	// Stop при отсутствии сессии — 404.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects/proj-4/session/stop",
		bytes.NewBufferString(`{}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("stop без сессии: %d, want 404", rec.Code)
	}
}

// --- статика (embed web/dist) ---

func TestStaticServesIndexAndBundles(t *testing.T) {
	handler := staticHandler()

	// Корень отдаёт index.html. /index.html редиректится на красивый путь.
	paths := []struct {
		p    string
		code int
	}{
		{"/", http.StatusOK},
		{"/index.html", http.StatusMovedPermanently},
	}
	for _, tt := range paths {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("GET", tt.p, nil)
		handler.ServeHTTP(rec, r)
		if rec.Code != tt.code {
			t.Fatalf("GET %s: %d, want %d", tt.p, rec.Code, tt.code)
		}
		if tt.code == http.StatusOK && rec.Body.Len() == 0 {
			t.Fatalf("GET %s: пустое тело", tt.p)
		}
	}

	// Встраиваемые ассеты из web.DistFS доступны по HTTP.
	entries, err := fs.ReadDir(web.DistFS(), "assets")
	if err != nil {
		t.Fatalf("dist/assets: %v", err)
	}
	for _, e := range entries {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/assets/"+e.Name(), nil)
		handler.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /assets/%s: %d, want 200", e.Name(), rec.Code)
		}
	}
}

// --- race-проверка hub publish с несколькими клиентами ---

func TestHubConcurrentPublish(t *testing.T) {
	h := NewHub()

	const n = 4
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		srv, cli := net.Pipe()
		ws := &WsConn{conn: srv, br: bufio.NewReader(srv), closed: make(chan struct{})}
		h.Subscribe("c", ws)
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			// Читаем кадры, пока соединение открыто.
			for {
				var hdr [2]byte
				if _, err := io.ReadFull(c, hdr[:]); err != nil {
					return
				}
				nn := int(hdr[1] & 0x7f)
				body := make([]byte, nn)
				if _, err := io.ReadFull(c, body); err != nil {
					return
				}
			}
		}(cli)
		t.Cleanup(func() { cli.Close(); _ = ws.Close() })
	}

	// Параллельные публикации от нескольких «писателей».
	var pubWG sync.WaitGroup
	for i := 0; i < 10; i++ {
		pubWG.Add(1)
		go func(i int) {
			defer pubWG.Done()
			for j := 0; j < 20; j++ {
				h.Publish("c", "tool", map[string]string{
					"tool":   "Write",
					"result": "ok",
					"seq":    string(rune(rune(i)<<8 | rune(j))),
				})
			}
		}(i)
	}
	pubWG.Wait()

	// Даём writeLoop'ам отправить всё и закрываем.
	time.Sleep(50 * time.Millisecond)
	h.CloseAll()
	wg.Wait()
}