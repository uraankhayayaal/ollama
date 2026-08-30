package codegenerator

import (
	"ai/agents"
	"ai/tools"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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
			Type:    agents.MessageTypeHuman,
			Message: "Напиши микросервис для расчета квадратного уровнения, придумай формат аргументов для передачи в код, пример запуска: go run .",
		},
	}
}

func (cg Codegenerator) GetAgentMemoryMessages(text []agents.Message) []agents.Message {
	return []agents.Message{
		{
			Type:    agents.MessageTypeSystem,
			Message: `Ты - опытный golang разработчик. Нельзя отвечать просто текстом. Используй интсрумент WriteFiles. Передавай все файлы в одном вызове спсиком в свйостве files.`,
		},
	}
}

func (cg Codegenerator) GetTools() []tools.ToolDefinition {
	return []tools.ToolDefinition{
		// {
		// 	Name:        "WriteFile",
		// 	Description: "Используй этот инструмент для сохранения написанного кода в файл.",
		// 	Parameters: map[string]any{
		// 		"type": "object", // Корень параметров ВСЕГДА должен быть object
		// 		"properties": map[string]any{
		// 			"filename":             map[string]any{"type": "string", "description": "Название файла, например 'main.go'"},
		// 			"content":              map[string]any{"type": "string", "description": "Код файла"},
		// 			"required":             []string{"filename", "content"}, // Делаем массив обязательным аргументом
		// 			"additionalProperties": false,                           // Обязательно для Structured Outputs / Strict Mode
		// 		},
		// 	},
		// },
		{
			Name:        "WriteFiles",
			Description: "Используй этот инструмент для сохранения файлов.",
			Parameters: map[string]any{
				"type": "object", // Корень параметров ВСЕГДА object
				"properties": map[string]any{
					"files": map[string]any{
						"type":        "array",
						"description": "Список файлов для записи",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"filename": map[string]any{"type": "string", "description": "Название файла с путем, например 'utils/math.go'"},
								"content":  map[string]any{"type": "string", "description": "Полный исходный код файла"},
							},
							"required":             []string{"filename", "content"}, // Обязательные поля для каждого файла
							"additionalProperties": false,
						},
					},
				},
				"required":             []string{"files"}, // Массив файлов обязателен для вызова инструмента
				"additionalProperties": false,
			},
		},
	}
}

// Метод возвращает слайс официальных инструментов Ollama
func (cr Codegenerator) GetToolsForOllama() []api.Tool {
	// --- Инструмент 1: WriteFile ---
	singleProps := api.NewToolPropertiesMap()
	singleProps.Set("filename", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Название файла, например 'main.go'",
	})
	singleProps.Set("content", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Код файла",
	})

	// --- Инструмент 2: WriteFiles (Вложенная схема) ---
	// Сначала создаем свойства для внутренних объектов массива (для каждого файла)
	itemProps := api.NewToolPropertiesMap()
	itemProps.Set("filename", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Название файла с путем, например 'utils/math.go'",
	})
	itemProps.Set("content", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Полный исходный код файла",
	})

	// Теперь создаем корневые свойства для WriteFiles, куда помещаем массив с itemProps
	bulkProps := api.NewToolPropertiesMap()
	bulkProps.Set("files", api.ToolProperty{
		Type:        api.PropertyType{"array"},
		Description: "Список файлов для записи",
		Items: &api.ToolProperty{
			Type:       api.PropertyType{"object"},
			Properties: itemProps, // Передаем упорядоченную мапу для внутренней схемы
			Required:   []string{"filename", "content"},
		},
	})

	return []api.Tool{
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "WriteFile",
				Description: "Используй этот инструмент для сохранения одного файла с кодом.",
				Parameters: api.ToolFunctionParameters{
					Type:       "object",
					Properties: singleProps, // Передаем *api.ToolPropertiesMap
					Required:   []string{"filename", "content"},
				},
			},
		},
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "WriteFiles",
				Description: "Используй этот инструмент для одновременного сохранения множества файлов (например, структуры всего проекта).",
				Parameters: api.ToolFunctionParameters{
					Type:       "object",
					Properties: bulkProps, // Передаем *api.ToolPropertiesMap
					Required:   []string{"files"},
				},
			},
		},
	}
}

