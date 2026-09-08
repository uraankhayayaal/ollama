package planner

import (
	"ai/agents"
	"ai/agents/qaengineer"
	"ai/runner"
	"ai/tools"
	"context"
	"strconv"
	"strings"
	"testing"
)

// qaLoopProvider — фейковый провайдер цикла «QA-тестирование → багрепорты →
// исправление → повторное тестирование». QA-инженер: первый вызов возвращает
// ответ с псевдо-вызовом BugReport (парсится в багрепорт), последующие —
// пустой ответ (дефектов нет). Планировщик возвращает план исправлений
// (agent = fixStepAgent). Остальные агенты — пустой успех.
type qaLoopProvider struct {
	qaPasses     int    // сколько раз вызывался QA-инженер
	fixStepAgent string // тип агента в плане исправлений (обычно "backend")
	fixScope     []string
	alwaysBugs   bool // QA всегда находит дефекты (имитация безрезультатных фиксов)
}

func (s *qaLoopProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	switch agent.(type) {
	case *qaengineer.QAEngineer:
		s.qaPasses++
		if !s.alwaysBugs && s.qaPasses > 1 {
			return &runner.AgentResponse{}, nil
		}
		return &runner.AgentResponse{Content: `BugReport(file="main.go", line=1, severity="blocker", text="ошибка: не обработан краевой случай")`}, nil

	case *Planner:
		fixAgent := s.fixStepAgent
		if fixAgent == "" {
			fixAgent = "backend"
		}
		scopeJSON := "[]"
		if len(s.fixScope) > 0 {
			scopeJSON = `["` + strings.Join(s.fixScope, `","`) + `"]`
		}
		return &runner.AgentResponse{Content: `{
			"project_name": "QALoopFix",
			"summary": "исправление по багрепортам",
			"steps": [
			  {"id": "fix` + strconv.Itoa(s.qaPasses) + `", "agent": "` + fixAgent + `", "prompt": "исправь баг", "depends_on": [], "description": "исправление", "scope": ` + scopeJSON + `}
			]
		}`}, nil
	}
	return &runner.AgentResponse{}, nil
}

func (s *qaLoopProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

// QA-инженер находит дефект → планировщик (триаж) даёт шаг backend →
// исправление выполняется → повторное тестирование дефектов не находит,
// багрепорты закрываются. Шаг qa успешно завершён.
func TestQABugLoopFixesAndPasses(t *testing.T) {
	ctx := context.Background()
	makeAcceptProject(t, "QALoopFix")

	t.Setenv("QA_FIX_ROUNDS", "3")

	plan := &Plan{
		ProjectName: "QALoopFix",
		Summary:     "тестирование",
		Steps: []Step{
			{ID: "q1", Agent: AgentQAEngineer, Prompt: "протестируй по контракту", Description: "тестирование"},
		},
	}

	rp := &qaLoopProvider{fixScope: []string{"main.go"}}
	exec := NewExecutor(rp, plan)
	if err := exec.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rp.qaPasses != 2 {
		t.Fatalf("QA должно пройти два раза (найти и подтвердить исправление), got %d", rp.qaPasses)
	}
	if !exec.completed["q1"] {
		t.Fatal("шаг тестирования должен быть отмечен завершённым")
	}
	// Шаг исправления добавлен в план и пройден.
	found := false
	for _, s := range exec.plan.Steps {
		if s.Agent == AgentBackendDev && strings.HasPrefix(s.ID, "qa-r") {
			found = true
			if !exec.completed[s.ID] {
				t.Fatalf("шаг исправления %s должен быть завершён", s.ID)
			}
		}
	}
	if !found {
		t.Fatal("не найден добавленный шаг исправления (backend) в плане")
	}
}

// Планировщик исправлений вернул шаг acceptor — цикл QA должен его
// проигнорировать и повторить тестирование, а не упасть на неподходящем типе.
func TestQABugLoopSkipsNonBackendFixes(t *testing.T) {
	ctx := context.Background()
	makeAcceptProject(t, "QALoopFix")

	t.Setenv("QA_FIX_ROUNDS", "3")

	plan := &Plan{
		ProjectName: "QALoopFix",
		Summary:     "тестирование",
		Steps: []Step{
			{ID: "q1", Agent: AgentQAEngineer, Prompt: "протестируй по контракту", Description: "тестирование"},
		},
	}

	rp := &qaLoopProvider{fixStepAgent: "acceptor"}
	exec := NewExecutor(rp, plan)
	if err := exec.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rp.qaPasses != 2 {
		t.Fatalf("ожидали повторное тестирование после пропуска acceptor-шага, got %d", rp.qaPasses)
	}
}

// Бюджет раундов исчерпан, а дефекты остались — шаг тестирования не пройден.
func TestQABugLoopExhaustsRounds(t *testing.T) {
	ctx := context.Background()
	makeAcceptProject(t, "QALoopFix")

	t.Setenv("QA_FIX_ROUNDS", "1")

	plan := &Plan{
		ProjectName: "QALoopFix",
		Summary:     "тестирование",
		Steps: []Step{
			{ID: "q1", Agent: AgentQAEngineer, Prompt: "протестируй по контракту", Description: "тестирование"},
		},
	}

	// QA всегда находит дефекты — с бюджетом 1 раунд тестирование падает.
	exec := NewExecutor(&qaLoopProvider{alwaysBugs: true}, plan)
	err := exec.Run(ctx)
	if err == nil {
		t.Fatal("ожидали ошибку: тестирование не пройдено при исчерпании раундов")
	}
	if !strings.Contains(err.Error(), "не пройдено") {
		t.Fatalf("ошибка должна говорить о непройденном тестировании, got: %v", err)
	}
}

// bugFiles: извлекает уникальные файлы багрепортов, нормализуя "./" префикс.
func TestBugFilesNormalizesAndDedups(t *testing.T) {
	files := bugFiles([]tools.BugReport{
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
	if files[1] != "internal/order/service.go" {
		t.Fatalf("files[1]: got %q, want internal/order/service.go", files[1])
	}
}
