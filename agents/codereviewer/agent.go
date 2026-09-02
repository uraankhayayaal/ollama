package codereviewer

import (
	"ai/agents"
	"ai/forges"
	"ai/langdetect"
	"ai/tools"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/ollama/ollama/api"
)

type Codereviewer struct {
	forge       forges.Forge
	IsUseMemory bool
	// postErrors накапливает ошибки публикации комментариев в ReviewMr.
	// Если хотя бы одно замечание не удалось опубликовать, ApproveMr
	// отказывается ставить апрув, чтобы не одобрить изменения вслепую.
	postErrors []string
}

// NewCodereviewer создаёт агента код-ревью. Ссылка может указывать на
// любой поддерживаемый хостинг (GitLab/GitHub) — тип определяется по URL,
// а токен берётся из переменной окружения <HOST>_TOKEN (см. forges.New).
func NewCodereviewer(args []string) *Codereviewer {
	if len(args) < 1 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "Ошибка: укажите ссылку на MR, например: go run . review <URL>")
		os.Exit(1)
	}
	prURL := args[0]

	token := pickToken(prURL)
	forge, err := forges.New(prURL, token)
	if err != nil {
		log.Fatalf("Ошибка создания провайдера ревью: %v", err)
	}

	return &Codereviewer{
		forge:       forge,
		IsUseMemory: true,
	}
}

// pickToken выбирает токен доступа в зависимости от хостинга:
// GitLab → GITLAB_TOKEN, GitHub → GITHUB_TOKEN.
func pickToken(prURL string) string {
	switch forges.DetectType(prURL) {
	case forges.KindGitHub:
		return os.Getenv("GITHUB_TOKEN")
	case forges.KindGitLab:
		return os.Getenv("GITLAB_TOKEN")
	default:
		return ""
	}
}

func (cr Codereviewer) GetMessages() []agents.Message {
	diff, err := cr.forge.GetDiff()
	if err != nil {
		log.Fatalf("Ошибка при получении изменений кода: %v", err)
	}

	return []agents.Message{
		{
			Type: agents.MessageTypeHuman,
			Message: "Изменения кода: " + diff +
				". Ответ только в виде вызовов функций ApproveMr или ReviewMr",
		},
	}
}

// diffFromMessages извлекает текст диффа из переданных user-сообщений.
// Дифф приходит от раннера в GetAgentMemoryMessages вместе с сообщениями.
func diffFromMessages(messages []agents.Message) string {
	for _, m := range messages {
		if m.Type == agents.MessageTypeHuman {
			return m.Message
		}
	}
	return ""
}

func (cr Codereviewer) GetTools() []tools.ToolDefinition {
	return []tools.ToolDefinition{
		{
			Name:        "ReviewMr",
			Description: "Рекомендовать и сделать замечания по результатам код-ревью",
			Parameters: map[string]any{
				"type": "object", // Корень параметров ВСЕГДА должен быть object
				"properties": map[string]any{
					"comments": map[string]any{ // Наш параметр-массив
						"type":        "array",
						"description": "Список замечаний и рекомендаций к коду",
						"items": map[string]any{ // Описание элементов внутри массива (объекты)
							"type": "object",
							"properties": map[string]any{
								"file_path": map[string]any{"type": "string", "description": "Путь к файлу, к которому приводится кодревью"},
								"line":      map[string]any{"type": "integer", "description": "Номер строки в новой версии файла, к которой относится комментарий"},
								"text":      map[string]any{"type": "string", "description": "Текст рекомендации или замечания"},
							},
							// Если включен Strict Mode, все поля в items.properties должны быть в required
							"required":             []string{"file_path", "line", "text"},
							"additionalProperties": false,
						},
					},
					"required":             []string{"comments"}, // Делаем массив обязательным аргументом
					"additionalProperties": false,                // Обязательно для Structured Outputs / Strict Mode
				},
			},
		},
		{
			Name:        "ApproveMr",
			Description: "Одобрить измений кода, если нет критичных багов и дефектов",
		},
	}
}

