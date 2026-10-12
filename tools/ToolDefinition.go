package tools

import "ai/domain"

// ToolDefinition описывает функцию для LLM. Тип принадлежит домену
// (domain.ToolDefinition); здесь — алиас для обратной совместимости импортов.
type ToolDefinition = domain.ToolDefinition

// ToolCall — запрос модели на вызов инструмента. Тип принадлежит домену
// (domain.ToolCall); здесь — алиас для обратной совместимости импортов.
type ToolCall = domain.ToolCall
