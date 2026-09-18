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

	// Лог внутри каталога проекта: <root>/logs/<проект>.log.
	localLogs := filepath.Join(dir, "logs")
	if err := os.MkdirAll(localLogs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localLogs, projName+".log"), []byte("=== Сессия ===\nпервая строка\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Глобальный каталог логов (LOG_DIR) с ещё одним файлом.
	global := t.TempDir()
	t.Setenv("LOG_DIR", global)
	if err := os.WriteFile(filepath.Join(global, "server.log"), []byte("глобальный лог\n"), 0o644); err != nil {
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
	names := map[string]bool{}
	content := map[string]string{}
	for _, f := range out.Files {
		names[f.Name] = true
		content[f.Name] = f.Content
	}
	if !names[projName+".log"] || !names["server.log"] {
		t.Fatalf("ожидались оба лога, files=%v", out.Files)
	}
	if out.Selected != projName+".log" {
		t.Fatalf("selected = %q, want %q", out.Selected, projName+".log")
	}
	if !strings.Contains(content[projName+".log"], "первая строка") {
		t.Fatalf("content проекта не содержит строки: %q", content[projName+".log"])
	}
	if !strings.Contains(content["server.log"], "глобальный лог") {
		t.Fatalf("content глобального лога не содержит строки: %q", content["server.log"])
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
