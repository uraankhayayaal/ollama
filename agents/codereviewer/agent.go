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
	"strings"

	"github.com/ollama/ollama/api"
)

type Codereviewer struct {
	forge       forges.Forge
	IsUseMemory bool
	cfg         Config
	// focus — цель ревью из аргумента CLI (например "безопасность").
	focus string
	// postErrors накапливает ошибки публикации комментариев в ReviewMr.
	// Если хотя бы одно замечание не удалось опубликовать, ApproveMr
	// отказывается ставить апрув, чтобы не одобрить изменения вслепую.
	postErrors []string
	// criticalFound — были ли опубликованы критичные замечания ("критично:").
	criticalFound bool
	// summaryPosted — публиковался ли уже итоговый отчёт в тред.
	summaryPosted bool
	// commentCount — сколько замечаний опубликовано за это ревью.
	commentCount int
}

// NewCodereviewer создаёт агента код-ревью. Ссылка может указывать на
// любой поддерживаемый хостинг (GitLab/GitHub) — тип определяется по URL,
// а токен берётся из переменной окружения <HOST>_TOKEN (см. forges.New).
//
// args[0] — URL MR/PR. args[1] — необязательная цель ревью (например
// "безопасность", "производительность"), подставляется в промпт.
func NewCodereviewer(args []string) *Codereviewer {
	if len(args) < 1 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "Ошибка: укажите ссылку на MR, например: go run . review <URL>")
		os.Exit(1)
	}
	prURL := args[0]

	focus := ""
	if len(args) >= 2 && args[1] != "" {
		focus = args[1]
	}

	token := pickToken(prURL)
	forge, err := forges.New(prURL, token)
	if err != nil {
		log.Fatalf("Ошибка создания провайдера ревью: %v", err)
	}

	return &Codereviewer{
		forge:       forge,
		IsUseMemory: true,
		cfg:         LoadConfig(),
		focus:       focus,
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
		// Не убиваем весь процесс при сбое получения диффа: сообщаем
		// ошибку моделью через user-сообщение, чтобы агент завершился,
		// не пытаясь постить замечания к несуществующему коду.
		return []agents.Message{
			{
				Type: agents.MessageTypeHuman,
				Message: fmt.Sprintf("Не удалось получить изменения кода: %v. "+
					"Заверши ревью без вызова инструментов и объясни причину.", err),
			},
		}
	}

	// Если включено — отсекаем сгенерированные и бинарные файлы, чтобы
	// не тратить контекст модели и не комментировать артефакты.
	if cr.cfg.SkipGenerated {
		diff = filterGeneratedDiff(diff)
	}

	// Пустой (или полностью отфильтрованный) дифф — завершаем без ревью.
	if strings.TrimSpace(diff) == "" {
		return []agents.Message{
			{
				Type: agents.MessageTypeHuman,
				Message: "Изменений для ревью нет (дифф пуст, либо содержит только " +
					"сгенерированные/бинарные файлы). Заверши ревью без вызова инструментов.",
			},
		}
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

	// 1. Лимит замечаний за одно ревью (Feature 1). Оставляем только
	//    первые MaxComments, остальные игнорируем.
	if cr.cfg.MaxComments > 0 && len(comments) > cr.cfg.MaxComments {
		comments = comments[:cr.cfg.MaxComments]
	}

	// Публикуем каждое замечание. При сбое фиксируем ошибку на структуре,
	// чтобы ApproveMr в последующем раунде отказался одобрять изменения.
	result := []map[string]string{}
	for _, comment := range comments {
		// 2. Отмечаем критичные замечания для блокировки апрува.
		if isCritical(comment.Text) {
			cr.criticalFound = true
		}

		err := cr.forge.PostComment(comment)
		if err != nil {
			cr.postErrors = append(cr.postErrors, err.Error())
			result = append(result, map[string]string{"status": "error", "message": err.Error()})
			continue
		}
		cr.commentCount++
		result = append(result, map[string]string{"status": "ok"})
	}

	// Кодируем результат обратно в JSON для модели (Tool message)
	resultJSON, _ := json.Marshal(result)

	return resultJSON
}

// isCritical определяет, помечено ли замечание как критичное.
// Критичность задаётся префиксом "критично:" в тексте комментария.
func isCritical(text string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), "критично:")
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

	// 2. Не одобряем, если есть критические замечания (блокирующие).
	if cr.cfg.BlockOnCritical && cr.criticalFound {
		result = map[string]string{
			"status":  "error",
			"message": "нельзя одобрить: есть критические замечания",
		}
		resultJSON, _ := json.Marshal(result)
		return resultJSON
	}

	// Не одобряем, если какое-то замечание не удалось опубликовать.
	if len(cr.postErrors) > 0 {
		result = map[string]string{
			"status":  "error",
			"message": fmt.Sprintf("нельзя одобрить: %d замечание(й) не были обработаны", len(cr.postErrors)),
		}
		resultJSON, _ := json.Marshal(result)
		return resultJSON
	}

	// 8. Собираем легенду апрува (что проверено) и прикладываем к одобрению.
	legend := fmt.Sprintf("Ревью пройдено. Опубликовано замечаний: %d. Изменений принимаю.",
		cr.commentCount)

	err := cr.forge.Approve(legend)
	if err != nil {
		result = map[string]string{"status": "error", "message": err.Error()}
	}

	resultJSON, _ := json.Marshal(result)
	return resultJSON
}

// PostSummaryToPR публикует итоговый отчёт-сводку в тред MR/PR (Feature 7).
// Вызывается раннером после завершения цикла агента, чтобы команда видела
// сводку ревью даже при частичном результате.
func (cr *Codereviewer) PostSummaryToPR() error {
	if cr.summaryPosted {
		return nil
	}

	var b strings.Builder
	b.WriteString("## Итоги код-ревью\n\n")
	fmt.Fprintf(&b, "- Замечаний опубликовано: **%d**\n", cr.commentCount)
	fmt.Fprintf(&b, "- Критических замечаний: **%s**\n", yesNo(cr.criticalFound))
	fmt.Fprintf(&b, "- Ошибок публикации: **%d**\n", len(cr.postErrors))
	if cr.focus != "" {
		fmt.Fprintf(&b, "- Фокус ревью: **%s**\n", cr.focus)
	}
	if len(cr.postErrors) > 0 {
		b.WriteString("\n> Часть замечаний не была опубликована. Проверьте лог агента.\n")
	}

	if err := cr.forge.PostSummary(b.String()); err != nil {
		return err
	}
	cr.summaryPosted = true
	return nil
}

func yesNo(v bool) string {
	if v {
		return "да"
	}
	return "нет"
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

		prompt := langdetect.ReviewPrompt(language, framework)

		// 5. Цель ревью из аргумента CLI — смещает фокус модели.
		if cr.focus != "" {
			prompt += fmt.Sprintf(
				"\n\t\tОсобый фокус ревью: %s. Удели этому аспекту приоритетное внимание.",
				cr.focus)
		}

		// Разъясняем семантику критичности для блокировки апрува (Feature 2).
		prompt += "\n\t\tЗамечание считается 'критично:', если оно нарушает работу приложения или безопасность. " +
			"Если есть хотя бы одно критическое замечание — НЕ вызывай ApproveMr."

		return []agents.Message{
			{
				Type:    agents.MessageTypeSystem,
				Message: prompt,
			},
		}
	}

	return []agents.Message{}
}
