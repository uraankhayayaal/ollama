package codegenerator

import (
	"ai/agents"
	"ai/tools"
	"ai/tools/filepath"
	"encoding/json"
	"fmt"

	"github.com/ollama/ollama/api"
)

type Codegenerator struct {
}

func NewCodegenerator() *Codegenerator {
	return &Codegenerator{}
}

func (cg Codegenerator) GetMessages() []agents.Message {
	return []agents.Message{
		{
			Type: agents.MessageTypeSystem,
			Message: " Ты - опытный golang разработчик" +
				"Сохрани этот рабочий код в файл с именем codereviewer.go.",
		},
		{
			Type: agents.MessageTypeHuman,
			Message: "Напиши простой скрипт, который получает на вход ссылку на МР в приватный gitlab. " +
				"Входной формат — JSON-строка строго в виде: {\"filename\":\"main.go\", \"content\":\"код внутри\"}",
		},
	}
}

func (cg Codegenerator) GetTools() []tools.ToolDefinition {
	return []tools.ToolDefinition{
		{
			Name:        "WriteFile",
			Description: "Используй этот инструмент для сохранения написанного кода в файл.",
			Parameters: map[string]any{
				"type": "object", // Корень параметров ВСЕГДА должен быть object
				"properties": map[string]any{
					"filename":             map[string]any{"type": "string", "description": "Название файла, например 'main.go'"},
					"content":              map[string]any{"type": "string", "description": "Код файла"},
					"required":             []string{"filename", "content"}, // Делаем массив обязательным аргументом
					"additionalProperties": false,                           // Обязательно для Structured Outputs / Strict Mode
				},
			},
		},
	}
}

// Метод возвращает слайс официальных инструментов Ollama
func (cr Codegenerator) GetToolsForOllama() []api.Tool {
	codeProps := api.NewToolPropertiesMap()
	codeProps.Set("filename", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Название файла, например 'main.go'",
	})
	codeProps.Set("content", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Код файла",
	})

	return []api.Tool{
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "WriteFile",
				Description: "Используй этот инструмент для сохранения написанного кода в файл.",
				Parameters: api.ToolFunctionParameters{
					Type:       "object",
					Properties: codeProps,
					Required:   []string{"filename", "content"},
				},
			},
		},
	}
}

func (cg Codegenerator) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	switch functionName {

	case "WriteFile":
		return cg.WriteFile(functionArgs), nil
	default:
		return nil, fmt.Errorf("function %s not implemented in MathAgent", functionName)
	}
}

func (cg Codegenerator) WriteFile(args map[string]any) []byte {
	// 1. Создаем переменную нужного типа
	var params map[string]string

	// 2. Сериализуем сырые данные обратно в JSON-байты
	bytes, err := json.Marshal(args)
	if err == nil {
		// 3. Десериализуем байты напрямую в вашу структуру []ReviewComment
		_ = json.Unmarshal(bytes, &params)
	}

	filename := params["filename"]
	content := params["content"]

	// 3. Вызываем вашу бизнес-логику (функцию, которая обрабатывает комментарии)
	// Внутри args.Comments уже лежит готовый срез (slice) []ReviewComment
	result := []map[string]string{}

	if err := filepath.WriteFile(filename, content); err != nil {
		result = append(result, map[string]string{"status": "error", "message": err.Error()})
	}

	// 4. Кодируем результат работы вашей функции обратно в JSON,
	// чтобы отправить его обратно в OpenAI (как Tool message)
	resultJSON, _ := json.Marshal(result)

	return resultJSON
}
