package tools

// Инструменты генератора кода. Определения — единый источник схемы:
// они используются в Definition() (для OpenAI/Yandex) и, после конвертации
// ToOllama, для провайдера Ollama. Реализация делегируется в *FileOps.

type writeFilesTool struct{ ops *FileOps }

func (t *writeFilesTool) Name() string { return "WriteFiles" }
func (t *writeFilesTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        "WriteFiles",
		Description: "Используй этот инструмент для сохранения множества файлов.",
		Parameters: map[string]any{
			"type": "object", // Корень параметров ВСЕГДА object
			"properties": map[string]any{
				"files": map[string]any{
					"type":        "array",
					"description": "Список файлов для записи",
					"items": map[string]any{
						"type":                 "object",
						"properties":           singleFileProps(),
						"required":             []string{"filename", "content"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"files"}, // Массив файлов обязателен для вызова инструмента
			"additionalProperties": false,
		},
	}
}
func (t *writeFilesTool) Execute(args map[string]any) ([]byte, error) { return t.ops.WriteFiles(args) }

type readFilesTool struct{ ops *FileOps }

func (t *readFilesTool) Name() string { return "ReadFiles" }
func (t *readFilesTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        "ReadFiles",
		Description: "Используй этот инструмент для чтения содержимого одного или нескольких файлов проекта.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"filenames": map[string]any{
					"type":        "array",
					"description": "Список путей к файлам, которые нужно прочитать, например ['main.go', 'utils/math.go']",
					"items": map[string]any{
						"type": "string",
					},
				},
				"lines": map[string]any{
					"type":        "string",
					"description": "Опционально: интервал строк для точечного чтения («хирургическое окно»), 1-based включительно, например '20-45' или '40'. Вместо файла целиком возвращаются только эти строки с номерами.",
				},
			},
			"required":             []string{"filenames"},
			"additionalProperties": false,
		},
	}
}
func (t *readFilesTool) Execute(args map[string]any) ([]byte, error) { return t.ops.ReadFiles(args) }

type deleteFilesTool struct{ ops *FileOps }

func (t *deleteFilesTool) Name() string { return "DeleteFiles" }
func (t *deleteFilesTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        "DeleteFiles",
		Description: "Используй этот инструмент для удаления ненужных файлов или папок.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"paths": map[string]any{
					"type":        "array",
					"description": "Список путей к файлам или папкам для удаления, например ['old_code.go', 'temp_dir']",
					"items": map[string]any{
						"type": "string",
					},
				},
			},
			"required":             []string{"paths"},
			"additionalProperties": false,
		},
	}
}
func (t *deleteFilesTool) Execute(args map[string]any) ([]byte, error) {
	return t.ops.DeleteFiles(args)
}

type runTool struct{ ops *FileOps }

func (t *runTool) Name() string { return "Run" }
func (t *runTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        "Run",
		Description: "Используй этот инструмент для запуска команд (например, go build, go vet) в выходной директории сгенерированного проекта, чтобы проверить, что код компилируется и проходит проверки. Возвращает stdout+stderr.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "Команда для запуска, например 'go build ./...'"},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
	}
}
func (t *runTool) Execute(args map[string]any) ([]byte, error) { return t.ops.Run(args) }

type listTool struct{ ops *FileOps }

func (t *listTool) Name() string { return "List" }
func (t *listTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        "List",
		Description: "Используй этот инструмент для получения списка (дерева) файлов в проекте, чтобы узнать, что уже создано, прежде чем читать или изменять.",
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"required":             []string{},
			"additionalProperties": false,
		},
	}
}
func (t *listTool) Execute(args map[string]any) ([]byte, error) { return t.ops.List(args) }

type appendFileTool struct{ ops *FileOps }

func (t *appendFileTool) Name() string { return "AppendFile" }
func (t *appendFileTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        "AppendFile",
		Description: "Используй этот инструмент для добавления текста в конец существующего файла (например, новой функции или реализации). Для больших правок лучше перезаписать файл через WriteFiles.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"filename": map[string]any{"type": "string", "description": "Путь к файлу для дополнения, например 'main.go'"},
				"content":  map[string]any{"type": "string", "description": "Текст, добавляемый в конец файла"},
			},
			"required":             []string{"filename", "content"},
			"additionalProperties": false,
		},
	}
}
func (t *appendFileTool) Execute(args map[string]any) ([]byte, error) { return t.ops.AppendFile(args) }

