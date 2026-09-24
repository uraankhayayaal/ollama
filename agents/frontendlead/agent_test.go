package frontendlead

import (
	"os"
	"path/filepath"
	"strings"
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
	if err := l.Write("README.md", "{}\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := filepath.Join(l.OutputDir, "README.md")
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
	// Лид документирует план в readme (WriteFiles/AppendFile), но не пишет код
	// (DeleteFiles/Run запрещены).
	for _, want := range []string{"List", "ReadFiles", "WriteFiles", "AppendFile"} {
		if !names[want] {
			t.Errorf("агент не включает инструмент %q", want)
		}
	}
	for _, disallowed := range []string{"DeleteFiles", "Run"} {
		if names[disallowed] {
			t.Errorf("агент не должен включать инструмент %q (лид не пишет код и не запускает команды)", disallowed)
		}
	}
}

// TestLeadWriteOnlyReadme: запись лида разрешена ТОЛЬКО в файлы readme* в корне
// проекта (документирование плана работ); код лид писать не должен.
func TestLeadWriteOnlyReadme(t *testing.T) {
	l := newTestLead(t)
	l.SetScope([]string{"frontend/"})
	if err := l.Write("README.md", "# План работ\n"); err != nil {
		t.Fatalf("запись в readme должна быть разрешена: %v", err)
	}
	if err := l.Write("readme.md", "# План работ\n"); err != nil {
		t.Fatalf("запись в readme* без учёта регистра должна быть разрешена: %v", err)
	}
	if err := l.Write("frontend/plan.json", "{}\n"); err == nil {
		t.Fatal("запись вне readme должна быть отклонена (лид ведёт только readme)")
	}
	if err := l.Write("main.go", "package main\n"); err == nil {
		t.Fatal("запись вне readme должна быть отклонена")
	}
}

// TestLeadPromptMentionsMakefile — Ф-2 PLAN-2026-09-24-todo-makefile.md: лид
// вшивает в задачи о сборке/тестах цель проверки из корневого Makefile.
func TestLeadPromptMentionsMakefile(t *testing.T) {
	l := newTestLead(t)
	p := l.GetSystemMessages(nil)[0].Message
	for _, want := range []string{"Makefile", "make frontend-build", "make frontend-test", "make frontend-lint"} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт фронтенд-лида не содержит %q:\n%s", want, p)
		}
	}
}
