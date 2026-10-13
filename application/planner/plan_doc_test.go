package planner

import (
	"ai/board"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderPlanDocBuildsNestedSections — PLAN.md содержит шаги планировщика,
// а декомпозиции лидов вложены внутрь соответствующих шагов с контрактами.
func TestRenderPlanDocBuildsNestedSections(t *testing.T) {
	plan := &Plan{
		ProjectName: "demo",
		Summary:     "Реализация сервиса хранения.",
		Steps: []Step{
			{
				ID:          "s1",
				Agent:       AgentBackendLead,
				Description: "декомпозиция бэкенда",
				Prompt:      "Спроектируй модули",
				Scope:       []string{"server/"},
				Tasks: []board.TaskSpec{
					{
						TaskID:          "BEL-01",
						Title:           "хранение",
						Description:     "type Store interface { Get(id string) (*Item, error) }\ntype Item struct { ID string; Name string }",
						AssignedRole:    "Senior Go Developer",
						SequenceOrder:   1,
						CanRunParallel:  true,
						Dependencies:    nil,
					},
					{
						TaskID:          "BEL-02",
						Title:           "ручка",
						Description:     "GET /api/items/{id} -> 200 {item}, 404 {error}",
						AssignedRole:    "Senior Go Developer",
						SequenceOrder:   2,
						CanRunParallel:  false,
						Dependencies:    []string{"BEL-01"},
					},
				},
			},
			{
				ID:          "s2",
				Agent:       AgentBackendDev,
				Description: "правка",
				Prompt:      "Доработай",
			},
		},
	}

	doc := renderPlanDoc(plan.ProjectName, plan)

	// Заголовок и сводка плана.
	if !strings.Contains(doc, "# План работ — demo") {
		t.Errorf("нет заголовка плана:\n%s", doc)
	}
	if !strings.Contains(doc, "Реализация сервиса хранения.") {
		t.Errorf("нет сводки плана:\n%s", doc)
	}
	// Шаг планировщика на верхнем уровне.
	if !strings.Contains(doc, "## Шаг 1 [Backend-лид] — декомпозиция бэкенда") {
		t.Errorf("нет секции шага планировщика:\n%s", doc)
	}
	// Задача лида вложена внутрь шага, до секции следующего шага.
	leadPos := strings.Index(doc, "#### 1.1 BEL-01 — хранение")
	if leadPos < 0 {
		t.Fatalf("нет задачи лида внутри шага:\n%s", doc)
	}
	nextStepPos := strings.Index(doc, "## Шаг 2 ")
	if nextStepPos < 0 {
		t.Fatalf("нет шага 2:\n%s", doc)
	}
	if leadPos > nextStepPos {
		t.Errorf("задача лида должна быть ДО секции шага 2:\n%s", doc)
	}
	// Контракт (типы/интерфейс) из описания задачи попал в документ.
	if !strings.Contains(doc, "type Store interface { Get(id string)") {
		t.Errorf("в документ не попал контракт задачи:\n%s", doc)
	}
	if !strings.Contains(doc, "GET /api/items/{id}") {
		t.Errorf("в документ не попал API-контракт:\n%s", doc)
	}
	// Шаг без декомпозиции помечается как ожидающий.
	if !strings.Contains(doc, "Декомпозиция ещё не выполнена") {
		t.Errorf("шаг без задач должен быть помечен:\n%s", doc)
	}
}

// TestWritePlanDocCreatesFile — writePlanDoc создаёт PLAN.md в корне проекта.
func TestWritePlanDocCreatesFile(t *testing.T) {
	name := "plandocproj"
	plan := &Plan{
		ProjectName: name,
		Summary:     "тест",
		Steps: []Step{
			{ID: "s1", Agent: AgentBackendLead, Description: "декомпозиция", Tasks: nil},
		},
	}
	root := planDocPath(name)
	defer os.RemoveAll(filepath.Dir(root))

	if err := writePlanDoc(name, plan); err != nil {
		t.Fatalf("writePlanDoc: %v", err)
	}
	data, err := os.ReadFile(root)
	if err != nil {
		t.Fatalf("read PLAN.md: %v", err)
	}
	if !strings.Contains(string(data), "# План работ — plandocproj") {
		t.Errorf("PLAN.md не содержит заголовок:\n%s", data)
	}
}