package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkfile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func readfile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Снимок фиксирует существующие файлы; откат возвращает директорию к снимку:
// восстановленные (изменённые/удалённые) и удалённые (созданные) файлы.
func TestSnapRestoreRestoresAndRemoves(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, filepath.Join(dir, "keep.go"), "package keep\n")
	mkfile(t, filepath.Join(dir, "changed.go"), "package changed\n// v1\n")
	mkfile(t, filepath.Join(dir, "gone.go"), "package gone\n")

	snap, err := NewSnap(dir)
	if err != nil {
		t.Fatalf("NewSnap: %v", err)
	}
	if snap.FileCount() != 3 {
		t.Fatalf("ожидалось 3 файла в снимке, got %d", snap.FileCount())
	}

	// Субагент: меняет файл, удаляет файл, создаёт новый.
	mkfile(t, filepath.Join(dir, "changed.go"), "package changed\n// v2\n")
	if err := os.Remove(filepath.Join(dir, "gone.go")); err != nil {
		t.Fatal(err)
	}
	mkfile(t, filepath.Join(dir, "new.go"), "package new\n")
	mkfile(t, filepath.Join(dir, "sub", "deep.go"), "package sub\n")

	restored, removed, err := snap.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if !containsStr(strings.Join(restored, ","), "changed.go") {
		t.Errorf("changed.go должен быть в restored, got %v", restored)
	}
	if !containsStr(strings.Join(restored, ","), "gone.go") {
		t.Errorf("gone.go должен быть в restored, got %v", restored)
	}
	if len(removed) != 2 || !containsStr(strings.Join(removed, ","), "new.go") ||
		!containsStr(strings.Join(removed, ","), "sub/deep.go") {
		t.Errorf("ожидали удаление new.go и sub/deep.go, got %v", removed)
	}

	if got := readfile(t, filepath.Join(dir, "changed.go")); !strings.Contains(got, "v1") {
		t.Errorf("changed.go не восстановлен, got %q", got)
	}
	if got := readfile(t, filepath.Join(dir, "gone.go")); got != "package gone\n" {
		t.Errorf("gone.go не восстановлен, got %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.go")); err == nil {
		t.Error("new.go не удалён при откате")
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "deep.go")); err == nil {
		t.Error("sub/deep.go не удалён при откате")
	}
}

// Повторный Restore — no-op: снимок уже актуален.
func TestSnapRestoreIdempotent(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, filepath.Join(dir, "a.go"), "package a\n")
	snap, err := NewSnap(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := snap.Restore(); err != nil {
		t.Fatalf("первый Restore: %v", err)
	}
	restored, removed, err := snap.Restore()
	if err != nil {
		t.Fatalf("второй Restore: %v", err)
	}
	if len(restored) != 0 || len(removed) != 0 {
		t.Errorf("повторный откат не должен менять файлы: restored=%v removed=%v", restored, removed)
	}
}

// Снимок пустой директории: откат должен удалить всё созданное после.
func TestSnapRestoreEmptyBaseline(t *testing.T) {
	dir := t.TempDir()
	snap, err := NewSnap(dir)
	if err != nil {
		t.Fatal(err)
	}
	mkfile(t, filepath.Join(dir, "junk.txt"), "junk")
	_, removed, err := snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "junk.txt" {
		t.Errorf("ожидали удаление junk.txt, got %v", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "junk.txt")); err == nil {
		t.Error("junk.txt должен быть удалён")
	}
}

// NewSnap на несуществующей директории — ошибка (откат будет пропущен).
func TestSnapNewOnMissingDir(t *testing.T) {
	if _, err := NewSnap(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("ожидалась ошибка для несуществующей директории")
	}
}

// Restore несуществующего снимка (nil) — безопасный no-op.
func TestSnapRestoreNil(t *testing.T) {
	var snap *Snap
	if restored, removed, err := snap.Restore(); err != nil || len(restored) != 0 || len(removed) != 0 {
		t.Fatalf("nil-снимок должен быть no-op: %v %v %v", restored, removed, err)
	}
}

