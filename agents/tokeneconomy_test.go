package agents_test

import (
	"ai/agents"
	"ai/agents/chatassist"
	"ai/shared/projects"
	"ai/tools"
	"os"
	"strings"
	"testing"
)

// Ф-2: агент с инструментом Run получает фрагмент про компактный вывод команд
// (agents.RunTokenEconomy) в системном промпте. Агент без Run — не получает.

const runFragmentMarker = "КОМПАКТНЫЙ ВЫВОД КОМАНД"

// knownTools — инструменты, которые фрагменты могут называть. Фрагмент,
// упоминающий инструмент, проверяется против реального набора агента.
var knownTools = []string{
	"Run", "ReadFiles", "ReadMap", "List", "WriteFiles", "AppendFile", "DeleteFiles",
	"SearchReplace", "PatchGoFunction", "LspCheck", "ReadAppLogs",
	"LspDefinition", "LspReferences", "LspHover", "CodeSearch", "WebSearch",
}

// withRunAgent — агент, у которого есть инструмент Run.
type withRunAgent struct {
	name   string
	system string
	tools  map[string]bool
	hasLSP bool
}

func setOf(defs []tools.ToolDefinition) map[string]bool {
	m := map[string]bool{}
	for _, d := range defs {
		m[d.Name] = true
	}
	return m
}

func unifiedAgent() *withRunAgent {
	dir := os.TempDir()
	agent := chatassist.NewAssistantInDir(dir, "tokeneconomy-test", "задание", nil, nil)
	return &withRunAgent{
		name:   "единый-агент",
		system: agent.GetSystemMessages(nil)[0].Message,
		tools:  setOf(agent.GetTools()),
		hasLSP: true,
	}
}

func TestRunTokenEconomyFragmentInUnifiedAgent(t *testing.T) {
	a := unifiedAgent()
	defer os.RemoveAll(projects.ProjectDir("tokeneconomy-test"))

	if !a.tools["Run"] {
		t.Fatalf("в наборе нет Run — тест невалиден")
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
			t.Errorf("промпт не содержит %q", want)
		}
	}
	if !strings.Contains(a.system, agents.RunTokenEconomy) {
		t.Errorf("в промпт не вставлен общий фрагмент agents.RunTokenEconomy")
	}
}

// TestTokenEconomyFragmentMentionsOnlyAvailableTools — фрагмент не должен
// обещать агенту инструменты, которых у него нет.
func TestTokenEconomyFragmentMentionsOnlyAvailableTools(t *testing.T) {
	defer os.RemoveAll(projects.ProjectDir("tokeneconomy-test"))
	a := unifiedAgent()

	shared := []string{"LspDefinition", "LspReferences", "LspHover", "LspCheck", "CodeSearch", "ReadMap"}
	for _, name := range shared {
		if strings.Contains(agents.RunTokenEconomy, name) {
			t.Errorf("общий Run-фрагмент упоминает %q — им нельзя делиться с агентами без LSP", name)
		}
	}
	if !strings.Contains(agents.LSPGrepFallback, "grep -rnw --exclude-dir=") {
		t.Errorf("LSP-фолбэк должен содержать точечный grep с --exclude-dir:\n%s", agents.LSPGrepFallback)
	}

	fragments := map[string]string{"RunTokenEconomy": agents.RunTokenEconomy}
	if a.hasLSP {
		fragments["LSPGrepFallback"] = agents.LSPGrepFallback
		if !strings.Contains(a.system, agents.LSPGrepFallback) {
			t.Errorf("LSP-фолбэк не вставлен в промпт")
		}
	}
	for fname, frag := range fragments {
		for _, name := range knownTools {
			if strings.Contains(frag, name) && !a.tools[name] {
				t.Errorf("фрагмент %s упоминает инструмент %q, которого нет в наборе", fname, name)
			}
		}
	}
}