// Метод возвращает слайс официальных инструментов Ollama
func (cr Codereviewer) GetToolsForOllama() []api.Tool {
	// 1. Конструируем свойства для инструмента ReviewMr
	reviewProps := api.NewToolPropertiesMap()

	// Описываем объект внутри массива (items для comments)
	commentItemsProps := api.NewToolPropertiesMap()
	commentItemsProps.Set("file_path", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Путь к файлу, к которому приводится кодревью",
	})
	commentItemsProps.Set("line", api.ToolProperty{
		Type:        api.PropertyType{"integer"},
		Description: "Номер строки в новой версии файла, к которой относится комментарий",
	})
	commentItemsProps.Set("text", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Текст рекомендации или замечания",
	})

	// Описываем сам массив "comments"
	reviewProps.Set("comments", api.ToolProperty{
		Type:        api.PropertyType{"array"},
		Description: "Список замечаний и рекомендаций к коду",
		Items: api.ToolFunctionParameters{
			Type:       "object",
			Properties: commentItemsProps,
			Required:   []string{"file_path", "line", "text"},
		},
	})

	return []api.Tool{
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "ReviewMr",
				Description: "Рекомендовать и сделать замечания по результатам код-ревью",
				Parameters: api.ToolFunctionParameters{
					Type:       "object",
					Properties: reviewProps,
					Required:   []string{"comments"},
				},
			},
		},
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "ApproveMr",
				Description: "Одобрить измений кода, если нет критичных багов и дефектов",
				Parameters: api.ToolFunctionParameters{
					Type:       "object",
					Properties: api.NewToolPropertiesMap(), // Пустая карта параметров
				},
			},
		},
	}
}

func (cr *Codereviewer) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	switch functionName {

	case "ReviewMr":
		return cr.ReviewMr(functionArgs), nil

	case "ApproveMr":
		return cr.ApproveMr(functionArgs), nil

	default:
		return nil, fmt.Errorf("function %s not implemented in MathAgent", functionName)
	}
}

func (cr *Codereviewer) ReviewMr(args map[string]any) []byte {
	var comments []forges.ReviewComment

	// comments приходит от разных провайдеров: иногда как JSON-строка
	// (Yandex), иногда уже как разобранный слайс. Парсим оба варианта.
	comments = parseComments(args["comments"])

	// Публикуем каждое замечание. При сбое фиксируем ошибку на структуре,
	// чтобы ApproveMr в последующем раунде отказался одобрять изменения.
	result := []map[string]string{}
	for _, comment := range comments {
		err := cr.forge.PostComment(comment)
		if err != nil {
			cr.postErrors = append(cr.postErrors, err.Error())
			result = append(result, map[string]string{"status": "error", "message": err.Error()})
		}
	}

	// Кодируем результат обратно в JSON для модели (Tool message)
	resultJSON, _ := json.Marshal(result)

	return resultJSON
}

// parseComments преобразует аргумент "comments" в слайс ReviewComment,
// независимо от того, пришёл он JSON-строкой или уже разобранным массивом.
func parseComments(raw any) []forges.ReviewComment {
	var comments []forges.ReviewComment

	switch v := raw.(type) {
	case string:
		// JSON-строка вида `[{"file_path": ...}]`.
		_ = json.Unmarshal([]byte(v), &comments)
	case []map[string]any:
		b, _ := json.Marshal(v)
		_ = json.Unmarshal(b, &comments)
	case map[string]any:
		b, _ := json.Marshal(v)
		_ = json.Unmarshal(b, &comments)
	case nil:
		return nil
	}

	return comments
}

func (cr *Codereviewer) ApproveMr(args map[string]any) []byte {
	result := map[string]string{}

	// Не одобряем изменения, если хотя бы одно замечание не опубликовано.
	if len(cr.postErrors) > 0 {
		result = map[string]string{
			"status":  "error",
			"message": fmt.Sprintf("нельзя одобрить: %d замечание(й) не были опубликованы", len(cr.postErrors)),
		}
		resultJSON, _ := json.Marshal(result)
		return resultJSON
	}

	err := cr.forge.Approve()
	if err != nil {
		result = map[string]string{"status": "error", "message": err.Error()}
	}

	resultJSON, _ := json.Marshal(result)
	return resultJSON
}

func (cr Codereviewer) GetAgentMemoryMessages(text []agents.Message) []agents.Message {
	if cr.IsUseMemory {
		// Определяем язык по диффу, переданному раннером в user-сообщениях.
		language := langdetect.Detect(diffFromMessages(text))

		// Для PHP дополнительно указываем фреймворк (Laravel), сохраняя
		// все прежние правила проекта (PSR-12, SOLID, lighthouse, DTO и пр.).
		framework := ""
		if language == langdetect.PHP {
			framework = "Laravel"
		}
		return []agents.Message{
			{
				Type:    agents.MessageTypeSystem,
				Message: langdetect.ReviewPrompt(language, framework),
			},
		}
	}

	return []agents.Message{}
}
