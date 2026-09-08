package developer

import (
	"ai/board"
	"ai/projects"
	"ai/tools"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

func TestRequiredToolFirstRoundDisabled(t *testing.T) {
	for name, mk := range map[string]func(string, string) *base{
		"backend":  func(p, d string) *base { return newBackendDeveloperInDir(p, d).base },
		"frontend": func(p, d string) *base { return newFrontendDeveloperInDir(p, d).base },
	} {
		d := mk("тестовое задание", t.TempDir())
		toolName, ok := d.RequiredToolFirstRound()
		if ok {
			t.Errorf("%s: не должен требовать инструмент в первом раунде, got %q", name, toolName)
		}
	}
}

// Специализация попадает в системный промпт: фронтендер работает только с
// клиентской частью, бэкендер — только с серверной.
func TestRoleInSystemPrompt(t *testing.T) {
	backend := newBackendDeveloperInDir("задание", t.TempDir())
	text := backend.GetSystemMessages(nil)[0].Message
	for _, want := range []string{"БЭКЕНД-РАЗРАБОТЧИК", "серверн", "изучи текущее состояние", "WriteFiles", "OutputDir"} {
		if !strings.Contains(text, want) {
			t.Errorf("бэкенд-промпт не содержит %q", want)
		}
	}
	if strings.Contains(text, "ФРОНТЕНД-РАЗРАБОТЧИК") {
		t.Errorf("бэкенд-промпт не должен содержать роль фронтендера")
	}

	frontend := newFrontendDeveloperInDir("задание", t.TempDir())
	text = frontend.GetSystemMessages(nil)[0].Message
	if !strings.Contains(text, "ФРОНТЕНД-РАЗРАБОТЧИК") {
		t.Errorf("фронтенд-промпт не содержит роль фронтендера")
	}
	if strings.Contains(text, "БЭКЕНД-РАЗРАБОТЧИК") {
		t.Errorf("фронтенд-промпт не должен содержать роль бэкендера")
	}
}

func TestNewDeveloperUsesProjectDir(t *testing.T) {
	cases := []struct {
		name string
		mk   func(string, string) *base
	}{
		{"backend", func(p, dir string) *base { return NewBackendDeveloper("dev-dir-test", p).base }},
		{"frontend", func(p, dir string) *base { return NewFrontendDeveloper("dev-dir-test", p).base }},
	}
	for _, tc := range cases {
		dir := projects.ProjectDir("dev-dir-test")
		os.RemoveAll(dir)
		defer os.RemoveAll(dir)

		d := tc.mk("задание", "")
		if d.OutputDir != dir {
			t.Errorf("%s: OutputDir = %q, ожидали %q", tc.name, d.OutputDir, dir)
		}
		if !dirExists(dir) {
			t.Errorf("%s: директория проекта не создана: %s", tc.name, dir)
		}
	}
}

func TestSetBoardStoreAddsBoardTools(t *testing.T) {
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "testproj"})

	d := newBackendDeveloperInDir("задание", t.TempDir())
	d.SetBoardStore(store)
	if d.Store != store {
		t.Fatal("Store не подключен")
	}
	if _, ok := d.Tools.Get(tools.BoardSetTaskStatus); !ok {
		t.Error("BoardSetTaskStatus должен появиться после подключения доски")
	}
	if _, ok := d.Tools.Get(tools.BoardGetTask); !ok {
		t.Error("BoardGetTask должен появиться после подключения доски")
	}

	before := len(d.GetTools())
	d.SetBoardStore(nil)
	if d.Store != store {
		t.Fatal("SetBoardStore(nil) не должен менять подключённую доску")
	}
	if len(d.GetTools()) != before {
		t.Error("SetBoardStore(nil) не должен менять набор инструментов")
	}
}

func TestFinalizeWritesSummaryAndReadme(t *testing.T) {
	dir := t.TempDir()
	d := newBackendDeveloperInDir("задание: создать сервер", dir)

	files := map[string]string{
		"server/main.go":     "package main\n\nfunc main() {}\n",
		"server/go.mod":      "module example.com/gen\n\ngo 1.26\n",
		"server/readme.md":   "# Сервер\n",
		"server/app_test.go": "package main\n",
	}
	for name, content := range files {
		if _, err := d.WriteFiles(map[string]any{"files": []map[string]any{{"filename": name, "content": content}}}); err != nil {
			t.Fatalf("WriteFiles %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/gen\n\ngo 1.26\n"), 0644); err != nil {
		t.Fatal(err)
	}

	d.Finalize()

	sumPath := filepath.Join(dir, "SUMMARY.md")
	sum, err := os.ReadFile(sumPath)
	if err != nil {
		t.Fatalf("SUMMARY.md не создан: %v", err)
	}
	if !strings.Contains(string(sum), "server/main.go") {
		t.Errorf("SUMMARY.md не содержит файлы проекта: %s", sum)
	}

	readmePath := filepath.Join(dir, "README.md")
	readme, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("README.md не создан: %v", err)
	}
	for _, want := range []string{"go.mod", "go run server/main.go", "server/app_test.go"} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("README.md не содержит %q", want)
		}
	}
}

func TestEnsureREADMEKeepsExisting(t *testing.T) {
	dir := t.TempDir()
	d := newFrontendDeveloperInDir("задание", dir)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("Авторский README"), 0644); err != nil {
		t.Fatal(err)
	}
	d.EnsureREADME()
	data, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "Авторский README" {
		t.Errorf("README перезаписан, а не должен: %q", data)
	}
}

func TestSetScopeLimitsWrites(t *testing.T) {
	dir := t.TempDir()
	d := newBackendDeveloperInDir("задание", dir)
	d.SetScope([]string{"server"})

	out, err := d.WriteFiles(map[string]any{"files": []map[string]any{
		{"filename": "server/api.go", "content": "package main"},
	}})
	if err != nil {
		t.Fatalf("запись в области: %v", err)
	}
	if !bytesContains(out, "server/api.go") {
		t.Errorf("ожидали успех записи, got %s", out)
	}

	out, err = d.WriteFiles(map[string]any{"files": []map[string]any{
		{"filename": "frontend/app.js", "content": "console.log(1)"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytesContains(out, "вне области работы") {
		t.Errorf("запись вне области должна вернуть ошибку в ответе, got: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "frontend", "app.js")); err == nil {
		t.Error("файл вне области не должен создаваться")
	}
}

func TestGetToolsAndCall(t *testing.T) {
	d := newBackendDeveloperInDir("задание", t.TempDir())

	defs := d.GetTools()
	names := map[string]bool{}
	for _, def := range defs {
		names[def.Name] = true
	}
	for _, want := range []string{"WriteFiles", "ReadFiles", "Run", "AppendFile"} {
		if !names[want] {
			t.Errorf("инструмент %q отсутствует в GetTools", want)
		}
	}
	if names[tools.BoardSetTaskStatus] {
		t.Errorf("BoardSetTaskStatus не должен быть без доски")
	}

	got, err := d.CallFunction("List", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Error("List вернул пустой ответ")
	}
}

// Утилиты теста.

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func bytesContains(b []byte, s string) bool {
	return strings.Contains(string(b), s)
}
