package qaengineer

import (
	"ai/agents/promptcheck"
	"os"
	"path/filepath"
	"strings"
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

// TestQAPromptMentionsMakefile — Ф-2/Ф-4 PLAN-2026-09-24-todo-makefile.md:
// единая команда автотестов — из корневого Makefile (make test / make e2e),
// приёмка через make build/make lint; запрет дев-процессов сохраняется.
func TestQAPromptMentionsMakefile(t *testing.T) {
	q := newTestQA(t)
	p := q.GetSystemMessages(nil)[0].Message
	for _, want := range []string{"Makefile", "make test", "make e2e", "make build", "make lint"} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт QA-инженера не содержит %q:\n%s", want, p)
		}
	}
	if !strings.Contains(p, "НЕ запускай приложение через Run") {
		t.Errorf("промпт должен сохранять запрет дев-процессов:\n%s", p)
	}
}

// TestPromptMentionsOnlyAvailableTools — согласованность промпта QA и его
// набора инструментов (Ф-2): инструкция «проверь то-то» неисполнима, если
// инструмента в наборе нет (модель получит «not in tool set»).
//
// Для QA ReadAppLogs обязателен отдельно: без запуска приложения в рантайме
// невозможно отличить «юнит-тесты зелёные» от «сервер не поднимается».
func TestPromptMentionsOnlyAvailableTools(t *testing.T) {
	qa, err := NewQAEngineerInDir("задание", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prompt := qa.GetSystemMessages(nil)[0].Message
	for _, p := range promptcheck.CheckToolSet("qa", prompt, qaToolNames) {
		t.Error(p)
	}
	for _, p := range promptcheck.CheckTypos("qa", prompt) {
		t.Error(p)
	}
	if !promptcheck.Contains(qaToolNames, "ReadAppLogs") {
		t.Error("qa: в наборе нет ReadAppLogs — проверять рантайм нечем")
	}
	if !promptcheck.MentionsWord(prompt, "ReadAppLogs") {
		t.Error("qa: промпт не упоминает ReadAppLogs")
	}
}
