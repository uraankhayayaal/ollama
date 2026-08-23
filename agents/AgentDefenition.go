package agents

import "ai/tools"

type Agent interface {
	GetMessages() []Message
	GetTools() []tools.ToolDefinition
	CallFunction(functionName string, functionArgs map[string]any) ([]byte, error)
}

type MessageType string

type Message struct {
	Type    MessageType
	Message string
}

const (
	MessageTypeAI       MessageType = "ai"
	MessageTypeHuman    MessageType = "human"
	MessageTypeSystem   MessageType = "system"
	MessageTypeGeneric  MessageType = "generic"
	MessageTypeFunction MessageType = "function"
	MessageTypeTool     MessageType = "tool"
)
