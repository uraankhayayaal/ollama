package tools

// Tool — общий интерфейс инструмента агента. Реализации живут в этом пакете
// (registry.go), а агенты выбирают нужные по имени через Select.
type Tool interface {
	// Name возвращает имя инструмента (например "WriteFiles").
	Name() string
	// Definition возвращает единый источник схемы инструмента:
	// JSON Schema параметров используется и для OpenAI/Yandex, и (после
	// конвертации ToOllama) для Ollama.
	Definition() ToolDefinition
	// Execute выполняет инструмент с аргументами, разобранными из JSON.
	Execute(args map[string]any) ([]byte, error)
}

// Deps — окружение агента, из которого инструменты получают контекст.
// Агент заполняет только те поля, которые нужны выбранным инструментам.
//
//	FileOps  — контекст файловых операций (генератор/рефакторинг кода).
//	Session  — состояние цикла код-ревью (ReviewMr/ApproveMr/NextChunk).
type Deps struct {
	FileOps *FileOps
	Session *ReviewSession
}
