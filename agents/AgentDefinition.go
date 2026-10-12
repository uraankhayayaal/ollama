package agents

import (
	"ai/domain"
	"ai/tools"

	"github.com/ollama/ollama/api"
)

// Message — алиас доменного типа (domain.AgentMessage): промпт-сообщение
// агента. MessageType и константы тоже принадлежат домену.
type Message = domain.AgentMessage

// MessageType — тип агентского сообщения.
type MessageType = domain.MessageType

const (
	MessageTypeAI       = domain.MessageTypeAI
	MessageTypeHuman    = domain.MessageTypeHuman
	MessageTypeSystem   = domain.MessageTypeSystem
	MessageTypeGeneric  = domain.MessageTypeGeneric
	MessageTypeFunction = domain.MessageTypeFunction
	MessageTypeTool     = domain.MessageTypeTool
)

type Agent interface {
	GetSystemMessages(text []Message) []Message
	GetUserMessages() []Message
	GetTools() []tools.ToolDefinition
	GetToolsForOllama() []api.Tool
	CallFunction(functionName string, functionArgs map[string]any) ([]byte, error)
}
