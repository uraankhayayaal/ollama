package backendlead

import (
	"ai/agents"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestLead создаёт Backend Tech Lead во временной директории.
func newTestLead(t *testing.T) *BackendLead {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	return newBackendLead(dir, "", Config{})
}

func TestLeadResolvePathRejectsTraversal(t *testing.T) {
	l := newTestLead(t)
	for _, bad := range []string{"../evil.go", "a/../../etc/passwd", "/etc/passwd"} {
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
	// 5.1: лид пишет скелетон (WriteFiles/AppendFile) и гоняет проверку/git
	// через Run; удаления файлов у него нет (реализацию не трогает).
	for _, want := range []string{"List", "ReadFiles", "WriteFiles", "AppendFile", "Run"} {
		if !names[want] {
			t.Errorf("агент не включает инструмент %q", want)
		}
	}
	if names["DeleteFiles"] {
		t.Error("агент не должен включать DeleteFiles (лид не удаляет файлы проекта)")
	}
}

// TestLeadWriteSkeletonAllowlist (5.1): readme-only снят — лид пишет скелетон
// по allowlist (директории 1-го уровня и корневые файлы), всё остальное
// запись отклоняет (гард в инструменте, а не только в промпте).
func TestLeadWriteSkeletonAllowlist(t *testing.T) {
	l := newTestLead(t)
	for _, ok := range []string{
		"README.md", "readme.md", "AGENTS.md",
		"server/internal/service/weather.go",
		"docs/api.md",
	} {
		if err := l.Write(ok, "// скелетон\n"); err != nil {
			t.Errorf("запись %q должна быть разрешена allowlist: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"main.go",
		"internal/service/x.go",
		"web/src/App.tsx",
		"server.go",
	} {
		if err := l.Write(bad, "package main\n"); err == nil {
			t.Errorf("запись %q вне allowlist должна быть отклонена", bad)
		}
	}
}

// TestLeadPromptSkeletonRules (5.3): системный промпт лида ведёт скелетон
// (не «не пишешь код»), знает про allowlist и Run и несёт общий Run-фрагмент
// компактного вывода.
func TestLeadPromptSkeletonRules(t *testing.T) {
	l := newTestLead(t)
	p := l.GetSystemMessages(nil)[0].Message
	for _, want := range []string{
		"скелетон", "СКЕЛЕТОН", "allowlist", "Run", "ВЕТКА ЭПИКА",
		agents.RunTokenEconomy,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт бэкенд-лида не содержит %q", want)
		}
	}
	if strings.Contains(p, "Ты НЕ пишешь код") {
		t.Error("промпт лида не должен запрещать код: роль — скелетон")
	}
}

// TestLeadPromptMentionsMakefile — Ф-2 PLAN-2026-09-24-todo-makefile.md: лид
// вшивает в задачи о сборке/тестах цель проверки из корневого Makefile.
func TestLeadPromptMentionsMakefile(t *testing.T) {
	l := newTestLead(t)
	p := l.GetSystemMessages(nil)[0].Message
	for _, want := range []string{"Makefile", "make backend-build", "make backend-test", "make backend-lint"} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт бэкенд-лида не содержит %q:\n%s", want, p)
		}
	}
}
