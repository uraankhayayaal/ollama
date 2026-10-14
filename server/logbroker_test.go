package server

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// readWsFrame читает один текстовый WS-кадр (сервер пишет немаскированные).
func readWsFrame(t *testing.T, r io.Reader) []byte {
	t.Helper()
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		t.Fatalf("ws заголовок: %v", err)
	}
	if hdr[0]&0x0f != 0x1 {
		t.Fatalf("opcode = %#x, want text", hdr[0])
	}
	n := int64(hdr[1] & 0x7f)
	switch n {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			t.Fatalf("ws ext16: %v", err)
		}
		n = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			t.Fatalf("ws ext64: %v", err)
		}
		n = int64(binary.BigEndian.Uint64(ext[:]))
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		t.Fatalf("ws тело: %v", err)
	}
	return body
}

// appendToFile дописывает строки в лог-файл.
func appendToFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// Главное: строки, дописанные после подписки, доезжают до WS-клиента как
// событие type="log". Раньше broadcast молча пропускал все файлы (splitKey не
// разбирал ключ dir::name) — в real-time не уходило ничего.
func TestLogBrokerBroadcastPublishesNewLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proj.log")
	if err := os.WriteFile(path, []byte("до подписки\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := NewHub()
	srv, cli := net.Pipe()
	defer cli.Close()
	ws := &WsConn{conn: srv, br: bufio.NewReader(srv), closed: make(chan struct{})}
	h.Subscribe("proj", ws)
	t.Cleanup(func() { _ = ws.Close() })

	b := newLogBroker(h)
	b.Subscribe("proj", []string{dir}, listLogFiles([]string{dir}))

	appendToFile(t, path, "новая строка\n")
	b.broadcast()

	if err := cli.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var ev struct {
		Type    string     `json:"type"`
		Payload logMessage `json:"payload"`
	}
	body := readWsFrame(t, cli)
	if err := json.Unmarshal(body, &ev); err != nil {
		t.Fatalf("json: %v (%s)", err, body)
	}
	if ev.Type != "log" {
		t.Fatalf("type = %q, want log", ev.Type)
	}
	if ev.Payload.Project != "proj" {
		t.Fatalf("project = %q, want proj", ev.Payload.Project)
	}
	if ev.Payload.File != "proj.log" {
		t.Fatalf("file = %q, want proj.log", ev.Payload.File)
	}
	if ev.Payload.Line != "новая строка" {
		t.Fatalf("line = %q, want «новая строка»", ev.Payload.Line)
	}
}
