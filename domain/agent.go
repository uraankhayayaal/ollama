package domain

type AgentRole string

const (
	RoleChatAssist AgentRole = "chat_assist"
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
