package chatassist

import (
	"testing"
	"time"

	"ai/chat"
)

func TestDialogueHistoryEmpty(t *testing.T) {
	if got := DialogueHistory(nil); got != "" {
		t.Fatalf("nil: %q", got)
	}
	if got := DialogueHistory([]chat.Message{}); got != "" {
		t.Fatalf("empty: %q", got)
	}
}

func TestDialogueHistorySkipsLastUser(t *testing.T) {
	hist := []chat.Message{
		{Role: chat.RoleUser, Content: "первый вопрос"},
		{Role: chat.RoleAssistant, Content: "первый ответ"},
		{Role: chat.RoleUser, Content: "второй вопрос"},
	}
	got := DialogueHistory(hist)
	if got == "" {
		t.Fatal("пустая история")
	}
	if contains(got, "второй вопрос") {
		t.Fatalf("последняя реплика пользователя должна быть пропущена: %q", got)
	}
	if !contains(got, "первый вопрос") || !contains(got, "первый ответ") {
		t.Fatalf("нет ранних реплик: %q", got)
	}
}

func TestDialogueHistoryChronologicalOrder(t *testing.T) {
	hist := []chat.Message{
		{Role: chat.RoleUser, Content: "вопрос 1"},
		{Role: chat.RoleAssistant, Content: "ответ 1"},
		{Role: chat.RoleUser, Content: "вопрос 2"},
		{Role: chat.RoleAssistant, Content: "ответ 2"},
	}
	got := DialogueHistory(hist)
	want := "[пользователь] вопрос 1\n[ассистент] ответ 1\n[пользователь] вопрос 2\n[ассистент] ответ 2"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDialogueHistoryMaxTurns(t *testing.T) {
	var hist []chat.Message
	for i := 0; i < DialogueMaxTurns+10; i++ {
		hist = append(hist, chat.Message{Role: chat.RoleUser, Content: "вопрос"})
		hist = append(hist, chat.Message{Role: chat.RoleAssistant, Content: "ответ"})
	}
	got := DialogueHistory(hist)
	count := 0
	for _, line := range splitLines(got) {
		if contains(line, "пользователь") || contains(line, "ассистент") {
			count++
		}
	}
	if count > DialogueMaxTurns {
		t.Fatalf("строк = %d, больше лимита %d", count, DialogueMaxTurns)
	}
}

func TestDialogueHistorySkipsNonDialogueRoles(t *testing.T) {
	hist := []chat.Message{
		{Role: chat.RoleUser, Content: "вопрос"},
		{Role: chat.RoleStatus, Content: "агент начал работу"},
		{Role: chat.RoleSystem, Content: "сессия создана"},
		{Role: chat.RoleAssistant, Content: "ответ"},
	}
	got := DialogueHistory(hist)
	if contains(got, "агент начал") || contains(got, "сессия создана") {
		t.Fatalf("служебные роли не должны попадать в историю: %q", got)
	}
	if !contains(got, "вопрос") || !contains(got, "ответ") {
		t.Fatalf("нет диалоговых реплик: %q", got)
	}
}

func TestDialogueHistoryTrimsWhitespace(t *testing.T) {
	hist := []chat.Message{
		{Role: chat.RoleUser, Content: "  "},
		{Role: chat.RoleAssistant, Content: "\nответ\n"},
	}
	got := DialogueHistory(hist)
	if contains(got, "  ") && !contains(got, "ответ") {
		t.Fatalf("пустые реплики должны быть пропущены: %q", got)
	}
	if got != "[ассистент] ответ" {
		t.Fatalf("got %q", got)
	}
}

func TestDialogueHistoryToolMessages(t *testing.T) {
	hist := []chat.Message{
		{Role: chat.RoleUser, Content: "создай эпик"},
		{Role: chat.RoleTool, Tool: "BoardCreateEpic", Content: "эпик создан"},
	}
	got := DialogueHistory(hist)
	if !contains(got, "BoardCreateEpic →] эпик создан") {
		t.Fatalf("нет инструментальной реплики: %q", got)
	}
}

func TestOneLine(t *testing.T) {
	got := OneLine("строка1\nстрока2\nстрока3", 100)
	if got != "строка1 строка2 строка3" {
		t.Fatalf("got %q", got)
	}
	long := OneLine("оченьдлиннаястрока", 5)
	if len(long) >= len("оченьдлиннаястрока") {
		t.Fatalf("лимит символов нарушен: %q", long)
	}
}

func TestDialogueHistoryAskRole(t *testing.T) {
	hist := []chat.Message{
		{Role: chat.RoleUser, Content: "вопрос?"},
		{Role: chat.RoleAsk, Ask: &chat.Ask{Questions: []chat.AskQuestion{
			{ID: "q1", Text: "Какой вариант?", Kind: chat.AskSingle, Options: []chat.AskOption{
				{ID: "a", Label: "A", Recommended: true},
				{ID: "b", Label: "B"},
			}},
		}}},
	}
	got := DialogueHistory(hist)
	if !contains(got, "[ассистент]") {
		t.Fatalf("нет ask-реплики: %q", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

var _ = time.Now
