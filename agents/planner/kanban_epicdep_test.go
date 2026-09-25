package planner

import (
	"ai/agents"
	"ai/board"
	"ai/projects"
	"ai/runner"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// epicDepProvider — фейковый провайдер для проверки разблокировки очереди
// лидов: на «Декомпозируй эпик» отдаёт одну задачу специалисту.
type epicDepProvider struct{ decomposed []string }

func (p *epicDepProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}
	if !strings.HasPrefix(umsg, "Декомпозируй эпик") {
		return &runner.AgentResponse{Content: "Задача выполнена."}, nil
	}
	p.decomposed = append(p.decomposed, umsg)
	return &runner.AgentResponse{Content: epicDepDecompositionJSON}, nil
}

// epicDepDecompositionJSON — одна задача без зависимостей: разобранный эпик
// сразу даёт работу, которую можно выполнить.
const epicDepDecompositionJSON = `{
  "lead_summary": "Makefile",
  "tasks": [
    {
      "task_id": "BE-01",
      "title": "Написать Makefile проекта",
      "description": "Контракт целей build/lint/test/e2e/up.",
      "assigned_role": "Go Developer",
      "sequence_order": 1,
      "can_run_parallel": true,
      "dependencies": []
    }
  ]
}`

func newEpicDepRunner(t *testing.T, store *board.Store) *KanbanRunner {
	t.Helper()
	return NewKanbanRunner(&epicDepProvider{}, store)
}

// TestPhaseLeadsUnblocksEpicDependency — взаимная блокировка задачи и её
// эпика-зависимости: DOL-01 ждёт `done` эпика ARCH-01, а разобрать ARCH-01
// phaseLeads не давал, пока на доске есть незавершённые задачи → цикл падал
// «Kanban-цикл N: нет прогресса». Ожидание: лид разбирает именно ARCH-01,
// не дожидаясь выполнения DOL-01.
func TestPhaseLeadsUnblocksEpicDependency(t *testing.T) {
	ctx := context.Background()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-epicdep"})
	t.Cleanup(func() { _ = os.RemoveAll(projects.ProjectDir("kanban-epicdep")) })

	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "ARCH-01", Title: "Makefile проекта", AssignedRole: "Backend Lead", SequenceOrder: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "ARCH-04", Title: "CI/CD пайплайн", AssignedRole: "DevOps Lead", SequenceOrder: 4},
	}); err != nil {
		t.Fatal(err)
	}
	// DOL-01 уже декомпозирован и ждёт готовности эпика ARCH-01.
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{
			TaskID:       "DOL-01",
			Title:        "CI/CD: пайплайн",
			AssignedRole: "DevOps Engineer",
			Dependencies: []string{"ARCH-01"},
		},
		EpicID: "ARCH-04",
		Status: board.StatusNew,
	}); err != nil {
		t.Fatal(err)
	}

	// Доска не «пустая»: DOL-01 новая — обычная очередь лидов закрыта.
	idle, err := (&KanbanRunner{store: store}).pipelineIdle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if idle {
		t.Fatal("доска должна быть непустой (DOL-01 новая)")
	}

	kr := newEpicDepRunner(t, store)
	progress, err := kr.phaseLeads(ctx)
	if err != nil {
		t.Fatalf("phaseLeads: %v", err)
	}
	if !progress {
		t.Fatal("phaseLeads должен разобрать эпик-зависимость ARCH-01, а не вернуть «нет прогресса»")
	}
	epic, err := store.GetEpic(ctx, "ARCH-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(epic.Tasks) == 0 {
		t.Fatalf("эпик ARCH-01 остался без задач: %+v", epic)
	}
	// Разбирать больше нечего: очередь лидов снова закрыта, но блокировки нет —
	// следующий вызов не должен «разбирать» тот же эпик бесконечно.
	progress2, err := kr.phaseLeads(ctx)
	if err != nil {
		t.Fatalf("phaseLeads (повтор): %v", err)
	}
	if progress2 {
		t.Error("после разбора эпик-зависимости phaseLeads не должен снова брать его же")
	}
}
