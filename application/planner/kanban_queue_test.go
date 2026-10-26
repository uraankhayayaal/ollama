package planner

import (
	"ai/agents/architect"
	"ai/agents/qaengineer"
	"ai/domain"
	"ai/domain/services"
	"ai/infrastructure/board"
	"ai/runner"
	"ai/shared/projects"
	"ai/tools"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// depsQueueProvider — провайдер для проверки очереди лидов по зависимостям:
// JSON-декомпозиция с task_id, уникальным для каждого эпика.
type depsQueueProvider struct{}

func (p *depsQueueProvider) Generate(ctx context.Context, agent services.Agent) (*runner.AgentResponse, error) {
	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}
	switch {
	case strings.HasPrefix(umsg, "Декомпозируй эпик"):
		epicID := "EPIC"
		for _, id := range []string{"INDEP-1", "WAIT-1"} {
			if strings.Contains(umsg, id) {
				epicID = id
				break
			}
		}
		content := fmt.Sprintf(`{"lead_summary":"x","tasks":[{"task_id":%q,"title":"Задача","description":"x","assigned_role":"Go Developer","sequence_order":1,"can_run_parallel":true,"dependencies":[]}]}`, "T-"+epicID)
		return &runner.AgentResponse{Content: content}, nil
	case strings.HasPrefix(umsg, "Ты — специалист"):
		return &runner.AgentResponse{Content: "Задача выполнена."}, nil
	}
	return &runner.AgentResponse{Content: ""}, nil
}

func (p *depsQueueProvider) ChatOnce(context.Context, domain.ChatRequest) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

// TestKanbanLeadQueueFollowsDependencies: декомпозиция следует по цепочке
// зависимостей эпиков, а не по занятости доски: эпик с выполненными
// зависимостями разбирается даже пока задачи другого эпика в работе; эпик
// с незавершённой зависимостью ждёт её выполнения.
func TestKanbanLeadQueueFollowsDependencies(t *testing.T) {
	ctx := context.Background()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-queue"})
	t.Cleanup(func() { _ = os.RemoveAll(projects.ProjectDir("kanban-queue")) })

	if err := store.SaveMeta(ctx, &board.Meta{ProjectName: "kanban-queue", Task: "x", Status: board.StatusDevelopment}); err != nil {
		t.Fatal(err)
	}
	// Работающий эпик: декомпозирован ранее, задача ещё в работе.
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "BE-01", Title: "Бэкенд", Description: "x",
			AssignedRole: "Backend Lead", SequenceOrder: 1, CanRunParallel: true},
		Status:        board.StatusSystemAnalysis,
		LeadSyncedRev: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "B-01", Title: "Сервис", Description: "x",
			AssignedRole: "Go Developer", SequenceOrder: 1},
		EpicID: "BE-01", Assignee: "Go Developer",
	}); err != nil {
		t.Fatal(err)
	}
	for _, st := range []board.Status{board.StatusTestCases, board.StatusTestCases, board.StatusDevelopment} {
		if err := store.SetTaskStatus(ctx, "B-01", st); err != nil {
			t.Fatal(err)
		}
	}
	// Эпик без зависимостей: разбирается параллельно с работающей волной.
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "INDEP-1", Title: "Независимый", Description: "x",
			AssignedRole: "Backend Lead", SequenceOrder: 2},
	}); err != nil {
		t.Fatal(err)
	}
	// Эпик с зависимостью от BE-01: ждёт его выполнения.
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "WAIT-1", Title: "Ждёт BE-01", Description: "x",
			AssignedRole: "Backend Lead", SequenceOrder: 3, Dependencies: []string{"BE-01"}},
	}); err != nil {
		t.Fatal(err)
	}

	kr := NewKanbanRunner(&depsQueueProvider{}, board.NewDomainStore(store))

	// Пока BE-01 в работе: WAIT-1 не разбирается (зависимость не выполнена),
	// но INDEP-1 разбирается — его зависимости пусты.
	progress, err := kr.phaseLeads(ctx)
	if err != nil {
		t.Fatalf("phaseLeads: %v", err)
	}
	if !progress {
		t.Fatal("phaseLeads должен разобрать независимый эпик, несмотря на работающую волну")
	}
	indep, _ := store.GetEpic(ctx, "INDEP-1")
	if len(indep.Tasks) == 0 {
		t.Fatal("независимый эпик не декомпозирован")
	}
	wait, _ := store.GetEpic(ctx, "WAIT-1")
	if len(wait.Tasks) != 0 {
		t.Fatalf("эпик с невыполненной зависимостью разобран раньше времени: %v", wait.Tasks)
	}

	// BE-01 выполнен — WAIT-1 снова в очереди лидов.
	if err := store.SetEpicStatus(ctx, "BE-01", board.StatusDone); err != nil {
		t.Fatal(err)
	}
	progress, err = kr.phaseLeads(ctx)
	if err != nil {
		t.Fatalf("phaseLeads: %v", err)
	}
	if !progress {
		t.Fatal("phaseLeads должен разобрать WAIT-1 после выполнения BE-01")
	}
	wait, _ = store.GetEpic(ctx, "WAIT-1")
	if len(wait.Tasks) == 0 {
		t.Fatal("WAIT-1 не декомпозирован после выполнения зависимости")
	}
}