func (cg Codegenerator) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	switch functionName {

	case "WriteFiles":
		return cg.WriteFiles(functionArgs)
	case "WriteFile":
		return cg.WriteFile(functionArgs)
	default:
		return nil, fmt.Errorf("function %s not implemented in MathAgent", functionName)
	}
}

func (cg Codegenerator) WriteFile(args map[string]any) ([]byte, error) {
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

	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		result = append(result, map[string]string{
			"filename": filename,
			"status":   "error",
			"message":  err.Error(),
		})
	} else {
		result = append(result, map[string]string{
			"filename": filename,
			"status":   "success",
		})
	}

	// 4. Кодируем результат работы вашей функции обратно в JSON,
	// чтобы отправить его обратно в OpenAI (как Tool message)
	resultJSON, _ := json.Marshal(result)

	return resultJSON, nil
}

// FileItem описывает структуру одного файла, приходящего из аргументов ИИ
type FileItem struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

// BulkParams соответствует корневому JSON-объекту параметров инструмента WriteFiles
type BulkParams struct {
	Files []FileItem `json:"files"`
}

// WriteFiles обрабатывает пакетную запись файлов, вызванную ИИ-агентом
func (cg Codegenerator) WriteFiles(args map[string]any) ([]byte, error) {
	// 1. Создаем переменную структуры для десериализации массива файлов
	var params BulkParams

	// 2. Сериализуем сырые данные map[string]any обратно в JSON-байты
	bytes, err := json.Marshal(args)
	if err == nil {
		// Десериализуем байты напрямую в нашу структуру BulkParams
		_ = json.Unmarshal(bytes, &params)
	}

	// Если массив файлов пустой (например, ошибка парсинга или агент ничего не передал)
	if len(params.Files) == 0 {
		resultJSON, _ := json.Marshal(map[string]string{
			"status":  "error",
			"message": "Список файлов пуст или неверный формат аргументов",
		})
		return resultJSON, nil
	}

	// 3. Вызываем бизнес-логику записи для каждого файла
	// Будем собирать статус по каждому файлу отдельно, чтобы агент видел полную картину
	result := []map[string]string{}

	for _, file := range params.Files {
		// Извлекаем путь к поддиректории из имени файла (например, из "models/user.go" получим "models")
		// Если файл лежит в корне (например, "main.go"), dir вернет "."
		dir := filepath.Dir(file.Filename)

		// Если путь содержит поддиректории, создаем их перед вызовом функции записи
		if dir != "." && dir != "/" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				result = append(result, map[string]string{
					"filename": file.Filename,
					"status":   "error",
					"message":  fmt.Sprintf("не удалось создать директорию %s: %v", dir, err),
				})
				continue // Пропускаем этот файл и переходим к следующему
			}
		}

		// Вызываем вашу функцию записи (предполагаем, что она возвращает error)
		// Замените "filepath.WriteFile" на ваш актуальный вызов (например, cg.writeFile или пакет.WriteFile)
		// Записываем файл на диск, используя стандартный os.WriteFile
		if err := os.WriteFile(file.Filename, []byte(file.Content), 0644); err != nil {
			result = append(result, map[string]string{
				"filename": file.Filename,
				"status":   "error",
				"message":  err.Error(),
			})
		} else {
			result = append(result, map[string]string{
				"filename": file.Filename,
				"status":   "success",
			})
		}
	}

	// 4. Кодируем результат работы обратно в JSON для отправки в модель (Tool Response)
	resultJSON, _ := json.Marshal(result)

	fmt.Println("Ответ инструмента множественной записи", result)

	return resultJSON, nil
}
