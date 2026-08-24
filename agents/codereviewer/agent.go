package codereviewer

import (
	"ai/agents"
	"ai/tools"
	"ai/tools/gitlab"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
)

type Codereviewer struct {
	Token  string
	MrUrl  string
	config *gitlab.GitLabConfig
}

func NewCodereviewer() *Codereviewer {
	token := os.Getenv("GITLAB_TOKEN")

	// Описание флагов: имя, дефолтное значение, описание для --help
	mrURL := flag.String("mr", "", "Ссылка на MR Gitlab")
	// ВАЖНО: парсинг аргументов нужно запустить до использования переменных
	flag.Parse()

	// Проверяем, осталось ли значение пустым
	if *mrURL == "" {
		fmt.Fprintln(os.Stderr, "Ошибка: флаг -mr является обязательным.")
		flag.Usage() // Выводит стандартную справку --help
		os.Exit(1)   // Завершаем работу с кодом ошибки
	}

	config, err := gitlab.ParseGitLabURL(*mrURL, token)
	if err != nil {
		log.Fatalf("Ошибка парсинга URL: %v", err)
	}

	return &Codereviewer{
		token,
		*mrURL,
		config,
	}
}

func (cr Codereviewer) GetMessages() []agents.Message {
	diff, err := gitlab.GetMRDiff(cr.config)
	if err != nil {
		log.Fatalf("Ошибка при получении изменений кода: %v", err)
	}

	return []agents.Message{
		{
			Type: agents.MessageTypeSystem,
			Message: "Ты — опытный ведущий разработчик. Твоя задача — провести ревью изменений кода. " +
				"Сначала внимательно изучи diff. Если найдешь баги, проблемы безопасности или архитектурные дефекты, оставь комментарии к конкретным строкам. " +
				"Ставь апрув если критических багов и ломающих изменений нет (или все замечания носят характер мелких улучшений).",
		},
		{
			Type:    agents.MessageTypeHuman,
			Message: "Начни код-ревью для текущего MR и выбери инструменты.",
		},
		{
			Type:    agents.MessageTypeHuman,
			Message: diff,
		},
	}
}

func (cr Codereviewer) GetTools() []tools.ToolDefinition {
	return []tools.ToolDefinition{
		{
			Name:        "ReviewMr",
			Description: "Оставить рекомендации и замечания по результатам код-ревью",
			Parameters: map[string]any{
				"type": "object", // Корень параметров ВСЕГДА должен быть object
				"properties": map[string]any{
					"comments": map[string]any{ // Наш параметр-массив
						"type":        "array",
						"description": "Список замечаний к коду",
						"items": map[string]any{ // Описание элементов внутри массива (объекты)
							"type": "object",
							"properties": map[string]any{
								"file_path": map[string]any{"type": "string", "description": "Путь к файлу, например 'main.go'"},
								"line":      map[string]any{"type": "integer", "description": "Номер строки в новой версии файла, к которой относится комментарий"},
								"text":      map[string]any{"type": "string", "description": "Текст рекомендации или описание ошибки и решения"},
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
			Description: "Поставить апрув (approve) к Merge Request, если изменения не критичны и не ломают систему",
		},
	}
}

func (cr Codereviewer) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	switch functionName {

	case "ReviewMr":
		return cr.ReviewMr(functionArgs), nil

	case "ApproveMr":
		return cr.ApproveMr(functionArgs), nil

	default:
		return nil, fmt.Errorf("function %s not implemented in MathAgent", functionName)
	}
}

func (cr Codereviewer) ReviewMr(args map[string]any) []byte {
	// 1. Создаем переменную нужного типа
	var comments []gitlab.ReviewComment

	// 2. Сериализуем сырые данные обратно в JSON-байты
	bytes, err := json.Marshal(args["comments"])
	if err == nil {
		// 3. Десериализуем байты напрямую в вашу структуру []ReviewComment
		_ = json.Unmarshal(bytes, &comments)
	}

	// 3. Вызываем вашу бизнес-логику (функцию, которая обрабатывает комментарии)
	// Внутри args.Comments уже лежит готовый срез (slice) []ReviewComment
	result := []map[string]string{}
	for _, comment := range comments {
		err := gitlab.PostCommentOnLine(cr.config, comment)
		if err != nil {
			result = append(result, map[string]string{"status": "error", "message": err.Error()})
		}
	}

	// 4. Кодируем результат работы вашей функции обратно в JSON,
	// чтобы отправить его обратно в OpenAI (как Tool message)
	resultJSON, _ := json.Marshal(result)

	return resultJSON
}

func (cr Codereviewer) ApproveMr(args map[string]any) []byte {
	// 1. Вызываем вашу бизнес-логику (функцию, которая обрабатывает комментарии)
	// Внутри args.Comments уже лежит готовый срез (slice) []ReviewComment
	result := map[string]string{}
	err := gitlab.ApproveMR(cr.config)
	if err != nil {
		result = map[string]string{"status": "error", "message": err.Error()}
	}

	// 2. Кодируем результат работы вашей функции обратно в JSON,
	// чтобы отправить его обратно в OpenAI (как Tool message)
	resultJSON, _ := json.Marshal(result)

	return resultJSON
}
