package planner

import (
	"ai/agents"
	"ai/agents/acceptor"
	"ai/agents/codegenerator"
	"ai/runner"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fixPlanProvider — фейковый провайдер: для планировщика возвращает план
// исправлений (один шаг refactor), для остальных агентов — пустой успех.
type fixPlanProvider struct {
	plans  int // сколько раз вызывался планировщик
	fixIDs int
}

func (s *fixPlanProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	if _, ok := agent.(*Planner); ok {
		s.plans++
		s.fixIDs++
		project := "AcceptLoopFix"
		if p, ok := agent.(*Planner); ok {
			project = p.ProjectName
		}
		return &runner.AgentResponse{Content: `{
			"project_name": "` + project + `",
			"summary": "исправление по приёмке",
			"steps": [
			  {"id": "fix` + strconv.Itoa(s.fixIDs) + `", "agent": "refactor", "prompt": "исправь проблему сборки", "project_name": "` + project + `", "depends_on": [], "description": "исправление", "scope": []}
			]
		}`}, nil
	}
	return &runner.AgentResponse{}, nil
}

func (s *fixPlanProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

// makeAcceptProject создаёт директорию временного проекта приёмки и
// регистрирует её очистку после теста.
func makeAcceptProject(t *testing.T, name string) string {
	t.Helper()
	dir := codegenerator.ProjectDir(name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Clean(dir)) })
	return dir
}

// План, состоящий только из шага acceptor, должен успешно пройти приёмку,
// если сборка и запуск зелёные (вердикт approve, без цикла исправлений).
func TestAcceptanceLoopApprove(t *testing.T) {
	ctx := context.Background()
	makeAcceptProject(t, "AcceptLoopHappy")

	t.Setenv("ACCEPT_BUILD_CMD", "true")
	t.Setenv("ACCEPT_RUN_CMD", "true")
	t.Setenv("ACCEPT_MAX_ROUNDS", "3")
	t.Setenv("CODEGEN_MAX_REPAIR_ROUNDS", "0")

	plan := &Plan{
		ProjectName: "AcceptLoopHappy",
		Summary:     "приёмка",
		Steps: []Step{
			{ID: "a1", Agent: AgentAcceptor, Prompt: "приёмка", Description: "приёмка"},
		},
	}

	exec := NewExecutor(&fixPlanProvider{}, plan)
	if err := exec.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rep := exec.acceptReports["a1"]
	if rep == nil {
		t.Fatal("не найден отчёт приёмки для шага a1")
	}
	if rep.Verdict != acceptor.VerdictApprove {
		t.Fatalf("вердикт: %s, ожидали approve (%s)", rep.Verdict, rep.Summary)
	}
}

// План, в котором приёмка падает по сборке: исполнитель передаёт отчёт
// планировщику, тот возвращает шаг refactor, а повторная приёмка по-прежнему
// падает. С бюджетом в 1 раунд Run должен завершиться ошибкой «не пройдена».
func TestAcceptanceLoopRejectsAfterRound(t *testing.T) {
	ctx := context.Background()
	makeAcceptProject(t, "AcceptLoopFix")

	t.Setenv("ACCEPT_BUILD_CMD", "false")
	t.Setenv("ACCEPT_MAX_ROUNDS", "1")
	t.Setenv("CODEGEN_MAX_REPAIR_ROUNDS", "0")

	plan := &Plan{
		ProjectName: "AcceptLoopFix",
		Summary:     "приёмка с падающей сборкой",
		Steps: []Step{
			{ID: "a1", Agent: AgentAcceptor, Prompt: "приёмка", Description: "приёмка"},
		},
	}

	fp := &fixPlanProvider{}
	exec := NewExecutor(fp, plan)
	err := exec.Run(ctx)
	if err == nil {
		t.Fatal("ожидали ошибку: приёмка не пройдена после раунда исправлений")
	}
	if !strings.Contains(err.Error(), "не пройдена") {
		t.Fatalf("ошибка должна говорить о непройденной приёмке, got: %v", err)
	}
	if fp.plans != 1 {
		t.Fatalf("планировщик исправлений должен вызываться ровно 1 раз, got %d", fp.plans)
	}
	rep := exec.acceptReports["a1"]
	if rep == nil || rep.Verdict != acceptor.VerdictReject {
		t.Fatalf("отчёт приёмки должен иметь вердикт reject, got %#v", rep)
	}
}

// Если планировщик исправлений не вернул план — цикл приёмки падает с
// понятной ошибкой, а не молча продолжает.
func TestAcceptanceLoopEmptyFixPlan(t *testing.T) {
	ctx := context.Background()
	makeAcceptProject(t, "AcceptLoopEmpty")

	t.Setenv("ACCEPT_BUILD_CMD", "false")
	t.Setenv("ACCEPT_MAX_ROUNDS", "3")
	t.Setenv("CODEGEN_MAX_REPAIR_ROUNDS", "0")

	plan := &Plan{
		ProjectName: "AcceptLoopEmpty",
		Summary:     "пустой план исправлений",
		Steps: []Step{
			{ID: "a1", Agent: AgentAcceptor, Prompt: "приёмка", Description: "приёмка"},
		},
	}

	// Провайдер возвращает пустую историю: ParsePlan завершится ошибкой.
	exec := NewExecutor(&stubProvider{}, plan)
	err := exec.Run(ctx)
	if err == nil {
		t.Fatal("ожидали ошибку при пустом плане исправлений")
	}
	if !strings.Contains(err.Error(), "исправлений") {
		t.Fatalf("ошибка должна упоминать исправления, got: %v", err)
	}
}
