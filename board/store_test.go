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

// TestStoreEpicPauseResumeCascade — пауза эпика (Ф-6) каскадом приостанавливает
// его задачи, которые ещё не взял в работу специалист, запоминая исходный
// статус; «в работе» доводит текущий раунд оркестратора. Возобновление
// возвращает задачи на прежнее место цепочки.
func TestStoreEpicPauseResumeCascade(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "ARC-01", Title: "Backend"}}); err != nil {
		t.Fatal(err)
	}
	// T-01 — «в работе» (специалист уже пишет код), T-02 — «готова к работе»,
	// T-03 — новая (ждёт зависимости).
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
	mustTask("T-01", StatusAnalysis, StatusReady, StatusInProgress)
	mustTask("T-02", StatusAnalysis, StatusReady)

	// Эпик доводится до «в работе», затем приостанавливается.
	for _, st := range []Status{StatusAnalysis, StatusReady, StatusInProgress, StatusPaused} {
		if err := s.SetEpicStatus(ctx, "ARC-01", st); err != nil {
			t.Fatalf("эпик -> %s: %v", st, err)
		}
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
		"T-01": StatusInProgress, // «в работе» пауза эпика не трогает
		"T-02": StatusPaused,
		"T-03": StatusPaused,
	})
	// Исходные статусы запомнены — возобновление вернёт задачи на место.
	for _, id := range []string{"T-02", "T-03"} {
		got, err := s.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		want := StatusReady
		if id == "T-03" {
			want = StatusNew
		}
		if got.ResumeStatus != want {
			t.Errorf("задача %s: resume_status = %s, ожидалось %s", id, got.ResumeStatus, want)
		}
	}

	// Возобновление: эпик paused -> ready, задачи возвращаются в свои статусы.
	if err := s.SetEpicStatus(ctx, "ARC-01", StatusReady); err != nil {
		t.Fatalf("эпик paused -> ready: %v", err)
	}
	assertTasks(map[string]Status{
		"T-01": StatusInProgress,
		"T-02": StatusReady,
		"T-03": StatusNew,
	})
	for _, id := range []string{"T-02", "T-03"} {
		got, err := s.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.ResumeStatus != "" {
			t.Errorf("задача %s: resume_status должен очиститься, получено %s", id, got.ResumeStatus)
		}
	}
}

// TestStoreEpicCancelCascade — отмена эпика (Ф-6) помечает отменёнными его
// незавершённые задачи (включая приостановленные), не удаляя записи: ветки и
// код остаются как артефакт, хронология доски сохраняется.
func TestStoreEpicCancelCascade(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "ARC-01"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"T-01", "T-02"} {
		if err := s.CreateTask(ctx, &Task{TaskSpec: TaskSpec{TaskID: id, SequenceOrder: 1}, EpicID: "ARC-01"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetTaskStatus(ctx, "T-01", StatusAnalysis); err != nil {
		t.Fatal(err)
	}

	// Эпик приостановлен (T-01/T-02 — «на паузе»), затем отменён.
	if err := s.SetEpicStatus(ctx, "ARC-01", StatusPaused); err != nil {
		t.Fatalf("эпик new -> paused: %v", err)
	}
	if err := s.SetEpicStatus(ctx, "ARC-01", StatusCancelled); err != nil {
		t.Fatalf("эпик paused -> cancelled: %v", err)
	}
	for _, id := range []string{"T-01", "T-02"} {
		got, err := s.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != StatusCancelled {
			t.Errorf("задача %s = %s, ожидалось отменена", id, got.Status)
		}
		if got.ResumeStatus != "" {
			t.Errorf("задача %s: resume_status должен очиститься при отмене", id)
		}
	}
	// Записи не удалены — хронология сохраняется.
	tasks, err := s.ListTasks(ctx)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("задачи должны остаться на доске: %d, err = %v", len(tasks), err)
	}
}

// TestStoreEpicPauseDoesNotBlockAllDone — приостановленный эпик не считается
// выполненным: задача пользователя не решена, пока эпик на паузе.
func TestStoreEpicPauseDoesNotBlockAllDone(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "ARC-01"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEpicStatus(ctx, "ARC-01", StatusPaused); err != nil {
		t.Fatalf("new -> paused: %v", err)
	}
	done, err := s.AllDone(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("эпик на паузе не даёт успешного решения задачи")
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
