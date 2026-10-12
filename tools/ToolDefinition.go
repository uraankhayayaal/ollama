package tools

import "ai/domain"

// ToolDefinition описывает функцию для LLM
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON Schema
}

// ToolCall — запрос модели на вызов инструмента. Тип принадлежит домену
// (domain.ToolCall); здесь — алиас для обратной совместимости импортов.
type ToolCall = domain.ToolCall
