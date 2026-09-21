package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai/board"
)

// TestHandleDeleteTaskRest — Ф-3/Ф-4: удаление задачи с доски по REST
// (DELETE /api/projects/{id}/tasks/{tid}) для обычного (не-git) проекта.
// Задача должна исчезнуть с доски, повторное удаление — 404, как и удаление
// несуществующей задачи.
func TestHandleDeleteTaskRest(t *testing.T) {
	srv, handler, mr := newTestServer(t)
	ctx := context.Background()

	project := "proj-del"
	registerTestDir(t, srv, project)

	store, err := board.NewStore(ctx, board.StoreConfig{Addr: mr.Addr(), Project: project})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Релиз"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "task-1", Title: "Фича"},
		EpicID:   "epic-1",
	}); err != nil {
		t.Fatal(err)
	}

	del := httptest.NewRecorder()
	handler.ServeHTTP(del, httptest.NewRequest(
		"DELETE", "/api/projects/"+project+"/tasks/task-1", nil))
	if del.Code != http.StatusOK {
		t.Fatalf("DELETE task: %d, body: %s", del.Code, del.Body.String())
	}

	if _, err := store.GetTask(ctx, "task-1"); err == nil {
		t.Fatalf("задача не удалена с доски")
	}

	// Повторное удаление — 404.
	del = httptest.NewRecorder()
	handler.ServeHTTP(del, httptest.NewRequest(
		"DELETE", "/api/projects/"+project+"/tasks/task-1", nil))
	if del.Code != http.StatusNotFound {
		t.Fatalf("повторный DELETE: %d, want 404 (body: %s)", del.Code, del.Body.String())
	}

	// Несуществующая задача — 404.
	del = httptest.NewRecorder()
	handler.ServeHTTP(del, httptest.NewRequest(
		"DELETE", "/api/projects/"+project+"/tasks/nope", nil))
	if del.Code != http.StatusNotFound {
		t.Fatalf("неизвестная задача: %d, want 404 (body: %s)", del.Code, del.Body.String())
	}
}

// TestHandleDeleteTaskGitRemovesBranch — Ф-3/Ф-1: для git-проекта удаление
// задачи снимает из side-реестра ветку задачи (removeTaskBranch), а с
// доски задача исчезает. Ветка из реестра git-брашей удаляется.
func TestHandleDeleteTaskGitRemovesBranch(t *testing.T) {
	git := &fakeGit{}
	srv, handler, mr := setupGitflow(t, git)
	ctx := context.Background()

	seedGitflowBoard(t, mr, srv)
	// У задачи task-1 зарегистрирована ветка (см. seedGitflowBoard).

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"DELETE", "/api/projects/myrepo/tasks/task-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE git-task: %d, body: %s", rec.Code, rec.Body.String())
	}

	// Задача снята с доски.
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	if _, err := store.GetTask(ctx, "task-1"); err == nil {
		t.Fatalf("git-задача не удалена с доски")
	}

	// Ветка задачи снята из side-реестра (Ф-3/Ф-1).
	if _, err := srv.reg.TaskBranch("myrepo", "task-1"); err == nil {
		t.Fatalf("ветка задачи не снята из side-реестра")
	}
}
