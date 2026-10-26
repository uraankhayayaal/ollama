package board

import (
	"context"
	"errors"
	"reflect"
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
	if e.Status != StatusDraft || e.ProjectName != "testproj" {
		t.Errorf("эпик создан неверно: %+v", e)
	}

	if err := s.SetEpicStatus(ctx, "ARC-01", StatusSystemAnalysis); err == nil {
		t.Fatal("draft -> system_analysis должен быть запрещён")
	}
	if err := s.SetEpicStatus(ctx, "ARC-01", StatusBusinessReview); err != nil {
		t.Fatalf("draft -> business_review: %v", err)
	}
	if err := s.SetEpicStatus(ctx, "ARC-01", StatusSystemAnalysis); err != nil {
		t.Fatalf("business_review -> system_analysis: %v", err)
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
		t.Fatal("infra -> done должен быть запрещён")
	}
	if err := s.SetTaskStatus(ctx, "T-01", StatusDevelopment); err == nil {
		t.Fatal("infra -> development должен быть запрещён")
	}
	if err := s.SetTaskStatus(ctx, "T-01", StatusTestCases); err != nil {
		t.Fatalf("infra -> test_cases: %v", err)
	}
	if err := s.SetTaskStatus(ctx, "T-01", StatusDevelopment); err != nil {
		t.Fatalf("test_cases -> development: %v", err)
	}
	if err := s.SetTaskStatus(ctx, "T-01", StatusDone); err != nil {
		t.Fatalf("development -> done: %v", err)
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
		s.SetEpicStatus(ctx, id, StatusBusinessReview)
		s.SetEpicStatus(ctx, id, StatusSystemAnalysis)
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
	s.SetTaskStatus(ctx, "T-01", StatusTestCases)
	s.SetTaskStatus(ctx, "T-01", StatusDevelopment)
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

// TestStoreEpicCancelCascade — отмена эпика (Ф-6) помечает отменёнными его
// незавершённые задачи, не удаляя записи: ветки и код остаются как артефакт,
// хронология доски сохраняется.
func TestStoreEpicCancelCascade(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "ARC-01", Title: "Backend"}}); err != nil {
		t.Fatal(err)
	}
	// T-01 — «в разработке», T-02 — «написание тестов», T-03 — новая.
	for _, id := range []string{"T-01", "T-02", "T-03"} {
		if err := s.CreateTask(ctx, &Task{TaskSpec: TaskSpec{TaskID: id, SequenceOrder: 1}, EpicID: "ARC-01"}); err != nil {
			t.Fatal(err)
		}
	}
	mustTask := func(id string, sts ...Status) {
		t.Helper()
		for _, st := range sts {
			if err := s.SetTaskStatus(ctx, id, st); err != nil {
				t.Fatalf("задача %s -> %s: %v", id, st, err)
			}
		}
	}
	mustTask("T-01", StatusTestCases, StatusDevelopment)
	mustTask("T-02", StatusTestCases)

	// Эпик отменён — незавершённые задачи тоже отменяются.
	if err := s.SetEpicStatus(ctx, "ARC-01", StatusCancelled); err != nil {
		t.Fatalf("эпик -> cancelled: %v", err)
	}
	assertTasks := func(want map[string]Status) {
		t.Helper()
		for id, st := range want {
			got, err := s.GetTask(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != st {
				t.Errorf("задача %s = %s, ожидалось %s", id, got.Status, st)
			}
		}
	}
	assertTasks(map[string]Status{
		"T-01": StatusCancelled,
		"T-02": StatusCancelled,
		"T-03": StatusCancelled,
	})
	// Записи не удалены — хронология сохраняется.
	tasks, err := s.ListTasks(ctx)
	if err != nil || len(tasks) != 3 {
		t.Fatalf("задачи должны остаться на доске: %d, err = %v", len(tasks), err)
	}
}

// TestStoreEpicCancelledDoesNotBlockAllDone — отменённый эпик не считается
// выполненным: задача пользователя не решена, когда эпик отменён.
func TestStoreEpicCancelledDoesNotBlockAllDone(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "ARC-01"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEpicStatus(ctx, "ARC-01", StatusCancelled); err != nil {
		t.Fatalf("draft -> cancelled: %v", err)
	}
	done, err := s.AllDone(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("отменённый эпик не даёт успешного решения задачи")
	}
}

func TestRepositoriesPersistOnNewEpicsAndTasks(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	s.SetRepositories([]string{"app", "app--packages--auth"})
	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "E-1"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(ctx, &Task{TaskSpec: TaskSpec{TaskID: "T-1"}, EpicID: "E-1"}); err != nil {
		t.Fatal(err)
	}
	e, err := s.GetEpic(ctx, "E-1")
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.GetTask(ctx, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"app", "app--packages--auth"}
	if !reflect.DeepEqual(e.Repositories, want) || !reflect.DeepEqual(task.Repositories, want) {
		t.Fatalf("repositories epic=%v task=%v", e.Repositories, task.Repositories)
	}
}
