package tools

import (
	"ai/infrastructure/git"
	"ai/tools/rag"
)

// RAGSearcher — селектор векторной памяти (алиас Searcher подпакета ai/tools/rag,
// E6b). Оставлен в tools для обратной совместимости импортов агентов/сервера.
type RAGSearcher = rag.Searcher

// Deps — окружение агента, из которого инструменты получают контекст.
type Deps struct {
	FileOps *FileOps
	Session *ReviewSession
	RAG     RAGSearcher
	GitExec git.Executor
}
