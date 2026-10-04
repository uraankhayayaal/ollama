package planner

import (
	"ai/agents"
	"ai/agents/architect"
	"ai/board"
	"ai/runner"
	"context"
	"strings"
	"testing"

	"ai/projects"
	"github.com/alicebob/miniredis/v2"
	"os"
)

// loopedProvider повторяет поведение kanbanProvider (архитектор публикует
// бэклог, лид декомпозирует эпик), но специалиста заставляет ЗАЦИКЛИТЬСЯ:
// цикл прерван детектором, задача не выполнена.
type loopedProvider struct{}

func (p *loopedProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}
	switch {
	case strings.HasPrefix(umsg, "Декомпозируй эпик"):
		return &runner.AgentResponse{Content: leadDecompositionJSON}, nil
	case strings.HasPrefix(umsg, "Ты — специалист"):
		return &runner.AgentResponse{
			Content:    "пробую снова",
			Looped:     true,
			LoopReason: "вызов WriteFiles повторён 3 раз с одними и теми же аргументами",
			Rounds:     6,
		}, nil
	default:
		if a, ok := agent.(*architect.Architect); ok {
			if _, err := a.CallFunction(architect.SubmitBacklogToolName, architectBacklogArgs); err != nil {
				return nil, err
			}
		}
		return &runner.AgentResponse{Content: ""}, nil
	}
}

func (p *loopedProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

func newLoopedKanbanRunner(t *testing.T) (*KanbanRunner, *board.Store) {
	t.Helper()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-loop"})

	projectDir := projects.ProjectDir("kanban-loop")
	t.Cleanup(func() { _ = os.RemoveAll(projectDir) })

	return NewKanbanRunner(&loopedProvider{}, store), store
}

// Специалист зациклился: задача НЕ помечается выполненной (тихий fallback
// «выполнена» скрыл бы потерю работы), а запуск падает с понятной причиной —
// чтобы задачу взяли с другой стороны.
func TestKanbanTaskLoopFailsInsteadOfMarkingDone(t *testing.T) {
	ctx := context.Background()
	kr, store := newLoopedKanbanRunner(t)

	err := kr.Run(ctx, "kanban-loop", "Сделай todo-приложение")
	if err == nil {
		t.Fatal("зацикливание специалиста должно останавливать запуск")
	}
	if !strings.Contains(err.Error(), "зациклился") {
		t.Fatalf("ошибка должна называть зацикливание, got %v", err)
	}
	if !strings.Contains(err.Error(), "WriteFiles") {
		t.Fatalf("ошибка должна нести причину петли, got %v", err)
	}

	tasks, terr := store.ListTasks(ctx)
	if terr != nil {
		t.Fatalf("ListTasks: %v", terr)
	}
	if len(tasks) == 0 {
		t.Fatal("задача должна существовать на доске")
	}
	for _, task := range tasks {
		if task.Status == board.StatusDone {
			t.Fatalf("задача %s не должна считаться выполненной при зацикливании: %+v", task.TaskID, task.Status)
		}
	}
}

// Зацикливание архитектора: эпики не публикуются, и запуск падает сразу на
// своей фазе, а не идёт дальше по конвейеру с пустой доской.
func TestKanbanArchitectLoopFails(t *testing.T) {
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-arch-loop"})
	projectDir := projects.ProjectDir("kanban-arch-loop")
	t.Cleanup(func() { _ = os.RemoveAll(projectDir) })

	kr := NewKanbanRunner(&architectLoopProvider{}, store)

	err := kr.Run(context.Background(), "kanban-arch-loop", "Сделай сервис")
	if err == nil {
		t.Fatal("зацикливание архитектора должно останавливать запуск")
	}
	if !strings.Contains(err.Error(), "зациклился") {
		t.Fatalf("ошибка должна называть зацикливание, got %v", err)
	}
	if epics, eerr := store.ListEpics(context.Background()); eerr != nil {
		t.Fatalf("ListEpics: %v", eerr)
	} else if len(epics) != 0 {
		t.Fatalf("при зацикливании архитектора эпиков быть не должно, got %d", len(epics))
	}
}

// architectLoopProvider — архитектор зациклился с первого же вызова.
type architectLoopProvider struct{}

func (p *architectLoopProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	return &runner.AgentResponse{
		Looped:     true,
		LoopReason: "последние 5 раундов подряд не дали ни одного успешного действия",
		Rounds:     6,
	}, nil
}

func (p *architectLoopProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}
