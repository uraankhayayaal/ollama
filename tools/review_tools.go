package tools

// Инструменты ревьюера. Определения — единый источник схемы: они используются
// в Definition() (OpenAI/Yandex) и, после конвертации ToOllama, для провайдера
// Ollama. Реализация делегируется в общий *ReviewSession, который разделяется
// между вызовами инструментов на протяжении цикла ревью.

type reviewMrTool struct{ ses *ReviewSession }

func (t *reviewMrTool) Name() string { return "ReviewMr" }
func (t *reviewMrTool) Definition() ToolDefinition {
	return ToolDefinition{
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
	}
}
func (t *reviewMrTool) Execute(args map[string]any) ([]byte, error) { return t.ses.ReviewMr(args) }

type approveMrTool struct{ ses *ReviewSession }

func (t *approveMrTool) Name() string { return "ApproveMr" }
func (t *approveMrTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        "ApproveMr",
		Description: "Одобрить измений кода, если нет критичных багов и дефектов",
	}
}
func (t *approveMrTool) Execute(args map[string]any) ([]byte, error) { return t.ses.ApproveMr(args) }

type nextChunkTool struct{ ses *ReviewSession }

func (t *nextChunkTool) Name() string { return "NextChunk" }
func (t *nextChunkTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name: "NextChunk",
		Description: "Получить следующую часть диффа для ревью, если текущая просмотрена. " +
			"Вызывать после ReviewMr по каждому чанку. Если частей больше нет, вместо него вызови ApproveMr.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}
func (t *nextChunkTool) Execute(args map[string]any) ([]byte, error) { return t.ses.NextChunk() }
