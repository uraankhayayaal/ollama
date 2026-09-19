package server

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
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

func assertLines(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("строки = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("строки[%d] = %q, want %q (все: %v)", i, got[i], want[i], got)
		}
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

// Содержимое до подписки не дублируется в стрим: его уже отдал REST-снапшот.
func TestLogBrokerSkipsPreexistingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proj.log")
	if err := os.WriteFile(path, []byte("старая\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := newLogBroker(NewHub())
	b.Subscribe("proj", []string{dir}, listLogFiles([]string{dir}))
	b.broadcast() // до новых записей — тишина

	appendToFile(t, path, "свежая\n")
	b.broadcast()

	b.mu.Lock()
	sf := b.subscribers["proj"].files[dir+"::proj.log"]
	b.mu.Unlock()
	if sf == nil {
		t.Fatal("файл не отслеживается")
	}
	assertLines(t, sf.readNew(), nil) // всё уже разобрано broadcast
}

// Незавершённая строка копится в remainder и склеивается на следующем такте.
func TestSubFileReadNewHandlesPartialLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proj.log")
	if err := os.WriteFile(path, []byte("нулевая\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sf := &subFile{name: "proj.log", path: path}
	if !sf.openAtEnd() {
		t.Fatal("openAtEnd = false")
	}
	if got := sf.readNew(); len(got) != 0 {
		t.Fatalf("до записи строк быть не должно: %v", got)
	}

	appendToFile(t, path, "первая\nвторая\nнепол")
	assertLines(t, sf.readNew(), []string{"первая", "вторая"})
	if sf.remainder != "непол" {
		t.Fatalf("remainder = %q, want «непол»", sf.remainder)
	}

	appendToFile(t, path, "ная\nтретья\n")
	assertLines(t, sf.readNew(), []string{"неполная", "третья"})
	if sf.remainder != "" {
		t.Fatalf("remainder = %q, want пусто", sf.remainder)
	}
}

// Удаление файла и ротация не должны ронять broadcast (раньше sf.file = nil
// давал панику на следующем такте) и должны перечитывать файл с начала.
func TestSubFileReadNewSurvivesDeleteAndRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proj.log")
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sf := &subFile{name: "proj.log", path: path}
	if !sf.openAtEnd() {
		t.Fatal("openAtEnd = false")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := sf.readNew(); got != nil {
		t.Fatalf("после удаления строк быть не должно: %v", got)
	}
	if sf.file != nil {
		t.Fatal("handle не закрыт после удаления файла")
	}
	// Повторный такт на отсутствующем файле — без паники.
	if got := sf.readNew(); got != nil {
		t.Fatalf("после удаления (2-й такт): %v", got)
	}

	if err := os.WriteFile(path, []byte("новая1\nновая2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertLines(t, sf.readNew(), []string{"новая1", "новая2"})

	// Ротация, когда новый файл БОЛЬШЕ старой позиции: одно сравнение размеров
	// её не ловит, хвост читался бы с середины чужого содержимого.
	if err := os.WriteFile(path, []byte("после ротации\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertLines(t, sf.readNew(), []string{"после ротации"})

	// Обычная допись ротацией не считается — иначе строки дублировались бы.
	appendToFile(t, path, "продолжение\n")
	assertLines(t, sf.readNew(), []string{"продолжение"})

	// Усечение (copytruncate): файл стал короче позиции.
	if err := os.WriteFile(path, []byte("короче\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertLines(t, sf.readNew(), []string{"короче"})
}

// Подписка идемпотентна и добирает файлы, созданные позже (rescan): проект
// может стартовать вообще без логов.
func TestLogBrokerSubscribeIdempotentAndRescans(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "proj.log"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := newLogBroker(NewHub())
	b.Subscribe("p", []string{dir}, listLogFiles([]string{dir}))
	b.Subscribe("p", []string{dir}, listLogFiles([]string{dir}))

	b.mu.Lock()
	n := len(b.subscribers["p"].files)
	b.mu.Unlock()
	if n != 1 {
		t.Fatalf("отслеживается файлов = %d, want 1 (дублей быть не должно)", n)
	}

	// Подписка при отсутствии логов — каталоги запоминаются.
	b.Subscribe("fresh", []string{dir}, listLogFiles([]string{dir}))
	if err := os.Remove(filepath.Join(dir, "proj.log")); err != nil {
		t.Fatal(err)
	}
	late := filepath.Join(dir, "late.log")
	if err := os.WriteFile(late, []byte("поздняя\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.rescan()

	b.mu.Lock()
	_, ok := b.subscribers["fresh"].files[dir+"::late.log"]
	b.mu.Unlock()
	if !ok {
		t.Fatal("rescan не подобрал лог-файл, созданный после подписки")
	}
}

// Конкурентные Subscribe (HTTP-хендлеры) и broadcast/rescan (тикер) не должны
// давать гонку на карте подписчиков. Запускать с -race.
func TestLogBrokerConcurrentSubscribeAndBroadcast(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proj.log")
	if err := os.WriteFile(path, []byte("start\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := newLogBroker(NewHub())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				b.rescan()
				b.broadcast()
			}
		}
	}()

	for i := 0; i < 20; i++ {
		b.Subscribe(fmt.Sprintf("p%d", i), []string{dir}, listLogFiles([]string{dir}))
		appendToFile(t, path, fmt.Sprintf("line %d\n", i))
	}
	cancel()
	<-done

	b.mu.Lock()
	subs := len(b.subscribers)
	b.mu.Unlock()
	if subs != 20 {
		t.Fatalf("подписок = %d, want 20", subs)
	}
}

// listLogFiles отбирает только *.log и не падает на отсутствующем каталоге.
func TestListLogFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.log", "b.LOG", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.log"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, n := range listLogFiles([]string{dir, filepath.Join(dir, "нет-такого")}) {
		got[n] = true
	}
	if !got["a.log"] || !got["b.LOG"] {
		t.Fatalf("не собраны *.log: %v", got)
	}
	if got["c.txt"] || got["sub.log"] {
		t.Fatalf("лишнее в списке: %v", got)
	}
}
