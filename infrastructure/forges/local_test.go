package forges

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalForgeGetDiff(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "util.go"), []byte("package main\n\nvar X = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	lf, err := NewLocalForge(dir)
	if err != nil {
		t.Fatal(err)
	}

	diff, err := lf.GetDiff()
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"main.go", "util.go", "+package main", "+var X = 1"} {
		if !strings.Contains(diff, want) {
			t.Errorf("дифф не содержит %q:\n%s", want, diff)
		}
	}
}

func TestLocalForgePostCommentAndApprove(t *testing.T) {
	dir := t.TempDir()
	lf, err := NewLocalForge(dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := lf.PostComment(ReviewComment{FilePath: "main.go", Line: 3, Text: "критично: nil deref"}); err != nil {
		t.Fatal(err)
	}
	if len(lf.Published) != 1 || lf.Published[0].Text != "критично: nil deref" {
		t.Fatalf("ожидали 1 опубликованный комментарий, got %#v", lf.Published)
	}

	if err := lf.PostSummary("2 замечания"); err != nil {
		t.Fatal(err)
	}
	if err := lf.Approve("одобрено"); err != nil {
		t.Fatal(err)
	}
	if !lf.Approved {
		t.Fatal("ожидали Approve == true")
	}
}

func TestIsIgnoredDir(t *testing.T) {
	for _, name := range []string{"node_modules", "vendor", "dist", "build", "target", "__pycache__", ".git", ".next", "bower_components"} {
		if !IsIgnoredDir(name) {
			t.Errorf("IsIgnoredDir(%q) = false, ожидали true", name)
		}
	}
	for _, name := range []string{"src", "internal", "main", "utils", "public"} {
		if IsIgnoredDir(name) {
			t.Errorf("IsIgnoredDir(%q) = true, ожидали false", name)
		}
	}
}

func TestLocalForgeGetDiffSkipsIgnoredDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "src.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "dep"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "dep", "big.js"), []byte("// huge lib\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "dist"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dist", "app.min.js"), []byte("build artifact\n"), 0644); err != nil {
		t.Fatal(err)
	}

	lf, err := NewLocalForge(dir)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := lf.GetDiff()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "src.go") {
		t.Errorf("дифф должен содержать src.go:\n%s", diff)
	}
	for _, bad := range []string{"node_modules", "big.js", "dist", "app.min.js"} {
		if strings.Contains(diff, bad) {
			t.Errorf("дифф не должен содержать %q:\n%s", bad, diff)
		}
	}
}

func TestLocalForgeGetDiffScoped(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "touched.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal", "order"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "order", "in.go"), []byte("package order\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "untouched.go"), []byte("package main\n// не тронут\n"), 0644); err != nil {
		t.Fatal(err)
	}

	lf, err := NewLocalForge(dir)
	if err != nil {
		t.Fatal(err)
	}
	lf.SetScope([]string{"touched.go", "internal/order/"})

	diff, err := lf.GetDiff()
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"touched.go", "internal/order/in.go"} {
		if !strings.Contains(diff, want) {
			t.Errorf("дифф должен содержать %q (в scope):\n%s", want, diff)
		}
	}
	for _, bad := range []string{"untouched.go"} {
		if strings.Contains(diff, bad) {
			t.Errorf("дифф не должен содержать %q (вне scope):\n%s", bad, diff)
		}
	}
}

func TestLocalForgeGetDiffCap(t *testing.T) {
	dir := t.TempDir()
	// Уникальное содержимое в каждом файле, чтобы проверить отсутствие
	// файлов вне бюджета по их уникальным строкам.
	for i := 0; i < 10; i++ {
		content := "// файл " + string(rune('a'+i)) + "\n" + strings.Repeat(fmt.Sprintf("payload %d line\n", i), 100)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.go", i)), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	lf, err := NewLocalForge(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Маленький бюджет: в дифф попадут лишь первые файлы, остальные —
	// в заметке об обрезании.
	lf.maxDiffSize = 500

	diff, err := lf.GetDiff()
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(diff, "Примечание: дифф превышает лимит") {
		t.Errorf("дифф должен содержать заметку об обрезании:\n%s", diff)
	}
	if len(diff) > 500+2048 {
		t.Errorf("дифф превысил бюджет: len=%d", len(diff))
	}
	// Контент последних файлов (вне бюджета) не должен попасть в дифф.
	for _, bad := range []string{"payload 8 line", "payload 9 line"} {
		if strings.Contains(diff, bad) {
			t.Errorf("дифф не должен содержать содержимое файла вне бюджета (%q):\n%s", bad, diff)
		}
	}
}

func TestLocalForgeGetDiffSkipsBinary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ok.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "logo.png"), []byte("\x89PNG\r\n\x1a\n\x00\x00binary"), 0644); err != nil {
		t.Fatal(err)
	}

	lf, err := NewLocalForge(dir)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := lf.GetDiff()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "ok.go") {
		t.Errorf("дифф должен содержать ok.go:\n%s", diff)
	}
	// Содержимое бинарного файла не должно попасть в дифф как код.
	if strings.Contains(diff, "binary") || strings.Contains(diff, "\x00") {
		t.Errorf("дифф не должен содержать содержимое бинарного файла:\n%q", diff)
	}
}

func TestDetectTypeLocal(t *testing.T) {
	if DetectType("local:///tmp/proj") != KindLocal {
		t.Error("local:// URL должен распознаваться как KindLocal")
	}
}

func TestNewLocalViaFactory(t *testing.T) {
	dir := t.TempDir()
	f, err := New("local://"+dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.(*LocalForge); !ok {
		t.Fatalf("ожидали *LocalForge, got %T", f)
	}
}
