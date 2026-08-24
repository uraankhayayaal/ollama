package agents

import (
	"ai/tools"

	"github.com/ollama/ollama/api"
)

type Agent interface {
	GetMessages() []Message
	GetTools() []tools.ToolDefinition
	GetToolsForOllama() []api.Tool
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
