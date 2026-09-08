package qalead

import (
	"os"
	"path/filepath"
	"testing"
)

// newTestLead создаёт QA Lead во временной директории.
func newTestLead(t *testing.T) *QALead {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	return newQALead(dir, "", Config{})
}

func TestLeadResolvePathRejectsTraversal(t *testing.T) {
	l := newTestLead(t)
	for _, bad := range []string{"../evil_test.go", "a/../../etc/passwd", "/etc/passwd"} {
		if _, err := l.ResolvePath(bad); err == nil {
			t.Errorf("ResolvePath(%q) должен вернуть ошибку выхода за пределы OutputDir", bad)
		}
	}
}

func TestLeadToolsAreSelected(t *testing.T) {
	l := newTestLead(t)
	got := l.GetTools()
	names := map[string]bool{}
	for _, td := range got {
		names[td.Name] = true
	}
	for _, want := range []string{"List", "ReadFiles"} {
		if !names[want] {
			t.Errorf("агент не включает инструмент %q", want)
		}
	}
	for _, disallowed := range []string{"WriteFiles", "DeleteFiles", "AppendFile", "Run"} {
		if names[disallowed] {
			t.Errorf("агент не должен включать инструмент %q (лид не пишет код и не запускает команды)", disallowed)
		}
	}
}

func TestLeadCannotWriteThroughTools(t *testing.T) {
	l := newTestLead(t)
	if _, err := l.CallFunction("WriteFiles", map[string]any{
		"files": []map[string]string{{"filename": "x.json", "content": "{}"}},
	}); err == nil {
		t.Fatal("лид не должен уметь писать файлы через инструменты")
	}
	if _, err := l.CallFunction("Run", map[string]any{"command": "pwd"}); err == nil {
		t.Fatal("лид не должен уметь запускать консольные команды через инструменты")
	}
}

func TestLeadConstrainScopePreventsWriteOutsideScope(t *testing.T) {
	l := newTestLead(t)
	l.SetScope([]string{"tests/"})
	if err := l.Write("tests/api_test.go", "package api\n"); err != nil {
		t.Fatalf("запись внутри области работы должна быть разрешена: %v", err)
	}
	if err := l.Write("main.go", "package main\n"); err == nil {
		t.Fatal("запись вне области работы должна быть отклонена")
	}
}
