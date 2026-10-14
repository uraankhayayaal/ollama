// Тесты правки и презентации конфликтных файлов (application/gitflow):
// безопасность путей, отбор остаточных маркеров, запись files/edits. Вынесено
// из `server/actions_resolve.go` (Этап 6, срез F3v).
package gitflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConflictSafeRelAndWithinDir(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "/etc/passwd", "../outside.go", "a/../../b.go"} {
		if _, err := ConflictSafeRel(bad); err == nil {
			t.Errorf("ConflictSafeRel(%q) — ожидалась ошибка", bad)
		}
	}
	got, err := ConflictSafeRel("./src/../src/main.go")
	if err != nil || got != "src/main.go" {
		t.Fatalf("ConflictSafeRel = %q, %v", got, err)
	}
	wt := filepath.Join(t.TempDir(), ".conflict-p-e")
	if !WithinDir(wt, filepath.Join(wt, "main.go")) {
		t.Fatal("файл внутри worktree не распознан")
	}
	if WithinDir(wt, wt+"-evil/main.go") {
		t.Fatal("путь-сосед распознан как внутри worktree")
	}
}

func TestNewConflictViewTruncatesBigFiles(t *testing.T) {
	dir := t.TempDir()
	small := "package main\n<<<<<<< HEAD\nv1\n=======\nv2\n>>>>>>> other\n"
	if err := os.WriteFile(filepath.Join(dir, "small.go"), []byte(small), 0o644); err != nil {
		t.Fatal(err)
	}
	big := "package main\n" + strings.Repeat("// filler\n", 8000) +
		"<<<<<<< HEAD\nv1\n=======\nv2\n>>>>>>> other\n"
	if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	v := NewConflictView(dir, []string{"small.go", "big.go"})
	if len(v.Files) != 2 || len(v.Content) != 2 {
		t.Fatalf("view = %+v", v)
	}
	if v.Content["small.go"] != small {
		t.Fatalf("малый файл не отдан целиком:\n%s", v.Content["small.go"])
	}
	if len(v.Truncated) != 1 || v.Truncated[0] != "big.go" {
		t.Fatalf("truncated = %v", v.Truncated)
	}
	frag := v.Content["big.go"]
	if !strings.Contains(frag, "<<<<<<< HEAD") || !strings.Contains(frag, "фрагментом") {
		t.Fatalf("фрагмент без маркеров/пояснения:\n%s", frag)
	}
	if len(frag) >= len(big) {
		t.Fatalf("фрагмент не короче файла (%d vs %d)", len(frag), len(big))
	}
	if !strings.Contains(v.Hint, "edits") {
		t.Fatalf("hint = %q", v.Hint)
	}
}

func TestNewConflictViewCleanFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "clean.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v := NewConflictView(dir, []string{"clean.go"})
	if len(v.Files) != 0 {
		t.Fatalf("файл без маркеров не должен попадать в files: %+v", v.Files)
	}
	if v.Hint != "конфликтных файлов не осталось" {
		t.Fatalf("hint = %q", v.Hint)
	}
}

func TestApplyConflictFilesReplaceAndEdit(t *testing.T) {
	dir := t.TempDir()
	body := "package main\n<<<<<<< HEAD\nv1\n=======\nv2\n>>>>>>> other\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"main.go": true}
	touched, err := ApplyConflictFiles(dir, allowed, []string{"main.go"},
		map[string]string{"main.go": "package main\nRESOLVED\n"},
		[]ConflictEdit{{File: "main.go", Old: "RESOLVED", New: "EDITED"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(touched) != 1 || touched[0] != "main.go" {
		t.Fatalf("touched = %v", touched)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "main.go"))
	if string(got) != "package main\nEDITED\n" {
		t.Fatalf("содержимое = %q", got)
	}
}

func TestApplyConflictFilesRejects(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"main.go": true}
	pending := []string{"main.go"}

	// Не конфликтный файл (нет в allowed).
	if _, err := ApplyConflictFiles(dir, allowed, pending, map[string]string{"other.go": "y"}, nil); err == nil {
		t.Fatal("не конфликтный файл должен отклоняться")
	}
	// Выход за пределы worktree.
	if _, err := ApplyConflictFiles(dir, map[string]bool{"../evil.go": true}, pending, map[string]string{"../evil.go": "z"}, nil); err == nil {
		t.Fatal("путь с выходом должен отклоняться")
	}
	// old встречается не ровно один раз.
	dup := filepath.Join(dir, "dup.go")
	if err := os.WriteFile(dup, []byte("A\nA\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyConflictFiles(dir, map[string]bool{"dup.go": true}, []string{"dup.go"}, nil,
		[]ConflictEdit{{File: "dup.go", Old: "A", New: "B"}}); err == nil {
		t.Fatal("неуникальный old должен отклоняться")
	}
	// Пустой old.
	if _, err := ApplyConflictFiles(dir, allowed, pending, nil,
		[]ConflictEdit{{File: "main.go", Old: "", New: "B"}}); err == nil {
		t.Fatal("пустой old должен отклоняться")
	}
}

func TestConflictMarkerAndRegion(t *testing.T) {
	for _, ln := range []string{"<<<<<<< HEAD", "=======", ">>>>>>> other", "  ||||||| base", "======= label"} {
		if !IsConflictMarkerLine(ln) {
			t.Errorf("IsConflictMarkerLine(%q) = false", ln)
		}
	}
	if IsConflictMarkerLine("package main") {
		t.Fatal("обычная строка — не маркер")
	}
	var b strings.Builder
	b.WriteString("head\n")
	b.WriteString(strings.Repeat("filler\n", 200))
	b.WriteString("<<<<<<< HEAD\nv1\n=======\nv2\n>>>>>>> other\n")
	out := ConflictRegion(b.String())
	if !strings.Contains(out, "<<<<<<< HEAD") || !strings.Contains(out, "фрагментом") {
		t.Fatalf("регион без маркеров/пояснения")
	}
	if strings.Contains(out, "filler") && len(out) > 4000 {
		t.Fatalf("регион не обрезан (%d)", len(out))
	}
}