// queueProvider — провайдер, фиксирующий порядок декомпозиций лидов: разные
// JSON-декомпозиции для бэкенд- и QA-эпика.
type queueProvider struct {
	decomposeOrder []string
}

func (p *queueProvider) Generate(ctx context.Context, agent services.Agent) (*runner.AgentResponse, error) {
	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}
	switch {
	case strings.HasPrefix(umsg, "Декомпозируй эпик"):
		if strings.Contains(umsg, "ARCH-QA") {
			p.decomposeOrder = append(p.decomposeOrder, "ARCH-QA")
			return &runner.AgentResponse{Content: qaQueueDecompositionJSON}, nil
		}
		p.decomposeOrder = append(p.decomposeOrder, "ARCH-BE")
		return &runner.AgentResponse{Content: backendQueueDecompositionJSON}, nil
	case strings.HasPrefix(umsg, "Ты — специалист"):
		// Специалист ставит задачу в done.
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
		if a, ok := agent.(*architect.Architect); ok {
			_, err := a.CallFunction(architect.SubmitBacklogToolName, queueArchitectBacklogArgs)
			if err != nil {
				return nil, err
			}
		}
		return &runner.AgentResponse{Content: ""}, nil
	}
}

func (p *queueProvider) ChatOnce(context.Context, domain.ChatRequest) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

// queueArchitectBacklogArgs — бэклог: бэкенд-эпик и QA-эпик.
var queueArchitectBacklogArgs = map[string]any{
	"architecture_summary": "Сервис: бэкенд + тестирование.",
	"tasks": []map[string]any{
		{
			"task_id":          "ARCH-BE",
			"title":            "Бэкенд-сервис",
			"description":      "Описание эпика",
			"assigned_role":    "Backend Lead",
			"sequence_order":   1,
			"can_run_parallel": true,
			"dependencies":     []string{},
		},
		{
			"task_id":          "ARCH-QA",
			"title":            "QA-план",
			"description":      "Тест-план по контрактам",
			"assigned_role":    "QA Lead",
			"sequence_order":   1,
			"can_run_parallel": true,
			"dependencies":     []string{},
		},
	},
}

const backendQueueDecompositionJSON = `{
  "lead_summary": "Бэкенд",
  "tasks": [
    {
      "task_id": "QB-01",
      "title": "Сервис",
      "description": "Контракт сервиса.",
      "assigned_role": "Go Developer",
      "sequence_order": 1,
      "can_run_parallel": true,
      "dependencies": []
    }
  ]
}`

const qaQueueDecompositionJSON = `{
  "lead_summary": "QA-план",
  "tasks": [
    {
      "task_id": "QQ-01",
      "title": "Тест-план",
      "description": "Контракт проверки.",
      "assigned_role": "QA Engineer",
      "sequence_order": 1,
      "can_run_parallel": true,
      "dependencies": []
    }
  ]
}`

