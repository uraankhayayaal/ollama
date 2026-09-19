package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
