package rag

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// WalkProject обходит дерево, исключая служебные каталоги, скрытые файлы,
// бинарные файлы и записи .gitignore.
func TestWalkProjectIgnores(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.go", "package main\nfunc main() {}\n")
	write(t, dir, "server/api.go", "package api\n")
	write(t, dir, "internal/auth/token.go", "package auth\n")
	// Служебные каталоги не обходятся.
	write(t, dir, "node_modules/lib/index.js", "export{}")
	write(t, dir, ".git/config", "x")
	write(t, dir, "dist/bundle.js", "export{}")
	// Скрытые файлы пропускаются.
	write(t, dir, ".env", "SECRET=1")
	// Бинарный файл отсекается по NUL-байту.
	write(t, dir, "server/logo.png", "PK\x00\x01\x00png")

	got, err := WalkProject(dir)
	if err != nil {
		t.Fatalf("WalkProject: %v", err)
	}
	want := []string{"internal/auth/token.go", "main.go", "server/api.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WalkProject:\n got %v\nwant %v", got, want)
	}
}

// .gitignore исключает файлы и каталоги; «!»-инверсия возвращает файлы обратно.
func TestWalkProjectGitignore(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.go", "package main\n")
	write(t, dir, "gen/output.go", "package gen\n")
	write(t, dir, "gen/keep.go", "package gen\n")
	write(t, dir, "docs/notes.md", "note")
	write(t, dir, "internal/tmp.secrets.go", "// secret\n")
	write(t, dir, ".gitignore",
		"# сгенерированное\n"+
			"gen/*\n"+
			"*.secrets.go\n"+
			"!gen/keep.go\n"+
			"docs/\n")
	// Директория docs игнорируется и не обходится.
	write(t, dir, "docs/api.md", "api")

	got, err := WalkProject(dir)
	if err != nil {
		t.Fatalf("WalkProject: %v", err)
	}
	want := []string{"gen/keep.go", "main.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WalkProject:\n got %v\nwant %v", got, want)
	}
}

// Вложенный .gitignore действует только внутри своего корня.
func TestWalkProjectNestedGitignore(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "server/api.go", "package api\n")
	write(t, dir, "server/generated/types.go", "package types\n")
	write(t, dir, "server/.gitignore", "generated/\n")
	// Тот же паттерн в другом подкаталоге не применяется к корню.
	write(t, dir, "generated/x.go", "package x\n")

	got, err := WalkProject(dir)
	if err != nil {
		t.Fatalf("WalkProject: %v", err)
	}
	want := []string{"generated/x.go", "server/api.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WalkProject:\n got %v\nwant %v", got, want)
	}
}

// Крупные файлы пропускаются, чтобы индекс не держал в памяти вендореные блоки.
func TestWalkProjectSkipsLargeFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.go", "package main\n")
	write(t, dir, "big.bin", string(make([]byte, maxIndexFileSize+1)))
	// Файл без NUL, но крупный — пропускается по размеру.
	bigText := make([]byte, maxIndexFileSize+1)
	for i := range bigText {
		bigText[i] = 'a'
	}
	write(t, dir, "huge.txt", string(bigText))

	got, err := WalkProject(dir)
	if err != nil {
		t.Fatalf("WalkProject: %v", err)
	}
	if len(got) != 1 || got[0] != "main.go" {
		t.Fatalf("WalkProject:\n got %v\nwant [main.go]", got)
	}
}

func TestScopeForPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"server/internal/auth/token.go", "server"},
		{"frontend/src/app.tsx", "frontend"},
		{"main.go", "root"},
		{"go.mod", "root"},
	}
	for _, tc := range cases {
		if got := ScopeForPath(tc.in); got != tc.want {
			t.Fatalf("ScopeForPath(%q): got %q, want %q", tc.in, got, tc.want)
		}
	}
}