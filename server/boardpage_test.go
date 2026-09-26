package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"ai/board"
)

// seedBoardPage заполняет доску miniredis'а эпиками/задачами/багами.
func seedBoardPage(t *testing.T, mr *miniredis.Miniredis, project string) *board.Store {
	t.Helper()
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: project})
	ctx := context.Background()

	for _, id := range []string{"epic-a", "epic-b", "epic-c"} {
		if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{TaskID: id, Title: "Эпик " + id}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 5; i++ {
		id := "task-" + strconv.Itoa(i)
		if err := store.CreateTask(ctx, &board.Task{TaskSpec: board.TaskSpec{TaskID: id, Title: id}, EpicID: "epic-a"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"bug-a", "bug-b"} {
		if err := store.CreateBugReport(ctx, &board.BugReport{BugID: id, EpicID: "epic-a", Title: id}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestBoardViewPageSlice(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	store := seedBoardPage(t, mr, "page-proj")
	ctx := context.Background()

	// Полный снимок (limit=0) — всё на месте + total.
	full, err := boardViewPage(ctx, store, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Epics) != 3 || len(full.Tasks) != 5 || len(full.Bugs) != 2 {
		t.Fatalf("полный снимок: epics=%d tasks=%d bugs=%d", len(full.Epics), len(full.Tasks), len(full.Bugs))
	}
	if full.Total == nil || full.Total.Epics != 3 || full.Total.Tasks != 5 || full.Total.Bugs != 2 {
		t.Fatalf("total = %+v", full.Total)
	}

	// Страница limit=2 offset=1.
	page, err := boardViewPage(ctx, store, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Epics) != 2 || len(page.Tasks) != 2 || len(page.Bugs) != 1 {
		t.Fatalf("страница: epics=%d tasks=%d bugs=%d", len(page.Epics), len(page.Tasks), len(page.Bugs))
	}
	if page.Total == nil || page.Total.Epics != 3 || page.Total.Tasks != 5 || page.Total.Bugs != 2 {
		t.Fatalf("страница total = %+v", page.Total)
	}

	// offset за пределами — пустые секции.
	beyond, err := boardViewPage(ctx, store, 2, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(beyond.Epics) != 0 || len(beyond.Tasks) != 0 || len(beyond.Bugs) != 0 {
		t.Fatalf("beyond: epics=%d tasks=%d bugs=%d", len(beyond.Epics), len(beyond.Tasks), len(beyond.Bugs))
	}
}

func TestGetBoardWithLimitOffsetOverHTTP(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	t.Setenv("BOARD_REDIS_ADDR", mr.Addr())
	seedBoardPage(t, mr, "board-page-http")

	srv, err := NewServer(Config{WorkspacesPath: t.TempDir() + "/workspaces.json"})
	if err != nil {
		t.Fatal(err)
	}
	handler := srv.routes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/projects/board-page-http?limit=2&offset=1", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET board: %d, body: %s", rec.Code, rec.Body.String())
	}
	var snap struct {
		Epics []any `json:"epics"`
		Tasks []any `json:"tasks"`
		Bugs  []any `json:"bugs"`
		Total *struct {
			Epics int `json:"epics"`
			Tasks int `json:"tasks"`
			Bugs  int `json:"bugs"`
		} `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Epics) != 2 || len(snap.Tasks) != 2 || len(snap.Bugs) != 1 {
		t.Fatalf("HTTP страница: epics=%d tasks=%d bugs=%d", len(snap.Epics), len(snap.Tasks), len(snap.Bugs))
	}
	if snap.Total == nil || snap.Total.Tasks != 5 {
		t.Fatalf("HTTP total = %+v", snap.Total)
	}
}

// TestBoardSnapshotIncludesTokenFields проверяет, что факт и прогноз расхода
// токенов попадают в снимок доски (Ф-4
// PLAN-2026-09-19-done-epic-task-token.md): поля сериализуются в JSON сущностей
// без отдельной обработки, потому что живут в самой сущности доски.
func TestBoardSnapshotIncludesTokenFields(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	store := seedBoardPage(t, mr, "tok-page-proj")
	ctx := context.Background()

	if err := store.FinalizeTaskTokens(ctx, "task-1", 12_000, 3_000); err != nil {
		t.Fatal(err)
	}
	if err := store.FinalizeEpicTokens(ctx, "epic-a", 40_000, 10_000); err != nil {
		t.Fatal(err)
	}
	// Прогноз остаётся в сущности после фиксации факта.
	task, err := store.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	task.TokenEstimate = 20_000
	if err := store.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	view, err := boardView(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	var gotTask *board.Task
	var gotEpic *board.Epic
	for i := range view.Tasks {
		if view.Tasks[i].TaskID == "task-1" {
			gotTask = view.Tasks[i]
		}
	}
	for i := range view.Epics {
		if view.Epics[i].TaskID == "epic-a" {
			gotEpic = view.Epics[i]
		}
	}
	if gotTask == nil || gotEpic == nil {
		t.Fatal("снимок доски не содержит task-1/epic-a")
	}
	if gotTask.TokensTotal != 15_000 || gotTask.TokenEstimate != 20_000 {
		t.Errorf("поля задачи в снимке = %+v, ожидалось total=15000 estimate=20000", gotTask.TokenUsage)
	}
	if gotEpic.TokensTotal != 50_000 {
		t.Errorf("поля эпика в снимке = %+v, ожидалось total=50000", gotEpic.TokenUsage)
	}

	// Те же поля видны в JSON-ответе REST.
	data, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, task := range raw.Tasks {
		if task["task_id"] != "task-1" {
			continue
		}
		for _, k := range []string{"tokens_in", "tokens_out", "tokens_total", "token_estimate"} {
			if _, ok := task[k]; !ok {
				t.Errorf("ключ %q отсутствует в JSON задачи снимка доски", k)
			}
		}
	}
}
