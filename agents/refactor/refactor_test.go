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

// Роль разработчика попадает в системный промпт: фронтендер работает только
// с клиентской частью, бэкендер — только с серверной. Без роли промпт нейтрален.
func TestSetRoleChangesSystemPrompt(t *testing.T) {
	cases := []struct {
		role  string
		want  string
		notIn string
	}{
		{"frontend", "ФРОНТЕНД-РАЗРАБОТЧИК", "БЭКЕНД-РАЗРАБОТЧИК"},
		{"backend", "БЭКЕНД-РАЗРАБОТЧИК", "ФРОНТЕНД-РАЗРАБОТЧИК"},
		{"server", "БЭКЕНД-РАЗРАБОТЧИК", "ФРОНТЕНД-РАЗРАБОТЧИК"},
		{"front", "ФРОНТЕНД-РАЗРАБОТЧИК", "БЭКЕНД-РАЗРАБОТЧИК"},
	}
	for _, tc := range cases {
		ra := newTestRefactor(t)
		ra.SetRole(tc.role)
		text := ra.GetSystemMessages(nil)[0].Message
		if !strings.Contains(text, tc.want) {
			t.Errorf("роль %q: промпт не содержит %q", tc.role, tc.want)
		}
		if strings.Contains(text, tc.notIn) {
			t.Errorf("роль %q: промпт не должен содержать %q", tc.role, tc.notIn)
		}
	}

	// Без роли — ни фронтендер, ни бэкендер.
	ra := newTestRefactor(t)
	text := ra.GetSystemMessages(nil)[0].Message
	if strings.Contains(text, "РАЗРАБОТЧИК") {
		t.Errorf("без роли промпт не должен выделять роль, got: %s", text)
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
