package planner

import (
	"ai/agents/architect"
	"ai/agents/businessanalyst"
	"ai/agents/qaengineer"
	"ai/domain"
	"ai/domain/services"
	"ai/infrastructure/board"
	"ai/runner"
	"ai/shared/projects"
	"ai/tools"
	"context"
	"encoding/json"
	"fmt"
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

func (p *kanbanProvider) Generate(ctx context.Context, agent services.Agent) (*runner.AgentResponse, error) {
	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}
	switch {
	case strings.HasPrefix(umsg, "Декомпозируй эпик"):
		return &runner.AgentResponse{Content: leadDecompositionJSON}, nil
	case strings.HasPrefix(umsg, "Ты — специалист"):
		var resp *runner.AgentResponse
		if _, ok := agent.(*qaengineer.QAEngineer); ok {
			resp = &runner.AgentResponse{
				Content: "Тестирование пройдено.",
				ToolCalls: []tools.ToolCall{
					{Name: "BoardSetTaskStatus", Arguments: `{"status":"done"}`},
				},
			}
		} else {
			resp = &runner.AgentResponse{
				Content: "Задача выполнена.",
				ToolCalls: []tools.ToolCall{
					{Name: "BoardSetTaskStatus", Arguments: `{"status":"done"}`},
				},
			}
		}
		executeBoardToolCalls(agent, resp)
		return resp, nil
	default:
		// Бизнес-аналитик: переводит эпик в system_analysis
		if ba, ok := agent.(*businessanalyst.BusinessAnalyst); ok {
			epicID := ""
			for _, m := range ba.GetUserMessages() {
				if idx := strings.Index(m.Message, "ID: "); idx >= 0 {
					lineEnd := strings.Index(m.Message[idx:], "\n")
					if lineEnd > 0 {
						epicID = strings.TrimSpace(m.Message[idx+4 : idx+lineEnd])
					}
					break
				}
			}
			if epicID != "" {
				_, _ = ba.CallFunction(tools.BoardSetEpicStatus, map[string]any{"epic_id": epicID, "status": "system_analysis"})
			}
			return &runner.AgentResponse{Content: "Требования уточнены."}, nil
		}
		// Архитектор: два режима — создание бэклога (эпики) и декомпозиция (задачи)
		if a, ok := agent.(*architect.Architect); ok {
			if epicID := extractArchitectEpicID(a.GetUserMessages()); epicID != "" {
				return &runner.AgentResponse{Content: architectDecomposeForEpic(epicID)}, nil
			}
			for _, m := range a.GetUserMessages() {
				if strings.Contains(m.Message, "Разбьёп эпик") {
					return &runner.AgentResponse{Content: architectDecomposeJSON}, nil
				}
			}
			// Создание бэклога (первичный архитектор)
			_, err := a.CallFunction(architect.SubmitBacklogToolName, architectBacklogArgs)
			if err != nil {
				return nil, err
			}
		}
		return &runner.AgentResponse{Content: ""}, nil
	}
}

func (p *kanbanProvider) ChatOnce(context.Context, domain.ChatRequest) (*runner.ModelReply, error) {
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

// architectDecomposeJSON — декомпозиция эпика архитектором: две задачи
// (первая — без петли, вторая — с петлёй для тестов эскалации).
// Устаревший вариант для одного эпика CALC-01.
const architectDecomposeJSON = `{
  "tasks": [
    {
      "task_id": "T-01",
      "title": "Задача 1",
      "description": "Контракт задачи.",
      "epic_id": "CALC-01",
      "assigned_role": "Go Developer",
      "sequence_order": 1,
      "can_run_parallel": true,
      "dependencies": []
    },
    {
      "task_id": "T-02",
      "title": "Задача 2",
      "description": "Контракт задачи.",
      "epic_id": "CALC-01",
      "assigned_role": "Go Developer",
      "sequence_order": 2,
      "can_run_parallel": false,
      "dependencies": ["T-01"]
    }
  ]
}`

// architectDecomposeForEpic генерирует декомпозиционный JSON с уникальными
// ID задач, привязанными к конкретному эпику.
func architectDecomposeForEpic(epicID string) string {
	task1 := epicID + "-T1"
	task2 := epicID + "-T2"
	return fmt.Sprintf(`{
  "tasks": [
    {
      "task_id": "%s",
      "title": "Задача 1",
      "description": "Контракт задачи.",
      "epic_id": "%s",
      "assigned_role": "Go Developer",
      "sequence_order": 1,
      "can_run_parallel": true,
      "dependencies": []
    },
    {
      "task_id": "%s",
      "title": "Задача 2",
      "description": "Контракт задачи.",
      "epic_id": "%s",
      "assigned_role": "Go Developer",
      "sequence_order": 2,
      "can_run_parallel": false,
      "dependencies": ["%s"]
    }
  ]
}`, task1, epicID, task2, epicID, task1)
}

// extractArchitectEpicID извлекает ID эпика из сообщений архитектора.
func extractArchitectEpicID(userMsgs []domain.AgentMessage) string {
	prefix := "Эпик: "
	for _, m := range userMsgs {
		idx := strings.Index(m.Message, prefix)
		if idx < 0 {
			continue
		}
		rest := m.Message[idx+len(prefix):]
		dash := strings.Index(rest, " —")
		if dash < 0 {
			dash = len(rest)
		}
		return strings.TrimSpace(rest[:dash])
	}
	return ""
}

// leadDecompositionJSON — декомпозиция эпика архитектором: две задачи одного
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

	return NewKanbanRunner(&kanbanProvider{}, board.NewDomainStore(store)), store
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

	t1, err := store.GetTask(ctx, "ARCH-01-T1")
	if err != nil || t1.Status != board.StatusDone {
		t.Fatalf("ARCH-01-T1 должен быть выполнен: %+v err=%v", t1, err)
	}
	t2, err := store.GetTask(ctx, "ARCH-01-T2")
	if err != nil || t2.Status != board.StatusDone {
		t.Fatalf("ARCH-01-T2 должен быть выполнен: %+v err=%v", t2, err)
	}
	if t2.EpicID != "ARCH-01" || t1.EpicID != "ARCH-01" {
		t.Errorf("задачи не связаны с эпиком ARCH-01: ARCH-01-T1=%s ARCH-01-T2=%s", t1.EpicID, t2.EpicID)
	}
	if t1.Assignee == "" {
		t.Error("у задачи ARCH-01-T1 не назначен специалист (assignee)")
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
		Status:      board.StatusDevelopment,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{
			TaskID: "ARCH-01", Title: "Бэкенд-сервис", Description: "x",
			AssignedRole: "Backend Lead", SequenceOrder: 1, CanRunParallel: true,
		},
		Status: board.StatusSystemAnalysis,
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
	if err := store.SetTaskStatus(ctx, "T-01", board.StatusTestCases); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskStatus(ctx, "T-01", board.StatusDevelopment); err != nil {
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

	kr := NewKanbanRunner(&kanbanProvider{}, board.NewDomainStore(store))
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
		ProjectName: "kanban-test", Task: "Задача", Status: board.StatusDevelopment,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "ARCH-01", Title: "Эпик", Description: "x",
			AssignedRole: "Backend Lead", SequenceOrder: 1, CanRunParallel: true},
		Status: board.StatusSystemAnalysis,
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
	if err := store.SetTaskStatus(ctx, "T-01", board.StatusTestCases); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskStatus(ctx, "T-01", board.StatusDevelopment); err != nil {
		t.Fatal(err)
	}

	kr := NewKanbanRunner(&kanbanProvider{}, board.NewDomainStore(store))
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
		ProjectName: "kanban-test", Task: "x", Status: board.StatusDevelopment,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "ARCH-01", Title: "Отменён", AssignedRole: "Backend Lead"},
		Status:   board.StatusCancelled,
	}); err != nil {
		t.Fatal(err)
	}

	kr := NewKanbanRunner(&kanbanProvider{}, board.NewDomainStore(store))
	if err := kr.Run(ctx, "kanban-test", "x"); err == nil {
		t.Fatal("ожидалась ошибка «нет прогресса» при отменённом эпике")
	}
}

