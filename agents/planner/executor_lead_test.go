package planner

import (
	"ai/agents"
	"ai/agents/backendlead"
	"ai/agents/frontendlead"
	"ai/projects"
	"ai/runner"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// leadDecompProvider — фейковый провайдер plan-режима: лиду возвращает
// JSON-декомпозицию, специалистам — короткое подтверждение. Записывает,
// сколько раз запускался лид и какие промпты получили специалисты.
type leadDecompProvider struct {
	leadCalls int
	specs     []string
}

const leadDecompJSON = `{
  "backend_lead_summary": "модуль сервисной части",
  "tasks": [
    {"task_id": "BEL-01", "title": "базовая структура", "description": "Создай go.mod и main.go с /health", "assigned_role": "Senior Go Developer", "sequence_order": 1, "can_run_parallel": true, "dependencies": []},
    {"task_id": "BEL-02", "title": "http handler", "description": "Добавь обработчик ручки", "assigned_role": "Senior Go Developer", "sequence_order": 2, "can_run_parallel": false, "dependencies": ["BEL-01"]}
  ]
}`

func (p *leadDecompProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	if _, ok := agent.(*backendlead.BackendLead); ok {
		p.leadCalls++
		return &runner.AgentResponse{Content: leadDecompJSON, Rounds: 1}, nil
	}
	// Специалист: фиксируем его задание (первое user-сообщение) для проверки
	// порядка последовательности sequence_order.
	msgs := agent.GetUserMessages()
	prompt := ""
	if len(msgs) > 0 {
		prompt = msgs[len(msgs)-1].Message
	}
	p.specs = append(p.specs, prompt)
	return &runner.AgentResponse{Content: "задача выполнена", Rounds: 1}, nil
}

func (p *leadDecompProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

// Lead-шаг плана: лид декомпозирует эпик на JSON-задачи, и исполнитель СРАЗУ
// реализует их специалистами (по порядку sequence_order), создавая файлы.
// Воспроизводит починку: план, делегирующий разработку лидам, снова создаёт
// код (до этого файлы не появлялись — лиды только декомпозировали).
func TestExecutorLeadStepImplementsDecomposition(t *testing.T) {
	ctx := context.Background()
	name := "leadplanproj"
	root := projects.ProjectDir(name)
	defer os.RemoveAll(filepath.Clean(root))

	plan := &Plan{
		ProjectName: name,
		Summary:     "реализация через лида",
		Steps: []Step{
			{ID: "s1", Agent: AgentBackendLead, Prompt: "Спроектируй бэкенд", Description: "декомпозиция бэкенда"},
		},
	}

	stub := &leadDecompProvider{}
	exec := NewExecutor(stub, plan)
	if err := exec.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if stub.leadCalls != 1 {
		t.Fatalf("лид должен запуститься ровно 1 раз, got %d", stub.leadCalls)
	}
	if len(stub.specs) != 2 {
		t.Fatalf("ожидали 2 задачи-специалиста, got %d", len(stub.specs))
	}
	// Специалисты выполняются в порядке sequence_order: сначала базовая
	// структура (BEL-01), затем http handler (BEL-02).
	if !strings.Contains(stub.specs[0], "BEL-01") || !strings.Contains(stub.specs[0], "go.mod") {
		t.Fatalf("первым должен быть BEL-01 (базовая структура), got %q", stub.specs[0])
	}
	if !strings.Contains(stub.specs[1], "BEL-02") {
		t.Fatalf("вторым должен быть BEL-02 (http handler), got %q", stub.specs[1])
	}
	if !exec.completed["s1"] {
		t.Fatal("шаг должен быть отмечен завершённым")
	}
	// Декомпозиция фиксируется в шаге плана и попадает в PLAN.md проекта
	// с полными описаниями-контрактами задач.
	step := exec.findStep("s1")
	if step == nil || len(step.Tasks) != 2 {
		t.Fatalf("шаг должен хранить декомпозицию лида, got %+v", step)
	}
	planDoc := planDocPath(name)
	data, err := os.ReadFile(planDoc)
	if err != nil {
		t.Fatalf("PLAN.md должен быть создан: %v", err)
	}
	doc := string(data)
	if !strings.Contains(doc, "## Шаг 1 [Backend-лид] — декомпозиция бэкенда") {
		t.Errorf("PLAN.md не содержит шаг планировщика:\n%s", doc)
	}
	if !strings.Contains(doc, "#### 1.1 BEL-01 — базовая структура") {
		t.Errorf("PLAN.md не содержит задачу лида внутри шага:\n%s", doc)
	}
	if !strings.Contains(doc, "go.mod") || !strings.Contains(doc, "/health") {
		t.Errorf("PLAN.md не содержит контракт задачи:\n%s", doc)
	}
}

// Лид не вернул JSON-декомпозицию — шаг падает, а не «успешно завершается».
func TestExecutorLeadStepRequiresDecomposition(t *testing.T) {
	ctx := context.Background()
	name := "leadfailproj"
	root := projects.ProjectDir(name)
	defer os.RemoveAll(filepath.Clean(root))

	plan := &Plan{
		ProjectName: name,
		Summary:     "лид без декомпозиции",
		Steps: []Step{
			{ID: "s1", Agent: AgentBackendLead, Prompt: "Декомпозируй", Description: "декомпозиция"},
		},
	}

	stub := &textProvider{}
	exec := NewExecutor(stub, plan)
	err := exec.Run(ctx)
	if err == nil {
		t.Fatal("ожидали ошибку: лид не вернул JSON-декомпозицию")
	}
	if !strings.Contains(err.Error(), "декомпозицию") {
		t.Fatalf("ошибка должна говорить о JSON-декомпозиции, got: %v", err)
	}
	if exec.completed["s1"] {
		t.Fatal("шаг без декомпозиции не должен быть завершён")
	}
}

// TestLeadWithHintSystemMessage — подсказка повторного запроса попадает в
// системные сообщения обёрнутого лида (и включает предыдущий ответ).
func TestLeadWithHintSystemMessage(t *testing.T) {
	lead := frontendlead.NewFrontendLead("proj", "Декомпозируй")
	wrapped := leadWithHint(lead, "раньше я ответил путями")
	msgs := wrapped.GetSystemMessages(nil)
	found := false
	for _, m := range msgs {
		if m.Type == agents.MessageTypeSystem && strings.Contains(m.Message, "JSON-декомпозиц") && strings.Contains(m.Message, "раньше я ответил путями") {
			found = true
		}
	}
	if !found {
		t.Errorf("в системных сообщениях нет подсказки с предыдущим ответом: %+v", msgs)
	}
	if got := wrapped.GetTools(); got == nil {
		t.Error("делегирование GetTools сломалось")
	}
}

// textProvider возвращает просто текст, без JSON-декомпозиции.
type textProvider struct{}

func (t *textProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	return &runner.AgentResponse{Content: "не понял задачи, отвечаю текстом", Rounds: 1}, nil
}

func (t *textProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}
