package planner

import (
	"ai/agents"
	"ai/agents/architect"
	"ai/agents/qaengineer"
	"ai/board"
	"ai/projects"
	"ai/runner"
	"ai/tools"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// kanbanProvider — фейковый провайдер, симулирующий модель на всех этапах
// Kanban-оркестрации:
//   - архитектор: публикует бэклог реальным вызовом submit_architecture_backlog
//     (эпики попадают на доску);
//   - лид: возвращает JSON-декомпозицию эпика на задачи;
//   - специалист: возвращает текст «задача выполнена» (оркестратор сам
//     переводит задачу в «выполнена»).
type kanbanProvider struct{}

func (p *kanbanProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}
	switch {
	case strings.HasPrefix(umsg, "Декомпозируй эпик"):
		return &runner.AgentResponse{Content: leadDecompositionJSON}, nil
	case strings.HasPrefix(umsg, "Ты — специалист"):
		// Разработчик всегда ставит задачу в testing (Этап 6); тестировщик —
		// в done (Этап 7). Без явного вызова BoardSetTaskStatus задача зависла бы
		// в testing/in_progress и recoverStuckTasks вернул бы её в ready — цикл.
		if _, ok := agent.(*qaengineer.QAEngineer); ok {
			return &runner.AgentResponse{
				Content: "Тестирование пройдено.",
				ToolCalls: []tools.ToolCall{
					{Name: "BoardSetTaskStatus", Arguments: `{"status":"done"}`},
				},
			}, nil
		}
		return &runner.AgentResponse{
			Content: "Задача выполнена.",
			ToolCalls: []tools.ToolCall{
				{Name: "BoardSetTaskStatus", Arguments: `{"status":"testing"}`},
			},
		}, nil
	default:
		// Системный архитектор: реально публикуем бэклог вызовом инструмента.
		if a, ok := agent.(*architect.Architect); ok {
			_, err := a.CallFunction(architect.SubmitBacklogToolName, architectBacklogArgs)
			if err != nil {
				return nil, err
			}
		}
		return &runner.AgentResponse{Content: ""}, nil
	}
}

func (p *kanbanProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

// architectBacklogArgs — бэклог архитектора для теста: один эпик бэкенда.
var architectBacklogArgs = map[string]any{
	"architecture_summary": "Общая архитектура: monolith-бэкенд с одним сервисом.",
	"tasks": []map[string]any{
		{
			"task_id":          "ARCH-01",
			"title":            "Бэкенд-сервис",
			"description":      "Описание эпика",
			"assigned_role":    "Backend Lead",
			"sequence_order":   1,
			"can_run_parallel": true,
			"dependencies":     []string{},
		},
	},
}

// leadDecompositionJSON — декомпозиция эпика лидом: две задачи одного
// специалиста с зависимостью T-02 -> T-01 (по одной задаче на специалиста за
// цикл — вторая задача ждёт завершения первой).
const leadDecompositionJSON = `{
  "lead_summary": "Модуль сервиса",
  "tasks": [
    {
      "task_id": "T-01",
      "title": "Сервис A",
      "description": "Контракт интерфейса сервиса A.",
      "assigned_role": "Go Developer",
      "sequence_order": 1,
      "can_run_parallel": true,
      "dependencies": []
    },
    {
      "task_id": "T-02",
      "title": "Сервис B (поверх A)",
      "description": "Контракт интерфейса сервиса B.",
      "assigned_role": "Go Developer",
      "sequence_order": 2,
      "can_run_parallel": false,
      "dependencies": ["T-01"]
    }
  ]
}`

// newKanbanRunner создаёт Kanban-оркестратор поверх in-memory Redis и убирает
// за собой выходную директорию проекта (temp/<project>).
func newKanbanRunner(t *testing.T) (*KanbanRunner, *board.Store) {
	t.Helper()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-test"})

	projectDir := projects.ProjectDir("kanban-test")
	t.Cleanup(func() { _ = os.RemoveAll(projectDir) })

	return NewKanbanRunner(&kanbanProvider{}, store), store
}

func TestKanbanSolvesUserTask(t *testing.T) {
	ctx := context.Background()
	kr, store := newKanbanRunner(t)

	if err := kr.Run(ctx, "kanban-test", "Сделай todo-приложение"); err != nil {
		t.Fatalf("Kanban Run: %v", err)
	}

	done, err := store.AllDone(ctx)
	if err != nil || !done {
		t.Fatalf("доска должна быть решена после полного цикла: done=%v err=%v", done, err)
	}

	meta, err := store.GetMeta(ctx)
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if meta.Status != board.StatusDone {
		t.Errorf("meta.status = %s, ожидалось done", meta.Status)
	}

	epics, _ := store.ListEpics(ctx)
	if len(epics) != 1 {
		t.Fatalf("эпиков %d, ожидалось 1", len(epics))
	}
	if epics[0].Status != board.StatusDone {
		t.Errorf("эпик не выполнен: %s", epics[0].Status)
	}
	if len(epics[0].Tasks) != 2 {
		t.Fatalf("эпик знает о %d задачах, ожидалось 2", len(epics[0].Tasks))
	}

	t1, err := store.GetTask(ctx, "T-01")
	if err != nil || t1.Status != board.StatusDone {
		t.Fatalf("T-01 должен быть выполнен: %+v err=%v", t1, err)
	}
	t2, err := store.GetTask(ctx, "T-02")
	if err != nil || t2.Status != board.StatusDone {
		t.Fatalf("T-02 должен быть выполнен: %+v err=%v", t2, err)
	}
	if t2.EpicID != "ARCH-01" || t1.EpicID != "ARCH-01" {
		t.Errorf("задачи не связаны с эпиком ARCH-01: T-01=%s T-02=%s", t1.EpicID, t2.EpicID)
	}
	if t1.Assignee == "" {
		t.Error("у задачи T-01 не назначен специалист (assignee)")
	}
}

// TestKanbanResumes: доска уже в процессе (эпик декомпозирован, одна задача
// выполнена) — повторный запуск доводит решение до конца.
func TestKanbanResumes(t *testing.T) {
	ctx := context.Background()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-test"})
	projectDir := projects.ProjectDir("kanban-test")
	t.Cleanup(func() { _ = os.RemoveAll(projectDir) })

	if err := store.SaveMeta(ctx, &board.Meta{
		ProjectName: "kanban-test",
		Task:        "Сделай todo-приложение",
		Status:      board.StatusInProgress,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{
			TaskID: "ARCH-01", Title: "Бэкенд-сервис", Description: "x",
			AssignedRole: "Backend Lead", SequenceOrder: 1, CanRunParallel: true,
		},
		Status: board.StatusAnalysis,
	}); err != nil {
		t.Fatal(err)
	}
	// T-01 уже выполнена (как будто прошлый запуск), T-02 ожидает.
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "T-01", Title: "A", Description: "x",
			AssignedRole: "Go Developer", SequenceOrder: 1, Dependencies: []string{}},
		EpicID: "ARCH-01", Assignee: "Go Developer",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskStatus(ctx, "T-01", board.StatusAnalysis); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskStatus(ctx, "T-01", board.StatusReady); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskStatus(ctx, "T-01", board.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskStatus(ctx, "T-01", board.StatusDone); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "T-02", Title: "B", Description: "x",
			AssignedRole: "Go Developer", SequenceOrder: 2, Dependencies: []string{"T-01"}},
		EpicID: "ARCH-01", Assignee: "Go Developer",
	}); err != nil {
		t.Fatal(err)
	}

	kr := NewKanbanRunner(&kanbanProvider{}, store)
	if err := kr.Run(ctx, "kanban-test", "Сделай todo-приложение"); err != nil {
		t.Fatalf("Kanban Run (resume): %v", err)
	}

	done, _ := store.AllDone(ctx)
	if !done {
		t.Fatal("после resume доска должна быть решена")
	}
	t2, _ := store.GetTask(ctx, "T-02")
	if t2.Status != board.StatusDone {
		t.Errorf("T-02 должен быть выполнен в resume: %s", t2.Status)
	}
}

