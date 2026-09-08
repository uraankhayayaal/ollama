package architect

import (
	"ai/board"
	"ai/tools"
	"context"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// newTestArchitect создаёт архитектора с хранилищем доски поверх in-memory
// Redis (miniredis). Проект размещается во временной директории, чтобы не
// засорять модульный temp/.
func newTestArchitect(t *testing.T) *Architect {
	t.Helper()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "testproj"})
	a := NewArchitectWithStore("testproj", "Задача пользователя", store)
	a.OutputDir = t.TempDir()
	return a
}

func TestRequiredToolFirstRound(t *testing.T) {
	a := newTestArchitect(t)
	name, ok := a.RequiredToolFirstRound()
	if !ok || name != SubmitBacklogToolName {
		t.Fatalf("RequiredToolFirstRound = %q, %v", name, ok)
	}
}

func TestToolsIncludeSubmitBacklog(t *testing.T) {
	a := newTestArchitect(t)
	got := a.GetTools()
	names := map[string]bool{}
	for _, td := range got {
		names[td.Name] = true
	}
	for _, want := range []string{"List", "ReadFiles", SubmitBacklogToolName} {
		if !names[want] {
			t.Errorf("агент не включает инструмент %q", want)
		}
	}
	for _, disallowed := range []string{"WriteFiles", "AppendFile", "DeleteFiles", "Run"} {
		if names[disallowed] {
			t.Errorf("архитектор не должен включать инструмент %q", disallowed)
		}
	}
	// Определение submit_architecture_backlog объявляет задачи в схеме.
	if td := defByName(got, SubmitBacklogToolName); td == nil {
		t.Fatal("не найдено определение submit_architecture_backlog")
	} else if _, ok := td.Parameters["properties"].(map[string]any)["tasks"]; !ok {
		t.Error("схема submit_architecture_backlog не содержит поле tasks")
	}
}

func defByName(defs []tools.ToolDefinition, name string) *tools.ToolDefinition {
	for i := range defs {
		if defs[i].Name == name {
			return &defs[i]
		}
	}
	return nil
}

func TestSubmitBacklogPersistsEpics(t *testing.T) {
	a := newTestArchitect(t)

	args := map[string]any{
		"architecture_summary": "Общее архитектурное решение",
		"tasks": []map[string]any{
			{
				"task_id":          "ARCH-01",
				"title":            "Backend модуль",
				"description":      "Описание",
				"assigned_role":    "Backend Lead",
				"sequence_order":   1,
				"can_run_parallel": true,
				"dependencies":     []string{},
			},
			{
				"task_id":          "ARCH-02",
				"title":            "Инфраструктура",
				"description":      "Описание",
				"assigned_role":    "DevOps Lead",
				"sequence_order":   2,
				"can_run_parallel": true,
				"dependencies":     []string{"ARCH-01"},
			},
		},
	}

	out, err := a.CallFunction(SubmitBacklogToolName, args)
	if err != nil {
		t.Fatalf("CallFunction: %v", err)
	}
	if !strings.Contains(string(out), `"status":"success"`) && !strings.Contains(string(out), `"status": "success"`) {
		t.Fatalf("ожидался успешный статус, получено: %s", out)
	}

	ctx := context.Background()
	epics, err := a.Store.ListEpics(ctx)
	if err != nil {
		t.Fatalf("ListEpics: %v", err)
	}
	if len(epics) != 2 {
		t.Fatalf("на доске %d эпиков, ожидалось 2", len(epics))
	}
	// Сводка архитектуры сохранилась в каждом эпике.
	for _, e := range epics {
		if e.Summary != "Общее архитектурное решение" {
			t.Errorf("эпик %s: architecture_summary = %q", e.TaskID, e.Summary)
		}
		if e.ProjectName != "testproj" || e.Status != board.StatusNew {
			t.Errorf("эпик %s: project=%s status=%s", e.TaskID, e.ProjectName, e.Status)
		}
	}

	// Повторный вызов идемпотентен: дубликаты пропускаются, ошибки нет.
	if _, err := a.CallFunction(SubmitBacklogToolName, args); err != nil {
		t.Fatalf("повторный submitBacklog: %v", err)
	}
	epics, _ = a.Store.ListEpics(ctx)
	if len(epics) != 2 {
		t.Fatalf("после повторного вызова эпиков %d, ожидалось 2", len(epics))
	}
}

func TestSubmitBacklogRejectsBadSchema(t *testing.T) {
	a := newTestArchitect(t)

	// Задача без task_id — CreateEpic вернёт ошибку валидации (JSON-ответ,
	// чтобы модель могла исправиться).
	out, err := a.CallFunction(SubmitBacklogToolName, map[string]any{
		"architecture_summary": "x",
		"tasks": []map[string]any{
			{"title": "Без ID", "assigned_role": "Backend Lead"},
		},
	})
	if err != nil {
		t.Fatalf("ожидался JSON-результат с ошибкой, получили ошибку: %v", err)
	}
	if !strings.Contains(string(out), `"error"`) && !strings.Contains(string(out), "error") {
		t.Fatalf("ожидался статус error, получено: %s", out)
	}

	// Пустой tasks — ошибка формата.
	if _, err := a.CallFunction(SubmitBacklogToolName, map[string]any{
		"architecture_summary": "x",
		"tasks":                []any{},
	}); err != nil {
		t.Fatalf("ожидался JSON-результат с ошибкой, получили ошибку: %v", err)
	}

	// Неизвестный инструмент — ошибка на стороне агента.
	if _, err := a.CallFunction("WriteFiles", map[string]any{
		"files": []map[string]string{{"filename": "x", "content": "y"}},
	}); err == nil {
		t.Fatal("архитектор не должен уметь вызывать WriteFiles")
	}
}
