package server

// Тесты логов рантайма (Ф-2): кольцевой буфер, трансляция в шину, REST-хвост
// и подписка инструмента ReadAppLogs на проект.
//
// Hermetic: miniredis для сессии, реальный буфер в памяти, никаких запусков
// процессов (сам инструмент покрыт в tools/applogs_test.go).

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ai/projects"
	"ai/runevents"
	"ai/tools"
)

// appLogCheck — разбор ответа инструмента ReadAppLogs в тестах сервера.
type appLogCheck struct {
	Status  string   `json:"status"`
	Source  string   `json:"source"`
	Message string   `json:"message"`
	Lines   []string `json:"lines"`
}

// getAppLog читает REST-хвост логов рантайма проекта.
func getAppLog(t *testing.T, handler http.Handler, project string, query string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+project+"/applog"+query, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d, ждём 200: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("разбор ответа: %v (%s)", err, rec.Body.String())
	}
	return out
}

func TestAppLogBufferKeepsTailAndCountsTotal(t *testing.T) {
	b := &appLogBuffer{}
	for i := 0; i < appLogBufferLines+50; i++ {
		b.push("local", "line")
	}
	if got := len(b.tail(0)); got != appLogDefaultTail {
		t.Fatalf("умолчание: %d строк, ждём %d", got, appLogDefaultTail)
	}
	if got := b.tail(10); len(got) != 10 {
		t.Fatalf("хвост: %d строк, ждём 10", len(got))
	}
	// Хвост — это хвост: буфер кольцевой, всего в нём не больше appLogBufferLines.
	if total := b.tail(appLogMaxTail); len(total) != appLogBufferLines {
		t.Fatalf("в буфере %d строк, ждём кольцевые %d", len(total), appLogBufferLines)
	}
	// Несохранённые строки забираются один раз (иначе лог проекта залило бы
	// повторами одной и той же пачки).
	if !b.takeDirty() {
		t.Fatalf("после push должен быть несохранённый хвост")
	}
	if b.takeDirty() {
		t.Fatalf("повторный takeDirty должен вернуть false")
	}
}

func TestAppLogBufferTailLimitsArgs(t *testing.T) {
	b := &appLogBuffer{}
	for i := 0; i < appLogMaxTail+100; i++ {
		b.push("local", "x")
	}
	if got := len(b.tail(-5)); got != appLogDefaultTail {
		t.Fatalf("отрицательный lines: %d, ждём дефолт %d", got, appLogDefaultTail)
	}
	if got := len(b.tail(appLogMaxTail * 10)); got != appLogMaxTail {
		t.Fatalf("lines выше потолка не ограничен: %d, ждём %d", got, appLogMaxTail)
	}
}

// TestSessionAppLogSinkWiredForProject — подписка инструмента должна доходить
// до сессии по каталогу проекта: без неё ReadAppLogs отработал бы вхолостую.
func TestSessionAppLogSinkWiredForProject(t *testing.T) {
	srv, _, _ := newTestServer(t)
	sess, _, err := srv.getOrCreate("sink-proj")
	if err != nil {
		t.Fatal(err)
	}
	sess.appendAppLog("local", "строка из подписки")
	lines := sess.appLog.tail(10)
	if len(lines) != 1 || lines[0].Line != "строка из подписки" {
		t.Fatalf("буфер сессии: %+v", lines)
	}
	if lines[0].Source != "local" {
		t.Fatalf("источник потерян: %+v", lines[0])
	}
	// Пустые строки в лог рантайма не идут: это шум приложения, а не сигнал.
	sess.appendAppLog("local", "   ")
	if got := len(sess.appLog.tail(10)); got != 1 {
		t.Fatalf("пустая строка попала в буфер: %d", got)
	}
}