// TestKanbanResumesInProgress: задача осталась «в работе» от прерванной сессии.
// Раньше перезапуск падал с «нет прогресса» (in_progress никто не исполняет,
// а она блокировала pipeline) — теперь сбрасывается в «готова к работе» и
// доводится до конца.
func TestKanbanResumesInProgress(t *testing.T) {
	ctx := context.Background()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-test"})
	projectDir := projects.ProjectDir("kanban-test")
	t.Cleanup(func() { _ = os.RemoveAll(projectDir) })

	if err := store.SaveMeta(ctx, &board.Meta{
		ProjectName: "kanban-test", Task: "Задача", Status: board.StatusInProgress,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "ARCH-01", Title: "Эпик", Description: "x",
			AssignedRole: "Backend Lead", SequenceOrder: 1, CanRunParallel: true},
		Status: board.StatusAnalysis,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "T-01", Title: "A", Description: "x",
			AssignedRole: "Go Developer", SequenceOrder: 1, Dependencies: []string{}},
		EpicID: "ARCH-01", Assignee: "Go Developer",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskStatus(ctx, "T-01", board.StatusAnalysis); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskStatus(ctx, "T-01", board.StatusReady); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskStatus(ctx, "T-01", board.StatusInProgress); err != nil {
		t.Fatal(err)
	}

	kr := NewKanbanRunner(&kanbanProvider{}, store)
	if err := kr.Run(ctx, "kanban-test", "Задача"); err != nil {
		t.Fatalf("Kanban Run (resume зависшей задачи): %v", err)
	}

	done, _ := store.AllDone(ctx)
	if !done {
		t.Fatal("доска должна быть решена после перезапуска")
	}
	t1, err := store.GetTask(ctx, "T-01")
	if err != nil || t1.Status != board.StatusDone {
		t.Fatalf("T-01 должен быть выполнен: %+v err=%v", t1, err)
	}
}

// TestKanbanNoProgressOnCancelled: отменённый эпик без задач не даёт прогресса.
func TestKanbanNoProgressOnCancelled(t *testing.T) {
	ctx := context.Background()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-test"})

	if err := store.SaveMeta(ctx, &board.Meta{
		ProjectName: "kanban-test", Task: "x", Status: board.StatusInProgress,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "ARCH-01", Title: "Отменён", AssignedRole: "Backend Lead"},
		Status:   board.StatusCancelled,
	}); err != nil {
		t.Fatal(err)
	}

	kr := NewKanbanRunner(&kanbanProvider{}, store)
	if err := kr.Run(ctx, "kanban-test", "x"); err == nil {
		t.Fatal("ожидалась ошибка «нет прогресса» при отменённом эпике")
	}
}