type patchGoFunctionTool struct{ ops *FileOps }

func (t *patchGoFunctionTool) Name() string { return "PatchGoFunction" }
func (t *patchGoFunctionTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        "PatchGoFunction",
		Description: "Заменяет ТОЛЬКО одну функцию в существующем Go-файле (семантически, через go/ast): находит функцию function_name (методы — по receiver) и заменяет её узел на код из body, остальное в файле не трогается. Используй для ТОЧЕЧНЫХ правок Go-кода вместо перезаписи файла целиком.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target_file":   map[string]any{"type": "string", "description": "Относительный путь к .go-файлу внутри OutputDir, например 'server/internal/user/service.go'"},
				"function_name": map[string]any{"type": "string", "description": "Имя заменяемой функции/метода, например 'CreateUser'"},
				"receiver":      map[string]any{"type": "string", "description": "Имя типа ресивера метода (например 'Service'). Обязательно, если в файле несколько функций с одинаковым именем (методы разных типов). Для обычных функций не указывай."},
				"body":          map[string]any{"type": "string", "description": "ПОЛНЫЙ исходник заменяющей функции, начиная с 'func': 'func (s *Service) CreateUser(ctx context.Context, u *User) error { ... }'. Имя функции в body обязано совпадать с function_name."},
				"imports": map[string]any{
					"type":        "array",
					"description": "Опционально: импорт-пути, которые нужно ДОБАВИТЬ в файл, если их ещё нет ('errors', 'fmt', 'alias \"path\"'). Существующие импорты не трогаются.",
					"items":       map[string]any{"type": "string"},
				},
			},
			"required":             []string{"target_file", "function_name", "body"},
			"additionalProperties": false,
		},
	}
}
func (t *patchGoFunctionTool) Execute(args map[string]any) ([]byte, error) {
	return t.ops.PatchGoFunction(args)
}

type searchReplaceTool struct{ ops *FileOps }

func (t *searchReplaceTool) Name() string { return "SearchReplace" }
func (t *searchReplaceTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        "SearchReplace",
		Description: "Точечная правка кода любого языка (Go, TypeScript/TSX, CSS) через блоки SEARCH/REPLACE: каждый SEARCH должен дословно совпасть с уже существующим кодом (включая отступы). Несовпадение — ошибка без записи; заменяется строго первое вхождение. Используй вместо перезаписи файла, когда правишь фрагмент внутри большого файла.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":        "array",
					"description": "Список файлов с блоками замены",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"filename": map[string]any{"type": "string", "description": "Путь к файлу, например 'frontend/src/App.tsx'"},
							"patches": map[string]any{
								"type":        "array",
								"description": "Блоки замены; применяются по порядку",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"search":  map[string]any{"type": "string", "description": "ТОЧНЫЙ кусок существующего кода, включая все отступы и переводы строк"},
										"replace": map[string]any{"type": "string", "description": "Изменённый/новый код на место search"},
									},
									"required":             []string{"search", "replace"},
									"additionalProperties": false,
								},
							},
							"content": map[string]any{"type": "string", "description": "Альтернатива patches: весь текст блоков <<<<<<< SEARCH ... ======= ... >>>>>>> REPLACE; будет разобран автоматически"},
						},
						"required":             []string{"filename"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"files"},
			"additionalProperties": false,
		},
	}
}
func (t *searchReplaceTool) Execute(args map[string]any) ([]byte, error) {
	return t.ops.SearchReplace(args)
}

// singleFileProps возвращает общую схему свойств для одного файла (filename + content).
func singleFileProps() map[string]any {
	return map[string]any{
		"filename": map[string]any{"type": "string", "description": "Название файла с путем, например 'utils/math.go'"},
		"content":  map[string]any{"type": "string", "description": "Полный исходный код файла"},
	}
}
