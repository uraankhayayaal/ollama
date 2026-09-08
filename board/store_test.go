package board

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// newTestStore создаёт хранилище доски поверх in-memory Redis (miniredis).
func newTestStore(t *testing.T) *Store {
	t.Helper()
	srv := miniredis.RunT(t)
	return NewStoreNoCheck(StoreConfig{
		Addr:    srv.Addr(),
		Project: "testproj",
	})
}

func TestStoreEpicLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "ARC-01", Title: "Backend"}}); err != nil {
		t.Fatalf("CreateEpic: %v", err)
	}
	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "ARC-01"}}); !errors.Is(err, ErrExists) {
		t.Fatalf("повторный CreateEpic должен вернуть ErrExists, получено %v", err)
	}

	e, err := s.GetEpic(ctx, "ARC-01")
	if err != nil {
		t.Fatalf("GetEpic: %v", err)
	}
	if e.Status != StatusNew || e.ProjectName != "testproj" {
		t.Errorf("эпик создан неверно: %+v", e)
	}

	if err := s.SetEpicStatus(ctx, "ARC-01", StatusReady); err == nil {
		t.Fatal("new -> ready должен быть запрещён")
	}
	if err := s.SetEpicStatus(ctx, "ARC-01", StatusAnalysis); err != nil {
		t.Fatalf("new -> analysis: %v", err)
	}
	if err := s.SetEpicStatus(ctx, "ARC-01", StatusReady); err != nil {
		t.Fatalf("analysis -> ready: %v", err)
	}

	epics, err := s.ListEpics(ctx)
	if err != nil || len(epics) != 1 {
		t.Fatalf("ListEpics = %v, err = %v", len(epics), err)
	}
}

func TestStoreTaskTwoWayLinkAndDependencies(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "ARC-01", Title: "Backend"}}); err != nil {
		t.Fatalf("CreateEpic: %v", err)
	}
	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "ARC-02", Title: "Frontend"}}); err != nil {
		t.Fatalf("CreateEpic: %v", err)
	}

	dep := func(epicID, taskID string, deps ...string) *Task {
		return &Task{TaskSpec: TaskSpec{TaskID: taskID, Title: taskID, Description: "xD", AssignedRole: "Dev", SequenceOrder: 1, CanRunParallel: true, Dependencies: deps}, EpicID: epicID}
	}

	// Задача с несуществующей зависимостью.
	if err := s.CreateTask(ctx, dep("ARC-01", "T-01", "T-99")); err == nil {
		t.Fatal("задача с несуществующей зависимостью должна быть отклонена")
	}
	// Задача с несуществующим эпиком.
	if err := s.CreateTask(ctx, dep("NO-EPIC", "T-01")); err == nil {
		t.Fatal("задача с несуществующим эпиком должна быть отклонена")
	}
	// Задача с зависимостью на другой эпик (эпик-артефакт как зависимость).
	if err := s.CreateTask(ctx, dep("ARC-01", "T-01", "ARC-02")); err != nil {
		t.Fatalf("зависимость на эпик должна быть разрешена: %v", err)
	}
	if err := s.CreateTask(ctx, dep("ARC-01", "T-02", "T-01")); err != nil {
		t.Fatalf("зависимость на задачу: %v", err)
	}
	if err := s.CreateTask(ctx, dep("ARC-02", "T-01")); !errors.Is(err, ErrExists) {
		t.Fatalf("повторный task_id должен вернуть ErrExists, получено %v", err)
	}

	// Двусторонняя связь: эпик ссылается на свои задачи.
	e, err := s.GetEpic(ctx, "ARC-01")
	if err != nil {
		t.Fatalf("GetEpic: %v", err)
	}
	if len(e.Tasks) != 2 { // T-01, T-02
		t.Fatalf("эпик знает о %d задачах, ожидалось 2: %v", len(e.Tasks), e.Tasks)
	}
	for _, want := range []string{"T-01", "T-02"} {
		found := false
		for _, id := range e.Tasks {
			if id == want {
				found = true
			}
		}
		if !found {
			t.Errorf("эпик не содержит задачу %q", want)
		}
	}

	// Наоборот: задачи привязаны к эпику.
	byEpic, err := s.TasksByEpic(ctx, "ARC-01")
	if err != nil || len(byEpic) != 2 {
		t.Fatalf("TasksByEpic = %v, err = %v", len(byEpic), err)
	}

	if err := s.SetTaskStatus(ctx, "T-01", StatusDone); err == nil {
		t.Fatal("new -> done должен быть запрещён")
	}
	if err := s.SetTaskStatus(ctx, "T-01", StatusReady); err == nil {
		t.Fatal("new -> ready должен быть запрещён")
	}
	if err := s.SetTaskStatus(ctx, "T-01", StatusAnalysis); err != nil {
		t.Fatalf("new -> analysis: %v", err)
	}
	if err := s.SetTaskStatus(ctx, "T-01", StatusReady); err != nil {
		t.Fatalf("analysis -> ready: %v", err)
	}
	if err := s.SetTaskStatus(ctx, "T-01", StatusInProgress); err != nil {
		t.Fatalf("ready -> in_progress: %v", err)
	}
	if err := s.SetTaskStatus(ctx, "T-01", StatusDone); err != nil {
		t.Fatalf("in_progress -> done: %v", err)
	}
}

