package planner

import (
	"context"
	"sync"
	"testing"
	"time"

	"ai/board"
)

// recordingGate — HITL-затвор с задаваемыми решениями и журналом вызовов.
type recordingGate struct {
	mu          sync.Mutex
	epicsDecide GateDecision
	tasksDecide GateDecision
	epicsCalled []string
	tasksCalled []string
}

func (g *recordingGate) Epics(_ context.Context, epics []*board.Epic) (GateDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, e := range epics {
		g.epicsCalled = append(g.epicsCalled, e.TaskID)
	}
	return g.epicsDecide, nil
}

func (g *recordingGate) Tasks(_ context.Context, tasks []*board.Task) (GateDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, t := range tasks {
		g.tasksCalled = append(g.tasksCalled, t.TaskID)
	}
	return g.tasksDecide, nil
}

func (g *recordingGate) epics() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.epicsCalled...)
}

func (g *recordingGate) tasks() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.tasksCalled...)
}

func approving() *recordingGate {
	return &recordingGate{
		epicsDecide: GateDecision{Approved: true},
		tasksDecide: GateDecision{Approved: true},
	}
}

// TestRunWithApprovingGateSolves подтверждает, что при утверждающем затворе
// оркестрация решает задачу так же, как в автономном режиме, но перед
// исполнением каждый этап проходит через HumanGate.
func TestRunWithApprovingGateSolves(t *testing.T) {
	ctx := context.Background()
	kr, store := newKanbanRunner(t)
	gate := approving()
	kr.SetGate(gate)

	if err := kr.Run(ctx, "kanban-test", "Сделай todo-приложение"); err != nil {
		t.Fatalf("Kanban Run: %v", err)
	}
	if done, err := store.AllDone(ctx); err != nil || !done {
		t.Fatalf("доска должна быть решена: done=%v err=%v", done, err)
	}
	if got := gate.epics(); len(got) == 0 {
		t.Fatal("затвор «эпики» не вызывался")
	}
	if got := gate.tasks(); len(got) == 0 {
		t.Fatal("затвор «задачи» не вызывался")
	}
}

// TestBlockingGatePausesRun проверяет, что затвор БЛОКИРУЕТ runner до решения
// человека: без подтверждения Run не завершается, после подтверждения — да.
func TestBlockingGatePausesRun(t *testing.T) {
	ctx := context.Background()
	kr, store := newKanbanRunner(t)

	approve := make(chan struct{})
	called := make(chan struct{}, 1)
	gate := &channelGate{approve: approve, called: called}
	kr.SetGate(gate)

	done := make(chan error, 1)
	go func() { done <- kr.Run(ctx, "kanban-test", "Сделай todo-приложение") }()

	// Ждём, что runner дошёл до затвора, но НЕ завершился без подтверждения.
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("runner не дошёл до затвора за отведённое время")
	}
	select {
	case err := <-done:
		t.Fatalf("runner завершился до подтверждения: %v", err)
	case <-time.After(300 * time.Millisecond):
		// Ожидаемо: runner ждёт решения человека.
	}

	close(approve)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run после подтверждения: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner не продолжил после подтверждения")
	}
	if ok, err := store.AllDone(ctx); err != nil || !ok {
		t.Fatalf("доска решена после подтверждения: done=%v err=%v", ok, err)
	}
}

// channelGate блокирует на канале approve до тех пор, пока его не закроют
// (эпики) — имитация паузы «ждать человека».
type channelGate struct {
	approve chan struct{}
	called  chan struct{}
	sync.Once
}

func (g *channelGate) Epics(ctx context.Context, _ []*board.Epic) (GateDecision, error) {
	g.Do(func() { close(g.called) })
	select {
	case <-g.approve:
		return GateDecision{Approved: true}, nil
	case <-ctx.Done():
		return GateDecision{}, ctx.Err()
	}
}

func (g *channelGate) Tasks(context.Context, []*board.Task) (GateDecision, error) {
	return GateDecision{Approved: true}, nil
}

