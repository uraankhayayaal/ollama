package tools

import (
	"github.com/ollama/ollama/api"
)

// ToOllama конвертирует определения инструментов (единый JSON Schema формат)
// в срез официальных инструментов Ollama. Это единая точка конвертации:
// агентам больше не нужно вручную поддерживать дублирующую схему
// для провайдера Ollama (как раньше в GetToolsForOllama).
func ToOllama(defs []ToolDefinition) []api.Tool {
	ollamaTools := make([]api.Tool, 0, len(defs))
	for _, def := range defs {
		ollamaTools = append(ollamaTools, toOllama(def))
	}
	return ollamaTools
}

// toOllama конвертирует одно определение в api.Tool.
func toOllama(def ToolDefinition) api.Tool {
	params := def.Parameters
	if params == nil {
		params = map[string]any{}
	}

	return api.Tool{
		Type: "function",
		Function: api.ToolFunction{
			Name:        def.Name,
			Description: def.Description,
			Parameters: api.ToolFunctionParameters{
				Type:       "object", // Корень параметров ВСЕГДА object
				Properties: schemaToProperties(params),
				Required:   schemaRequired(params),
			},
		},
	}
}

// schemaToProperties строит упорядоченную карту свойств из JSON-схемы.
func schemaToProperties(schema map[string]any) *api.ToolPropertiesMap {
	props := api.NewToolPropertiesMap()
	raw, _ := schema["properties"].(map[string]any)
	for name, value := range raw {
		propSchema, ok := value.(map[string]any)
		if !ok {
			continue
		}
		props.Set(name, schemaToProperty(propSchema))
	}
	return props
}

// schemaToProperty конвертирует одно свойство JSON-схемы в api.ToolProperty,
// рекурсивно разворачивая вложенные схемы (items для массивов, properties
// для объектов) и обязательные поля.
func schemaToProperty(schema map[string]any) api.ToolProperty {
	prop := api.ToolProperty{}
	if t, ok := schema["type"].(string); ok {
		prop.Type = api.PropertyType{t}
	}
	if d, ok := schema["description"].(string); ok {
		prop.Description = d
	}

	// Массив: items описывают элемент.
	if items, ok := schema["items"].(map[string]any); ok {
		prop.Items = schemaToProperty(items)
	}

	// Объект: рекурсивно конвертируем вложенную схему свойств.
	if nested, ok := schema["properties"].(map[string]any); ok {
		child := api.NewToolPropertiesMap()
		for name, value := range nested {
			childSchema, ok := value.(map[string]any)
			if !ok {
				continue
			}
			child.Set(name, schemaToProperty(childSchema))
		}
		prop.Properties = child
		if req := schemaRequired(schema); len(req) > 0 {
			prop.Required = req
		}
	}

	return prop
}

// schemaRequired достаёт список обязательных полей из JSON-схемы.
// Поле "required" в схемах задаётся как []string, но для универсальности
// принимаем и []any (вид после json.Unmarshal).
func schemaRequired(schema map[string]any) []string {
	switch raw := schema["required"].(type) {
	case []string:
		return raw
	case []any:
		required := make([]string, 0, len(raw))
		for _, r := range raw {
			if s, ok := r.(string); ok {
				required = append(required, s)
			}
		}
		return required
	default:
		return nil
	}
}
