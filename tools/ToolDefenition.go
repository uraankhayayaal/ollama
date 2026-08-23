package tools

// ToolDefinition описывает функцию для LLM
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]interface{} // JSON Schema
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON-строка с аргументами
}