// TestWaitEpicsRejectedReworks проверяет контракт отклонения: эпики и их
// задачи удаляются с доски, чтобы архитектор переделал.
func TestWaitEpicsRejectedReworks(t *testing.T) {
	ctx := context.Background()
	_, store := newKanbanRunner(t)

	epic := &board.Epic{TaskSpec: board.TaskSpec{TaskID: "E-01", Title: "Эпик"}}
	if err := store.CreateEpic(ctx, epic); err != nil {
		t.Fatal(err)
	}
	task := &board.Task{TaskSpec: board.TaskSpec{TaskID: "T-01", Title: "Задача"}, EpicID: "E-01"}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	gate := &recordingGate{epicsDecide: GateDecision{Approved: false}}
	kr := NewKanbanRunner(nil, store)
	kr.SetGate(gate)
	if err := kr.waitEpics(ctx); err != nil {
		t.Fatalf("waitEpics: %v", err)
	}

	if epics, _ := store.ListEpics(ctx); len(epics) != 0 {
		t.Fatalf("после отклонения должны остаться эпики: %d", len(epics))
	}
	if tasks, _ := store.ListTasks(ctx); len(tasks) != 0 {
		t.Fatalf("после отклонения должны остаться задачи: %d", len(tasks))
	}
}

// TestWaitTasksRejectedReworks проверяет контракт отклонения готовых задач:
// эпик удаляется каскадом, раздача специалистам не происходит.
func TestWaitTasksRejectedReworks(t *testing.T) {
	ctx := context.Background()
	_, store := newKanbanRunner(t)

	epic := &board.Epic{TaskSpec: board.TaskSpec{TaskID: "E-02", Title: "Эпик"}}
	if err := store.CreateEpic(ctx, epic); err != nil {
		t.Fatal(err)
	}
	task := &board.Task{TaskSpec: board.TaskSpec{TaskID: "T-02", Title: "Задача"}, EpicID: "E-02"}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	// Доводим задачу до «готова к работе» (новая -> в анализе -> готова).
	for _, st := range []board.Status{board.StatusAnalysis, board.StatusReady} {
		if err := store.SetTaskStatus(ctx, "T-02", st); err != nil {
			t.Fatalf("переход в %s: %v", st, err)
		}
	}

	gate := &recordingGate{tasksDecide: GateDecision{Approved: false}}
	kr := NewKanbanRunner(nil, store)
	kr.SetGate(gate)
	if err := kr.waitReadyTasks(ctx); err != nil {
		t.Fatalf("waitReadyTasks: %v", err)
	}

	if epics, _ := store.ListEpics(ctx); len(epics) != 0 {
		t.Fatalf("после отклонения должны остаться эпики: %d", len(epics))
	}
	if tasks, _ := store.ListTasks(ctx); len(tasks) != 0 {
		t.Fatalf("после отклонения должны остаться задачи: %d", len(tasks))
	}
	if got := gate.tasks(); len(got) != 1 || got[0] != "T-02" {
		t.Fatalf("gate.tasks = %v, want [T-02]", got)
	}
}

// TestWaitTasksApprovedKeepsBoard: утверждение не трогает доску — задачи
// остаются готовыми к исполнению.
func TestWaitTasksApprovedKeepsBoard(t *testing.T) {
	ctx := context.Background()
	_, store := newKanbanRunner(t)

	epic := &board.Epic{TaskSpec: board.TaskSpec{TaskID: "E-03", Title: "Эпик"}}
	if err := store.CreateEpic(ctx, epic); err != nil {
		t.Fatal(err)
	}
	task := &board.Task{TaskSpec: board.TaskSpec{TaskID: "T-03", Title: "Задача"}, EpicID: "E-03"}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	for _, st := range []board.Status{board.StatusAnalysis, board.StatusReady} {
		if err := store.SetTaskStatus(ctx, "T-03", st); err != nil {
			t.Fatalf("переход в %s: %v", st, err)
		}
	}

	kr := NewKanbanRunner(nil, store)
	kr.SetGate(approving())
	if err := kr.waitReadyTasks(ctx); err != nil {
		t.Fatalf("waitReadyTasks: %v", err)
	}

	if epics, _ := store.ListEpics(ctx); len(epics) != 1 {
		t.Fatalf("эпик не должен удаляться при утверждении: %d", len(epics))
	}
	if tasks, _ := store.ListTasks(ctx); len(tasks) != 1 {
		t.Fatalf("задача не должна удаляться при утверждении: %d", len(tasks))
	}
}
