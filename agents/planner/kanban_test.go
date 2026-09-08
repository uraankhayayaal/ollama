package planner

import (
	"ai/agents"
	"ai/agents/architect"
	"ai/agents/qaengineer"
	"ai/agents/qalead"
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
		return &runner.AgentResponse{Content: "Задача выполнена."}, nil
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

// --- Багрепорты и фаза-гейтинг ---

// bugFlowProvider — провайдер для полного потока багрепорта:
//   - архитектор (основной режим): публикует бэклог с QA-эпиком;
//   - архитектор (режим экспертизы, ReviewMode): выносит вердикт fix и создаёт
//     эпик исправления инструментом BoardReviewBugReport;
//   - QA Lead: триаж new -> confirmed инструментом BoardSetBugStatus;
//   - лиды: JSON-декомпозиция (разные задачи для бэклога и фикса);
//   - QA-специалист: при выполнении находит дефект и публикует багрепорт
//     инструментом BoardCreateBugReport.
type bugFlowProvider struct{}

func (p *bugFlowProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}
	switch {
	case strings.HasPrefix(umsg, "Декомпозируй эпик"):
		if strings.Contains(umsg, "FIX-01") {
			return &runner.AgentResponse{Content: fixDecompositionJSON}, nil
		}
		return &runner.AgentResponse{Content: qaLeadDecompositionJSON}, nil
	case strings.HasPrefix(umsg, "Ты — специалист"):
		// QA-специалист при выполнении своей задачи находит дефект и публикует
		// багрепорт инструментом доски.
		if a, ok := agent.(*qaengineer.QAEngineer); ok {
			_, err := a.CallFunction(tools.BoardCreateBug, map[string]any{
				"bug_id": "BUG-01", "title": "GET /users 500",
				"description": "При возрастании ответа 500; контракт нарушен",
				"task_id":     "Q-01", "epic_id": "ARCH-01", "reporter_role": "QA Engineer",
			})
			if err != nil {
				return nil, err
			}
		}
		return &runner.AgentResponse{Content: "Задача выполнена."}, nil
	case strings.HasPrefix(umsg, "Проведи триаж"):
		if a, ok := agent.(*qalead.QALead); ok {
			_, err := a.CallFunction(tools.BoardSetBugStatus, map[string]any{
				"bug_id": "BUG-01", "status": "confirmed",
			})
			if err != nil {
				return nil, err
			}
		}
		return &runner.AgentResponse{Content: "Триаж выполнен."}, nil
	default:
		if a, ok := agent.(*architect.Architect); ok {
			if a.ReviewMode {
				// Экспертиза: вердикт fix создаёт эпик исправления.
				_, err := a.CallFunction(tools.BoardReviewBug, map[string]any{
					"bug_id": "BUG-01", "verdict": "fix",
					"epic_task_id": "FIX-01", "epic_title": "Починить /users",
					"epic_description": "Исправить обработку ошибок БД", "epic_assigned_role": "Backend Lead",
					"epic_sequence_order": 1,
				})
				if err != nil {
					return nil, err
				}
				return &runner.AgentResponse{Content: ""}, nil
			}
			// Архитектор (основной режим): бэклог с QA-эпиком.
			_, err := a.CallFunction(architect.SubmitBacklogToolName, qaArchitectBacklogArgs)
			if err != nil {
				return nil, err
			}
		}
		return &runner.AgentResponse{Content: ""}, nil
	}
}

