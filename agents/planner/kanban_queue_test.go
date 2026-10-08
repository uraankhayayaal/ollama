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

// TestKanbanLeadQueueWaitsUntilTasksDone: декомпозиция идёт по очереди. Пока у
// декомпозированного эпика есть незавершённые задачи, следующий эпик лиду не
// выдаётся; после их завершения — следующий эпик берётся в работу.
func TestKanbanLeadQueueWaitsUntilTasksDone(t *testing.T) {
	ctx := context.Background()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-queue"})
	t.Cleanup(func() { _ = os.RemoveAll(projects.ProjectDir("kanban-queue")) })

	if err := store.SaveMeta(ctx, &board.Meta{ProjectName: "kanban-queue", Task: "x", Status: board.StatusInProgress}); err != nil {
		t.Fatal(err)
	}
	// Работающий эпик: декомпозирован ранее, задача ещё в работе.
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "BE-01", Title: "Бэкенд", Description: "x",
			AssignedRole: "Backend Lead", SequenceOrder: 1, CanRunParallel: true},
		Status:        board.StatusInProgress,
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
	for _, st := range []board.Status{board.StatusAnalysis, board.StatusReady, board.StatusInProgress} {
		if err := store.SetTaskStatus(ctx, "B-01", st); err != nil {
			t.Fatal(err)
		}
	}
	// Ожидающий эпик QA Lead: должен декомпозироваться только после B-01.
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "QA-01", Title: "QA-план", Description: "x",
			AssignedRole: "QA Lead", SequenceOrder: 1},
	}); err != nil {
		t.Fatal(err)
	}

	kr := NewKanbanRunner(&kanbanProvider{}, store)

	// Пока B-01 в работе — следующий эпик лиду не выдаётся.
	progress, err := kr.phaseLeads(ctx)
	if err != nil {
		t.Fatalf("phaseLeads: %v", err)
	}
	if progress {
		t.Fatal("phaseLeads не должен выдавать декомпозицию, пока есть незавершённые задачи")
	}
	qa, _ := store.GetEpic(ctx, "QA-01")
	if len(qa.Tasks) != 0 {
		t.Fatalf("QA-эпик декомпозирован раньше времени: %v", qa.Tasks)
	}

	// Задача выполнена — волна завершена, следующая декомпозиция разрешена.
	if err := store.SetTaskStatus(ctx, "B-01", board.StatusDone); err != nil {
		t.Fatal(err)
	}
	progress, err = kr.phaseLeads(ctx)
	if err != nil {
		t.Fatalf("phaseLeads: %v", err)
	}
	if !progress {
		t.Fatal("phaseLeads должен декомпозировать QA-эпик после завершения волны задач")
	}
	qa, _ = store.GetEpic(ctx, "QA-01")
	if len(qa.Tasks) == 0 {
		t.Fatal("QA-эпик не декомпозирован после завершения предыдущей волны")
	}
}

// queueProvider — провайдер, фиксирующий порядок декомпозиций лидов: разные
// JSON-декомпозиции для бэкенд- и QA-эпика.
type queueProvider struct {
	decomposeOrder []string
}

func (p *queueProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
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
		if a, ok := agent.(*architect.Architect); ok {
			_, err := a.CallFunction(architect.SubmitBacklogToolName, queueArchitectBacklogArgs)
			if err != nil {
				return nil, err
			}
		}
		return &runner.AgentResponse{Content: ""}, nil
	}
}

func (p *queueProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
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

// TestKanbanLeadQueueOrderQALast: qalead задачи выполняются после всех задач
// разработки — в бэклоге из бэкенд- и QA-эпика первым декомпозируется бэкенд,
// QA-эпик берётся в работу только после завершения задач разработки.
func TestKanbanLeadQueueOrderQALast(t *testing.T) {
	ctx := context.Background()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-qa-last"})
	t.Cleanup(func() { _ = os.RemoveAll(projects.ProjectDir("kanban-qa-last")) })

	p := &queueProvider{}
	kr := NewKanbanRunner(p, store)
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
