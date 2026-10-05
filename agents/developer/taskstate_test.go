package developer

// Тесты State Tracking (Ф-6, этап 2): разработчик пишет состояние раунда в
// запись своей задачи на доске — прогресс, ошибку проверки и точки отката.

import (
	"context"
	"strings"
	"testing"

	"ai/board"
	"ai/runner"
	"ai/tools"

	"github.com/alicebob/miniredis/v2"
)

// stateStore поднимает доску проекта поверх in-memory Redis и создаёт на ней
// эпик с задачей (задача без эпика доска не примет).
func stateStore(t *testing.T) *board.Store {
	t.Helper()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "app"})
	ctx := context.Background()
	if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{TaskID: "E-1", Title: "epic"}}); err != nil {
		t.Fatalf("CreateEpic: %v", err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "T-1", Title: "задача", SequenceOrder: 1},
		EpicID:   "E-1",
		Status:   board.StatusInProgress,
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return store
}

func stateAgentFor(store *board.Store) *base {
	d := &base{FileOps: &tools.FileOps{}, Store: store, label: "backend-разработчик"}
	d.SetTaskID("T-1")
	return d
}

// TestReportRoundStateRecordsProgressAndError — раунд с упавшей проверкой
// оставляет на доске состояние «чинит ошибки», текст падения и пульс; точки
// отката не двигаются, потому что коммита не было.
func TestReportRoundStateRecordsProgressAndError(t *testing.T) {
	store := stateStore(t)
	d := stateAgentFor(store)
	d.ReportRoundState(runner.RoundState{
		Round: 3, State: "fixing_errors", VerifyRan: true, VerifyFailed: true,
		VerifyCommand: "go test ./...", VerifyOutput: "--- FAIL: TestLogin",
		Touched: 2,
	})
	got, err := store.GetTask(context.Background(), "T-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.AgentState != board.AgentStateFixingErrors || got.ActiveAgent != "backend-разработчик" {
		t.Fatalf("состояние/агент: %+v", got)
	}
	if !strings.Contains(got.LastError, "go test ./...") || !strings.Contains(got.LastError, "TestLogin") {
		t.Fatalf("last_error: %q", got.LastError)
	}
	if got.HeartbeatAt == "" {
		t.Fatal("heartbeat не записан")
	}
	if got.Checkpoint != nil {
		t.Fatalf("чекпойнт без коммита не должен появляться: %+v", got.Checkpoint)
	}
}

// TestReportRoundStateMovesCheckpoints — last_sha двигается на каждом коммите
// раунда, last_good_sha — только на раунде с зелёной проверкой. Это и есть
// две разные точки ручного отката: «вернуться к рабочему» и «вернуться к
// последней правке».
func TestReportRoundStateMovesCheckpoints(t *testing.T) {
	ctx := context.Background()
	store := stateStore(t)
	d := stateAgentFor(store)

	// Раунд с коммитом, но без проверки: last_good на месте.
	d.ReportRoundState(runner.RoundState{Round: 1, State: "writing_code", CommitSHA: "aaa", Committed: true, Touched: 1})
	got, _ := store.GetTask(ctx, "T-1")
	if got.Checkpoint == nil || got.Checkpoint.LastSHA != "aaa" || got.Checkpoint.LastGoodSHA != "" {
		t.Fatalf("после коммита без проверки: %+v", got.Checkpoint)
	}

	// Раунд с коммитом и зелёной проверкой: last_good догоняет last.
	d.ReportRoundState(runner.RoundState{
		Round: 2, State: "running_tests", VerifyRan: true,
		CommitSHA: "bbb", Committed: true, Touched: 1,
	})
	got, _ = store.GetTask(ctx, "T-1")
	if got.Checkpoint.LastGoodSHA != "bbb" || got.Checkpoint.LastSHA != "bbb" {
		t.Fatalf("после зелёной проверки: %+v", got.Checkpoint)
	}
	if got.LastError != "" {
		t.Fatalf("ошибка предыдущего раунда не должна висеть: %q", got.LastError)
	}

	// Раунд с упавшей проверкой: last_good остаётся на последнем зелёном.
	d.ReportRoundState(runner.RoundState{
		Round: 3, State: "fixing_errors", VerifyRan: true, VerifyFailed: true,
		VerifyCommand: "go test ./...", CommitSHA: "ccc", Committed: true, Touched: 1,
	})
	got, _ = store.GetTask(ctx, "T-1")
	if got.Checkpoint.LastSHA != "ccc" {
		t.Fatalf("last_sha: %+v", got.Checkpoint)
	}
	if got.Checkpoint.LastGoodSHA != "bbb" {
		t.Fatalf("last_good_sha сдвинулся на сломанном раунде: %+v", got.Checkpoint)
	}
	if got.LastError == "" {
		t.Fatal("падение должно попасть в last_error")
	}
}

// TestReportRoundStateSkipsWithoutStoreOrTaskID — без доски (standalone CLI) и
// без id задачи запись не происходит, но и падения нет.
func TestReportRoundStateSkipsWithoutStoreOrTaskID(t *testing.T) {
	store := stateStore(t)
	rs := runner.RoundState{Round: 1, State: "writing_code", CommitSHA: "aaa", Committed: true}

	noStore := &base{FileOps: &tools.FileOps{}}
	noStore.SetTaskID("T-1")
	noStore.ReportRoundState(rs)

	noTask := &base{FileOps: &tools.FileOps{}, Store: store}
	noTask.ReportRoundState(rs)

	got, _ := store.GetTask(context.Background(), "T-1")
	if got.Checkpoint != nil || got.AgentState != "" {
		t.Fatalf("без taskID состояние записано: %+v", got)
	}
}
