package server

import (
	"context"
	"net/http"
	"testing"

	"ai/infrastructure/board"
)

// driveTaskDone доводит задачу до done через свежее хранилище (без хуков
// сервера): FSM требует соседних переходов.
func driveTaskDone(t *testing.T, project, taskID string) {
	t.Helper()
	ctx := context.Background()
	store, err := board.NewStore(ctx, board.LoadConfig().StoreConfig(project))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer store.Close()
	for _, st := range []board.Status{board.StatusInfra, board.StatusTestCases, board.StatusDevelopment, board.StatusDone} {
		if err := store.SetTaskStatus(ctx, taskID, st); err != nil {
			t.Fatalf("task %s -> %s: %v", taskID, st, err)
		}
	}
}

// POST .../reopen без комментария отклоняется, задача остаётся выполненной.
func TestReopenTaskEndpointRequiresComment(t *testing.T) {
	h := newInjTestServer(t)
	if err := createInjTask(t, "mytrip", "BEL-07"); err != nil {
		t.Fatal(err)
	}
	driveTaskDone(t, "mytrip", "BEL-07")

	rec := doJSON(t, h, http.MethodPost, "/api/projects/mytrip/tasks/BEL-07/reopen", map[string]any{}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reopen без комментария = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if task := taskFromBoard(t, h, "mytrip", "BEL-07"); task.Status != board.StatusDone {
		t.Fatalf("задача = %s, ожидалась done (переоткрытие без комментария запрещено)", task.Status)
	}
}

// Комментарий, добавленный к задаче, переоткрывает её и помечается учтённым.
func TestReopenTaskEndpointWithExistingComment(t *testing.T) {
	h := newInjTestServer(t)
	if err := createInjTask(t, "mytrip", "BEL-08"); err != nil {
		t.Fatal(err)
	}
	driveTaskDone(t, "mytrip", "BEL-08")

	rec := doJSON(t, h, http.MethodPut, "/api/projects/mytrip/tasks/BEL-08", map[string]any{
		"comments": []map[string]any{
			{"author": "user", "type": "user", "body": "доработать кнопку"},
		},
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT комментария = %d: %s", rec.Code, rec.Body.String())
	}

	var reopened board.Task
	rec = doJSON(t, h, http.MethodPost, "/api/projects/mytrip/tasks/BEL-08/reopen", map[string]any{}, &reopened)
	if rec.Code != http.StatusOK {
		t.Fatalf("reopen = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if reopened.Status != board.StatusInfra {
		t.Fatalf("статус = %s, ожидался infra", reopened.Status)
	}
	found := false
	for _, c := range reopened.Comments {
		if c.Body == "доработать кнопку" && c.Accounted {
			found = true
		}
	}
	if !found {
		t.Fatalf("комментарий не помечен учтённым: %+v", reopened.Comments)
	}
}

// Тело запроса может само нести комментарий-обоснование.
func TestReopenTaskEndpointWithBody(t *testing.T) {
	h := newInjTestServer(t)
	if err := createInjTask(t, "mytrip", "BEL-09"); err != nil {
		t.Fatal(err)
	}
	driveTaskDone(t, "mytrip", "BEL-09")

	var reopened board.Task
	rec := doJSON(t, h, http.MethodPost, "/api/projects/mytrip/tasks/BEL-09/reopen", map[string]any{
		"body": "переделать форму",
	}, &reopened)
	if rec.Code != http.StatusOK {
		t.Fatalf("reopen = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if reopened.Status != board.StatusInfra {
		t.Fatalf("статус = %s, ожидался infra", reopened.Status)
	}
	if len(reopened.Comments) != 1 || !reopened.Comments[0].Accounted {
		t.Fatalf("комментарий из тела не сохранён/не учтён: %+v", reopened.Comments)
	}
}

// Переоткрыть можно только выполненную задачу; несуществующая — 404.
func TestReopenTaskEndpointErrors(t *testing.T) {
	h := newInjTestServer(t)
	if err := createInjTask(t, "mytrip", "BEL-10"); err != nil {
		t.Fatal(err)
	}
	// Задача ещё не выполнена.
	rec := doJSON(t, h, http.MethodPost, "/api/projects/mytrip/tasks/BEL-10/reopen", map[string]any{
		"body": "доработать",
	}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reopen невыполненной задачи = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodPost, "/api/projects/mytrip/tasks/НЕТ/reopen", map[string]any{
		"body": "доработать",
	}, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("reopen несуществующей задачи = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}
