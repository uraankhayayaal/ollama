package runner

import (
	"ai/tools"
	"strings"
	"testing"
)

// msg — короткая помощь для построения сообщений истории.
func msg(role, content string) Message {
	return Message{Role: role, Content: content}
}

// Сжатие в пределах бюджета — история возвращается как есть.
func TestCompressHistoryWithinBudget(t *testing.T) {
	msgs := []Message{
		msg("system", "ты агент"),
		msg("user", "задача"),
		msg("assistant", "делаю"),
		msg("tool", "ок"),
	}
	if got := CompressHistory(msgs, 1<<20); len(got) != len(msgs) {
		t.Fatalf("в пределах бюджета история не должна меняться, got %d (want %d)", len(got), len(msgs))
	}
}

// Неактивное сжатие (budget<=0) — история без изменений.
func TestCompressHistoryDisabledBudget(t *testing.T) {
	msgs := []Message{msg("user", strings.Repeat("x", 100000))}
	if got := CompressHistory(msgs, 0); len(got) != len(msgs) {
		t.Fatal("budget=0 должен отключать сжатие")
	}
}

// Длинная история сжимается: система и задача остаются, актуальный хвост —
// сохраняется, а старейшие сообщения середины отбрасываются.
func TestCompressHistoryKeepsHeadAndTail(t *testing.T) {
	var msgs []Message
	msgs = append(msgs,
		msg("system", "ты агент"),
		msg("system", "правила"),
		msg("user", "постановка задачи"),
	)
	task := strings.Repeat("В", 2500)
	for i := 0; i < 8; i++ {
		msgs = append(msgs,
			msg("assistant", "шаг"),
			Message{Role: "tool", ToolName: "ReadFiles", ToolCallID: "c1", Content: task},
		)
	}
	msgs = append(msgs, msg("assistant", "готово"))

	comp := CompressHistory(msgs, 3000)

	// Голова сохранена целиком.
	if comp[0].Content != "ты агент" || comp[1].Content != "правила" || comp[2].Content != "постановка задачи" {
		t.Fatalf("голова (система+задача) должна сохраниться первой, got %v", comp[:3])
	}
	// Хвост актуален: последний ответ модели присутствует.
	if comp[len(comp)-1].Content != "готово" {
		t.Fatalf("последний ответ модели должен сохраниться, got %q", comp[len(comp)-1].Content)
	}
	if len(comp) >= len(msgs) {
		t.Fatalf("история должна сжаться: %d -> %d (ожидали меньше)", len(msgs), len(comp))
	}
	// Сжатая история укладывается в бюджет.
	if estimateLen(comp) > 3000*2 {
		t.Fatalf("сжатая история выходит за бюджет: ~%d", estimateLen(comp))
	}
}

// Хвост не должен начинаться с tool-сообщения: результат отброшенного вызова
// инструмента не остаётся без своего assistant(toolcalls).
func TestCompressHistoryTailDoesNotOpenWithTool(t *testing.T) {
	sys := []Message{msg("system", "ты агент"), msg("user", "задача")}
	task := strings.Repeat("Т", 2000)
	var msgs []Message
	msgs = append(msgs, sys...)
	msgs = append(msgs,
		Message{Role: "assistant", ToolCalls: []tools.ToolCall{{ID: "a", Name: "List", Arguments: "{}"}}},
		Message{Role: "tool", ToolName: "List", ToolCallID: "a", Content: task},
		Message{Role: "assistant", ToolCalls: []tools.ToolCall{{ID: "b", Name: "List", Arguments: "{}"}}},
		Message{Role: "tool", ToolName: "List", ToolCallID: "b", Content: task},
		Message{Role: "assistant", ToolCalls: []tools.ToolCall{{ID: "c", Name: "List", Arguments: "{}"}}},
		Message{Role: "tool", ToolName: "List", ToolCallID: "c", Content: task},
		msg("assistant", "готово"),
	)

	comp := CompressHistory(msgs, 2500)
	if len(comp) >= len(msgs) {
		t.Fatalf("ожидали сжатие, got %d == %d", len(comp), len(msgs))
	}
	for i, m := range comp {
		if i > 0 && m.Role == "tool" && comp[i-1].Role != "assistant" {
			t.Fatalf("tool-сообщение на %d позиции без предыдущего assistant", i)
		}
	}
	// Каждый сохранённый tool имеет свой assistant-вызов рядом.
	if comp[len(comp)-1].Content != "готово" {
		t.Fatalf("хвост должен заканчиваться ответом модели, got %q", comp[len(comp)-1].Content)
	}
}

// Точность: первый вызов инструментов — не в голове и не в хвосте — исчезает,
// голова и хвост сохраняются (ассертации целостности — в других тестах).
func TestCompressHistoryDropsOldestPair(t *testing.T) {
	task := strings.Repeat("Д", 1000)
	msgs := []Message{
		msg("system", "sys"),
		msg("user", "задача"),
		Message{Role: "assistant", ToolCalls: []tools.ToolCall{{ID: "1", Name: "List"}}},
		Message{Role: "tool", ToolName: "List", ToolCallID: "1", Content: task},
		Message{Role: "assistant", ToolCalls: []tools.ToolCall{{ID: "2", Name: "List"}}},
		Message{Role: "tool", ToolName: "List", ToolCallID: "2", Content: task},
		msg("user", "продолжай"),
		msg("assistant", "итог"),
	}
	head := 2
	comp := CompressHistory(msgs, 1200)

	// Первая пара (вызов) наверняка отброшена.
	for _, m := range comp {
		if m.Role == "tool" && m.ToolCallID == "1" {
			t.Fatal("старейшая пара assistant+tool должна быть выброшена")
		}
	}
	// Голова на месте.
	if comp[0].Content != "sys" || comp[head-1].Content != "задача" {
		t.Fatalf("голова сохранена, got %v", comp)
	}
}