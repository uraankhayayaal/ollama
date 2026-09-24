package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// Ф-2 PLAN-2026-09-24-wip-architect-intelligence.md: побочная эвристика DetectStack.
// Синтетические проекты: консоль без frontend, монорепо с server+frontend,
// php/laravel-проект.

func TestDetectStack_GoConsoleNoFrontend(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"go.mod", "main.go"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := detectStackAt(dir)
	if got.Status != "ok" {
		t.Fatalf("status = %q", got.Status)
	}
	if got.Kind != "go" {
		t.Fatalf("kind = %q, want go", got.Kind)
	}
	if got.Roles.Frontend {
		t.Fatal("console-проект: frontend не должен быть активен")
	}
	if !got.Roles.Backend {
		t.Fatal("console-проект на go: backend должен быть активен")
	}
	if got.Roles.DevOps {
		t.Fatal("console-проект без infra: devops не должен быть активен")
	}
	if !containsMarker(got.Markers, "go.mod") {
		t.Fatalf("markers не содержит go.mod: %v", got.Markers)
	}
}

func TestDetectStack_MonorepoServerFrontend(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"server", "frontend", "tests"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := detectStackAt(dir)
	if got.Kind != "node" {
		t.Fatalf("kind = %q, want node", got.Kind)
	}
	if !got.Roles.Frontend {
		t.Fatal("monorepo: frontend должен быть активен (frontend/)")
	}
	if !got.Roles.Backend {
		t.Fatal("monorepo: backend должен быть активен (server/)")
	}
	if !got.Roles.QA {
		t.Fatal("monorepo: qa должен быть активен (tests/)")
	}
}

func TestDetectStack_PhpLaravel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "resources/views"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := detectStackAt(dir)
	if got.Kind != "php" {
		t.Fatalf("kind = %q, want php", got.Kind)
	}
	if !got.Roles.Backend {
		t.Fatal("php/laravel: backend должен быть активен")
	}
}

// Ф-1 PLAN-2026-09-24-todo-makefile.md: DetectStack детерминированно сообщает
// о наличии корневого Makefile (поле makefile + маркер "Makefile"). Без файла —
// false и без маркера.
func TestDetectStack_MakefilePresence(t *testing.T) {
	with := t.TempDir()
	if err := os.WriteFile(filepath.Join(with, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(with, "Makefile"), []byte("build:\n\t@echo ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := detectStackAt(with)
	if !got.Makefile {
		t.Fatal("Makefile присутствует — makefile должен быть true")
	}
	if !containsMarker(got.Markers, "Makefile") {
		t.Fatalf("markers не содержит Makefile: %v", got.Markers)
	}

	without := t.TempDir()
	if err := os.WriteFile(filepath.Join(without, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = detectStackAt(without)
	if got.Makefile {
		t.Fatal("Makefile отсутствует — makefile должен быть false")
	}
	if containsMarker(got.Markers, "Makefile") {
		t.Fatalf("markers не должен содержать Makefile: %v", got.Markers)
	}
}

func TestDetectStack_ToolThroughRegistry(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ops := &FileOps{OutputDir: dir}
	s := Select([]string{DetectStack}, Deps{FileOps: ops})
	out, err := s.Execute(DetectStack, map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := string(out)
	if got == "" {
		t.Fatal("пустой результат DetectStack")
	}
}
