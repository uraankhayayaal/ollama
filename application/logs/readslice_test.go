package logs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadLogSliceBasic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	content := "строка1\nстрока2\nстрока3\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLogSlice(path, "", 100, 0, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "test.log" {
		t.Fatalf("Name = %q", got.Name)
	}
	if got.Total != 3 {
		t.Fatalf("Total = %d, want 3", got.Total)
	}
	if got.Lines != 3 {
		t.Fatalf("Lines = %d", got.Lines)
	}
	want := strings.TrimSuffix(content, "\n")
	if got.Content != want {
		t.Fatalf("Content = %q, want %q", got.Content, want)
	}
}

func TestReadLogSliceGrep(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	content := "error: foo\ninfo: bar\nerror: baz\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLogSlice(path, "error", 100, 0, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 3 {
		t.Fatalf("Total = %d, want 3", got.Total)
	}
	if got.Matched != 2 {
		t.Fatalf("Matched = %d, want 2", got.Matched)
	}
	if got.Content != "error: foo\nerror: baz" {
		t.Fatalf("Content = %q", got.Content)
	}
}

func TestReadLogSliceLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	content := "a\nb\nc\nd\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLogSlice(path, "", 2, 0, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lines != 2 {
		t.Fatalf("Lines = %d, want 2", got.Lines)
	}
	if got.Truncated != true {
		t.Fatal("Truncated = false, want true")
	}
	if got.Content != "c\nd" {
		t.Fatalf("Content = %q", got.Content)
	}
}

func TestReadLogSliceMissingFile(t *testing.T) {
	_, err := ReadLogSlice("/nonexistent/path.log", "", 100, 0, 4096)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadLogSliceContentMaxBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	content := "verylongline1\nverylongline2\nverylongline3\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// contentMaxBytes = 20 — обрежет с конца
	got, err := ReadLogSlice(path, "", 100, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if len(got.Content) > 20 {
		t.Fatalf("Content len = %d, want <= 20", len(got.Content))
	}
}
