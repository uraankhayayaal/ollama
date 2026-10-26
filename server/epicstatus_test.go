package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai/infrastructure/board"
)

// TestSetEpicStatusCancel — REST-кнопка «Отменить» эпика: статус эпика меняется,
// незавершённые задачи идут каскадом, а git-ветка и код НЕ трогаются.
func TestSetEpicStatusCancel(t *testing.T) {
	git := &fakeGit{}
	srv, handler, mr := newTestServerGit(t, git, nil)
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")

	ctx := context.Background()
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Релиз"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "task-1", SequenceOrder: 1}, EpicID: "epic-1",
	}); err != nil {
		t.Fatal(err)
	}

	post := func(epicID, status string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/projects/myrepo/epics/"+epicID+"/status",
			strings.NewReader(`{"status":"`+status+`"}`))
		handler.ServeHTTP(rec, req)
		return rec
	}
	epicStatus := func(rec *httptest.ResponseRecorder) board.Status {
		t.Helper()
		var e board.Epic
		if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
			t.Fatalf("разбор ответа эпика: %v (body: %s)", err, rec.Body.String())
		}
		return e.Status
	}
	taskStatus := func(id string) board.Status {
		t.Helper()
		task, err := store.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return task.Status
	}

	// Отмена: эпик и его незавершённые задачи помечаются отменёнными.
	if rec := post("epic-1", "cancelled"); rec.Code != http.StatusOK {
		t.Fatalf("отмена эпика: %d, body: %s", rec.Code, rec.Body.String())
	} else if got := epicStatus(rec); got != board.StatusCancelled {
		t.Fatalf("статус эпика = %s, ожидалось cancelled", got)
	}
	if got := taskStatus("task-1"); got != board.StatusCancelled {
		t.Fatalf("задача после отмены эпика = %s, ожидалось cancelled", got)
	}

	// Git-команд не выполнялось.
	if calls := git.callsList(); len(calls) != 0 {
		t.Fatalf("ожидалось ноль git-команд, получено: %v", calls)
	}
}

// TestSetEpicStatusErrors — ошибки REST-перевода статуса эпика: неизвестный
// статус (400), несуществующий эпик (404), запрещённый переход (400).
func TestSetEpicStatusErrors(t *testing.T) {
	git := &fakeGit{}
	srv, handler, mr := newTestServerGit(t, git, nil)
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")

	ctx := context.Background()
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1"},
	}); err != nil {
		t.Fatal(err)
	}

	post := func(epicID, status string) int {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/projects/myrepo/epics/"+epicID+"/status",
			strings.NewReader(`{"status":"`+status+`"}`))
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK && rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
			t.Fatalf("неожиданный код %d, body: %s", rec.Code, rec.Body.String())
		}
		return rec.Code
	}

	if code := post("epic-1", "bogus"); code != http.StatusBadRequest {
		t.Fatalf("неизвестный статус: %d, ожидалось 400", code)
	}
	if code := post("epic-missing", "cancelled"); code != http.StatusNotFound {
		t.Fatalf("несуществующий эпик: %d, ожидалось 404", code)
	}
	// draft -> done конечным автоматом не разрешён.
	if code := post("epic-1", "done"); code != http.StatusBadRequest {
		t.Fatalf("запрещённый переход: %d, ожидалось 400", code)
	}
	// draft -> business_review — разрешён.
	if code := post("epic-1", "business_review"); code != http.StatusOK {
		t.Fatalf("draft -> business_review: %d, ожидалось 200", code)
	}
}
