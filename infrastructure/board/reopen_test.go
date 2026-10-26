package board

import (
	"context"
	"strings"
	"testing"
)

// readyReopenTask создаёт эпик и задачу и доводит задачу до development.
func readyReopenTask(t *testing.T, s *Store, ctx context.Context, taskID string) {
	t.Helper()
	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "E-1", Title: "epic"}}); err != nil {
		t.Fatalf("CreateEpic: %v", err)
	}
	if err := s.CreateTask(ctx, &Task{
		TaskSpec: TaskSpec{TaskID: taskID, Title: taskID, SequenceOrder: 1},
		EpicID:   "E-1",
		Assignee: "Senior Go Developer",
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	for _, st := range []Status{StatusTestCases, StatusDevelopment} {
		if err := s.SetTaskStatus(ctx, taskID, st); err != nil {
			t.Fatalf("task -> %s: %v", st, err)
		}
	}
}

// doneReopenTask доводит задачу до done (при needEpicDone — и эпик тоже).
func doneReopenTask(t *testing.T, s *Store, ctx context.Context, taskID string, needEpicDone bool) {
	t.Helper()
	readyReopenTask(t, s, ctx, taskID)
	if err := s.SetTaskStatus(ctx, taskID, StatusDone); err != nil {
		t.Fatalf("task -> done: %v", err)
	}
	if needEpicDone {
		for _, st := range []Status{StatusBusinessReview, StatusSystemAnalysis, StatusDone} {
			if err := s.SetEpicStatus(ctx, "E-1", st); err != nil {
				t.Fatalf("epic -> %s: %v", st, err)
			}
		}
	}
}

func TestReopenTaskWithNewComment(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	doneReopenTask(t, s, ctx, "T-1", false)

	task, accounted, err := s.ReopenTask(ctx, "T-1", Comment{
		Author: "user", Type: CommentTypeUser, Body: "переделать кнопку",
	})
	if err != nil {
		t.Fatalf("ReopenTask: %v", err)
	}
	if task.Status != StatusInfra {
		t.Fatalf("статус = %s, ожидался infra", task.Status)
	}
	// Исполнитель сохраняется — дорабатывает тот же специалист.
	if task.Assignee != "Senior Go Developer" {
		t.Fatalf("исполнитель = %q, ожидался прежний", task.Assignee)
	}
	if accounted != 1 {
		t.Fatalf("учтено комментариев = %d, ожидался 1", accounted)
	}
	got, err := s.GetTask(ctx, "T-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != StatusInfra {
		t.Fatalf("на доске статус = %s", got.Status)
	}
	if len(got.Comments) != 1 {
		t.Fatalf("комментариев = %d, ожидался 1", len(got.Comments))
	}
	if !got.Comments[0].Accounted {
		t.Fatal("новый комментарий не помечен учтённым")
	}
	if got.Comments[0].Body != "переделать кнопку" {
		t.Fatalf("тело комментария = %q", got.Comments[0].Body)
	}
}

func TestReopenTaskSucceedsWithExistingUnaccountedComment(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	doneReopenTask(t, s, ctx, "T-1", false)
	if err := s.SetTaskComments(ctx, "T-1", Comments{
		{Author: "user", Type: CommentTypeUser, Body: "первое замечание"},
		{Author: "user", Type: CommentTypeUser, Body: "уже учтённое", Accounted: true},
	}); err != nil {
		t.Fatalf("SetTaskComments: %v", err)
	}

	_, accounted, err := s.ReopenTask(ctx, "T-1", Comment{})
	if err != nil {
		t.Fatalf("ReopenTask: %v", err)
	}
	if accounted != 1 {
		t.Fatalf("учтено = %d, ожидался 1 (только необработанный)", accounted)
	}
	got, _ := s.GetTask(ctx, "T-1")
	for i, c := range got.Comments {
		if !c.Accounted {
			t.Fatalf("комментарий %d не учтён: %+v", i, c)
		}
	}
}

func TestReopenTaskRequiresComment(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	doneReopenTask(t, s, ctx, "T-1", false)

	_, _, err := s.ReopenTask(ctx, "T-1", Comment{})
	if err == nil || !strings.Contains(err.Error(), "без комментария") {
		t.Fatalf("ожидалась ошибка про комментарий, получено %v", err)
	}
	got, _ := s.GetTask(ctx, "T-1")
	if got.Status != StatusDone {
		t.Fatalf("задача без комментария всё же переоткрылась: %s", got.Status)
	}
}

func TestReopenTaskRejectsNotDone(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	readyReopenTask(t, s, ctx, "T-1")

	_, _, err := s.ReopenTask(ctx, "T-1", Comment{Body: "доработать"})
	if err == nil || !strings.Contains(err.Error(), "выполненную") {
		t.Fatalf("ожидалась ошибка про невыполненную задачу, получено %v", err)
	}
}

// Повторно переоткрыть задачу по уже учтённому комментарию нельзя: после
// доработки он остаётся учтённым, и без нового комментария переоткрытие
// отклоняется.
func TestReopenTaskCannotReuseAccountedComment(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	doneReopenTask(t, s, ctx, "T-1", false)
	if err := s.SetTaskComments(ctx, "T-1", Comments{
		{Author: "user", Type: CommentTypeUser, Body: "замечание"},
	}); err != nil {
		t.Fatalf("SetTaskComments: %v", err)
	}
	if _, _, err := s.ReopenTask(ctx, "T-1", Comment{}); err != nil {
		t.Fatalf("первое переоткрытие: %v", err)
	}
	// Доводим задачу снова до done.
	for _, st := range []Status{StatusTestCases, StatusDevelopment, StatusDone} {
		if err := s.SetTaskStatus(ctx, "T-1", st); err != nil {
			t.Fatalf("task -> %s: %v", st, err)
		}
	}
	if _, _, err := s.ReopenTask(ctx, "T-1", Comment{}); err == nil {
		t.Fatal("повторное переоткрытие по учтённому комментарию должно быть запрещено")
	}
}

func TestReopenTaskReopensDoneEpic(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	doneReopenTask(t, s, ctx, "T-1", true)
	if e, _ := s.GetEpic(ctx, "E-1"); e.Status != StatusDone {
		t.Fatalf("предусловие не выполнено: эпик = %s", e.Status)
	}

	if _, _, err := s.ReopenTask(ctx, "T-1", Comment{Body: "доработать"}); err != nil {
		t.Fatalf("ReopenTask: %v", err)
	}
	e, err := s.GetEpic(ctx, "E-1")
	if err != nil {
		t.Fatalf("GetEpic: %v", err)
	}
	if e.Status != StatusSystemAnalysis {
		t.Fatalf("эпик = %s, ожидался system_analysis (иначе оркестратор не увидит задачу)", e.Status)
	}
}
