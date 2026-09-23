package planner

import (
	"ai/agents"
	"ai/agents/architect"
	"ai/board"
	"ai/projects"
	"ai/runner"
	"ai/stackdetect"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// stackProvider — фейковый провайдер, симулирующий архитектора, который
// следует правилу Ф-2 «сначала DetectStack»: перед публикацией эпиков он
// вызывает инструмент DetectStack и создаёт Frontend Lead-эпик только если
// детект показал frontend-состав (roles.frontend=true). Остальные роли — как у
// kanbanProvider (лиды декомпозируют, специалисты выполняют).
type stackProvider struct {
	detectCalled bool
}

func (p *stackProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}
	switch {
	case strings.HasPrefix(umsg, "Декомпозируй эпик"):
		return &runner.AgentResponse{Content: p.leadDecomposition(umsg)}, nil
	case strings.HasPrefix(umsg, "Ты — специалист"):
		return &runner.AgentResponse{Content: "Задача выполнена."}, nil
	case strings.HasPrefix(umsg, "Проведи триаж"):
		return &runner.AgentResponse{Content: ""}, nil
	default:
		return p.architectBacklog(agent)
	}
}

// leadDecomposition — декомпозиция эпика лидом с уникальными task_id
// (ведутся от архитектурного эпика ARCH-*), чтобы эпики не конфликтовали.
func (p *stackProvider) leadDecomposition(umsg string) string {
	prefix := "S"
	if strings.Contains(umsg, "ARCH-FE") {
		prefix = "FE"
	}
	return `{
  "lead_summary": "Декомпозиция",
  "tasks": [
    {
      "task_id": "` + prefix + `-01",
      "title": "Задача 1",
      "description": "Контракт задачи.",
      "assigned_role": "Go Developer",
      "sequence_order": 1,
      "can_run_parallel": true,
      "dependencies": []
    }
  ]
}`
}

// architectBacklog — симулирует архитектора: сначала DetectStack (Ф-2), затем
// публикация бэклога по фактическим ролям.
func (p *stackProvider) architectBacklog(agent agents.Agent) (*runner.AgentResponse, error) {
	a, ok := agent.(*architect.Architect)
	if !ok {
		return &runner.AgentResponse{Content: ""}, nil
	}

	out, err := a.CallFunction(toolsDetectStack, map[string]any{})
	if err != nil {
		return nil, err
	}
	p.detectCalled = true
	roles := stackRolesFromJSON(string(out))

	tasks := []map[string]any{
		{
			"task_id":          "ARCH-BE",
			"title":            "Бэкенд-сервис",
			"description":      "Консольный Go-сервис",
			"assigned_role":    "Backend Lead",
			"sequence_order":   1,
			"can_run_parallel": true,
			"dependencies":     []string{},
		},
	}
	if roles.frontend {
		tasks = append(tasks, map[string]any{
			"task_id":          "ARCH-FE",
			"title":            "Frontend",
			"description":      "Клиентская часть",
			"assigned_role":    "Frontend Lead",
			"sequence_order":   2,
			"can_run_parallel": true,
			"dependencies":     []string{},
		})
	}
	if _, err := a.CallFunction(architect.SubmitBacklogToolName, map[string]any{
		"architecture_summary": "Консольное приложение.",
		"tasks":                tasks,
	}); err != nil {
		return nil, err
	}
	return &runner.AgentResponse{Content: ""}, nil
}

func (p *stackProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

const toolsDetectStack = "DetectStack"

func stackRolesFromJSON(s string) struct{ frontend bool } {
	// Формат ответа DetectStack: {"status":"ok","kind":"go","roles":{"frontend":bool,...},...}.
	// Ищем простыми строками (hermetic, без полного JSON-парсинга).
	out := struct{ frontend bool }{}
	if strings.Contains(s, `"frontend":true`) || strings.Contains(s, `"frontend": true`) {
		out.frontend = true
	}
	return out
}

// TestCanbanDetectStackConsoleGoNoFrontend — Ф-2 end-to-end на уровне
// оркестратора: консольное Go-приложение (temp/<проект>/go.mod + main.go) →
// архитектор детектит стек, Frontend Lead-эпик НЕ появляется; доска решается
// одним Backend-эпиком.
func TestCanbanDetectStackConsoleGoNoFrontend(t *testing.T) {
	ctx := context.Background()
	project := "kanban-stack-console"
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: project})
	t.Cleanup(func() { _ = os.RemoveAll(projects.ProjectDir(project)) })

	dir := projects.ProjectDir(project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"go.mod", "main.go"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("// console"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	p := &stackProvider{}
	kr := NewKanbanRunner(p, store)
	if err := kr.Run(ctx, project, "Сделай консольную утилиту"); err != nil {
		t.Fatalf("Kanban Run: %v", err)
	}
	if !p.detectCalled {
		t.Fatal("архитектор не вызвал DetectStack до публикации эпиков")
	}
	// Стек самого корня известен детекту (утилита с ящиком).
	if got := stackdetect.DetectKind(dir); got != stackdetect.KindGo {
		t.Fatalf("DetectKind = %s, ожидался go", got)
	}

	epics, err := store.ListEpics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range epics {
		if e.AssignedRole == "Frontend Lead" {
			t.Fatalf("консольное Go-приложение не должно создавать эпик Frontend Lead: %+v", e)
		}
	}
	if len(epics) != 1 {
		t.Fatalf("эпиков %d, ожидался 1 (только Backend)", len(epics))
	}
	done, _ := store.AllDone(ctx)
	if !done {
		t.Fatal("доска должна быть решена")
	}
}

// TestCanbanDetectStackMonorepoIncludesFrontend — Ф-2: монорепо с
// server/ + frontend/ → детект включает frontend, архитектор публикует
// Frontend Lead-эпик; доска решается.
func TestCanbanDetectStackMonorepoIncludesFrontend(t *testing.T) {
	ctx := context.Background()
	project := "kanban-stack-monorepo"
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: project})
	t.Cleanup(func() { _ = os.RemoveAll(projects.ProjectDir(project)) })

	dir := projects.ProjectDir(project)
	for _, d := range []string{"server", "frontend"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &stackProvider{}
	kr := NewKanbanRunner(p, store)
	if err := kr.Run(ctx, project, "Сделай сервис и клиент"); err != nil {
		t.Fatalf("Kanban Run: %v", err)
	}

	epics, err := store.ListEpics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var hasFE bool
	for _, e := range epics {
		if e.AssignedRole == "Frontend Lead" {
			hasFE = true
		}
	}
	if !hasFE {
		t.Fatal("монорепо с frontend/: Frontend Lead-эпик должен появиться")
	}
	done, _ := store.AllDone(ctx)
	if !done {
		t.Fatal("доска должна быть решена")
	}
}