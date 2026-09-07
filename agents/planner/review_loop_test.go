package planner

import (
	"ai/agents"
	"ai/agents/codereviewer"
	"ai/forges"
	"ai/runner"
	"context"
	"strconv"
	"strings"
	"testing"
)

// reviewLoopProvider — фейковый провайдер цикла «ревью → исправление → ревью».
// codereviewer: первый вызов возвращает текстовое ревью с псевдо-вызовом
// ReviewMr (публикует замечания через ParseTextReview), последующие — пустой
// ответ (замечаний нет). Планировщик возвращает план исправлений (refactor).
// Остальные агенты — пустой успех.
type reviewLoopProvider struct {
	reviews      int    // сколько раз вызывался codereviewer
	fixStepAgent string // тип агента в плане исправлений (обычно "refactor")
	fixScope     []string
}

func (s *reviewLoopProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	switch agent.(type) {
	case *codereviewer.Codereviewer:
		s.reviews++
		if s.reviews == 1 {
			return &runner.AgentResponse{Content: `ReviewMr(file_path="main.go", line=1, text="ошибка: не обработан краевой случай")`}, nil
		}
		return &runner.AgentResponse{}, nil

	case *Planner:
		fixAgent := s.fixStepAgent
		if fixAgent == "" {
			fixAgent = "refactor"
		}
		scopeJSON := "[]"
		if len(s.fixScope) > 0 {
			scopeJSON = `["` + strings.Join(s.fixScope, `","`) + `"]`
		}
		return &runner.AgentResponse{Content: `{
			"project_name": "ReviewLoopFix",
			"summary": "исправление по ревью",
			"steps": [
			  {"id": "fix` + strconv.Itoa(s.reviews) + `", "agent": "` + fixAgent + `", "prompt": "исправь замечание", "depends_on": [], "description": "исправление", "scope": ` + scopeJSON + `}
			]
		}`}, nil
	}
	return &runner.AgentResponse{}, nil
}

func (s *reviewLoopProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

// reviewAlwaysCommentsProvider — codereviewer всегда возвращает замечания.
// Имитирует ситуацию, когда исправления не помогают.
type reviewAlwaysCommentsProvider struct{}

func (s *reviewAlwaysCommentsProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	if _, ok := agent.(*codereviewer.Codereviewer); ok {
		return &runner.AgentResponse{Content: `ReviewMr(file_path="main.go", line=1, text="критично: баг в логике")`}, nil
	}
	return &runner.AgentResponse{}, nil
}

func (s *reviewAlwaysCommentsProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

// codereviewer находит замечание → планировщик даёт шаг refactor → исправление
// выполняется → повторное ревью замечаний не находит. Ревью пройдено.
func TestReviewLoopFixesAndPasses(t *testing.T) {
	ctx := context.Background()
	makeAcceptProject(t, "ReviewLoopFix")

	t.Setenv("REVIEW_FIX_ROUNDS", "3")
	t.Setenv("CODEGEN_MAX_REPAIR_ROUNDS", "0")

	plan := &Plan{
		ProjectName: "ReviewLoopFix",
		Summary:     "ревью",
		Steps: []Step{
			{ID: "r1", Agent: AgentCodeReviewer, Prompt: "ревью", Scope: []string{"main.go"}, Description: "ревью"},
		},
	}

	rp := &reviewLoopProvider{fixScope: []string{"main.go"}}
	exec := NewExecutor(rp, plan)
	if err := exec.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rp.reviews != 2 {
		t.Fatalf("ревью должно пройти два раза (найти и подтвердить исправление), got %d", rp.reviews)
	}
	if !exec.completed["r1"] {
		t.Fatal("шаг ревью должен быть отмечен завершённым")
	}
	// Шаг исправления добавлен в план и пройден.
	found := false
	for _, s := range exec.plan.Steps {
		if s.Agent == AgentRefactor && strings.HasPrefix(s.ID, "review-r") {
			found = true
			if !exec.completed[s.ID] {
				t.Fatalf("шаг исправления %s должен быть завершён", s.ID)
			}
		}
	}
	if !found {
		t.Fatal("не найден добавленный шаг исправления (refactor) в плане")
	}
}

// Планировщик исправлений вернул шаг acceptor — цикл ревью должен его
// проигнорировать и повторить ревью, а не упасть на неподходящем типе агента.
func TestReviewLoopSkipsNonRefactorFixes(t *testing.T) {
	ctx := context.Background()
	makeAcceptProject(t, "ReviewLoopFix")

	t.Setenv("REVIEW_FIX_ROUNDS", "3")
	t.Setenv("CODEGEN_MAX_REPAIR_ROUNDS", "0")

	plan := &Plan{
		ProjectName: "ReviewLoopFix",
		Summary:     "ревью",
		Steps: []Step{
			{ID: "r1", Agent: AgentCodeReviewer, Prompt: "ревью", Scope: []string{"main.go"}, Description: "ревью"},
		},
	}

	rp := &reviewLoopProvider{fixStepAgent: "acceptor"}
	exec := NewExecutor(rp, plan)
	if err := exec.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rp.reviews != 2 {
		t.Fatalf("ожидали повторное ревью после пропуска acceptor-шага, got %d", rp.reviews)
	}
}

// Бюджет раундов исчерпан, а замечания остались — ревью не пройдено.
func TestReviewLoopExhaustsRounds(t *testing.T) {
	ctx := context.Background()
	makeAcceptProject(t, "ReviewLoopFix")

	t.Setenv("REVIEW_FIX_ROUNDS", "1")
	t.Setenv("CODEGEN_MAX_REPAIR_ROUNDS", "0")

	plan := &Plan{
		ProjectName: "ReviewLoopFix",
		Summary:     "ревью",
		Steps: []Step{
			{ID: "r1", Agent: AgentCodeReviewer, Prompt: "ревью", Scope: []string{"main.go"}, Description: "ревью"},
		},
	}

	// Провайдер всегда возвращает замечания — с бюджетом 1 раунд ревью падает.
	exec := NewExecutor(&reviewAlwaysCommentsProvider{}, plan)
	err := exec.Run(ctx)
	if err == nil {
		t.Fatal("ожидали ошибку: ревью не пройдено при исчерпании раундов")
	}
	if !strings.Contains(err.Error(), "не пройдено") {
		t.Fatalf("ошибка должна говорить о непройденном ревью, got: %v", err)
	}
}

// commentFiles: извлекает уникальные файлы замечаний, нормализуя "./" префикс.
func TestCommentFilesNormalizesAndDedups(t *testing.T) {
	files := commentFiles([]forges.ReviewComment{
		{FilePath: "./src/main.go", Line: 1},
		{FilePath: "src/main.go", Line: 2},
		{FilePath: "./internal/order/service.go", Line: 5},
		{FilePath: "", Line: 3},
	})
	if len(files) != 2 {
		t.Fatalf("files: got %v, want 2 уникальных", files)
	}
	if files[0] != "src/main.go" {
		t.Fatalf("files[0]: got %q, want src/main.go", files[0])
	}
}