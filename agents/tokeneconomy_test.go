package agents_test

import (
	"ai/agents"
	"ai/agents/architect"
	"ai/agents/backendlead"
	"ai/agents/chatassist"
	"ai/agents/developer"
	"ai/agents/devops"
	"ai/agents/devopslead"
	"ai/agents/frontendlead"
	"ai/agents/qaengineer"
	"ai/agents/qalead"
	"ai/projects"
	"ai/tools"
	"os"
	"strings"
	"testing"
)

// Ф-2 PLAN-2026-09-19-todo-bash.md: общий фрагмент промпта о компактном выводе
// команд (agents.RunTokenEconomy) попадает только в промпты агентов, у которых
// есть инструмент Run, и не упоминает инструментов, которых у агента нет.

const runFragmentMarker = "КОМПАКТНЫЙ ВЫВОД КОМАНД"

// knownTools — инструменты, которые фрагменты могут называть. Фрагмент,
// упоминающий инструмент, проверяется против реального набора агента.
var knownTools = []string{
	"Run", "ReadFiles", "ReadMap", "List", "WriteFiles", "AppendFile", "DeleteFiles",
	"SearchReplace", "PatchGoFunction", "LspCheck",
	"LspDefinition", "LspReferences", "LspHover", "CodeSearch",
}

// withRunAgent — агент, у которого есть инструмент Run.
type withRunAgent struct {
	name   string
	system string
	tools  map[string]bool
	hasLSP bool
}

func runAgents(t *testing.T) []withRunAgent {
	t.Helper()
	dir := t.TempDir()

	backend := developer.NewBackendDeveloper("tokeneconomy-dev-test", "задание")
	defer os.RemoveAll(projects.ProjectDir("tokeneconomy-dev-test"))
	qa, err := qaengineer.NewQAEngineerInDir("задание", dir)
	if err != nil {
		t.Fatalf("QA-инженер: %v", err)
	}
	dev, err := devops.NewDevopsInDir("задание", dir)
	if err != nil {
		t.Fatalf("DevOps-инженер: %v", err)
	}
	return []withRunAgent{
		{"backend-разработчик", backend.GetSystemMessages(nil)[0].Message, setOf(backend.GetTools()), true},
		{"QA-инженер", qa.GetSystemMessages(nil)[0].Message, setOf(qa.GetTools()), false},
		{"DevOps-инженер", dev.GetSystemMessages(nil)[0].Message, setOf(dev.GetTools()), false},
	}
}

func setOf(defs []tools.ToolDefinition) map[string]bool {
	m := map[string]bool{}
	for _, d := range defs {
		m[d.Name] = true
	}
	return m
}

func TestRunTokenEconomyFragmentInRunAgents(t *testing.T) {
	for _, a := range runAgents(t) {
		if !a.tools["Run"] {
			t.Fatalf("%s: в наборе нет Run — тест невалиден", a.name)
		}
		for _, want := range []string{
			runFragmentMarker,
			"pytest -q",
			"npm test -- --silent",
			"git log -n 3 --oneline",
			"git blame -L 10,20",
			"БЕЗ -v",
		} {
			if !strings.Contains(a.system, want) {
				t.Errorf("%s: промпт не содержит %q", a.name, want)
			}
		}
		if !strings.Contains(a.system, agents.RunTokenEconomy) {
			t.Errorf("%s: в промпт не вставлен общий фрагмент agents.RunTokenEconomy", a.name)
		}
	}
}

