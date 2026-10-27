package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"ai/infrastructure/workspace"
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
	// Кадры длиннее 125 байт (например, gate с русским summary или снимок
	// доски) несут extended length — без его разбора тело читается со сдвигом.
	switch n {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			t.Fatalf("чтение extended length: %v", err)
		}
		n = int(ext[0])<<8 | int(ext[1])
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			t.Fatalf("чтение extended length: %v", err)
		}
		for _, b := range ext {
			n = n<<8 | int(b)
		}
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		t.Fatalf("тело кадра: %v", err)
	}
	return body
}

// TestSessionSnapshotDeliversStateRightAfterSubscribe проверяет механизм А:
// broadcastSnapshot публикует клиенту актуальное состояние сессии
// (status + board + tokens) сразу после WS-подключения. Раньше клиент ждал
// первого события статуса — проект с уже идущей оркестрацией при
// открытии/рефреше показывал бы устаревший «idle».
func TestSessionSnapshotDeliversStateRightAfterSubscribe(t *testing.T) {
	srv, _, _ := newTestServer(t)

	sess, _, err := srv.getOrCreate("snap-proj")
	if err != nil {
		t.Fatalf("getOrCreate: %v", err)
	}
	// Имитируем идущую оркестрацию: снапшот обязан сообщить running.
	sess.mu.Lock()
	sess.running = true
	sess.mu.Unlock()

	srvConn, cliConn := net.Pipe()
	defer cliConn.Close()
	ws := &WsConn{conn: srvConn, br: bufio.NewReader(srvConn), closed: make(chan struct{})}
	t.Cleanup(func() { _ = ws.Close() })
	srv.hub.Subscribe("snap-proj", ws)

	sess.broadcastSnapshot()

	got := map[string]bool{}
	for i := 0; i < 3; i++ {
		var ev struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(readFramePayload(t, cliConn), &ev); err != nil {
			t.Fatalf("кадр %d: %v", i, err)
		}
		got[ev.Type] = true
		if ev.Type == "status" {
			var st struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(ev.Payload, &st); err != nil {
				t.Fatalf("status payload: %v", err)
			}
			if st.Status != "running" {
				t.Fatalf("status = %q, want running", st.Status)
			}
		}
	}
	for _, typ := range []string{"status", "board", "tokens"} {
		if !got[typ] {
			t.Fatalf("снапшот не содержит событие %q", typ)
		}
	}

	sess.mu.Lock()
	sess.running = false
	sess.mu.Unlock()
}
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

// --- внутренние слушатели шины проекта (Ф-2) ---

// TestHubEmitListeners — Listen/Emit/Unlisten: событие доставляется подписчику
// только своего проекта; повторная подписка заменяет слушателя; после
// Unlisten доставки нет.
func TestHubEmitListeners(t *testing.T) {
	h := NewHub()

	got := make(chan ProjectEvent, 8)
	h.Listen("p1", "a", func(ev ProjectEvent) { got <- ev })
	h.Listen("p2", "a", func(ev ProjectEvent) { t.Fatalf("чужой проект получил событие: %+v", ev) })

	h.Emit("p1", ProjectEvent{Type: EventBoardChanged, Detail: "тест"})
	select {
	case ev := <-got:
		if ev.Type != EventBoardChanged || ev.Detail != "тест" {
			t.Fatalf("событие = %+v, want board_changed/тест", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("слушатель не получил событие")
	}

	// Повторная подписка под тем же именем заменяет слушателя.
	replaced := false
	h.Listen("p1", "a", func(ev ProjectEvent) { replaced = true })
	h.Emit("p1", ProjectEvent{Type: EventBoardChanged})
	if !replaced {
		t.Fatal("повторная подписка не заменила слушателя")
	}
	if n := len(got); n != 0 {
		t.Fatalf("старый слушатель ещё жив: %d событий в канале", n)
	}

	// Unlisten отписывает.
	h.Unlisten("p1", "a")
	h.Emit("p1", ProjectEvent{Type: EventChatUpdated})
	select {
	case ev := <-got:
		t.Fatalf("событие после Unlisten: %+v", ev)
	default:
	}
}

// readWSEvent читает один WS-кадр и возвращает тип события и сырой payload.
func readWSEvent(t *testing.T, conn net.Conn, timeout time.Duration) (string, json.RawMessage, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		var ev struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		hdr := make([]byte, 2)
		if _, err := io.ReadFull(conn, hdr); err != nil {
			continue
		}
		length := int64(hdr[1] & 0x7f)
		switch length {
		case 126:
			ext := make([]byte, 2)
			if _, err := io.ReadFull(conn, ext); err != nil {
				continue
			}
			length = int64(ext[0])<<8 | int64(ext[1])
		case 127:
			ext := make([]byte, 8)
			if _, err := io.ReadFull(conn, ext); err != nil {
				continue
			}
			length = 0
			for _, b := range ext {
				length = length<<8 | int64(b)
			}
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(conn, body); err != nil {
			continue
		}
		if err := json.Unmarshal(body, &ev); err != nil {
			continue
		}
		return ev.Type, ev.Payload, true
	}
	return "", nil, false
}

// readWSStatus ищет в потоке WS первый кадр status и возвращает его payload.
func readWSStatus(t *testing.T, conn net.Conn) *statusEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		typ, payload, ok := readWSEvent(t, conn, 2*time.Second)
		if !ok || typ != "status" {
			continue
		}
		var st statusEvent
		if err := json.Unmarshal(payload, &st); err != nil {
			continue
		}
		return &st
	}
	return nil
}

// newTestServer создаёт тестовый сервер с miniredis и чистым registry.
func newTestServer(t *testing.T) (*Server, http.Handler, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	wsPath := filepath.Join(t.TempDir(), "workspaces.json")
	srv, err := NewServer(Config{WorkspacesPath: wsPath})
	if err != nil {
		t.Fatal(err)
	}
	return srv, srv.routes(), mr
}

// registerTestDir регистрирует обычный (non-git) проект.
func registerTestDir(t *testing.T, srv *Server, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.reg.Add(workspace.AddParams{Name: name, Kind: workspace.KindDir, Root: dir, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	return dir
}
