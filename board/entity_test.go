package board

import (
	"strings"
	"testing"
)

func TestUnmarshalBacklog(t *testing.T) {
	raw := "```json\n{\n  \"architecture_summary\": \"План приложения\",\n  \"tasks\": [\n    {\"task_id\": \"ARC-01\", \"title\": \"Backend модуль\", \"description\": \"Описание\", \"assigned_role\": \"Backend Lead\", \"sequence_order\": \"1\", \"can_run_parallel\": \"true\", \"dependencies\": []},\n    {\"task_id\": \"ARC-02\", \"title\": \"Infra\", \"description\": \"Описание\\nс новой строкой\", \"assigned_role\": \"DevOps Lead\", \"sequence_order\": 2, \"can_run_parallel\": false, \"dependencies\": [\"ARC-01\"]}\n  ]\n}\n```"
	b, err := UnmarshalBacklog(raw)
	if err != nil {
		t.Fatalf("UnmarshalBacklog: %v", err)
	}
	if b.ArchitectureSummary != "План приложения" {
		t.Errorf("architecture_summary = %q", b.ArchitectureSummary)
	}
	if len(b.Tasks) != 2 {
		t.Fatalf("задач в бэклоге %d, ожидалось 2", len(b.Tasks))
	}

	first := b.Tasks[0]
	if first.TaskID != "ARC-01" || first.SequenceOrder.Int() != 1 || !first.CanRunParallel.Bool() {
		t.Errorf("задача 1 разобрана неверно: %+v", first)
	}

	second := b.Tasks[1]
	if second.SequenceOrder.Int() != 2 || second.CanRunParallel.Bool() {
		t.Errorf("задача 2 разобрана неверно: %+v", second)
	}
	if !strings.Contains(second.Description, "\n") {
		t.Errorf("перенос строки в description потерян: %q", second.Description)
	}
	if len(second.Dependencies) != 1 || second.Dependencies[0] != "ARC-01" {
		t.Errorf("dependencies = %v", second.Dependencies)
	}
}

func TestUnmarshalTasksTolerantToLeadRootKey(t *testing.T) {
	raw := "{\"frontend_lead_summary\": \"Модуль UI\", \"tasks\": [{\"task_id\": \"FEL-01\", \"title\": \"Компонент\", \"description\": \"xD\", \"assigned_role\": \"Frontend Dev\", \"sequence_order\": 1, \"can_run_parallel\": true, \"dependencies\": []}]}"
	tasks, err := UnmarshalTasks(raw)
	if err != nil {
		t.Fatalf("UnmarshalTasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].TaskID != "FEL-01" {
		t.Fatalf("tasks = %+v", tasks)
	}
}

func TestValidateTransition(t *testing.T) {
	valid := []struct{ from, to Status }{
		{StatusNew, StatusAnalysis},
		{StatusNew, StatusCancelled},
		{StatusAnalysis, StatusReady},
		{StatusAnalysis, StatusCancelled},
		{StatusReady, StatusInProgress},
		{StatusReady, StatusCancelled},
		{StatusInProgress, StatusDone},
		{StatusInProgress, StatusCancelled},
		{StatusDone, StatusDone},
	}
	for _, c := range valid {
		if err := ValidateTransition(c.from, c.to); err != nil {
			t.Errorf("%s -> %s должно быть разрешено: %v", c.from, c.to, err)
		}
	}

	invalid := []struct{ from, to Status }{
		{StatusNew, StatusReady},
		{StatusNew, StatusDone},
		{StatusAnalysis, StatusInProgress},
		{StatusReady, StatusDone},
		{StatusInProgress, StatusAnalysis},
		{StatusDone, StatusNew},
		{StatusCancelled, StatusNew},
		{StatusNew, "unknown"},
	}
	for _, c := range invalid {
		if err := ValidateTransition(c.from, c.to); err == nil {
			t.Errorf("%s -> %s должно быть запрещено", c.from, c.to)
		}
	}
}
