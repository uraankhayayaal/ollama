package acceptor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// План проверки обязан совпадать с тем, что реально выполняет приёмка, иначе
// QA запускает тесты не той командой, которой проверяет acceptor, и «зелёные
// тесты» перестают что-либо значить.
func TestVerifyPlanMatchesAcceptCommands(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module app\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := VerifyPlanFor(root, dir, DefaultConfig())
	if p.Kind != string(KindGo) {
		t.Errorf("тип проекта: ожидался %q, получено %q", KindGo, p.Kind)
	}
	if p.Build != "go build ./..." {
		t.Errorf("Build: ожидалось %q, получено %q", "go build ./...", p.Build)
	}
	if p.Test != "go test ./..." || p.Tool != "go test" {
		t.Errorf("Test/Tool: ожидалось %q/%q, получено %q/%q", "go test ./...", "go test", p.Test, p.Tool)
	}
	if p.Analyze != "go vet ./..." {
		t.Errorf("Analyze: ожидалось %q, получено %q", "go vet ./...", p.Analyze)
	}
	if p.Lint == "" {
		t.Error("Lint: ожидалась команда проверки стиля Go")
	}
	if p.Start == "" {
		t.Error("Start: ожидалась команда запуска сервиса")
	}

	// Makefile-цель test приоритетнее автодетекта — и Tool обязан соответствовать
	// именно ей.
	if err := os.WriteFile(filepath.Join(root, "Makefile"),
		[]byte("build:\n\tgo build ./...\ntest:\n\tgo test -race ./...\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p = VerifyPlanFor(root, dir, DefaultConfig())
	if p.MakefileDir != root {
		t.Errorf("MakefileDir: ожидался корень приёмки, получено %q", p.MakefileDir)
	}
	if p.Test != "make test" || p.Tool != "make test" {
		t.Errorf("Test/Tool по Makefile: ожидалось %q/%q, получено %q/%q", "make test", "make test", p.Test, p.Tool)
	}
	if p.Build != "make build" {
		t.Errorf("Build по Makefile: ожидалось %q, получено %q", "make build", p.Build)
	}
	if p.MakeCommand("lint") != "make lint" {
		t.Errorf("MakeCommand(lint) без цели: ожидалось %q, получено %q", "make lint", p.MakeCommand("lint"))
	}
	if got := p.TestTargets(); len(got) == 0 {
		t.Error("TestTargets: ожидался список целей Makefile")
	}

	// env поверх всего.
	t.Setenv("ACCEPT_TEST_CMD", "go test -count=1 ./...")
	p = VerifyPlanFor(root, dir, LoadConfig())
	if p.Test != "go test -count=1 ./..." || p.Tool != "go test -count=1 ./..." {
		t.Errorf("Test/Tool из env: получено %q/%q", p.Test, p.Tool)
	}
}

// Проект без тестов (каталог манифестов) — пустая команда и внятная подсказка,
// а не выдуманный «go test ./...», который упадёт с exit 2.
func TestVerifyPlanWithoutTests(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "infra")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deployment.yaml"),
		[]byte("apiVersion: apps/v1\nkind: Deployment\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := VerifyPlanFor(root, dir, DefaultConfig())
	if p.Test != "" {
		t.Errorf("Test: ожидалась пустая команда, получено %q", p.Test)
	}
	if p.Tool != "" {
		t.Errorf("Tool: ожидалось пустое, получено %q", p.Tool)
	}
	if h := p.TestHint(); h == "" {
		t.Error("TestHint: ожидалась подсказка для агента")
	}
}

// make-цель должна быть самозавершающейся: приёмка и CI не должны висеть на
// сервере, поэтому подсказка это проговаривает.
func TestVerifyPlanTestHint(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "svc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("test:\n\tgo test ./...\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module svc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := VerifyPlanFor(root, dir, DefaultConfig())
	h := p.TestHint()
	if h == "" {
		t.Fatal("TestHint пуст")
	}
	if !strings.Contains(h, "make test") {
		t.Errorf("подсказка не упоминает команду: %q", h)
	}
}

// Приоритет make-целей над автодетектом — тот же, что в acceptOne: если
// проект объявил lint, приёмка проверяет стиль через него, а не через
// gofmt/prettier из нашего автодетекта.
func TestVerifyPlanLintPrefersMakeTarget(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "svc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module svc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("lint:\n\tgolangci-lint run\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := VerifyPlanFor(root, dir, DefaultConfig())
	if p.Lint != "make lint" {
		t.Errorf("Lint: ожидалось %q, получено %q", "make lint", p.Lint)
	}
	// Анализатор при make test отсутствует — приёмка проверяет его через make test.
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("lint:\n\tgolangci-lint run\ntest:\n\tgo test ./...\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p = VerifyPlanFor(root, dir, DefaultConfig())
	if p.Analyze != "make test" {
		t.Errorf("Analyze: ожидалось %q (запасной вариант приёмки), получено %q", "make test", p.Analyze)
	}
}
