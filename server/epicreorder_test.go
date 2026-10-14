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

// TestReorderEpicsEndpoint — REST-пересортировка эпиков (Web UI: перетаскивание
// строк/стрелки): порядок из тела применяется как приоритет (первый ID —
// наибольший), ответ несёт {updated, restarting}. Оркестрация в тесте не идёт,
// поэтому restarting=false и Session не трогается.
func TestReorderEpicsEndpoint(t *testing.T) {
	git := &fakeGit{}
	srv, handler, mr := newTestServerGit(t, git, nil)
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")

	ctx := context.Background()
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	for _, id := range []string{"EP-1", "EP-2", "EP-3"} {
		if err := store.CreateEpic(ctx, &board.Epic{
			TaskSpec: board.TaskSpec{TaskID: id, Title: id},
		}); err != nil {
			t.Fatalf("CreateEpic(%s): %v", id, err)
		}
	}

	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/projects/myrepo/epics/reorder",
			strings.NewReader(body))
		handler.ServeHTTP(rec, req)
		return rec
	}

	rec := post(`{"order": ["EP-3", "EP-1", "EP-2"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("пересортировка: %d, body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK         bool `json:"ok"`
		Updated    int  `json:"updated"`
		Restarting bool `json:"restarting"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("разбор ответа: %v (body: %s)", err, rec.Body.String())
	}
	if !resp.OK || resp.Updated != 3 || resp.Restarting {
		t.Errorf("ответ = %+v, want {ok, updated:3, restarting:false}", resp)
	}
	prios := map[string]int{}
	for _, id := range []string{"EP-1", "EP-2", "EP-3"} {
		e, err := store.GetEpic(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		prios[id] = e.Priority
	}
	if prios["EP-3"] != 3 || prios["EP-1"] != 2 || prios["EP-2"] != 1 {
		t.Errorf("приоритеты = %v, want EP-3:3 EP-1:2 EP-2:1", prios)
	}

	// Ошибки: пустой/битый JSON (400), неизвестный эпик (404).
	if rec := post(`{"order": []}`); rec.Code != http.StatusBadRequest {
		t.Errorf("пустой order: %d, ожидалось 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if rec := post(`{"order": "EP-1"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("order не массив: %d, ожидалось 400", rec.Code)
	}
	if rec := post("not-json"); rec.Code != http.StatusBadRequest {
		t.Errorf("битый JSON: %d, ожидалось 400", rec.Code)
	}
	if rec := post(`{"order": ["EP-1", "NO-SUCH"]}`); rec.Code != http.StatusNotFound {
		t.Errorf("неизвестный эпик: %d, ожидалось 404 (body: %s)", rec.Code, rec.Body.String())
	}
}
