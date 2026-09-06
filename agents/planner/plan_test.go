package planner

import (
	"strings"
	"testing"
)

func TestParsePlanJSON(t *testing.T) {
	raw := `{
  "project_name": "storageService",
  "summary": "Создать микросервис",
  "steps": [
    {"id": "s1", "agent": "codegenerator", "prompt": "Создай проект", "scope": ["src/main.go", "internal/order/"], "description": "Генерация"},
    {"id": "s2", "agent": "codereviewer", "prompt": "Проверь код", "depends_on": ["s1"], "scope": ["src/main.go"], "description": "Ревью"}
  ]
}`

	plan, err := ParsePlan(raw)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if plan.ProjectName != "storageService" {
		t.Fatalf("project_name: got %q", plan.ProjectName)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("steps: got %d", len(plan.Steps))
	}
	if plan.Steps[0].Agent != AgentCodeGenerator {
		t.Fatalf("step[0].agent: got %q", plan.Steps[0].Agent)
	}
	if len(plan.Steps[1].DependsOn) != 1 || plan.Steps[1].DependsOn[0] != "s1" {
		t.Fatalf("step[1].depends_on: got %#v", plan.Steps[1].DependsOn)
	}
	if len(plan.Steps[0].Scope) != 2 || plan.Steps[0].Scope[0] != "src/main.go" || plan.Steps[0].Scope[1] != "internal/order/" {
		t.Fatalf("step[0].scope: got %#v", plan.Steps[0].Scope)
	}
	if len(plan.Steps[1].Scope) != 1 || plan.Steps[1].Scope[0] != "src/main.go" {
		t.Fatalf("step[1].scope: got %#v", plan.Steps[1].Scope)
	}
}

func TestParsePlanMarkdownWrapper(t *testing.T) {
	raw := "```json\n{\"project_name\":\"x\",\"summary\":\"s\",\"steps\":[{\"id\":\"a\",\"agent\":\"refactor\",\"prompt\":\"p\",\"description\":\"d\"}]}\n```"

	plan, err := ParsePlan(raw)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if plan.ProjectName != "x" {
		t.Fatalf("project_name: got %q", plan.ProjectName)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("steps: got %d", len(plan.Steps))
	}
}

func TestParsePlanInvalid(t *testing.T) {
	if _, err := ParsePlan("просто текст без JSON"); err == nil {
		t.Fatal("ожидали ошибку при невалидном плане")
	}
}

// Модель вставляет в JSON-строки реальные переводы строк и возвраты каретки
// (проявлявшийся ранее баг: «invalid character '\r' in string literal»).
// ParsePlan должен это пережить и разобрать план.
func TestParsePlanToleratesControlCharsInStrings(t *testing.T) {
	raw := "```json\n" +
		"{\n" +
		"  \"project_name\": \"todo\"," +
		"  \"summary\": \"Создать апи\"\r\n," +
		"  \"steps\": [\n" +
		"    {\"id\": \"s1\", \"agent\": \"codegenerator\", \"prompt\": \"Создай структуру: go.mod\r\nСодержимое:\n1. main.go\n2. go.mod\", \"description\": \"Генерация\"}\n" +
		"  ]\n" +
		"}\n" +
		"```"

	plan, err := ParsePlan(raw)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if plan.ProjectName != "todo" {
		t.Fatalf("project_name: got %q", plan.ProjectName)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("steps: got %d", len(plan.Steps))
	}
	if !strings.Contains(plan.Steps[0].Prompt, "go.mod") || !strings.Contains(plan.Steps[0].Prompt, "main.go") {
		t.Fatalf("prompt с управляющими символами разобран неверно: %q", plan.Steps[0].Prompt)
	}
	// Управляющие символы должны быть превращены в нормальные переводы строк,
	// а не просто выброшены: содержимое промпта не должно ломаться.
	if got := strings.Count(plan.Steps[0].Prompt, "\n"); got < 2 {
		t.Fatalf("ожидали переводы строк в prompt, got %d, prompt=%q", got, plan.Steps[0].Prompt)
	}
}

func TestComputeWavesOrder(t *testing.T) {
	plan := &Plan{Steps: []Step{
		{ID: "s3", Agent: AgentCodeReviewer, DependsOn: []string{"s2"}},
		{ID: "s1", Agent: AgentCodeGenerator},
		{ID: "s2", Agent: AgentRefactor, DependsOn: []string{"s1"}},
	}}

	waves, err := plan.ComputeWaves()
	if err != nil {
		t.Fatalf("ComputeWaves: %v", err)
	}
	// s3 зависит от s2, s2 зависит от s1: волны [s1], [s2], [s3].
	if len(waves) != 3 {
		t.Fatalf("waves: got %v, want 3 волны", waves)
	}
	if len(waves[0]) != 1 || waves[0][0] != "s1" {
		t.Fatalf("waves[0]: got %v, want [s1]", waves[0])
	}
	if len(waves[1]) != 1 || waves[1][0] != "s2" {
		t.Fatalf("waves[1]: got %v, want [s2]", waves[1])
	}
	if len(waves[2]) != 1 || waves[2][0] != "s3" {
		t.Fatalf("waves[2]: got %v, want [s3]", waves[2])
	}
}

func TestComputeWavesParallel(t *testing.T) {
	plan := &Plan{Steps: []Step{
		{ID: "a", Agent: AgentCodeGenerator},
		{ID: "b", Agent: AgentCodeGenerator},
		{ID: "c", Agent: AgentRefactor, DependsOn: []string{"a", "b"}},
	}}

	waves, err := plan.ComputeWaves()
	if err != nil {
		t.Fatalf("ComputeWaves: %v", err)
	}
	// a и b независимы → одна волна; c зависит от обеих → вторая.
	if len(waves) != 2 {
		t.Fatalf("waves: got %v, want 2 волны", waves)
	}
	if len(waves[0]) != 2 {
		t.Fatalf("waves[0]: got %v, want 2 независимых шага", waves[0])
	}
	if len(waves[1]) != 1 || waves[1][0] != "c" {
		t.Fatalf("waves[1]: got %v, want [c]", waves[1])
	}
}

func TestComputeWavesCycle(t *testing.T) {
	plan := &Plan{Steps: []Step{
		{ID: "a", DependsOn: []string{"b"}},
		{ID: "b", DependsOn: []string{"a"}},
	}}
	if _, err := plan.ComputeWaves(); err == nil {
		t.Fatal("ожидали ошибку при цикле зависимостей")
	}
}

func TestComputeWavesEmpty(t *testing.T) {
	plan := &Plan{}
	waves, err := plan.ComputeWaves()
	if err != nil {
		t.Fatalf("ComputeWaves пустого плана: %v", err)
	}
	if len(waves) != 0 {
		t.Fatalf("waves: got %v, want 0", waves)
	}
}
