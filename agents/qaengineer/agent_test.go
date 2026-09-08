package qaengineer

import (
	"os"
	"path/filepath"
	"testing"
)

// newTestQA создаёт QA-инженера во временной директории.
func newTestQA(t *testing.T) *QAEngineer {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	return newQAEngineer(dir, "", Config{})
}

func TestQAResolvePathRejectsTraversal(t *testing.T) {
	q := newTestQA(t)
	for _, bad := range []string{"../evil_test.go", "a/../../etc/passwd", "/etc/passwd"} {
		if _, err := q.ResolvePath(bad); err == nil {
			t.Errorf("ResolvePath(%q) должен вернуть ошибку выхода за пределы OutputDir", bad)
		}
	}
}

func TestQAWriteWritesIntoOutputDir(t *testing.T) {
	q := newTestQA(t)
	if err := q.Write("api_test.go", "package api\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := filepath.Join(q.OutputDir, "api_test.go")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("файл %q не создан: %v", want, err)
	}
}

func TestQAToolsAreSelected(t *testing.T) {
	q := newTestQA(t)
	got := q.GetTools()
	names := map[string]bool{}
	for _, td := range got {
		names[td.Name] = true
	}
	for _, want := range []string{"WriteFiles", "ReadFiles", "DeleteFiles", "AppendFile", "List", "Run"} {
		if !names[want] {
			t.Errorf("агент не включает инструмент %q", want)
		}
	}
}

func TestQAConstrainScopePreventsWriteOutsideScope(t *testing.T) {
	q := newTestQA(t)
	q.SetScope([]string{"tests/"})
	if err := q.Write("tests/api_test.go", "package api\n"); err != nil {
		t.Fatalf("запись внутри области работы должна быть разрешена: %v", err)
	}
	if err := q.Write("main.go", "package main\n"); err == nil {
		t.Fatal("запись вне области работы должна быть отклонена")
	}
}
