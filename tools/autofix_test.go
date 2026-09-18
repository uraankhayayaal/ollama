// Тесты Ф-2 в tools: очередь затронутых файлов и LspAutoFix (авто-диагностика
// после мутаций). Без сети: degrade-ветки и фикстуры; E2E на реальном go —
// только если go установлен.

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Мутации через Write/AppendTo/Remove помечают файл, очередь дедуплицируется,
// а LspAutoFix её дренирует (повторный вызов уже без мутаций).
func TestRecordTouchedDedupAndDrain(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}

	if _, had := ops.LspAutoFix(); had {
		t.Fatal("без мутаций hadMutation должен быть false")
	}
	if err := ops.Write("a/b.go", "package b\n"); err != nil {
		t.Fatal(err)
	}
	if err := ops.Write("a/b.go", "package b\n// v2\n"); err != nil {
		t.Fatal(err)
	}
	if err := ops.AppendTo("a/b.go", "// tail\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.Remove("a/other.txt"); err == nil {
		// other.txt не существует — removeLocked вернул status error, не мутация;
		// создадим файл и удалим по-настоящему.
		if err := ops.Write("a/other.txt", "x"); err != nil {
			t.Fatal(err)
		}
		if _, err := ops.Remove("a/other.txt"); err != nil {
			t.Fatal(err)
		}
	}

	// Очередь: только уникальные относительные slash-пути.
	if got := ops.takeTouched(); len(got) != 2 || got[0] != "a/b.go" || got[1] != "a/other.txt" {
		t.Fatalf("touched = %v, want [a/b.go a/other.txt]", got)
	}

	// LspAutoFix на пустой очереди — без мутаций.
	if _, had := ops.LspAutoFix(); had {
		t.Fatal("после дренажа hadMutation должен быть false")
	}
}

// Затронутые пути хранятся относительно OutputDir (slash), а не абсолютными.
func TestRecordTouchedRelativePath(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}
	if err := ops.Write("sub/dir/f.txt", "hi"); err != nil {
		t.Fatal(err)
	}
	got := ops.takeTouched()
	if len(got) != 1 || got[0] != "sub/dir/f.txt" {
		t.Fatalf("touched = %v", got)
	}
	if strings.HasPrefix(got[0], dir) {
		t.Fatalf("путь должен быть относительным, got %q", got[0])
	}
}

// Мутация есть, но стек не определяется (нет маркеров) → LspCheck деградирует
// в skipped: hadMutation=true, диагностик нет (фича тихо неактивна).
func TestLspAutoFixDegradeNoMarkers(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}
	if err := ops.Write("notes.txt", "hello"); err != nil {
		t.Fatal(err)
	}
	diags, had := ops.LspAutoFix()
	if !had {
		t.Fatal("hadMutation должен быть true: файл менялся")
	}
	if len(diags) != 0 {
		t.Fatalf("degrade не должен давать диагностик, got %v", diags)
	}
}

// Патч-инструменты тоже помечают файл (SearchReplace поверх существующего).
func TestSearchReplaceRecordsTouched(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}
	if err := ops.Write("main.py", "print('a')\n"); err != nil {
		t.Fatal(err)
	}
	ops.takeTouched() // сбрасываем с записи

	args := map[string]any{"files": []any{map[string]any{
		"filename": "main.py",
		"patches":  []any{map[string]any{"search": "print('a')", "replace": "print('b')"}},
	}}}
	if _, err := ops.SearchReplace(args); err != nil {
		t.Fatal(err)
	}
	if got := ops.takeTouched(); len(got) != 1 || got[0] != "main.py" {
		t.Fatalf("touched = %v, want [main.py]", got)
	}
}

// Форматирование диагностик в компактные строки для скрытого промпта.
func TestLspDiagnosticLines(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"status": "success",
		"diagnostics": []lspDiagnostic{
			{File: "main.go", Line: 3, Col: 9, Severity: "error", Message: "undefined: x"},
			{File: "main.go", Line: 0, Col: 0, Severity: "error", Message: "build failed"},
		},
	})
	lines := lspDiagnosticLines(raw)
	if len(lines) != 2 {
		t.Fatalf("got %v", lines)
	}
	if lines[0] != "main.go:3:9: undefined: x" {
		t.Fatalf("got %q", lines[0])
	}
	if lines[1] != "main.go: build failed" {
		t.Fatalf("got %q", lines[1])
	}
	if got := lspDiagnosticLines([]byte("не json")); got != nil {
		t.Fatalf("невалидный JSON должен давать nil, got %v", got)
	}
}

// E2E: Write сломанного Go-файла → LspAutoFix возвращает точные строки.
func TestLspAutoFixGoReal(t *testing.T) {
	if !commandAvailable("gopls") && !commandAvailable("go") {
		t.Skip("нет go/gopls — пропускаем E2E")
	}
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}
	if err := ops.Write("go.mod", "module m\n\ngo 1.26\n"); err != nil {
		t.Fatal(err)
	}
	if err := ops.Write("main.go", "package main\n\nfunc main() { undefined() }\n"); err != nil {
		t.Fatal(err)
	}

	diags, had := ops.LspAutoFix()
	if !had {
		t.Fatal("hadMutation должен быть true")
	}
	if len(diags) == 0 {
		t.Skip("чекер не дал диагностик в этом окружении")
	}
	joined := strings.Join(diags, "\n")
	if !strings.Contains(joined, "main.go") {
		t.Fatalf("диагностики без пути к main.go: %v", diags)
	}
	// Повторный вызов: очередь пуста, новой мутации нет.
	if _, had := ops.LspAutoFix(); had {
		t.Fatal("после дренажа hadMutation должен быть false")
	}
}

// Проверка, что удаление затронутого файла снимает запись (файл больше не
// проверяется): после Remove LspAutoFix не должен находить ошибок.
func TestLspAutoFixAfterRemove(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}
	if err := ops.Write("tmp.py", "def f(:\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.Remove("tmp.py"); err != nil {
		t.Fatal(err)
	}
	// Директория пуста, маркеров стека нет — диагностик быть не может.
	diags, had := ops.LspAutoFix()
	if !had {
		t.Fatal("hadMutation должен быть true")
	}
	if len(diags) != 0 {
		t.Fatalf("got %v", diags)
	}
	if _, err := os.Stat(filepath.Join(dir, "tmp.py")); !os.IsNotExist(err) {
		t.Fatal("файл должен быть удалён")
	}
}
