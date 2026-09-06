package planner

import (
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

func TestTopoSortOrder(t *testing.T) {
	plan := &Plan{Steps: []Step{
		{ID: "s3", Agent: AgentCodeReviewer, DependsOn: []string{"s2"}},
		{ID: "s1", Agent: AgentCodeGenerator},
		{ID: "s2", Agent: AgentRefactor, DependsOn: []string{"s1"}},
	}}

	e := NewExecutor(nil, plan)
	order, err := e.topoSort()
	if err != nil {
		t.Fatalf("topoSort: %v", err)
	}
	// s3 зависит от s2, s2 зависит от s1 → порядок s1, s2, s3.
	expect := []string{"s1", "s2", "s3"}
	for i := range expect {
		if order[i] != expect[i] {
			t.Fatalf("order: got %v, want %v", order, expect)
		}
	}
}

func TestTopoSortCycle(t *testing.T) {
	plan := &Plan{Steps: []Step{
		{ID: "a", DependsOn: []string{"b"}},
		{ID: "b", DependsOn: []string{"a"}},
	}}
	e := NewExecutor(nil, plan)
	if _, err := e.topoSort(); err == nil {
		t.Fatal("ожидали ошибку при цикле зависимостей")
	}
}