package domain

// AgentType — тип ролевого агента, которому делегируется шаг плана.
// Значения канонические: совпадают с сериализуемым в чекпоинтах/PLAN.md видом
// (planner исторически использовал именно их; ранее здесь были расходящиеся
// «backend_dev»/«qa_engineer» и т.п. — исправлено при переносе типов в domain).
type AgentType string

const (
	AgentBackendDev   AgentType = "backend"
	AgentFrontendDev  AgentType = "frontend"
	AgentDevops       AgentType = "devops"
	AgentDevopsLead   AgentType = "devopslead"
	AgentQAEngineer   AgentType = "qa"
	AgentFrontendLead AgentType = "frontendlead"
	AgentBackendLead  AgentType = "backendlead"
	// Типы вне ролей плана (архитектор и вспомогательные агенты).
	AgentArchitect    AgentType = "architect"
	AgentPromptCheck  AgentType = "prompt_check"
	AgentCodeReviewer AgentType = "code_reviewer"
	AgentChatAssist   AgentType = "chat_assist"
)

type Role string

const (
	RoleFrontend Role = "frontend"
	RoleBackend  Role = "backend"
)

type Step struct {
	ID          string    `json:"id"`
	Agent       AgentType `json:"agent"`
	Prompt      string    `json:"prompt"`
	ProjectName string    `json:"project_name,omitempty"`
	DependsOn   []string  `json:"depends_on,omitempty"`
	Description string    `json:"description"`
	Scope       []string  `json:"scope,omitempty"`
	Role        Role      `json:"role,omitempty"`
}

type Plan struct {
	ProjectName string `json:"project_name"`
	Summary     string `json:"summary"`
	Steps       []Step `json:"steps"`
}