func TestStoreAllDone(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	done, err := s.AllDone(ctx)
	if err != nil || done {
		t.Fatalf("пустая доска не должна считаться решённой: done=%v err=%v", done, err)
	}

	// Два эпика без задач.
	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "ARC-01"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "ARC-02"}}); err != nil {
		t.Fatal(err)
	}
	if done, _ := s.AllDone(ctx); done {
		t.Fatal("доска с невыполненными эпиками не может быть решённой")
	}

	finish := func(id string) {
		s.SetEpicStatus(ctx, id, StatusAnalysis)
		s.SetEpicStatus(ctx, id, StatusReady)
		s.SetEpicStatus(ctx, id, StatusInProgress)
		s.SetEpicStatus(ctx, id, StatusDone)
	}
	finish("ARC-01")
	if done, _ := s.AllDone(ctx); done {
		t.Fatal("решение требует выполнения ВСЕХ эпиков")
	}

	// Задача к эпику ARC-02.
	if err := s.CreateTask(ctx, &Task{TaskSpec: TaskSpec{TaskID: "T-01", SequenceOrder: 1}, EpicID: "ARC-02"}); err != nil {
		t.Fatal(err)
	}
	finish("ARC-02")
	if done, _ := s.AllDone(ctx); done {
		t.Fatal("решение требует выполнения всех задач")
	}
	s.SetTaskStatus(ctx, "T-01", StatusAnalysis)
	s.SetTaskStatus(ctx, "T-01", StatusReady)
	s.SetTaskStatus(ctx, "T-01", StatusInProgress)
	s.SetTaskStatus(ctx, "T-01", StatusDone)
	if done, err := s.AllDone(ctx); err != nil || !done {
		t.Fatalf("все эпики и задачи выполнены — доска должна быть решённой: done=%v err=%v", done, err)
	}
}

func TestStoreAllDoneNotWithCancelled(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "ARC-01"}}); err != nil {
		t.Fatal(err)
	}
	// Отменённая запись => решение не успешно (терминальные статусы не
	// снимаются, поэтому отменяем эпик до завершения).
	if err := s.SetEpicStatus(ctx, "ARC-01", StatusCancelled); err != nil {
		t.Fatalf("new -> cancelled: %v", err)
	}
	if done, _ := s.AllDone(ctx); done {
		t.Fatal("отменённая запись не даёт успешного решения")
	}
}

func TestMeta(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.GetMeta(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetMeta без сохранения должен вернуть ErrNotFound: %v", err)
	}
	if err := s.SaveMeta(ctx, &Meta{Task: "Сделать приложение"}); err != nil {
		t.Fatalf("SaveMeta: %v", err)
	}
	m, err := s.GetMeta(ctx)
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if m.Task != "Сделать приложение" || m.ProjectName != "testproj" {
		t.Errorf("meta: %+v", m)
	}
}
