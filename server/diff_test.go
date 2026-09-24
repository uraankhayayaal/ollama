package server

import "testing"

func TestParseUnifiedDiffSubmodule(t *testing.T) {
	raw := "diff --git a/packages/auth b/packages/auth\nindex 1234567..abcdef0 160000\n--- a/packages/auth\n+++ b/packages/auth\n@@ -1 +1 @@\n-Subproject commit 1234567\n+Subproject commit abcdef0\n"
	d := parseUnifiedDiff(raw)
	if len(d.Files) != 1 {
		t.Fatalf("files=%+v", d.Files)
	}
	if got := d.Files[0].Path; got != "packages/auth" {
		t.Fatalf("path=%q", got)
	}
	if d.Files[0].Status != diffSubmodule {
		t.Fatalf("status=%q", d.Files[0].Status)
	}
}

func TestPrefixDiffPath(t *testing.T) {
	got := prefixDiffPath("diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go", "packages/auth")
	want := "diff --git a/packages/auth/main.go b/packages/auth/main.go\n--- a/packages/auth/main.go\n+++ b/packages/auth/main.go"
	if got != want {
		t.Fatalf("prefixDiffPath = %q, want %q", got, want)
	}
}
