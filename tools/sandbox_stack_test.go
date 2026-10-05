package tools

// Тесты выбора образа песочницы по стеку проекта. Живой повод — mytrip/FEL-05:
// монорепозиторий (Go-бэкенд в корне + Node-фронтенд в frontend/) попал в
// образ golang без npm, агент скачал Node в корень, и в коммит задачи уехали
// node.tar.gz (46 МБ) и 4287 файлов тулчейна.

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFileIn(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDetectSandboxStackMonorepoUsesDevImage — Go в корне и Node во
// frontend/ = два стека → dev-образ с go+node+python, а не голый golang.
func TestDetectSandboxStackMonorepoUsesDevImage(t *testing.T) {
	dir := t.TempDir()
	writeFileIn(t, filepath.Join(dir, "go.mod"))
	writeFileIn(t, filepath.Join(dir, "frontend", "package.json"))
	if got := detectSandboxStack(dir); got != "" {
		t.Fatalf("монорепозиторий (go + node) должен давать dev-образ (пустой стек), получено %q", got)
	}
}

// TestDetectSandboxStackGoOnly — одиночный Go-проект по-прежнему идёт в
// golang-образ: мультистек не должен ломать обычный случай.
func TestDetectSandboxStackGoOnly(t *testing.T) {
	dir := t.TempDir()
	writeFileIn(t, filepath.Join(dir, "go.mod"))
	if got := detectSandboxStack(dir); got != "go" {
		t.Fatalf("стек go = %q, ожидался go", got)
	}
}

// TestDetectSandboxStackNodeOnly — одиночный Node-проект: node-образ.
func TestDetectSandboxStackNodeOnly(t *testing.T) {
	dir := t.TempDir()
	writeFileIn(t, filepath.Join(dir, "package.json"))
	if got := detectSandboxStack(dir); got != "node" {
		t.Fatalf("стек node = %q, ожидался node", got)
	}
}

// TestDetectSandboxStackPythonOnly — одиночный Python-проект.
func TestDetectSandboxStackPythonOnly(t *testing.T) {
	dir := t.TempDir()
	writeFileIn(t, filepath.Join(dir, "requirements.txt"))
	if got := detectSandboxStack(dir); got != "python" {
		t.Fatalf("стек python = %q, ожидался python", got)
	}
}

// TestDetectSandboxStackSkipsVendorAndHidden — манифесты в node_modules/,
// vendor/ и скрытых каталогах не превращают одиночный проект в мультистек
// (там лежат чужие зависимости, а не стек проекта).
func TestDetectSandboxStackSkipsVendorAndHidden(t *testing.T) {
	dir := t.TempDir()
	writeFileIn(t, filepath.Join(dir, "go.mod"))
	writeFileIn(t, filepath.Join(dir, "node_modules", "left-pad", "package.json"))
	writeFileIn(t, filepath.Join(dir, "vendor", "example.com", "pkg", "package.json"))
	writeFileIn(t, filepath.Join(dir, ".cache", "package.json"))
	if got := detectSandboxStack(dir); got != "go" {
		t.Fatalf("мусор в зависимостях не должен менять стек, получено %q", got)
	}
}

// TestDetectSandboxStacksIsDeterministic — карта стеков одинакова при разных
// порядках обхода каталогов (раньше выбор стека зависел от map/random).
func TestDetectSandboxStacksIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	writeFileIn(t, filepath.Join(dir, "go.mod"))
	writeFileIn(t, filepath.Join(dir, "requirements.txt"))
	for i := 0; i < 20; i++ {
		stacks := detectSandboxStacks(dir, 1)
		if !stacks["go"] || !stacks["python"] || len(stacks) != 2 {
			t.Fatalf("набор стеков нестабилен: %+v", stacks)
		}
		if got := detectSandboxStack(dir); got != "" {
			t.Fatalf("два стека → dev-образ, получено %q", got)
		}
	}
}

// TestDetectSandboxStackEmptyProjectUsesDevImage — нет манифестов вовсе →
// dev-образ (прежнее поведение сохранено).
func TestDetectSandboxStackEmptyProjectUsesDevImage(t *testing.T) {
	dir := t.TempDir()
	if got := detectSandboxStack(dir); got != "" {
		t.Fatalf("пустой проект должен давать dev-образ, получено %q", got)
	}
}
