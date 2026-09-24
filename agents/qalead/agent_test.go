package qalead

import (
	"os"
	"path/filepath"
	"strings"
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

func TestLeadCannotWriteThroughTools(t *testing.T) {
	l := newTestLead(t)
	// Запись вне readme через инструменты запрещена (readme-only scope):
	// WriteFiles возвращает статус "error" внутри JSON-результата.
	out, err := l.CallFunction("WriteFiles", map[string]any{
		"files": []map[string]string{{"filename": "x.json", "content": "{}"}},
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка инструмента: %v", err)
	}
	if !strings.Contains(string(out), `"status":"error"`) {
		t.Fatalf("запись не-readme файла должна вернуть статус error, got: %s", out)
	}
	if _, err := l.CallFunction("Run", map[string]any{"command": "pwd"}); err == nil {
		t.Fatal("лид не должен уметь запускать консольные команды через инструменты")
	}
}

// TestLeadWriteOnlyReadme: запись лида разрешена ТОЛЬКО в файлы readme* в корне
// проекта (документирование плана работ); код лид писать не должен.
func TestLeadWriteOnlyReadme(t *testing.T) {
	l := newTestLead(t)
	l.SetScope([]string{"tests/"})
	if err := l.Write("README.md", "# План работ\n"); err != nil {
		t.Fatalf("запись в readme должна быть разрешена: %v", err)
	}
	if err := l.Write("readme.md", "# План работ\n"); err != nil {
		t.Fatalf("запись в readme* без учёта регистра должна быть разрешена: %v", err)
	}
	if err := l.Write("tests/api_test.go", "package api\n"); err == nil {
		t.Fatal("запись вне readme должна быть отклонена (лид ведёт только readme)")
	}
	if err := l.Write("main.go", "package main\n"); err == nil {
		t.Fatal("запись вне readme должна быть отклонена")
	}
}

// TestLeadPromptMentionsMakefile — Ф-2 PLAN-2026-09-24-todo-makefile.md: лид
// QA фиксирует единую команду автотестов из корневого Makefile (make test /
// make e2e / make build / make lint).
func TestLeadPromptMentionsMakefile(t *testing.T) {
	l := newTestLead(t)
	p := l.GetSystemMessages(nil)[0].Message
	for _, want := range []string{"Makefile", "make test", "make e2e", "make build", "make lint"} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт QA-лида не содержит %q:\n%s", want, p)
		}
	}
}
