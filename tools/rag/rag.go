// Package rag — инструменты семантического поиска (RAG поверх Qdrant),
// вынесенные из пакета tools в подпакет по домену (E6b).
//
// Подпакет вправе импортировать infrastructure/rag: RAG-контракт богаче порта
// services.RAGClient (Searcher/ProjectInfo/UnavailableError/DetectBranch), а
// перенос всей этой поверхности в domain/services загрязнил бы домен
// (DetectBranch вообще ходит в git). Обратная зависимость от файлового
// контекста инвертирована интерфейсом Context — реализуется *tools.FileOps.
package rag

import (
	"os"
	"strconv"
	"strings"
	"time"

	infrarag "ai/infrastructure/rag"
)

// Имена инструментов в реестре (см. tools/registry.go newTool).
const (
	CodeSearch  = "CodeSearch"
	IndexStatus = "RagIndexStatus"
)

// Searcher — клиент RAG (см. infrastructure/rag). Инструменты деградируют в
// skipped, если Searcher == nil.
type Searcher = infrarag.Searcher

// Context — файловый контекст, нужный RAG-инструментам: имя проекта (фильтр
// payload project_name) и рабочий каталог (определение ветки). Читается ЛЕНИВО:
// оркестрация задаёт их на старте задачи (SetProjectName/SetOutputDir) уже после
// сборки набора инструментов. Реализуется *tools.FileOps; может быть nil.
type Context interface {
	ProjectName() string
	WorkingDir() string
}

// projectNameOf — имя проекта для фильтра RAG (явно заданное имя, иначе
// basename рабочего каталога — логика в Context). Пусто → инструмент skipped.
func projectNameOf(ctx Context) string {
	if ctx == nil {
		return ""
	}
	return ctx.ProjectName()
}

// ragLimits читает лимиты выдачи CodeSearch из окружения: RAG_MAX_RESULTS
// (по умолчанию infrarag.DefaultMaxResults) и RAG_READ_MAX_TOTAL (по умолчанию
// infrarag.DefaultSearchMaxTotal). 0 — значения по умолчанию берёт сам Search.
func ragLimits() (maxResults, maxTotal int) {
	if v := strings.TrimSpace(os.Getenv("RAG_MAX_RESULTS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxResults = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("RAG_READ_MAX_TOTAL")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxTotal = n
		}
	}
	return maxResults, maxTotal
}

// ragSearchTimeout — лимит на весь цикл поиска (Ping + эмбеддинг + Query),
// чтобы код с недоступным/зависшим Qdrant не вешал шаг агента.
func ragSearchTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("RAG_SEARCH_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 30 * time.Second
}
