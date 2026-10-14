package diff

import "testing"

func TestParseUnifiedDiffSubmodule(t *testing.T) {
	raw := "diff --git a/packages/auth b/packages/auth\nindex 1234567..abcdef0 160000\n--- a/packages/auth\n+++ b/packages/auth\n@@ -1 +1 @@\n-Subproject commit 1234567\n+Subproject commit abcdef0\n"
	d := Parse(raw)
	if len(d.Files) != 1 {
		t.Fatalf("files=%+v", d.Files)
	}
	if got := d.Files[0].Path; got != "packages/auth" {
		t.Fatalf("path=%q", got)
	}
	if d.Files[0].Status != Submodule {
		t.Fatalf("status=%q", d.Files[0].Status)
	}
}

func TestPrefixDiffPath(t *testing.T) {
	got := PrefixPath("diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go", "packages/auth")
	want := "diff --git a/packages/auth/main.go b/packages/auth/main.go\n--- a/packages/auth/main.go\n+++ b/packages/auth/main.go"
	if got != want {
		t.Fatalf("PrefixPath = %q, want %q", got, want)
	}
}

func TestParseStatusesAndCounts(t *testing.T) {
	raw := "diff --git a/new.go b/new.go\n--- /dev/null\n+++ b/new.go\n@@ -0,0 +1,2 @@\n+one\n+two\n" +
		"diff --git a/gone.go b/gone.go\n--- a/gone.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-gone\n" +
		"diff --git a/old.go b/old.go\n--- a/old.go\n+++ b/old.go\n@@ -1 +1 @@\n-a\n+b\n"
	d := Parse(raw)
	if len(d.Files) != 3 {
		t.Fatalf("files=%+v", d.Files)
	}
	byPath := map[string]FileEntry{}
	for _, f := range d.Files {
		byPath[f.Path] = f
	}
	if byPath["new.go"].Status != Added || byPath["new.go"].Added != 2 {
		t.Fatalf("new.go=%+v", byPath["new.go"])
	}
	if byPath["gone.go"].Status != Removed || byPath["gone.go"].Deleted != 1 {
		t.Fatalf("gone.go=%+v", byPath["gone.go"])
	}
	if byPath["old.go"].Status != Modified || byPath["old.go"].Added != 1 || byPath["old.go"].Deleted != 1 {
		t.Fatalf("old.go=%+v", byPath["old.go"])
	}
	if d.File["new.go"] == "" || d.File["gone.go"] == "" {
		t.Fatal("патчи файлов не сохранены")
	}
}

func TestParseRenameAndQuotedPath(t *testing.T) {
	raw := "diff --git a/old name.go b/new name.go\nsimilarity index 100%\nrename from old name.go\nrename to new name.go\n"
	d := Parse(raw)
	if len(d.Files) != 1 || d.Files[0].Status != Renamed || d.Files[0].Path != "new name.go" {
		t.Fatalf("rename: %+v", d.Files)
	}
	if got := PathOf("\"a/with space.go\""); got != "a/with space.go" {
		t.Fatalf("PathOf quoted = %q", got)
	}
	if got := PathOf("b/x.go"); got != "x.go" {
		t.Fatalf("PathOf = %q", got)
	}
}

func TestSplitBlocks(t *testing.T) {
	blocks := Split("diff --git a/a b/a\nx\ndiff --git a/b b/b\ny\n")
	if len(blocks) != 2 {
		t.Fatalf("blocks=%d", len(blocks))
	}
}
