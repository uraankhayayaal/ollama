package planner

import "encoding/json"

// AgentType — тип агента, которому делегируется задача.
type AgentType string

const (
	AgentCodeGenerator AgentType = "codegenerator"
	AgentRefactor      AgentType = "refactor"
	AgentCodeReviewer  AgentType = "codereviewer"
)

// Step — один этап плана, делегируемый конкретному агенту.
type Step struct {
	ID          string    `json:"id"`
	Agent       AgentType `json:"agent"`
	Prompt      string    `json:"prompt"`
	ProjectName string    `json:"project_name,omitempty"`
	DependsOn   []string  `json:"depends_on,omitempty"`
	Description string    `json:"description"`
	// Scope — область работы агента: конкретные файлы и/или директории
	// проекта, на которых сосредоточен этот шаг (например "src/main.go"
	// или "internal/order/"). Пустой — агент работает со всем проектом.
	Scope []string `json:"scope,omitempty"`
}

// Plan — структурированный план, возвращаемый планировщиком.
type Plan struct {
	ProjectName string `json:"project_name"`
	Summary     string `json:"summary"`
	Steps       []Step `json:"steps"`
}

// ParsePlan разбирает JSON-ответ планировщика в Plan.
// Если модель вернала текст без JSON — возвращает ошибку.
func ParsePlan(data string) (*Plan, error) {
	data = extractJSON(data)
	var p Plan
	if err := json.Unmarshal([]byte(data), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// extractJSON пытается найти JSON-объект в тексте модели:
// ищет первый '{' и последний '}', чтобы отбросить markdown-обёртки.
func extractJSON(s string) string {
	start := -1
	for i, c := range s {
		if c == '{' {
			start = i
			break
		}
	}
	if start < 0 {
		return s
	}
	end := -1
	for i := len(s) - 1; i >= start; i-- {
		if s[i] == '}' {
			end = i
			break
		}
	}
	if end < 0 {
		return s
	}
	return s[start : end+1]
}
