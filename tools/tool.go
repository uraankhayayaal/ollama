package tools

import (
	"ai/domain/services"
	"ai/infrastructure/git"
)

// Deps — окружение агента, из которого инструменты получают контекст.
// Агент заполняет только те поля, которые нужны выбранным инструментам.
//
//	FileOps  — контекст файловых операций (генератор/рефакторинг кода).
//	Session  — состояние цикла код-ревью (ReviewMr/ApproveMr/NextChunk).
//	Board    — общая Kanban-доска проекта (инструменты Board*).
//	RAG      — селектор векторной памяти (Qdrant, CodeSearch). Опционален:
//	           при nil или недоступном Qdrant инструмент возвращает skipped
//	           с подсказкой использовать ReadMap/ReadFiles (degrade, как ЛСП).
//	           Интерфейс (Ping+Search) вместо *rag.Client позволяет hermetic-
//	           тестам агентов подставлять fake-реализацию без сети.
//	GitExec  — исполнитель git (по умолчанию git CLI). Использует только
//	           ResolveGitConflicts (Ф-4), работающий в конфликтном worktree.
type Deps struct {
	FileOps *FileOps
	Session *ReviewSession
	Board   services.BoardStore
	RAG     RAGSearcher
	GitExec git.Executor
}