func (p *bugFlowProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

// qaArchitectBacklogArgs — бэклог с одним QA-эпиком (задачу выполняет
// QA-специалист, который найдёт дефект).
var qaArchitectBacklogArgs = map[string]any{
	"architecture_summary": "Бэкенд с REST API и автотестами.",
	"tasks": []map[string]any{
		{
			"task_id":          "ARCH-01",
			"title":            "Проверка контракта API",
			"description":      "Проверить /users на соответствие контракту",
			"assigned_role":    "QA Lead",
			"sequence_order":   1,
			"can_run_parallel": true,
			"dependencies":     []string{},
		},
	},
}

// qaLeadDecompositionJSON — одна QA-задача, при выполнении которой специалист
// находит дефект.
const qaLeadDecompositionJSON = `{
  "lead_summary": "Тест-план API",
  "tasks": [
    {
      "task_id": "Q-01",
      "title": "Смоук-тест /users",
      "description": "Проверить контракт эндпоинта.",
      "assigned_role": "QA Engineer",
      "sequence_order": 1,
      "can_run_parallel": true,
      "dependencies": []
    }
  ]
}`

// fixDecompositionJSON — декомпозиция эпика исправления (без пересечения
// task_id с бэклогом).
const fixDecompositionJSON = `{
  "lead_summary": "Исправление /users",
  "tasks": [
    {
      "task_id": "F-01",
      "title": "Починить обработку ошибок",
      "description": "Исправить обработку ошибок БД в хандлере.",
      "assigned_role": "Go Developer",
      "sequence_order": 1,
      "can_run_parallel": true,
      "dependencies": []
    }
  ]
}`

// TestKanbanPhaseGating: фаза-гейтинг оркестратора «инфраструктура → приложение
// → тестирование» в рамках эпика: прикладные задачи ждут инфраструктурных,
// тестовые — всех не тестовых.
func TestKanbanPhaseGating(t *testing.T) {
	ctx := context.Background()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-gating"})
	projectDir := projects.ProjectDir("kanban-gating")
	t.Cleanup(func() { _ = os.RemoveAll(projectDir) })

	kr := NewKanbanRunner(&kanbanProvider{}, store)

	if err := store.SaveMeta(ctx, &board.Meta{ProjectName: "kanban-gating", Task: "x", Status: board.StatusInProgress}); err != nil {
		t.Fatal(err)
	}
	// Пропускаем фазу архитектора/лидов: готовый эпик с задачами сразу.
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "EPIC-01", Title: "Полный стек",
			Description: "x", AssignedRole: "Backend Lead", SequenceOrder: 1, CanRunParallel: true},
		Status:        board.StatusReady,
		LeadSyncedRev: 1,
	}); err != nil {
		t.Fatal(err)
	}
	mk := func(id, role string) {
		if err := store.CreateTask(ctx, &board.Task{
			TaskSpec: board.TaskSpec{TaskID: id, Title: id, Description: "x",
				AssignedRole: role, CanRunParallel: true},
			EpicID: "EPIC-01", Assignee: role,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mk("INF-01", "DevOps Engineer")
	mk("APP-01", "Go Developer")
	mk("TST-01", "QA Engineer")

	statusOf := func(id string) board.Status {
		tk, err := store.GetTask(ctx, id)
		if err != nil {
			t.Fatalf("GetTask %s: %v", id, err)
		}
		return tk.Status
	}

	// Раунд 1: инфраструктура уходит в «готова к работе», приложение и
	// тестирование заблокированы гейтом.
	if _, err := kr.phaseReady(ctx); err != nil {
		t.Fatalf("phaseReady: %v", err)
	}
	if got := statusOf("INF-01"); got != board.StatusReady {
		t.Fatalf("INF-01 = %s, ожидалось ready", got)
	}
	if got := statusOf("APP-01"); got != board.StatusNew {
		t.Fatalf("APP-01 = %s, ожидалось new (ждёт инфраструктуру)", got)
	}
	if got := statusOf("TST-01"); got != board.StatusNew {
		t.Fatalf("TST-01 = %s, ожидалось new (ждёт прикладные)", got)
	}

	// Инфраструктура выполнена.
	for _, st := range []board.Status{board.StatusInProgress, board.StatusDone} {
		if err := store.SetTaskStatus(ctx, "INF-01", st); err != nil {
			t.Fatal(err)
		}
	}

	// Раунд 2: приложение разблокировано и доходит до «готова в работе»;
	// тестирование всё ещё ждёт.
	if _, err := kr.phaseReady(ctx); err != nil {
		t.Fatalf("phaseReady: %v", err)
	}
	if got := statusOf("APP-01"); got != board.StatusReady {
		t.Fatalf("APP-01 = %s, ожидалось ready после инфраструктуры", got)
	}
	if got := statusOf("TST-01"); got != board.StatusNew {
		t.Fatalf("TST-01 = %s, ожидалось new (ждёт прикладные)", got)
	}

	// Приложение выполнено.
	for _, st := range []board.Status{board.StatusInProgress, board.StatusDone} {
		if err := store.SetTaskStatus(ctx, "APP-01", st); err != nil {
			t.Fatal(err)
		}
	}

	// Раунд 3: тестирование разблокировано.
	if _, err := kr.phaseReady(ctx); err != nil {
		t.Fatalf("phaseReady: %v", err)
	}
	if got := statusOf("TST-01"); got != board.StatusReady {
		t.Fatalf("TST-01 = %s, ожидалось ready после прикладных", got)
	}
}

// TestKanbanBugFlow: полный конвейер багрепорта — QA находит дефект, QA Lead
// подтверждает, архитектор создаёт эпик исправления, после его выполнения
// багрепорт закрывается, задача пользователя решена.
func TestKanbanBugFlow(t *testing.T) {
	ctx := context.Background()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-bug"})
	projectDir := projects.ProjectDir("kanban-bug")
	t.Cleanup(func() { _ = os.RemoveAll(projectDir) })

	kr := NewKanbanRunner(&bugFlowProvider{}, store)
	if err := kr.Run(ctx, "kanban-bug", "Проверь контракт API и исправь дефекты"); err != nil {
		t.Fatalf("Kanban Run (bug flow): %v", err)
	}

	done, err := store.AllDone(ctx)
	if err != nil || !done {
		t.Fatalf("доска должна быть решена после потока багрепорта: done=%v err=%v", done, err)
	}

	bug, err := store.GetBugReport(ctx, "BUG-01")
	if err != nil {
		t.Fatalf("GetBugReport: %v", err)
	}
	if bug.Status != board.BugStatusFixed {
		t.Fatalf("багрепорт должен быть fixed после исправления, получено %s", bug.Status)
	}

	fix, err := store.GetEpic(ctx, "FIX-01")
	if err != nil {
		t.Fatalf("эпик исправления не создан: %v", err)
	}
	if fix.Status != board.StatusDone {
		t.Fatalf("эпик исправления должен быть выполнен, получено %s", fix.Status)
	}

	task, err := store.GetTask(ctx, "F-01")
	if err != nil || task.Status != board.StatusDone {
		t.Fatalf("задача исправления должна быть выполнена: %+v err=%v", task, err)
	}
}