// TestTokenEconomyFragmentMentionsOnlyAvailableTools — фрагмент не должен
// обещать агенту инструменты, которых у него нет: общий Run-фрагмент не
// называет LSP/CodeSearch/ReadMap, а LSP-фолбэк добавляется только тем, у кого
// эти инструменты есть.
func TestTokenEconomyFragmentMentionsOnlyAvailableTools(t *testing.T) {
	shared := []string{"LspDefinition", "LspReferences", "LspHover", "LspCheck", "CodeSearch", "ReadMap"}
	for _, name := range shared {
		if strings.Contains(agents.RunTokenEconomy, name) {
			t.Errorf("общий Run-фрагмент упоминает %q — им нельзя делиться с агентами без LSP", name)
		}
	}
	if !strings.Contains(agents.LSPGrepFallback, "grep -rnw --exclude-dir=") {
		t.Errorf("LSP-фолбэк должен содержать точечный grep с --exclude-dir:\n%s", agents.LSPGrepFallback)
	}

	for _, a := range runAgents(t) {
		fragments := map[string]string{"RunTokenEconomy": agents.RunTokenEconomy}
		if a.hasLSP {
			fragments["LSPGrepFallback"] = agents.LSPGrepFallback
			if !strings.Contains(a.system, agents.LSPGrepFallback) {
				t.Errorf("%s: LSP-фолбэк не вставлен в промпт", a.name)
			}
		} else if strings.Contains(a.system, agents.LSPGrepFallback) {
			t.Errorf("%s: LSP-фолбэк вставлен агенту без LSP-инструментов", a.name)
		}
		for fname, frag := range fragments {
			for _, name := range knownTools {
				if strings.Contains(frag, name) && !a.tools[name] {
					t.Errorf("%s: фрагмент %s упоминает инструмент %q, которого нет в наборе", a.name, fname, name)
				}
			}
		}
	}
}

// TestRunFragmentAbsentWithoutRunTool — агенты без инструмента Run (лиды,
// архитектор, чат-ассистент) не получают фрагмент про команды: упоминать Run
// в их промпте бессмысленно и провоцирует вызов несуществующего инструмента.
func TestRunFragmentAbsentWithoutRunTool(t *testing.T) {
	dir := t.TempDir()

	be, err := backendlead.NewBackendLeadInDir("задание", dir)
	if err != nil {
		t.Fatalf("Backend Lead: %v", err)
	}
	fe, err := frontendlead.NewFrontendLeadInDir("задание", dir)
	if err != nil {
		t.Fatalf("Frontend Lead: %v", err)
	}
	qa, err := qalead.NewQALeadInDir("задание", dir)
	if err != nil {
		t.Fatalf("QA Lead: %v", err)
	}
	dl, err := devopslead.NewDevopsLeadInDir("задание", dir)
	if err != nil {
		t.Fatalf("DevOps Lead: %v", err)
	}
	arch := architect.NewArchitectWithStore("tokeneconomy-arch-test", "задание", nil)
	defer os.RemoveAll(projects.ProjectDir("tokeneconomy-arch-test"))
	assistant := chatassist.NewAssistantInDir(dir, "tokeneconomy-chat-test", "задание", nil, nil)
	defer os.RemoveAll(projects.ProjectDir("tokeneconomy-chat-test"))

	cases := []struct {
		name   string
		system string
		tools  map[string]bool
	}{
		{"Backend Lead", be.GetSystemMessages(nil)[0].Message, setOf(be.GetTools())},
		{"Frontend Lead", fe.GetSystemMessages(nil)[0].Message, setOf(fe.GetTools())},
		{"QA Lead", qa.GetSystemMessages(nil)[0].Message, setOf(qa.GetTools())},
		{"DevOps Lead", dl.GetSystemMessages(nil)[0].Message, setOf(dl.GetTools())},
		{"Архитектор", arch.GetSystemMessages(nil)[0].Message, setOf(arch.GetTools())},
		{"Чат-ассистент", assistant.GetSystemMessages(nil)[0].Message, setOf(assistant.GetTools())},
	}
	for _, c := range cases {
		if c.tools["Run"] {
			t.Fatalf("%s: в наборе появился Run — тест невалиден", c.name)
		}
		if strings.Contains(c.system, runFragmentMarker) || strings.Contains(c.system, agents.RunTokenEconomy) {
			t.Errorf("%s: фрагмент про Run попал в промпт агента без инструмента Run", c.name)
		}
	}
}
