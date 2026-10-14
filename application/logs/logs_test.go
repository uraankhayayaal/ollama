package logs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCollectProjectDedupAndFilter проверяет отбор «своего» файла из общего
// каталога, сбор всех файлов локального logs/ и дедупликацию по имени
// (приоритет — общий каталог).
func TestCollectProjectDedupAndFilter(t *testing.T) {
	global := t.TempDir()
	local := t.TempDir()
	own := "proj.log"

	writeFile(t, filepath.Join(global, own), "общий\n")
	writeFile(t, filepath.Join(global, "other.log"), "чужой\n")
	writeFile(t, filepath.Join(global, "server.log"), "служебный\n")
	writeFile(t, filepath.Join(local, "app.log"), "локальный\n")
	writeFile(t, filepath.Join(local, own), "локальная копия\n")

	out, cut := CollectProject([]string{global, local}, global, own)
	if cut {
		t.Fatal("обрезки быть не должно")
	}
	names := map[string]string{}
	for _, e := range out {
		names[e.Name] = e.Content
	}
	if len(out) != 2 {
		t.Fatalf("ожидались 2 файла (own + app.log), got %d: %+v", len(out), names)
	}
	if names[own] != "общий\n" {
		t.Fatalf("приоритет общего каталога нарушен: %q", names[own])
	}
	if names["app.log"] != "локальный\n" {
		t.Fatalf("локальный app.log не собран: %q", names["app.log"])
	}
	if _, ok := names["other.log"]; ok {
		t.Fatal("чужой лог попал в выборку")
	}
	if _, ok := names["server.log"]; ok {
		t.Fatal("служебный server.log попал в выборку")
	}
	// Отсортировано по имени.
	if out[0].Name != "app.log" || out[1].Name != own {
		t.Fatalf("порядок не по имени: %+v", out)
	}
}

// TestCollectFilesOnlyLogs — берутся только *.log (регистр не важен), каталоги
// пропускаются.
func TestCollectFilesOnlyLogs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.log"), "a")
	writeFile(t, filepath.Join(dir, "B.LOG"), "b")
	writeFile(t, filepath.Join(dir, "note.txt"), "n")
	if err := os.MkdirAll(filepath.Join(dir, "sub.log"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := CollectFiles(dir)
	if len(out) != 2 {
		t.Fatalf("ожидались 2 файла, got %d: %+v", len(out), out)
	}
	if CollectFiles(filepath.Join(dir, "nope")) != nil {
		t.Fatal("несуществующий каталог должен дать nil")
	}
}

// TestReadTailTruncates — большой файл читается с конца и помечается обрезкой.
func TestReadTailTruncates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.log")
	big := strings.Repeat("x", MaxBytes+100)
	writeFile(t, path, big)

	got, cut := ReadTail(path)
	if !cut {
		t.Fatal("ожидалась пометка обрезки")
	}
	if !strings.HasPrefix(got, "(лог обрезан до последних записей)\n") {
		t.Fatalf("нет заголовка обрезки: %q", got[:40])
	}

	small := filepath.Join(dir, "small.log")
	writeFile(t, small, "строка\n")
	if got, cut := ReadTail(small); cut || got != "строка\n" {
		t.Fatalf("маленький файл: cut=%v got=%q", cut, got)
	}
	if got, _ := ReadTail(filepath.Join(dir, "absent.log")); got != "" {
		t.Fatalf("отсутствующий файл должен дать пусто, got %q", got)
	}
}
