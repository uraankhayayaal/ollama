package tools

// Инструмент CodeSearch — семантический поиск по кодовой базе проекта
// (RAG поверх Qdrant, см. PLAN-qdrant.md, Ф-3).
//
// Модель передаёт описание функциональности одной строкой (query) и, при
// необходимости, scope (server/frontend/...). Инструмент эмбеддит запрос,
// опрашивает Qdrant (фильтр по проекту + scope) и возвращает топ-N чанков:
// file, координаты строк, score и сниппет. Это поиск «по смыслу» по всей
// кодовой базе — в отличие от сплошного чтения файлов.
//
// Degrade: RAG опционален. Без клиента (tools.Deps.RAG == nil) или при
// недоступном Qdrant/эмбеддингах — статус skipped с подсказкой использовать
// ReadMap/ReadFiles; шаг не падает (по образцу ЛСП-инструментов).

import (
	"ai/rag"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// CodeSearch — имя инструмента в реестре (см. registry.go newTool).
const CodeSearch = "CodeSearch"

// RAGSearcher — минимальный интерфейс семантического поиска, используемый
// инструментом. Реализуется *rag.Client; выделен, чтобы hermetic-тесты
// работали с fake-реализацией без сети.
type RAGSearcher interface {
	Ping(ctx context.Context) error
	Search(ctx context.Context, p rag.SearchParams) ([]rag.SearchResult, error)
}

// codeSearchTool — обёртка инструмента CodeSearch в реестре.
type codeSearchTool struct {
	// searcher — клиент RAG (нил — инструмент деградирует в skipped).
	searcher RAGSearcher
	// ops — файловый контекст: имя проекта берётся из положения OutputDir
	// (temp/<имя>), чтобы поиск шёл по коду только своего проекта.
	ops *FileOps
}

func (t *codeSearchTool) Name() string { return CodeSearch }
func (t *codeSearchTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name: CodeSearch,
		Description: "Используй этот инструмент, когда ищешь, ГДЕ в проекте реализована определённая функциональность " +
			"(например, «где валидируется токен сессии»), по смыслу, а не по именам файлов. Передай описание одной строкой в query " +
			"(и опционально scope — область проекта: server/frontend/...). Инструмент найдёт в векторной памяти кодовой базы топ " +
			"релевантных функций/методов/структур и вернёт их координаты (file, start_line–end_line), score релевантности и сниппет — " +
			"экономнее, чем читать файлы подряд. Если вернул status skipped (индекс не построен или Qdrant/эмбеддинги недоступны) — " +
			"читай файлы через ReadMap/ReadFiles.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Описание функциональности, которую ищешь, одной строкой (например «где валидируется токен сессии»).",
				},
				"scope": map[string]any{
					"type":        "string",
					"description": "Опционально: область проекта (первый сегмент пути: server, frontend, internal, ...). Пусто — поиск по всему проекту.",
				},
			},
			"required":             []string{"query"},
			"additionalProperties": false,
		},
	}
}
func (t *codeSearchTool) Execute(args map[string]any) ([]byte, error) { return t.exec(args) }

// CodeSearchParams — JSON-параметры инструмента CodeSearch.
type CodeSearchParams struct {
	Query string `json:"query"`
	Scope string `json:"scope"`
}

// exec выполняет семантический поиск и возвращает JSON вида
// {"status":"success","results":[{file,start_line,end_line,score,snippet}]}.
// Все ветки возвращают JSON, ошибка Go используется только для внутренних
// сбоев сериализации (единый стиль файловых инструментов).
func (t *codeSearchTool) exec(args map[string]any) ([]byte, error) {
	var p CodeSearchParams
	if raw, err := json.Marshal(args); err == nil {
		_ = json.Unmarshal(raw, &p)
	}
	query := strings.TrimSpace(p.Query)
	if query == "" {
		out, _ := json.Marshal(map[string]any{
			"status":  "error",
			"message": "параметр query обязателен и не должен быть пустым",
		})
		return out, nil
	}

	if t.searcher == nil {
		return codeSearchJSON(map[string]any{
			"status":  "skipped",
			"message": "векторная память RAG не подключена (нет клиента Qdrant) — используй ReadMap/ReadFiles, либо настрой QDRANT_ADDR и построй индекс командой 'go run . index <имя_проекта>'",
		}), nil
	}

	// Имя проекта — базовое имя OutputDir (temp/<имя>): поиск ограничен кодом
	// этого проекта (фильтр payload project_name).
	project := projectFromOutputDir(t.ops)
	if project == "" {
		return codeSearchJSON(map[string]any{
			"status":  "skipped",
			"message": "не определён проект (OutputDir не задан) — используй ReadMap/ReadFiles",
		}), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), ragSearchTimeout())
	defer cancel()

	// Лёгкий контроль доступности Qdrant перед поиском: недоступность даёт
	// различимую ошибку-подсказку (Ping не трогает эмбеддер).
	if err := t.searcher.Ping(ctx); err != nil {
		if rag.IsUnavailable(err) {
			return codeSearchJSON(map[string]any{
				"status":  "skipped",
				"message": err.Error() + " — используй ReadMap/ReadFiles",
			}), nil
		}
		return codeSearchJSON(map[string]any{"status": "error", "message": err.Error()}), nil
	}

	limit, maxTotal := ragLimits()
	results, err := t.searcher.Search(ctx, rag.SearchParams{
		Project:  project,
		Query:    query,
		Scope:    strings.TrimSpace(p.Scope),
		Limit:    limit,
		MaxTotal: maxTotal,
	})
	if err != nil {
		var unavail *rag.UnavailableError
		if errors.As(err, &unavail) {
			return codeSearchJSON(map[string]any{
				"status":  "skipped",
				"message": err.Error() + " — используй ReadMap/ReadFiles",
			}), nil
		}
		return codeSearchJSON(map[string]any{"status": "error", "message": err.Error()}), nil
	}
	if len(results) == 0 {
		return codeSearchJSON(map[string]any{
			"status":  "success",
			"results": []rag.SearchResult{},
			"message": "по запросу ничего не найдено — сформулируй query иначе или расширь scope",
		}), nil
	}

	return codeSearchJSON(map[string]any{"status": "success", "results": results}), nil
}

// codeSearchJSON сериализует результат инструмента.
func codeSearchJSON(v map[string]any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// projectFromOutputDir выводит имя проекта из OutputDir (базовое имя пути):
// temp/<имя> → "<имя>". Пустой OutputDir — "" (skipped).
func projectFromOutputDir(ops *FileOps) string {
	if ops == nil || ops.OutputDir == "" {
		return ""
	}
	return filepath.Base(filepath.Clean(ops.OutputDir))
}

// ragLimits читает лимиты выдачи CodeSearch из окружения: RAG_MAX_RESULTS
// (по умолчанию rag.DefaultMaxResults) и RAG_READ_MAX_TOTAL (по умолчанию
// rag.DefaultSearchMaxTotal). 0 — значения по умолчанию берёт сам rag.Search.
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
