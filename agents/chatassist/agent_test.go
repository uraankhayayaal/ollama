package chatassist

import (
	"ai/agents"
	"os"
	"path/filepath"
	"testing"
)

// newTestAssistant создаёт ассистента во временной директории без доски.
func newTestAssistant(t *testing.T, prompt string) *Assistant {
	t.Helper()
	return newAssistant(filepath.Join(t.TempDir(), "proj"), prompt, nil)
}

var _ agents.Agent = (*Assistant)(nil)

func TestAssistantInterface(t *testing.T) {
	a := newTestAssistant(t, "что сейчас делает проект?")
	if len(a.GetSystemMessages(nil)) == 0 {
		t.Fatal("системный промпт не задан")
	}
	um := a.GetUserMessages()
	if len(um) != 1 || um[0].Type != agents.MessageTypeHuman {
		t.Fatalf("ожидается один human-промпт с вопросом, получено %+v", um)
	}
	if msg, must := a.RequiredToolFirstRound(); must {
		t.Fatalf("ассистент не обязан вызывать инструмент, got %q", msg)
	}
}

func TestAssistantToolsAreReadOnly(t *testing.T) {
	a := newTestAssistant(t, "вопрос")
	names := map[string]bool{}
	for _, td := range a.GetTools() {
		names[td.Name] = true
	}
	want := []string{"List", "ReadFiles", "ReadMap"}
	for _, w := range want {
		if !names[w] {
			t.Errorf("ассистент не включает инструмент %q", w)
		}
	}
	// Пишущих и исполняющих инструментов в наборе быть не должно.
	for _, w := range []string{"WriteFiles", "AppendFile", "DeleteFiles", "Run", "SearchReplace"} {
		if names[w] {
			t.Errorf("read-only ассистент не должен включать инструмент %q", w)
		}
	}
	// Доска не подключена (store == nil): Board-инструменты в набор не входят.
	for _, w := range []string{"BoardListEpics", "BoardCreateEpic", "BoardSetTaskStatus"} {
		if names[w] {
			t.Errorf("ассистент без доски не должен включать Board-инструмент %q", w)
		}
	}
}

func TestAssistantCallFunctionRejectsWrite(t *testing.T) {
	a := newTestAssistant(t, "вопрос")
	if _, err := a.CallFunction("WriteFiles", nil); err == nil {
		t.Fatal("попытка вызвать записывающий инструмент должна завершиться ошибкой (not in tool set)")
	}
}

func TestAssistantWritesIntoOutputDir(t *testing.T) {
	// sanity: ассистент остаётся FileOps-агентом, способным читать проект.
	a := newTestAssistant(t, "покажи структуру")
	if err := os.WriteFile(filepath.Join(a.OutputDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := a.CallFunction("List", map[string]any{"path": "."})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("List не вернул содержимое проекта")
	}
}
