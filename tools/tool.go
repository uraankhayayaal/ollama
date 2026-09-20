package tools

import (
	"ai/board"
	"ai/gitops"
	"ai/rag"
)

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
//	Board    — общая Kanban-доска проекта (инструменты Board*).
//	RAG      — клиент векторной памяти (Qdrant, CodeSearch). Опционален:
//	           при nil или недоступном Qdrant инструмент возвращает skipped
//	           с подсказкой использовать ReadMap/ReadFiles (degrade, как ЛСП).
//	GitExec  — исполнитель git (по умолчанию git CLI). Использует только
//	           ResolveGitConflicts (Ф-4), работающий в конфликтном worktree.
type Deps struct {
	FileOps *FileOps
	Session *ReviewSession
	Board   *board.Store
	RAG     *rag.Client
	GitExec gitops.Executor
}
