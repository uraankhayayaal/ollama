package board

// Тесты гарда «phantom done» (Ф-6, инцидент FEL-04): проверка выполняется ДО
// перехода в done, ошибка гарда возвращается вызывающему, а задача остаётся в
// прежнем статусе. Без этого fallback оркестратора закрывает задачу, которую
// никто не сделал, а статус done терминален — откатить нельзя.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestSetTaskStatusDoneGuardBlocks — отказ гарда не меняет статус.
func TestSetTaskStatusDoneGuardBlocks(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := store.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "E-1", Title: "эпик"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &Task{TaskSpec: TaskSpec{TaskID: "T-1", Title: "задача"}, EpicID: "E-1"}); err != nil {
		t.Fatal(err)
	}
	for _, st := range []Status{StatusAnalysis, StatusReady, StatusInProgress} {
		if err := store.SetTaskStatus(ctx, "T-1", st); err != nil {
			t.Fatalf("переход в %s: %v", st, err)
		}
	}
	wantErr := errors.New("в ветке задачи нет ни одного коммита")
	var seenFrom Status
	store.TaskDoneGuard = func(_ context.Context, task *Task, from Status) error {
		seenFrom = from
		if task.Status != StatusInProgress {
			t.Fatalf("гард должен видеть задачу в прежнем статусе, не в done: %s", task.Status)
		}
		return wantErr
	}
	err := store.SetTaskStatus(ctx, "T-1", StatusDone)
	if !errors.Is(err, wantErr) {
		t.Fatalf("ошибка гарда должна доходить до вызывающего, получено: %v", err)
	}
	if seenFrom != StatusInProgress {
		t.Fatalf("гард получил from=%q, ожидался %q", seenFrom, StatusInProgress)
	}
	got, err := store.GetTask(ctx, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusInProgress {
		t.Fatalf("после отказа статус %q, ожидался %q", got.Status, StatusInProgress)
	}
	// Задача снова берётся в работу и уже с результатом закрывается.
	store.TaskDoneGuard = nil
	if err := store.SetTaskStatus(ctx, "T-1", StatusDone); err != nil {
		t.Fatalf("done без гарда: %v", err)
	}
}

// TestSetTaskStatusDoneGuardIgnoresOtherStatuses — гард справедлив только для
// done: обычные переходы он не блокирует (иначе доска встанет на первом же
// шаге).
func TestSetTaskStatusDoneGuardIgnoresOtherStatuses(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := store.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "E-1", Title: "эпик"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &Task{TaskSpec: TaskSpec{TaskID: "T-1", Title: "задача"}, EpicID: "E-1"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	store.TaskDoneGuard = func(context.Context, *Task, Status) error {
		calls++
		return errors.New("гард не должен вызываться")
	}
	if err := store.SetTaskStatus(ctx, "T-1", StatusAnalysis); err != nil {
		t.Fatalf("переход в анализ: %v", err)
	}
	if err := store.SetTaskStatus(ctx, "T-1", StatusReady); err != nil {
		t.Fatalf("переход в ready: %v", err)
	}
	if err := store.SetTaskStatus(ctx, "T-1", StatusInProgress); err != nil {
		t.Fatalf("переход в работу: %v", err)
	}
	if calls != 0 {
		t.Fatalf("гард вызван %d раз(а) для не-done переходов", calls)
	}
	err := store.SetTaskStatus(ctx, "T-1", StatusDone)
	if err == nil || !strings.Contains(err.Error(), "гард не должен вызываться") {
		t.Fatalf("на done гард обязан сработать, получено: %v", err)
	}
	if calls != 1 {
		t.Fatalf("гард вызван %d раз(а), ожидался 1", calls)
	}
}
