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

func TestUnmarshalBacklogWithOpportunities(t *testing.T) {
	raw := `{"architecture_summary": "План", "tasks": [{"task_id": "ARC-01", "title": "Бэкенд", "description": "x", "assigned_role": "Backend Lead", "sequence_order": 1, "can_run_parallel": true, "dependencies": []}], "opportunities": [{"target_role": "QA Lead", "suggestion": "Смоук-тесты"}, {"target_role": "DevOps Lead", "suggestion": "Канарейка"}]}`
	b, err := UnmarshalBacklog(raw)
	if err != nil {
		t.Fatalf("UnmarshalBacklog: %v", err)
	}
	if len(b.Opportunities) != 2 {
		t.Fatalf("opportunities = %d, ожидалось 2", len(b.Opportunities))
	}
	if b.Opportunities[0].TargetRole != "QA Lead" || b.Opportunities[0].Suggestion != "Смоук-тесты" {
		t.Errorf("opportunity 0 = %+v", b.Opportunities[0])
	}
	if b.Opportunities[1].TargetRole != "DevOps Lead" {
		t.Errorf("opportunity 1 = %+v", b.Opportunities[1])
	}
}

// TestUnmarshalBacklogOpportunitiesOptional — старые вызовы без поля
// opportunities остаются валидными (опциональность, Ф-6).
func TestUnmarshalBacklogOpportunitiesOptional(t *testing.T) {
	raw := `{"architecture_summary": "План", "tasks": [{"task_id": "ARC-01", "title": "Бэкенд", "description": "x", "assigned_role": "Backend Lead", "sequence_order": 1, "can_run_parallel": true, "dependencies": []}]}`
	b, err := UnmarshalBacklog(raw)
	if err != nil {
		t.Fatalf("UnmarshalBacklog: %v", err)
	}
	if len(b.Opportunities) != 0 {
		t.Fatalf("opportunities без поля = %d, ожидался 0", len(b.Opportunities))
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

// TestUnmarshalTasksBareArray — модель вместо {"tasks":[...]} вернула голый
// массив задач (возможно, с маркдаун-обёрткой): разбор должен пройти.
func TestUnmarshalTasksBareArray(t *testing.T) {
	raw := "```json\n[{\"task_id\": \"RL-01\", \"title\": \"Правка\", \"description\": \"Исправить\", \"assigned_role\": \"Backend Dev\", \"sequence_order\": 1, \"can_run_parallel\": true, \"dependencies\": []}]\n```"
	tasks, err := UnmarshalTasks(raw)
	if err != nil {
		t.Fatalf("UnmarshalTasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].TaskID != "RL-01" {
		t.Fatalf("tasks = %+v", tasks)
	}
}

// TestUnmarshalTasksPathPrefixProse — лид вместо JSON вернул текст с путями
// (например, /private/...): разбор не должен ни упасть, ни вернуть часть
// выдуманных задач — возвращается ошибка (исполнитель затем переспрашивает).
func TestUnmarshalTasksPathPrefixProse(t *testing.T) {
	raw := "/private/var/www/ollama/temp/calc2/frontend/src/App.tsx\nнужно исправить сборку, см. лог приёмки"
	if _, err := UnmarshalTasks(raw); err == nil {
		t.Fatal("ожидалась ошибка разбора для прозы с путями")
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
		// Пауза (Ф-6): любой активный статус можно приостановить, возобновление
		// возвращает запись в «готова к работе», отмена из паузы допустима.
		{StatusNew, StatusPaused},
		{StatusAnalysis, StatusPaused},
		{StatusReady, StatusPaused},
		{StatusInProgress, StatusPaused},
		{StatusPaused, StatusPaused},
		{StatusPaused, StatusReady},
		{StatusPaused, StatusCancelled},
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
		// Терминальные статусы не приостанавливаются; из паузы — только
		// возобновление или отмена.
		{StatusDone, StatusPaused},
		{StatusCancelled, StatusPaused},
		{StatusPaused, StatusInProgress},
		{StatusPaused, StatusDone},
		{StatusPaused, StatusAnalysis},
	}
	for _, c := range invalid {
		if err := ValidateTransition(c.from, c.to); err == nil {
			t.Errorf("%s -> %s должно быть запрещено", c.from, c.to)
		}
	}
}
