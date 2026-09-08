package frontendlead

import (
	"os"
	"path/filepath"
	"testing"
)

// newTestLead создаёт Frontend Tech Lead во временной директории.
func newTestLead(t *testing.T) *FrontendLead {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	return newFrontendLead(dir, "", Config{})
}

func TestLeadResolvePathRejectsTraversal(t *testing.T) {
	l := newTestLead(t)
	for _, bad := range []string{"../evil.tsx", "a/../../etc/passwd", "/etc/passwd"} {
		if _, err := l.ResolvePath(bad); err == nil {
			t.Errorf("ResolvePath(%q) должен вернуть ошибку выхода за пределы OutputDir", bad)
		}
	}
}

func TestLeadWriteWritesIntoOutputDir(t *testing.T) {
	l := newTestLead(t)
	if err := l.Write("FRONTEND_PLAN.json", "{}\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := filepath.Join(l.OutputDir, "FRONTEND_PLAN.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("файл %q не создан: %v", want, err)
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

func TestLeadConstrainScopePreventsWriteOutsideScope(t *testing.T) {
	l := newTestLead(t)
	l.SetScope([]string{"frontend/"})
	if err := l.Write("frontend/plan.json", "{}\n"); err != nil {
		t.Fatalf("запись внутри области работы должна быть разрешена: %v", err)
	}
	if err := l.Write("main.go", "package main\n"); err == nil {
		t.Fatal("запись вне области работы должна быть отклонена")
	}
}
