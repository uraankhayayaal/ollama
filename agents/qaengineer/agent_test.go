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

// TestQAPromptUsesAcceptorVerifyPlan — Ф-3: команды проверки в промпте QA
// приходят из приёмки (agents/acceptor.VerifyPlanFor), а не из прозы промпта.
// Проверяется на настоящем Makefile-проекте: если план перестанет вычисляться
// или Makefile перестанет иметь приоритет, в промпте появятся «(не определена)».
func TestQAPromptUsesAcceptorVerifyPlan(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mk := "build:\n\tgo build ./...\ntest:\n\tgo test ./...\ne2e:\n\tgo run . & p=$$!; sleep 1; kill $$p\nlint:\n\tgofmt -l .\n"
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte(mk), 0o644); err != nil {
		t.Fatal(err)
	}
	q := newQAEngineer(dir, "", Config{})
	prompt := q.GetSystemMessages(nil)[0].Message

	for _, want := range []string{"КОМАНДЫ ПРОВЕРКИ", "make test", "make build", "make e2e"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("промпт QA-инженера не содержит %q:\n%s", want, prompt)
		}
	}
	// Сборка и тесты для Makefile-проекта определены — «не определена» здесь
	// была бы провалом автодетекта. А вот «запуск сервиса» без цели run и без
	// узнаваемого стека не определён честно, и это допустимо.
	for _, unresolved := range []string{"сборка: (не определена", "автотесты: (не определена", "анализ/линт: (не определена", "стиль: (не определена"} {
		if strings.Contains(prompt, unresolved) {
			t.Errorf("команда проверки не определилась (%q) для Makefile-проекта:\n%s", unresolved, prompt)
		}
	}
	if !strings.Contains(prompt, "НЕ запускай приложение через Run") {
		t.Errorf("промпт должен сохранять запрет дев-процессов:\n%s", prompt)
	}
}

// TestQAPromptWithoutProjectSaysTestsUnknown — обратный случай: пустой каталог
// (или каталог только с манифестами) не должен приводить к выдуманной команде
// и к молчаливому пропуску тестов — промпт требует указать это в отчёте.
func TestQAPromptWithoutProjectSaysTestsUnknown(t *testing.T) {
	q := newTestQA(t)
	prompt := q.GetSystemMessages(nil)[0].Message
	if !strings.Contains(prompt, "автоопределение не сработало") {
		t.Errorf("при неопределённой команде тестов промпт должен требовать указать это в отчёте:\n%s", prompt)
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

// TestQAPromptTestPortfolio — Ф-3: три уровня тестов (unit / интеграционные /
// E2E веба на Playwright), детерминированная синхронизация и degrade-правило
// для недоступного окружения. Каждый пункт здесь — то, что агент реально
// забывал: «зелёный» отчёт без запуска тестов проходил как успех.
func TestQAPromptTestPortfolio(t *testing.T) {
	q := newTestQA(t)
	p := q.GetSystemMessages(nil)[0].Message
	for _, want := range []string{
		"unit-тесты", "интеграционные", "Playwright", "playwright.config.ts",
		"health", "ПАДАЕТ на текущем коде", "ПРОХОДИТ после исправления",
		"degrade", "Отсутствие запуска — не", "Не мокай собственную логику",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт QA-инженера не содержит %q", want)
		}
	}
}
