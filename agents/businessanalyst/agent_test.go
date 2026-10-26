package businessanalyst

import (
	"ai/infrastructure/board"
	"ai/tools"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

func newTestBA(t *testing.T) *BusinessAnalyst {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "out")
	return newBusinessAnalyst(dir, "Уточни требования")
}

func newTestBAWithBoard(t *testing.T) *BusinessAnalyst {
	t.Helper()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "testproj"})
	ba := newTestBA(t)
	ba.SetBoardStore(board.NewDomainStore(store))
	return ba
}

func TestResolvePathRejectsTraversal(t *testing.T) {
	ba := newTestBA(t)
	for _, bad := range []string{"../evil.md", "a/../../etc/passwd", "/etc/passwd"} {
		if _, err := ba.ResolvePath(bad); err == nil {
			t.Errorf("ResolvePath(%q) должен вернуть ошибку", bad)
		}
	}
}

func TestSystemPromptContains(t *testing.T) {
	ba := newTestBA(t)
	p := ba.GetSystemMessages(nil)[0].Message
	for _, want := range []string{
		"бизнес-аналитик",
		"business_review",
		"system_analysis",
		"BoardGetEpic",
		"BoardUpdateEpic",
		"BoardSetEpicStatus",
		"User Stories",
		"Acceptance Criteria",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт не содержит %q", want)
		}
	}
}

func TestSystemPromptNoCodeGeneration(t *testing.T) {
	ba := newTestBA(t)
	p := ba.GetSystemMessages(nil)[0].Message
	if !strings.Contains(p, "Не пиши код") {
		t.Error("промпт должен запрещать написание кода")
	}
}

func TestSystemPromptNoTaskCreation(t *testing.T) {
	ba := newTestBA(t)
	p := ba.GetSystemMessages(nil)[0].Message
	if !strings.Contains(p, "не создавай задачи") {
		t.Error("промпт должен запрещать создание задач")
	}
}

func TestUserMessages(t *testing.T) {
	ba := newTestBA(t)
	msgs := ba.GetUserMessages()
	if len(msgs) != 1 {
		t.Fatalf("GetUserMessages = %d, ожидалось 1", len(msgs))
	}
	if msgs[0].Type != "human" {
		t.Errorf("тип сообщения = %s, ожидалось human", msgs[0].Type)
	}
	if msgs[0].Message != "Уточни требования" {
		t.Errorf("неверный текст сообщения: %q", msgs[0].Message)
	}
}

func TestRequiredToolFirstRound(t *testing.T) {
	ba := newTestBA(t)
	name, ok := ba.RequiredToolFirstRound()
	if ok {
		t.Errorf("RequiredToolFirstRound = %q, ожидалось пустой", name)
	}
}

func TestBoardToolsIncluded(t *testing.T) {
	ba := newTestBAWithBoard(t)
	got := ba.GetTools()
	names := map[string]bool{}
	for _, td := range got {
		names[td.Name] = true
	}
	for _, want := range []string{
		tools.BoardGetEpic, tools.BoardUpdateEpic, tools.BoardSetEpicStatus,
		"ReadFiles", "List",
	} {
		if !names[want] {
			t.Errorf("инструмент %q отсутствует в GetTools", want)
		}
	}
}

func TestLabel(t *testing.T) {
	ba := newTestBA(t)
	if got := ba.Label(); got != "Бизнес-аналитик" {
		t.Errorf("Label = %q, ожидалось %q", got, "Бизнес-аналитик")
	}
}

func TestCallFunctionRejectsUnknownTool(t *testing.T) {
	ba := newTestBA(t)
	_, err := ba.CallFunction("nonexistent_tool", nil)
	if err == nil {
		t.Fatal("CallFunction(nonexistent_tool) должен вернуть ошибку")
	}
	if !strings.Contains(err.Error(), "nonexistent_tool") {
		t.Errorf("ошибка должна содержать имя инструмента: %v", err)
	}
}
