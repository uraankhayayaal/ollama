package refactor

import (
	"ai/agents/codegenerator"
	"os"
	"strings"
	"testing"
)

func newTestRefactor(t *testing.T) *RefactorAgent {
	t.Helper()
	cg, err := codegenerator.NewCodegeneratorInDir("тестовое задание", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &RefactorAgent{Codegenerator: cg}
}

func TestRequiredToolFirstRoundDisabled(t *testing.T) {
	ra := newTestRefactor(t)
	name, ok := ra.RequiredToolFirstRound()
	if ok {
		t.Errorf("рефактор не должен требовать инструмент в первом раунде, got %q", name)
	}
}

func TestGetSystemMessagesUsesRefactorPrompt(t *testing.T) {
	ra := newTestRefactor(t)
	msgs := ra.GetSystemMessages(nil)
	if len(msgs) != 1 {
		t.Fatalf("ожидали 1 системное сообщение, got %d", len(msgs))
	}
	text := msgs[0].Message
	for _, want := range []string{"рефакторинг", "изучи текущее состояние", "WriteFiles"} {
		if !strings.Contains(text, want) {
			t.Errorf("системный промпт не содержит %q", want)
		}
	}
}

func TestNewRefactorAgentRejectsMissingProject(t *testing.T) {
	name := "refactor-no-such-project-" + t.Name()
	if _, err := NewRefactorAgent("задание", name); err == nil {
		t.Fatal("ожидали ошибку для несуществующего проекта")
	}
}

func TestNewRefactorAgentWrapsExistingProject(t *testing.T) {
	name := "refactor-test-project"
	dir := codegenerator.ProjectDir(name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	ra, err := NewRefactorAgent("правки", name)
	if err != nil {
		t.Fatal(err)
	}
	if ra.Prompt != "правки" {
		t.Errorf("Prompt = %q, ожидали %q", ra.Prompt, "правки")
	}
	if ra.OutputDir != dir {
		t.Errorf("OutputDir = %q, ожидали %q", ra.OutputDir, dir)
	}
}
