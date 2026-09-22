package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai/board"
)

// TestSetEpicStatusPauseResumeCancel — REST-кнопки «Пауза»/«Продолжить»/
// «Отменить» эпика (Ф-6): статус эпика меняется, незавершённые задачи идут
// каскадом, а git-ветка и код НЕ трогаются (отката нет — ветка остаётся
// артефактом). Проверяется и то, что ни одной git-команды не выполняется.
func TestSetEpicStatusPauseResumeCancel(t *testing.T) {
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

	// Пауза: эпик «на паузе», его новая задача — тоже.
	if rec := post("epic-1", "paused"); rec.Code != http.StatusOK {
		t.Fatalf("пауза эпика: %d, body: %s", rec.Code, rec.Body.String())
	} else if got := epicStatus(rec); got != board.StatusPaused {
		t.Fatalf("статус эпика = %s, ожидалось paused", got)
	}
	if got := taskStatus("task-1"); got != board.StatusPaused {
		t.Fatalf("задача после паузы эпика = %s, ожидалось paused", got)
	}

	// Возобновление: эпик снова «готов к работе», задача вернулась в исходный
	// статус (new — она ещё не была в анализе).
	if rec := post("epic-1", "ready"); rec.Code != http.StatusOK {
		t.Fatalf("возобновление эпика: %d, body: %s", rec.Code, rec.Body.String())
	} else if got := epicStatus(rec); got != board.StatusReady {
		t.Fatalf("статус эпика = %s, ожидалось ready", got)
	}
	if got := taskStatus("task-1"); got != board.StatusNew {
		t.Fatalf("задача после возобновления = %s, ожидалось new", got)
	}

	// Отмена: эпик и его незавершённые задачи помечаются отменёнными, записи
	// остаются на доске (без удаления и без отката кода).
	if rec := post("epic-1", "cancelled"); rec.Code != http.StatusOK {
		t.Fatalf("отмена эпика: %d, body: %s", rec.Code, rec.Body.String())
	} else if got := epicStatus(rec); got != board.StatusCancelled {
		t.Fatalf("статус эпика = %s, ожидалось cancelled", got)
	}
	if got := taskStatus("task-1"); got != board.StatusCancelled {
		t.Fatalf("задача после отмены эпика = %s, ожидалось cancelled", got)
	}

	// Откатывать код нечего: пауза/возобновление/отмена не выполняют git-команд.
	if calls := git.callsList(); len(calls) != 0 {
		t.Fatalf("ожидалось ноль git-команд (без отката кода), получено: %v", calls)
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
	if code := post("epic-missing", "paused"); code != http.StatusNotFound {
		t.Fatalf("несуществующий эпик: %d, ожидалось 404", code)
	}
	// new -> done конечным автоматом не разрешён.
	if code := post("epic-1", "done"); code != http.StatusBadRequest {
		t.Fatalf("запрещённый переход: %d, ожидалось 400", code)
	}
	// Пауза допустима из любого активного статуса.
	if code := post("epic-1", "paused"); code != http.StatusOK {
		t.Fatalf("new -> paused: %d, ожидалось 200", code)
	}
}