// TestRunEventAppLogReachesBufferAndBus — путь через шину агентского цикла:
// событие TypeAppLog должно попасть и в буфер, и в WS-шину (оттуда его берёт
// «рантайм»-вкладка UI).
func TestRunEventAppLogReachesBufferAndBus(t *testing.T) {
	srv, _, _ := newTestServer(t)
	sess, _, err := srv.getOrCreate("bus-proj")
	if err != nil {
		t.Fatal(err)
	}
	// Подписка WS-клиента: проверяем настоящую трансляцию, а не вызов publish.
	netSrv, cli := net.Pipe()
	defer cli.Close()
	ws := &WsConn{conn: netSrv, br: bufio.NewReader(netSrv), closed: make(chan struct{})}
	srv.hub.Subscribe("bus-proj", ws)
	t.Cleanup(func() { _ = ws.Close() })

	sess.routeRunEvent(runevents.Event{
		Type: runevents.TypeAppLog, Source: "docker", Content: "panic: бум", Agent: "developer",
	})
	lines := sess.appLog.tail(10)
	if len(lines) != 1 || lines[0].Line != "panic: бум" || lines[0].Source != "docker" {
		t.Fatalf("буфер: %+v", lines)
	}

	if err := cli.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	body := readWsFrame(t, cli)
	var ev struct {
		Type    string          `json:"type"`
		Payload runevents.Event `json:"payload"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		t.Fatalf("json: %v (%s)", err, body)
	}
	if ev.Type != "applog" {
		t.Fatalf("тип трансляции: %q, ждём applog", ev.Type)
	}
	if ev.Payload.Content != "panic: бум" || ev.Payload.Source != "docker" {
		t.Fatalf("полезная нагрузка: %+v", ev.Payload)
	}
	// Логи рантайма — поток: каждая строка не должна дёргать общую трансляцию
	// чата (иначе болтливое приложение зальёт событиями всю сессию).
	hist, err := sess.chat.History(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 0 {
		t.Fatalf("лог рантайма попал в чат: %d сообщений", len(hist))
	}
}

// TestRESTAppLogEmptyIsSkipped — до первого запуска рантайма хвост пуст. Это
// не 404: сессия жива, просто логов ещё нет, и модель/UI должны понимать
// разницу.
func TestRESTAppLogEmptyIsSkipped(t *testing.T) {
	srv, handler, _ := newTestServer(t)
	if _, _, err := srv.getOrCreate("empty-proj"); err != nil {
		t.Fatal(err)
	}
	out := getAppLog(t, handler, "empty-proj", "")
	if out["status"] != "skipped" {
		t.Fatalf("status: %v, ждём skipped", out["status"])
	}
	if hint, _ := out["hint"].(string); !strings.Contains(hint, "ReadAppLogs") {
		t.Fatalf("подсказка должна называть инструмент: %q", hint)
	}
}

// TestRESTAppLogUnknownSessionIsSkipped — сессии нет вовсе: тоже skipped, а не
// ошибка 404, чтобы UI не показывал «сломанную» панель.
func TestRESTAppLogUnknownSessionIsSkipped(t *testing.T) {
	_, handler, _ := newTestServer(t)
	out := getAppLog(t, handler, "never-seen-proj", "")
	if out["status"] != "skipped" {
		t.Fatalf("status: %v, ждём skipped", out["status"])
	}
	if lines, ok := out["lines"].([]any); !ok || len(lines) != 0 {
		t.Fatalf("lines: %v, ждём пустой массив", out["lines"])
	}
}

// TestRESTAppLogReturnsTailWithCount — основной сценарий: строки доехали до
// REST вместе с источником и временем.
func TestRESTAppLogReturnsTailWithCount(t *testing.T) {
	srv, handler, _ := newTestServer(t)
	sess, _, err := srv.getOrCreate("tail-proj")
	if err != nil {
		t.Fatal(err)
	}
	sess.appendAppLog("local", "первая")
	sess.appendAppLog("docker", "вторая")

	out := getAppLog(t, handler, "tail-proj", "?lines=1")
	if out["status"] != "ok" {
		t.Fatalf("status: %v, ждём ok", out["status"])
	}
	if cnt, _ := out["count"].(float64); cnt != 1 {
		t.Fatalf("count: %v, ждём 1", out["count"])
	}
	lines, _ := out["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("строк: %d, ждём 1", len(lines))
	}
	last, _ := lines[0].(map[string]any)
	if last["line"] != "вторая" || last["source"] != "docker" {
		t.Fatalf("последняя строка: %+v", last)
	}
	if last["time"] == nil {
		t.Fatalf("без времени строку рантайма не отсортировать: %+v", last)
	}
}

// TestAppLogSinkReplacedAndCancelled — подписка принадлежит конкретной
// сессии: переподписка (перезапуск проекта) и снятие старой не должны гасить
// «рантайм» новой сессии.
func TestAppLogSinkReplacedAndCancelled(t *testing.T) {
	dir := t.TempDir()
	var first, second int
	cancelFirst := tools.SetAppLogSink(dir, func(string, string) { first++ })
	tools.SetAppLogSink(dir, func(string, string) { second++ })
	cancelFirst()

	ops := &tools.FileOps{OutputDir: dir}
	raw, err := ops.ReadAppLogs(map[string]any{"command": "echo живая-подписка", "wait_ms": 400})
	if err != nil {
		t.Fatal(err)
	}
	var res appLogCheck
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("разбор ответа инструмента: %v (%s)", err, raw)
	}
	if res.Status != "ok" {
		t.Fatalf("status: %q", res.Status)
	}
	if second == 0 {
		t.Fatalf("подписка новой сессии не получила строки (first=%d, second=%d)", first, second)
	}
	if first != 0 {
		t.Fatalf("снятая подписка не должна получать строки: %d", first)
	}

	// Снятие собственной подписки действительно её убирает.
	cancel := tools.SetAppLogSink(dir, func(string, string) { second++ })
	cancel()
	_, _ = ops.ReadAppLogs(map[string]any{"command": "echo после-снятия", "wait_ms": 400})
	before := second
	_, _ = ops.ReadAppLogs(map[string]any{"command": "echo после-снятия-2", "wait_ms": 400})
	if second != before {
		t.Fatalf("подписка не снята: счётчик вырос %d → %d", before, second)
	}
}

// TestAppLogSinkIgnoresEmptyDir — вызов без каталога не должен паниковать и не
// должен вставать в реестр под пустым ключом (иначе все безымянные вызовы
// делили бы одну подписку).
func TestAppLogSinkIgnoresEmptyDir(t *testing.T) {
	cancel := tools.SetAppLogSink("", func(string, string) {})
	defer cancel()
	ops := &tools.FileOps{OutputDir: t.TempDir()}
	if raw, err := ops.ReadAppLogs(map[string]any{"command": "echo ok", "wait_ms": 400}); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(string(raw), `"status": "ok"`) {
		t.Fatalf("ответ инструмента: %s", raw)
	}
}

// TestSessionAppLogMatchesProjectDir — привязка подписки к каталогу проекта
// должна идти через projects.ProjectDir, иначе инструмент (работающий в этом
// каталоге) не найдёт подписку сессии.
func TestSessionAppLogMatchesProjectDir(t *testing.T) {
	srv, _, _ := newTestServer(t)
	const name = "dir-proj"
	if _, _, err := srv.getOrCreate(name); err != nil {
		t.Fatal(err)
	}
	dir := projects.ProjectDir(name)
	// Каталог проекта создаёт сам оркестратор, а не newSession: без него
	// запуск процесса падает на несуществующем c.Dir.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ops := &tools.FileOps{OutputDir: dir}
	raw, err := ops.ReadAppLogs(map[string]any{"command": "echo из-проектного-каталога", "wait_ms": 400})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"status": "ok"`) {
		t.Fatalf("ответ инструмента: %s", raw)
	}
	sess := srv.session(name)
	if sess == nil {
		t.Fatalf("сессия %s не найдена", name)
	}
	found := false
	for _, l := range sess.appLog.tail(50) {
		if l.Line == "из-проектного-каталога" {
			found = true
		}
	}
	if !found {
		t.Fatalf("строка из каталога проекта не дошла до сессии: %+v", sess.appLog.tail(5))
	}
}
