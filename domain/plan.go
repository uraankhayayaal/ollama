package domain

type AgentType string

const (
	AgentBackendDev   AgentType = "backend_dev"
	AgentFrontendDev  AgentType = "frontend_dev"
	AgentDevops       AgentType = "devops"
	AgentDevopsLead   AgentType = "devops_lead"
	AgentQAEngineer   AgentType = "qa_engineer"
	AgentFrontendLead AgentType = "frontend_lead"
	AgentBackendLead  AgentType = "backend_lead"
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
