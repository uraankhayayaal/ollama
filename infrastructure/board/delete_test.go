package board

import (
	"context"
	"strings"
	"testing"
)

func mustCreateEpic(s *Store, ctx context.Context, id string) {
	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: id, Title: id}}); err != nil {
		panic(err)
	}
}

func mustCreateTask(s *Store, ctx context.Context, epicID, id string) {
	if err := s.CreateTask(ctx, &Task{TaskSpec: TaskSpec{TaskID: id, SequenceOrder: 1}, EpicID: epicID}); err != nil {
		panic(err)
	}
}

func mustStatus(s *Store, ctx context.Context, id string, st Status) {
	if err := s.SetTaskStatus(ctx, id, st); err != nil {
		panic(err)
	}
}

// TestDeleteTaskDraftAndInfra — задачи в начальных статусах можно удалить.
func TestDeleteTaskDraftAndInfra(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	mustCreateEpic(s, ctx, "E-1")
	mustCreateTask(s, ctx, "E-1", "T-01") // draft
	mustCreateTask(s, ctx, "E-1", "T-02") // draft

	// draft -> infra -> удаление
	if err := s.SetTaskStatus(ctx, "T-02", StatusTestCases); err != nil {
		t.Fatal(err)
	}

	// Удаление T-01 (draft) — OK
	if err := s.DeleteTask(ctx, "T-01"); err != nil {
		t.Fatalf("удаление draft-задачи: %v", err)
	}
	// T-01 не найдена
	if _, err := s.GetTask(ctx, "T-01"); err == nil {
		t.Fatal("T-01 должна быть удалена")
	}
}

// TestDeleteTaskDone — завершенную задачу можно удалить.
func TestDeleteTaskDone(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	mustCreateEpic(s, ctx, "E-1")
	mustCreateTask(s, ctx, "E-1", "T-01")

	mustStatus(s, ctx, "T-01", StatusTestCases)
	mustStatus(s, ctx, "T-01", StatusDevelopment)
	mustStatus(s, ctx, "T-01", StatusDone)

	if err := s.DeleteTask(ctx, "T-01"); err != nil {
		t.Fatalf("удаление done-задачи: %v", err)
	}
	if _, err := s.GetTask(ctx, "T-01"); err == nil {
		t.Fatal("T-01 должна быть удалена")
	}
}

// TestDeleteTaskInProgressBlocked — задачи в работе нельзя удалить.
func TestDeleteTaskInProgressBlocked(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	mustCreateEpic(s, ctx, "E-1")
	mustCreateTask(s, ctx, "E-1", "T-01")

	mustStatus(s, ctx, "T-01", StatusTestCases)

	if err := s.DeleteTask(ctx, "T-01"); err == nil {
		t.Fatal("удаление test_cases-задачи должно быть заблокировано")
	}
}

// TestDeleteTaskDevelopmentBlocked — задачи в разработке нельзя удалить.
func TestDeleteTaskDevelopmentBlocked(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	mustCreateEpic(s, ctx, "E-1")
	mustCreateTask(s, ctx, "E-1", "T-01")

	mustStatus(s, ctx, "T-01", StatusTestCases)
	mustStatus(s, ctx, "T-01", StatusDevelopment)

	if err := s.DeleteTask(ctx, "T-01"); err == nil {
		t.Fatal("удаление development-задачи должно быть заблокировано")
	}
}

// TestDeleteTaskCancelledBlocked — отменённые задачи нельзя удалить.
func TestDeleteTaskCancelledBlocked(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	mustCreateEpic(s, ctx, "E-1")
	mustCreateTask(s, ctx, "E-1", "T-01")

	mustStatus(s, ctx, "T-01", StatusCancelled)

	if err := s.DeleteTask(ctx, "T-01"); err == nil {
		t.Fatal("удаление cancelled-задачи должно быть заблокировано")
	}
}

// TestDeleteEpicEmpty — пустой эпик удаляется всегда.
func TestDeleteEpicEmpty(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	mustCreateEpic(s, ctx, "E-1")

	if err := s.DeleteEpic(ctx, "E-1"); err != nil {
		t.Fatalf("удаление пустого эпика: %v", err)
	}
	if _, err := s.GetEpic(ctx, "E-1"); err == nil {
		t.Fatal("E-1 должен быть удалён")
	}
}

// TestDeleteEpicWithDoneTasks — эпик с выполненными задачами удаляется.
func TestDeleteEpicWithDoneTasks(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	mustCreateEpic(s, ctx, "E-1")
	mustCreateTask(s, ctx, "E-1", "T-01")
	mustCreateTask(s, ctx, "E-1", "T-02")

	mustStatus(s, ctx, "T-01", StatusTestCases)
	mustStatus(s, ctx, "T-01", StatusDevelopment)
	mustStatus(s, ctx, "T-01", StatusDone)

	mustStatus(s, ctx, "T-02", StatusTestCases)
	mustStatus(s, ctx, "T-02", StatusDevelopment)
	mustStatus(s, ctx, "T-02", StatusDone)

	if err := s.DeleteEpic(ctx, "E-1"); err != nil {
		t.Fatalf("удаление эпика с done-задачами: %v", err)
	}
	if _, err := s.GetEpic(ctx, "E-1"); err == nil {
		t.Fatal("E-1 должен быть удалён")
	}
}

// TestDeleteEpicWithInProgressBlocked — эпик с задачами в работе нельзя удалить.
func TestDeleteEpicWithInProgressBlocked(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	mustCreateEpic(s, ctx, "E-1")
	mustCreateTask(s, ctx, "E-1", "T-01")

	mustStatus(s, ctx, "T-01", StatusTestCases)
	mustStatus(s, ctx, "T-01", StatusDevelopment)

	err := s.DeleteEpic(ctx, "E-1")
	if err == nil {
		t.Fatal("удаление эпика с development-задачей должно быть заблокировано")
	}
	if !strings.Contains(err.Error(), "удаление задачи") {
		t.Fatalf("ожидалась ошибка удаления задачи, получено: %v", err)
	}
}

// TestDeleteTaskRemovesFromEpic — удаление задачи разрывает двустороннюю связь.
func TestDeleteTaskRemovesFromEpic(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	mustCreateEpic(s, ctx, "E-1")
	mustCreateTask(s, ctx, "E-1", "T-01")
	mustCreateTask(s, ctx, "E-1", "T-02")

	if err := s.DeleteTask(ctx, "T-01"); err != nil {
		t.Fatal(err)
	}

	e, err := s.GetEpic(ctx, "E-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Tasks) != 1 || e.Tasks[0] != "T-02" {
		t.Fatalf("эпик содержит %v, ожидалось [T-02]", e.Tasks)
	}

	tasks, err := s.TasksByEpic(ctx, "E-1")
	if err != nil || len(tasks) != 1 {
		t.Fatalf("TasksByEpic вернул %d задач, ожидалось 1", len(tasks))
	}
}
