package server

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGetLogsReturnsProjectLogs(t *testing.T) {
	srv, handler, _ := newTestServer(t)
	projName := "proj-log"
	dir := registerTestDir(t, srv, projName)

	// Лог самого приложения внутри каталога проекта: <root>/logs/app.log.
	localLogs := filepath.Join(dir, "logs")
	if err := os.MkdirAll(localLogs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localLogs, "app.log"), []byte("лог приложения\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Общий каталог сервера: файл ЭТОГО проекта, файл ЧУЖОГО проекта и
	// служебный server.log.
	global := t.TempDir()
	t.Setenv("LOG_DIR", global)
	write := map[string]string{
		projName + ".log": "=== Сессия ===\nпервая строка\n",
		"other-proj.log":  "чужой проект\n",
		"server.log":      "служебный лог процесса\n",
	}
	for name, body := range write {
		if err := os.WriteFile(filepath.Join(global, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/projects/"+projName+"/logs", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out logsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	content := map[string]string{}
	for _, f := range out.Files {
		names[f.Name] = true
		content[f.Name] = f.Content
	}

	// Свой лог + логи приложения из каталога проекта.
	if !names[projName+".log"] {
		t.Fatalf("нет лога проекта, files=%v", out.Files)
	}
	if !names["app.log"] {
		t.Fatalf("нет лога приложения из <root>/logs, files=%v", out.Files)
	}
	// Чужие проекты и служебный лог процесса в панель не попадают.
	if names["other-proj.log"] {
		t.Fatalf("отдан лог чужого проекта: %v", out.Files)
	}
	if names["server.log"] {
		t.Fatalf("отдан служебный server.log: %v", out.Files)
	}
	if out.Selected != projName+".log" {
		t.Fatalf("selected = %q, want %q", out.Selected, projName+".log")
	}
	if !strings.Contains(content[projName+".log"], "первая строка") {
		t.Fatalf("content проекта не содержит строки: %q", content[projName+".log"])
	}
	if !strings.Contains(content["app.log"], "лог приложения") {
		t.Fatalf("content лога приложения неверный: %q", content["app.log"])
	}
}

// Файл проекта из общего каталога приоритетнее одноимённого в <root>/logs:
// logs/<проект>.log — канонический лог оркестрации.
func TestGetLogsPrefersGlobalProjectFile(t *testing.T) {
	srv, handler, _ := newTestServer(t)
	projName := "proj-prio"
	dir := registerTestDir(t, srv, projName)

	localLogs := filepath.Join(dir, "logs")
	if err := os.MkdirAll(localLogs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localLogs, projName+".log"), []byte("локальная копия\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	global := t.TempDir()
	t.Setenv("LOG_DIR", global)
	if err := os.WriteFile(filepath.Join(global, projName+".log"), []byte("общий каталог\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/projects/"+projName+"/logs", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out logsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Files) != 1 {
		t.Fatalf("ожидался один файл (дубли не нужны), files=%v", out.Files)
	}
	if !strings.Contains(out.Files[0].Content, "общий каталог") {
		t.Fatalf("взят не файл из общего каталога: %q", out.Files[0].Content)
	}
}

func TestGetLogsUnknownProject404(t *testing.T) {
	_, handler, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/projects/no-such-project/logs", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, want 404", rec.Code)
	}
}

func TestGetLogsEmptyDir(t *testing.T) {
	srv, handler, _ := newTestServer(t)
	registerTestDir(t, srv, "proj-no-logs")
	t.Setenv("LOG_DIR", t.TempDir())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/projects/proj-no-logs/logs", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out logsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Files) != 0 {
		t.Fatalf("файлов быть не должно: %v", out.Files)
	}
}

// Очистка обрезает ровно те файлы, которые отдаёт GET: свой лог + логи
// приложения из <root>/logs. Чужие логи и служебный server.log в общем
// каталоге не трогаем — иначе кнопка в панели стирала бы логи других проектов.
func TestDeleteLogsTruncatesProjectLogsOnly(t *testing.T) {
	srv, handler, _ := newTestServer(t)
	projName := "proj-clear"
	dir := registerTestDir(t, srv, projName)

	localLogs := filepath.Join(dir, "logs")
	if err := os.MkdirAll(localLogs, 0o755); err != nil {
		t.Fatal(err)
	}
	global := t.TempDir()
	t.Setenv("LOG_DIR", global)

	files := map[string]string{
		filepath.Join(global, projName+".log"):  "=== Сессия ===\nпервая строка\n",
		filepath.Join(global, "other-proj.log"): "чужой проект\n",
		filepath.Join(global, "server.log"):     "служебный лог процесса\n",
		filepath.Join(localLogs, "app.log"):     "лог приложения\n",
		filepath.Join(localLogs, "worker.log"):  "лог воркера\n",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/projects/"+projName+"/logs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		OK      bool     `json:"ok"`
		Cleared []string `json:"cleared"`
		Count   int      `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Count != 3 {
		t.Fatalf("ответ = %+v, ожидались ok и 3 файла", out)
	}
	cleared := map[string]bool{}
	for _, n := range out.Cleared {
		cleared[n] = true
	}
	for _, want := range []string{projName + ".log", "app.log", "worker.log"} {
		if !cleared[want] {
			t.Fatalf("не очищен %s: %v", want, out.Cleared)
		}
	}
	for _, not := range []string{"other-proj.log", "server.log"} {
		if cleared[not] {
			t.Fatalf("очищен лишний файл %s: %v", not, out.Cleared)
		}
	}

	// Свои логи обрезаны, но не удалены: файл остаётся на месте (иначе панель
	// моргнула бы «файлов нет»), а содержимое — только заголовок очистки.
	for _, path := range []string{filepath.Join(global, projName+".log"), filepath.Join(localLogs, "app.log"), filepath.Join(localLogs, "worker.log")} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("файл %s пропал после очистки: %v", path, err)
		}
		if strings.TrimSpace(string(body)) == "" {
			t.Fatalf("%s очищен в ноль — заголовок очистки должен остаться", path)
		}
		if strings.Contains(string(body), "строка") || strings.Contains(string(body), "лог приложения") {
			t.Fatalf("%s сохранил прежнее содержимое: %q", path, body)
		}
	}
	// Чужие логи на месте.
	if body, _ := os.ReadFile(filepath.Join(global, "other-proj.log")); string(body) != "чужой проект\n" {
		t.Fatalf("лог чужого проекта изменён: %q", body)
	}
	if body, _ := os.ReadFile(filepath.Join(global, "server.log")); string(body) != "служебный лог процесса\n" {
		t.Fatalf("служебный server.log изменён: %q", body)
	}

	// GET после очистки отдаёт пустой хвост — старых строк в панели не будет.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/projects/"+projName+"/logs", nil))
	var view logsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Files) != 3 {
		t.Fatalf("после очистки ожидались те же 3 файла, files=%v", view.Files)
	}
	for _, f := range view.Files {
		if strings.Contains(f.Content, "первая строка") {
			t.Fatalf("снапшот всё ещё содержит старое содержимое: %q", f.Content)
		}
	}
}

func TestDeleteLogsUnknownProject404(t *testing.T) {
	_, handler, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/projects/no-such-project/logs", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, want 404", rec.Code)
	}
}

// Проект без лог-файлов: очистка не ошибка, а пустой результат — кнопка в UI
// просто остаётся неактивной, но и ручной вызов не должен падать.
func TestDeleteLogsWithoutFiles(t *testing.T) {
	srv, handler, _ := newTestServer(t)
	registerTestDir(t, srv, "proj-nofiles")
	t.Setenv("LOG_DIR", t.TempDir())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/projects/proj-nofiles/logs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		OK    bool `json:"ok"`
		Count int  `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Count != 0 {
		t.Fatalf("ответ = %+v, ожидались ok и 0 файлов", out)
	}
}

// Сквозной путь очистки: панель подписана на проект (GET → Subscribe),
// сервер обрезает лог (DELETE), агент дописывает — брокер обязан отдать новые
// строки в WS, а не молчать (осталась бы старая позиция чтения) и не
// переиграть прежнее содержимое.
func TestDeleteLogsStreamContinuesAfterClear(t *testing.T) {
	srv, handler, _ := newTestServer(t)
	projName := "proj-clear-ws"
	registerTestDir(t, srv, projName)
	global := t.TempDir()
	t.Setenv("LOG_DIR", global)
	path := filepath.Join(global, projName+".log")
	if err := os.WriteFile(path, []byte("=== Сессия ===\nпервая строка\nвторая строка\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Подписка на WS-хаб СЕРВЕРА: брокер публикует именно в него.
	wsSrv, wsCli := net.Pipe()
	defer wsCli.Close()
	ws := &WsConn{conn: wsSrv, br: bufio.NewReader(wsSrv), closed: make(chan struct{})}
	srv.hub.Subscribe(projName, ws)
	t.Cleanup(func() { _ = ws.Close() })

	// Подписка брокера — как её делает handleGetLogs.
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest("GET", "/api/projects/"+projName+"/logs", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET code=%d body=%s", get.Code, get.Body.String())
	}

	del := httptest.NewRecorder()
	handler.ServeHTTP(del, httptest.NewRequest("DELETE", "/api/projects/"+projName+"/logs", nil))
	if del.Code != http.StatusOK {
		t.Fatalf("DELETE code=%d body=%s", del.Code, del.Body.String())
	}

	// Агент пишет после очистки.
	appendToFile(t, path, "строка после очистки\n")
	srv.logBroker.broadcast()

	if err := wsCli.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for {
		var ev struct {
			Type    string     `json:"type"`
			Payload logMessage `json:"payload"`
		}
		body := readWsFrame(t, wsCli)
		if err := json.Unmarshal(body, &ev); err != nil {
			t.Fatalf("json: %v (%s)", err, body)
		}
		if ev.Type != "log" || ev.Payload.Project != projName {
			continue
		}
		lines = append(lines, ev.Payload.Line)
		if len(lines) == 2 {
			break
		}
	}
	// Заголовок очистки приходит первым (брокер перечитал файл с начала), и
	// только за ним — новая запись. Прежних строк в потоке быть не должно.
	if !strings.HasPrefix(lines[0], "=== Логи очищены ") {
		t.Fatalf("первой строкой пришло %q, ожидался заголовок очистки", lines[0])
	}
	if lines[1] != "строка после очистки" {
		t.Fatalf("вторая строка = %q", lines[1])
	}
	for _, l := range lines {
		if strings.Contains(l, "первая строка") || strings.Contains(l, "вторая строка") {
			t.Fatalf("прежнее содержимое попало в стрим после очистки: %q", l)
		}
	}
}
