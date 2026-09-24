package planner

import (
	"ai/agents"
	"ai/agents/architect"
	"ai/board"
	"ai/projects"
	"ai/runner"
	"ai/tools"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// fakeAskTool — герметичный аналог серверного моста askTool{Session} для
// тестов Ф-4: реализует tools.Tool, на вызов возвращает status=answered.
type fakeAskTool struct{}

func (t *fakeAskTool) Name() string { return "AskUser" }

func (t *fakeAskTool) Definition() tools.ToolDefinition {
	return tools.ToolDefinition{
		Name:        "AskUser",
		Description: "Структурно спросить пользователя при неоднозначности.",
		Parameters:  map[string]any{"type": "object"},
	}
}

func (t *fakeAskTool) Execute(map[string]any) ([]byte, error) {
	return json.Marshal(map[string]any{"status": "answered", "answers": []any{}})
}

// askFirstProvider — фейковый провайдер: архитектор (основной режим) сначала
// уточняет ТЗ у пользователя вызовом AskUser (правило Ф-4 «корректность
// задачи»), и только после ответа публикует бэклог submit_architecture_backlog.
// Остальные роли — как у kanbanProvider.
type askFirstProvider struct{}

func (p *askFirstProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
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
		a, ok := agent.(*architect.Architect)
		if !ok || a.ReviewMode || a.ReviewerMode {
			return &runner.AgentResponse{Content: ""}, nil
		}
		// AskUser должен быть инжектирован через KanbanRunner.SetArchitectExtras:
		// иначе архитектор «не спрашивает, а угадывает».
		t, okAsk := a.Tools.Get("AskUser")
		if !okAsk {
			return nil, errors.New("AskUser не инжектирован в архитектора (SetArchitectExtras не сработал)")
		}
		// Задаём вопрос ДО публикации бэклога.
		out, err := t.Execute(map[string]any{
			"questions": []map[string]any{{
				"id": "stack", "text": "Какой стек?",
				"kind": "single", "options": []map[string]any{
					{"id": "go", "label": "Go", "recommended": true},
				},
			}},
		})
		if err != nil {
			return nil, err
		}
		if !strings.Contains(string(out), `"status":"answered"`) {
			return nil, errors.New("AskUser не вернул status=answered")
		}
		if _, err := a.CallFunction(architect.SubmitBacklogToolName, architectBacklogArgs); err != nil {
			return nil, err
		}
		return &runner.AgentResponse{Content: ""}, nil
	}
}

func (p *askFirstProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

// TestKanbanArchitectAsksUserBeforeBacklog — Ф-4: архитектор задаёт уточняющий
// вопрос AskUser ДО публикации бэклога (extras переданы SetArchitectExtras);
// после ответа публикует эпики, и доска решается полностью.
func TestKanbanArchitectAsksUserBeforeBacklog(t *testing.T) {
	ctx := context.Background()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-ask"})
	t.Cleanup(func() { _ = os.RemoveAll(projects.ProjectDir("kanban-ask")) })

	ask := &fakeAskTool{}
	kr := NewKanbanRunner(&askFirstProvider{}, store)
	kr.SetArchitectExtras(ask)

	if err := kr.Run(ctx, "kanban-ask", "Сделай todo-приложение, но ТЗ противоречиво"); err != nil {
		t.Fatalf("Kanban Run: %v", err)
	}
	done, err := store.AllDone(ctx)
	if err != nil || !done {
		t.Fatalf("доска должна быть решена после AskUser -> submit_architecture_backlog: done=%v err=%v", done, err)
	}

	// AskUser действительно вызывался (прокси-счётчик), а не «пропущен».
	epics, _ := store.ListEpics(ctx)
	if len(epics) != 1 {
		t.Fatalf("эпиков %d, ожидался 1", len(epics))
	}
}

// autonomyProvider — фейковый провайдер, проверяющий автономность архитектора
// в консольном режиме (без server-мостов): AskUser НЕ в наборе инструментов
// архитектора, RAG не подключён (degrade), бэклог публикуется без падений.
type autonomyProvider struct{ noAsk, noRAG bool }

func (p *autonomyProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
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
		a, ok := agent.(*architect.Architect)
		if !ok || a.ReviewMode || a.ReviewerMode {
			return &runner.AgentResponse{Content: ""}, nil
		}
		_, okAsk := a.Tools.Get("AskUser")
		p.noAsk = !okAsk
		p.noRAG = a.RAG == nil
		if _, err := a.CallFunction(architect.SubmitBacklogToolName, architectBacklogArgs); err != nil {
			return nil, err
		}
		return &runner.AgentResponse{Content: ""}, nil
	}
}

func (p *autonomyProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

// TestKanbanArchitectAutonomousWithoutExtras — Ф-4: консольный режим (без
// SetArchitectExtras/SetRAG) — у архитектора нет AskUser и RAG, он автономно
// публикует бэклог, доска решается; падений нет (degrade).
func TestKanbanArchitectAutonomousWithoutExtras(t *testing.T) {
	ctx := context.Background()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-ask-auto"})
	t.Cleanup(func() { _ = os.RemoveAll(projects.ProjectDir("kanban-ask-auto")) })

	p := &autonomyProvider{}
	kr := NewKanbanRunner(p, store)
	if err := kr.Run(ctx, "kanban-ask-auto", "Сделай todo-приложение"); err != nil {
		t.Fatalf("Kanban Run (автономный режим): %v", err)
	}
	if !p.noAsk {
		t.Error("в автономном режиме AskUser не должен присутствовать в наборе инструментов архитектора")
	}
	if !p.noRAG {
		t.Error("в автономном режиме RAG должен быть nil (degrade)")
	}
	done, _ := store.AllDone(ctx)
	if !done {
		t.Fatal("доска должна быть решена в автономном режиме")
	}
}

// TestSetArchitectExtrasIdempotent — повторный вызов SetArchitectExtras и
// prepareArchitect не дублирует инструменты (Set.Add идемпотентен по имени).
func TestPrepareArchitectWithExtras(t *testing.T) {
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-prep"})
	t.Cleanup(func() { _ = os.RemoveAll(projects.ProjectDir("kanban-prep")) })

	kr := NewKanbanRunner(nil, store)
	kr.SetArchitectExtras(&fakeAskTool{})
	kr.SetArchitectExtras(&fakeAskTool{})

	arch := architect.NewArchitectWithStore("kanban-prep", "Задача", store)
	arch = kr.prepareArchitect(arch)
	got := arch.GetTools()
	count := 0
	for _, td := range got {
		if td.Name == "AskUser" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("AskUser в наборе инструментов %d раз(а), ожидался 1", count)
	}
	if _, ok := arch.Tools.Get("AskUser"); !ok {
		t.Fatal("AskUser отсутствует в наборе инструментов после prepareArchitect")
	}
}