// Scoped-снимок (NewSnapScoped) фиксирует только файлы области: соседний
// каталог — «чужой» — не попадает ни в снимок, ни в удаление при откате.
// Исключённые пути (например PLAN.md) не восстанавливаются и не удаляются.
func TestSnapScopedRestoreIsolatesScope(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, filepath.Join(dir, "frontend", "app.tsx"), "export const a = 1\n")
	mkfile(t, filepath.Join(dir, "server", "main.go"), "package main\n")
	mkfile(t, filepath.Join(dir, "PLAN.md"), "# plan\n")

	snap, err := NewSnapScoped(dir, []string{"frontend/"}, "PLAN.md")
	if err != nil {
		t.Fatalf("NewSnapScoped: %v", err)
	}
	if got := snap.FileCount(); got != 1 {
		t.Fatalf("в снимок должен попасть только frontend/app.tsx, got %d файлов", got)
	}

	// Шаг «падает»: внутри своей области всё изменилось, соседний каталог —
	// нет; PLAN.md изменён исполнителем и не должен откатываться.
	mkfile(t, filepath.Join(dir, "frontend", "app.tsx"), "export const a = 2\n")
	mkfile(t, filepath.Join(dir, "frontend", "new.ts"), "export const b = 1\n")
	mkfile(t, filepath.Join(dir, "server", "main.go"), "package main\nfunc Hack() {}\n")
	mkfile(t, filepath.Join(dir, "PLAN.md"), "# plan v2\n")

	restored, removed, err := snap.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if len(restored) != 1 || restored[0] != "frontend/app.tsx" {
		t.Errorf("ожидали восстановить только frontend/app.tsx, got %v", restored)
	}
	if len(removed) != 1 || removed[0] != "frontend/new.ts" {
		t.Errorf("ожидали удалить только frontend/new.ts, got %v", removed)
	}

	if got := readfile(t, filepath.Join(dir, "frontend", "app.tsx")); !strings.Contains(got, "a = 1") {
		t.Errorf("app.tsx не восстановлен: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "frontend", "new.ts")); err == nil {
		t.Error("new.ts должен быть удалён при откате")
	}
	if got := readfile(t, filepath.Join(dir, "server", "main.go")); !strings.Contains(got, "func Hack()") {
		t.Errorf("чужой файл соседнего каталога не должен откатываться: %q", got)
	}
	if got := readfile(t, filepath.Join(dir, "PLAN.md")); got != "# plan v2\n" {
		t.Errorf("исключённый PLAN.md не должен откатываться: %q", got)
	}
}

// Scoped-снимок по диапазону файлов и записей с префиксом "./".
func TestSnapScopedFilesAndPrefixedEntries(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, filepath.Join(dir, "a.go"), "package a\n")
	mkfile(t, filepath.Join(dir, "sub", "b.go"), "package b\n")
	mkfile(t, filepath.Join(dir, "sub", "c.go"), "package c\n")

	snap, err := NewSnapScoped(dir, []string{"./a.go", "./sub/"})
	if err != nil {
		t.Fatalf("NewSnapScoped: %v", err)
	}
	if got := snap.FileCount(); got != 3 {
		t.Fatalf("ожидали 3 файла (a.go, sub/b.go, sub/c.go), got %d", got)
	}

	// Пустой scope = полный снимок, как NewSnap.
	full, err := NewSnapScoped(dir, nil)
	if err != nil {
		t.Fatalf("NewSnapScoped(nil): %v", err)
	}
	if got := full.FileCount(); got != 3 {
		t.Fatalf("пустой scope должен покрывать весь root, got %d", got)
	}
}

func TestSnapDiffDetectsChanges(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, filepath.Join(dir, "keep.go"), "package keep\n")          // не трогаем
	mkfile(t, filepath.Join(dir, "mod.go"), "package mod\nvar A = 1\n") // будем править

	snap, err := NewSnap(dir)
	if err != nil {
		t.Fatalf("NewSnap: %v", err)
	}

	// Ничего не менялось — пустой diff.
	a, m, r, err := snap.Diff()
	if err != nil || len(a)+len(m)+len(r) != 0 {
		t.Fatalf("ожидали пустой diff: a=%v m=%v r=%v err=%v", a, m, r, err)
	}

	// Субагент: правит mod.go, создаёт new.go, удаляет... вернее удалил бы —
	// используем отдельный файл для проверки удаления.
	mkfile(t, filepath.Join(dir, "del.go"), "package del\n")
	mk2, _ := NewSnap(dir) // снимок с del.go

	// Scoped-снимок того же момента: изменения вне области соседнего шага
	// в его diff попадать не должны.
	scoped, err := NewSnapScoped(dir, []string{"./mod.go"})
	if err != nil {
		t.Fatalf("NewSnapScoped: %v", err)
	}

	mkfile(t, filepath.Join(dir, "new.go"), "package new\nvar N = 42\n")
	if err := os.WriteFile(filepath.Join(dir, "mod.go"), []byte("package mod\nvar A = 7\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "del.go")); err != nil {
		t.Fatal(err)
	}

	a, m, r, err = mk2.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	eq := func(name string, got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %v, ожидали %v", name, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: got %v, ожидали %v", name, got, want)
			}
		}
	}
	eq("added", a, "new.go")
	eq("modified", m, "mod.go")
	eq("removed", r, "del.go")

	a, m, r, _ = scoped.Diff()
	eq("scoped added", a)
	eq("scoped modified", m, "mod.go")
	eq("scoped removed", r)
}
