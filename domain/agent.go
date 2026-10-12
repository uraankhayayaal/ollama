package domain

type AgentRole string

const (
	RoleArchitect    AgentRole = "architect"
	RoleBackendLead  AgentRole = "backend_lead"
	RoleBackendDev   AgentRole = "backend_dev"
	RoleFrontendLead AgentRole = "frontend_lead"
	RoleFrontendDev  AgentRole = "frontend_dev"
	RoleDevops       AgentRole = "devops"
	RoleDevopsLead   AgentRole = "devops_lead"
	RoleQAEngineer   AgentRole = "qa_engineer"
	RoleCodeReviewer AgentRole = "code_reviewer"
	RolePromptCheck  AgentRole = "prompt_check"
	RoleChatAssist   AgentRole = "chat_assist"
)

type AgentDefinition struct {
	Role           AgentRole
	Name           string
	SystemPrompt   string
	AllowedTools   []string
	ModelTier      string
	MaxRounds      int
	StopConditions []string
}

// MessageType — тип агентского сообщения (system/user/generic/...).
type MessageType string

const (
	MessageTypeAI       MessageType = "ai"
	MessageTypeHuman    MessageType = "human"
	MessageTypeSystem   MessageType = "system"
	MessageTypeGeneric  MessageType = "generic"
	MessageTypeFunction MessageType = "function"
	MessageTypeTool     MessageType = "tool"
)

// AgentMessage — системное или пользовательское сообщение агента (промпт),
// не путать с domain.Message (сообщение диалога LLM). Тип принадлежит домену;
// agents.Message — алиас для обратной совместимости.
type AgentMessage struct {
	Type    MessageType
	Message string
}
