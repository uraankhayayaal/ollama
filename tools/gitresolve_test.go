package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGitExec — hermetic исполнитель git для инструмента резолва: отвечает по
// точному совпадению call-строки ("dir | git argv..."), как gitops.fakeExecutor.
type fakeGitExec struct {
	resp  map[string]string
	calls []string
}

func (f *fakeGitExec) Exec(_ context.Context, dir string, argv ...string) (string, error) {
	call := dir + " | git " + strings.Join(argv[1:], " ")
	f.calls = append(f.calls, call)
	return f.resp[call], nil
}

// newResolveTool собирает инструмент с заданным OutputDir и фейковым git.
func newResolveTool(dir string, ex *fakeGitExec) *resolveGitConflictsTool {
	if ex == nil {
		ex = &fakeGitExec{}
	}
	return &resolveGitConflictsTool{ops: &FileOps{OutputDir: dir}, ex: ex}
}

func TestResolveGitConflictsNoOutputDir(t *testing.T) {
	tool := &resolveGitConflictsTool{}
	data, err := tool.Execute(map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out map[string]any
	if json.Unmarshal(data, &out) != nil || out["status"] != "error" {
		t.Fatalf("ответ без OutputDir = %s", data)
	}
}

func TestResolveGitConflictsActionConflicts(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("triv.txt", "x\n<<<<<<< HEAD\ns\n=======\ns\n>>>>>>> b\ny\n")
	write("hard.txt", "a\n<<<<<<< HEAD\nA\n=======\nB\n>>>>>>> b\n")

	ex := &fakeGitExec{resp: map[string]string{
		dir + " | git ls-files -u": "100644 1 1\ttriv.txt\n100644 2 2\ttriv.txt\n100644 3 3\ttriv.txt\n" +
			"100644 1 1\thard.txt\n100644 2 2\thard.txt\n100644 3 3\thard.txt\n",
		dir + " | git diff -- hard.txt": "diff --git a/hard.txt b/hard.txt\n@@ -1 +1,5 @@\na\n+<<<<<<< HEAD\n+A\n+=======\n+B\n+>>>>>>> b\n",
	}}
	tool := newResolveTool(dir, ex)
	data, err := tool.Execute(map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Status   string   `json:"status"`
		Files    []string `json:"files"`
		Resolved []string `json:"resolved"`
		Diff     string   `json:"diff"`
	}
	if json.Unmarshal(data, &out) != nil {
		t.Fatalf("JSON = %s", data)
	}
	if out.Status != "conflicts" {
		t.Fatalf("status = %q", out.Status)
	}
	if strings.Join(out.Files, ",") != "hard.txt" || strings.Join(out.Resolved, ",") != "triv.txt" {
		t.Fatalf("files/resolved = %v/%v", out.Files, out.Resolved)
	}
	if !strings.Contains(out.Diff, "hard.txt") {
		t.Fatalf("diff не содержит путь: %s", out.Diff)
	}
	// Тривиальный файл авто-резолвлен на диске и застейджен.
	data, _ = os.ReadFile(filepath.Join(dir, "triv.txt"))
	if string(data) != "x\ns\ny\n" {
		t.Fatalf("triv.txt на диске = %q", data)
	}
	if got := strings.Join(ex.calls, "|"); !strings.Contains(got, "git add -- triv.txt") {
		t.Fatalf("нет git add тривиального: %v", ex.calls)
	}
	// Сложный файл не тронут (маркеры на месте).
	data, _ = os.ReadFile(filepath.Join(dir, "hard.txt"))
	if !strings.Contains(string(data), "<<<<<<< HEAD") {
		t.Fatalf("hard.txt модифицирован: %q", data)
	}
}

func TestResolveGitConflictsActionResolveStillConflicts(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "triv.txt"), []byte("x\n<<<<<<< HEAD\ns\n=======\ns\n>>>>>>> b\ny\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "hard.txt"), []byte("a\n<<<<<<< HEAD\nA\n=======\nB\n>>>>>>> b\n"), 0o644)

	ex := &fakeGitExec{resp: map[string]string{
		dir + " | git ls-files -u": "100644 2 2\thard.txt\n100644 3 3\thard.txt\n",
		dir + " | git diff -- hard.txt": "diff --git a/hard.txt b/hard.txt\n",
	}}
	tool := newResolveTool(dir, ex)
	data, err := tool.Execute(map[string]any{
		"action": "resolve",
		"files":  []any{"triv.txt", "hard.txt"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Status string   `json:"status"`
		Files  []string `json:"files"`
	}
	if json.Unmarshal(data, &out) != nil {
		t.Fatalf("JSON = %s", data)
	}
	if out.Status != "still_conflicts" || strings.Join(out.Files, ",") != "hard.txt" {
		t.Fatalf("status/files = %s/%v", out.Status, out.Files)
	}
	if got := strings.Join(ex.calls, "|"); !strings.Contains(got, "git add -- triv.txt") {
		t.Fatalf("тривиальный не застейджен: %v", ex.calls)
	}
}

func TestResolveGitConflictsActionResolveSuccess(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "clean.go"), []byte("package main\n"), 0o644)

	ex := &fakeGitExec{resp: map[string]string{
		dir + " | git ls-files -u": "",
	}}
	tool := newResolveTool(dir, ex)
	data, err := tool.Execute(map[string]any{
		"action": "resolve",
		"files":  []any{"clean.go"},
		"message": "резолв main",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &out) != nil {
		t.Fatalf("JSON = %s", data)
	}
	if out.Status != "resolved" {
		t.Fatalf("status = %q, полный ответ: %s", out.Status, data)
	}
	if got := strings.Join(ex.calls, "|"); !strings.Contains(got, "git add -A") ||
		!strings.Contains(got, "git commit -m резолв main") {
		t.Fatalf("нет add -A/commit: %v", ex.calls)
	}
}

func TestResolveGitConflictsRejectsEscapingPaths(t *testing.T) {
	dir := t.TempDir()
	ex := &fakeGitExec{resp: map[string]string{
		dir + " | git ls-files -u": "100644 2 2\t../escaped.txt\n100644 3 3\t../escaped.txt\n",
	}}
	tool := newResolveTool(dir, ex)
	data, err := tool.Execute(map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Status string   `json:"status"`
		Files  []string `json:"files"`
	}
	if json.Unmarshal(data, &out) != nil || out.Status != "conflicts" {
		t.Fatalf("JSON = %s", data)
	}
	// Путь за пределами OutputDir: не должен попадать в files и не пишемся на диск.
	if strings.Join(out.Files, ",") != "../escaped.txt" {
		t.Fatalf("files = %v", out.Files)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escaped.txt")); err == nil {
		t.Fatal("не должны были создать файл вне OutputDir")
	}
}