// TestKanbanLeadQueueOrderQALast: задачи qaengineer выполняются после всех задач
// разработки — в бэклоге из бэкенд- и QA-эпика первым декомпозируется бэкенд,
// QA-эпик берётся в работу только после завершения задач разработки.
func TestKanbanLeadQueueOrderQALast(t *testing.T) {
	ctx := context.Background()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-qa-last"})
	t.Cleanup(func() { _ = os.RemoveAll(projects.ProjectDir("kanban-qa-last")) })

	p := &queueProvider{}
	kr := NewKanbanRunner(p, board.NewDomainStore(store))
	if err := kr.Run(ctx, "kanban-qa-last", "Сделай сервис и QA"); err != nil {
		t.Fatalf("Kanban Run: %v", err)
	}

	done, _ := store.AllDone(ctx)
	if !done {
		t.Fatal("доска должна быть решена")
	}
	if len(p.decomposeOrder) != 2 || p.decomposeOrder[0] != "ARCH-BE" || p.decomposeOrder[1] != "ARCH-QA" {
		t.Fatalf("порядок декомпозиций = %v, ожидалось [ARCH-BE ARCH-QA]", p.decomposeOrder)
	}
	be, _ := store.GetEpic(ctx, "ARCH-BE")
	qa, _ := store.GetEpic(ctx, "ARCH-QA")
	if be.Status != board.StatusDone || qa.Status != board.StatusDone {
		t.Fatalf("эпики должны быть выполнены: be=%s qa=%s", be.Status, qa.Status)
	}
}

// TestNextLeadEpicPriorityWithinPhase: ручная пересортировка (Priority) меняет
// очередь лидов только внутри фазы — инфраструктура всё равно раньше
// приложения, тестирование (QA Last) позже всей разработки, даже если у них
// больший приоритет. Внутри фазы приоритет > sequence_order > task_id.
func TestNextLeadEpicPriorityWithinPhase(t *testing.T) {
	ctx := context.Background()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-prio"})
	t.Cleanup(func() { _ = os.RemoveAll(projects.ProjectDir("kanban-prio")) })

	kr := NewKanbanRunner(&kanbanProvider{}, board.NewDomainStore(store))
	epics := []*board.Epic{
		// Тест с большим приоритетом, но фаза — последняя.
		{TaskSpec: board.TaskSpec{TaskID: "TEST-1", Title: "QA-план", AssignedRole: "QA Lead", SequenceOrder: 1}, Priority: 9},
		// Инфраструктура с малым приоритетом — но фаза первая.
		{TaskSpec: board.TaskSpec{TaskID: "INFRA-1", Title: "CI", AssignedRole: "DevOps Lead", SequenceOrder: 2}, Priority: 1},
		// Приложение: приоритет 9 → 5 → 0.
		{TaskSpec: board.TaskSpec{TaskID: "APP-2", Title: "Бэкенд 2", AssignedRole: "Backend Lead", SequenceOrder: 3}, Priority: 5},
		{TaskSpec: board.TaskSpec{TaskID: "APP-1", Title: "Бэкенд 1", AssignedRole: "Backend Lead", SequenceOrder: 3}, Priority: 9},
		{TaskSpec: board.TaskSpec{TaskID: "APP-0", Title: "Бэкенд 0", AssignedRole: "Backend Lead", SequenceOrder: 3}, Priority: 0},
	}
	for _, e := range epics {
		if err := store.CreateEpic(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	var got []string
	for range epics {
		e, _, _, err := kr.nextLeadEpic(ctx, epics)
		if err != nil {
			t.Fatal(err)
		}
		if e == nil {
			break
		}
		got = append(got, e.TaskID)
		// Убираем выданный эпик, чтобы очередь продолжала двигаться.
		remaining := epics[:0]
		for _, x := range epics {
			if x.TaskID != e.TaskID {
				remaining = append(remaining, x)
			}
		}
		epics = remaining
	}

	want := []string{"INFRA-1", "APP-1", "APP-2", "APP-0", "TEST-1"}
	if len(got) != len(want) {
		t.Fatalf("очередь лидов = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("очередь лидов = %v, want %v", got, want)
		}
	}
}
