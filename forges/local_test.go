package forges

import (
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