// taskIDFromPrompt достаёт идентификатор задачи из промпта специалиста
// (planner.taskPrompt: «Задача на доске: <ID> — <title>»). Тестовые провайдеры
// подставляют его в BoardSetTaskStatus — боевая модель берёт ID оттуда же.
func taskIDFromPrompt(umsg string) string {
	const marker = "Задача на доске: "
	i := strings.Index(umsg, marker)
	if i < 0 {
		return ""
	}
	rest := umsg[i+len(marker):]
	if j := strings.Index(rest, " — "); j >= 0 {
		rest = rest[:j]
	} else if j := strings.IndexAny(rest, " \n\t"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// executeBoardToolCalls исполняет инструменты доски (Board*), возвращённые
// фейковым провайдером, — так же, как это делает боевой агентский цикл
// (llm.OllamaProvider.Generate → usecases.RunAgent → runner.Generate).
// Ошибки перехода (например, отказ гарда «phantom done») намеренно
// игнорируются: оркестратор сам разбирает итоговый статус задачи.
func executeBoardToolCalls(agent services.Agent, resp *runner.AgentResponse) {
	if resp == nil || agent == nil {
		return
	}
	for _, tc := range resp.ToolCalls {
		if !strings.HasPrefix(tc.Name, "Board") {
			continue
		}
		args := map[string]any{}
		if strings.TrimSpace(tc.Arguments) != "" {
			if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
				continue
			}
		}
		if _, ok := args["task_id"]; !ok {
			if us := agent.GetUserMessages(); len(us) > 0 {
				if id := taskIDFromPrompt(us[0].Message); id != "" {
					args["task_id"] = id
				}
			}
		}
		// FSM-пропуск для тестов: BoardSetTaskStatus("done") из StatusInfra
		// блокируется (FSM: infra->test_cases->development->done).
		// Обходим, прогнав статус через цепочку, затем PatchTask(done) напрямую,
		// чтобы обходить handoffGate (git/build-проверки), недоступный в тестах.
		if tc.Name == "BoardSetTaskStatus" {
			if st, _ := args["status"].(string); st == string(domain.StatusDone) {
				if id, ok := args["task_id"].(string); ok && id != "" {
					if store := agentBoardStore(agent); store != nil {
						// Переводим в StatusDevelopment (FSM-валидный путь)
						_ = store.SetTaskStatus(context.Background(), id, domain.StatusTestCases)
						_ = store.SetTaskStatus(context.Background(), id, domain.StatusDevelopment)
						// PatchTask(done) — обход ValidateTransition и handoffGate
						_ = store.PatchTask(context.Background(), id, func(t *domain.Task) error {
							t.Status = domain.StatusDone
							return nil
						})
						continue
					}
				}
			}
		}
		_, _ = agent.CallFunction(tc.Name, args)
	}
}

// agentBoardStore пытается извлечь BoardStore из агента.
// Возвращает nil, если агент не имеет доски.
func agentBoardStore(agent services.Agent) services.BoardStore {
	type boardStoreGetter interface {
		GetBoard() services.BoardStore
	}
	if g, ok := agent.(boardStoreGetter); ok {
		return g.GetBoard()
	}
	return nil
}
