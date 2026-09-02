package codereviewer

import (
	"ai/agents"
	"ai/forges"
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
	var comments []forges.ReviewComment

	// 2. Сериализуем сырые данные обратно в JSON-байты
	bytes, err := json.Marshal(args["comments"])
	if err == nil {
		// 3. Десериализуем байты напрямую в структуру ReviewComment
		_ = json.Unmarshal(bytes, &comments)
	}

	// 4. Публикуем каждое замечание через выбранный провайдер
	result := []map[string]string{}
	for _, comment := range comments {
		err := cr.forge.PostComment(comment)
		if err != nil {
			result = append(result, map[string]string{"status": "error", "message": err.Error()})
		}
	}

	// 5. Кодируем результат обратно в JSON для модели (Tool message)
	resultJSON, _ := json.Marshal(result)

	return resultJSON
}

func (cr Codereviewer) ApproveMr(args map[string]any) []byte {
	result := map[string]string{}
	err := cr.forge.Approve()
	if err != nil {
		result = map[string]string{"status": "error", "message": err.Error()}
	}

	resultJSON, _ := json.Marshal(result)
	return resultJSON
}

func (cr Codereviewer) GetAgentMemoryMessages(text []agents.Message) []agents.Message {
	if cr.IsUseMemory {
		return []agents.Message{
			{
				Type: agents.MessageTypeSystem,
				Message: `Ты — опытный ведущий Laravel PHP разработчик. Твоя задача — провести кодевью.
					Найдешь баги, проблемы безопасности или дефекты, оставь комментарии к конкретным строкам.
					Комменатрия начинай со слов 'для заметки:' или 'критично:',
					где для заметки: - мелкие баги или дефекты, котрые блокируют запуск,
						критично: - критические ошибки, явное нарушение работы приложения.
					Рекомендации делаешь на русском и с интсрументом 'ReviewMr'.
					Одобряещь изменения с интсрументом 'ApproveMr' если нет критических ошибок.
					Проверить код на соответствие руководства по стилю кодирования PSR-12.
					Проверить код на соблюдение патернов проектирования SOLID, DRY, KISS.
					Для файлов с расширением .graphql проверять систаксис и логику согласно библиотеки laravel lighthouse.
					В ресолверах ./Resolvers не должно быть бизнес логики, они должны вызывать метод batchLoader-а или сервиса.
					В запросах графкуль ./Queries, мутациях ./Mutatioins не должно быть бизнес логики, они должны вызывать метод сервиса.
					В батчлоадерах ./Batch сложная логика для оптимизации запросов, решения проблемы N+1, кэширования по тэгам, задча сделать один общий запрос к БД, и максимально всю работу закэшировать.
					Бизнес логика приложения должна быть определена и описана в сервисах, папка ./serices.
					Используются репозиотрии ./repositories, они имеют статические методы, чтобы импользовать в разных частях приложения без внедрения зависимостей.
					Используются клиенты ./clients, они совершают соединение в другие сервисы (свои и сторонние) их публичные методы должны возвращать DTO объекты.
					Используются DTO ./DTO, они описываю те данные которые не описывают модели (Model), Являются контрактами между разными частями сервиса и внешними сервисами.
					Если класс не имеет более одной зависимости и сложно реализации, то ипредпочтительно импользовать автобиндинг laravel, без лишнего объявления в dic.
					В проекте используется laravel octane swoole, проверять регистрацию клаассов, singltone, scope, bind.
					Проверять что нет статических переменных или свойств, которые нарушали бы работу swoole.
					Если Разработчик указывает константные значение, типа констант в классе, перменных окружения, конфиге, без орфографических ошибок, то довреять ему и не предлагать убедиться в правильности.`,
			},
		}
	}

	return []agents.Message{}
}
