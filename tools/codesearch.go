package tools

// Инструмент CodeSearch — семантический поиск по кодовой базе проекта
// (RAG поверх Qdrant, см. PLAN-2026-09-19-done-qdrant.md, Ф-3; поиск с учётом
// ветки — PLAN-2026-09-27-done-branch-aware-rag.md, Р-5/Р-6).
//
// Модель передаёт описание функциональности одной строкой (query) и, при
// необходимости, scope (server/frontend/...). Инструмент эмбеддит запрос,
// опрашивает Qdrant (фильтр по проекту + scope + ветка) и возвращает топ-N
// чанков: file, координаты строк, score и сниппет. Это поиск «по смыслу» по
// всей кодовой базе — в отличие от сплошного чтения файлов.
//
// Ветка: поиск версионный — агент в ветке ai/epic/ARCH-01 видит свою ветку и
// актуальный main, но НЕ изменения соседних эпиков. Если параметр branch не
// задан, он определяется из рабочего каталога агента (detectBranch, кэш на
// инструмент) — ветка в рамках шага не меняется; вне git — main. Каждый
// результат помечен строкой происхождения [Файл: … | Ветка: … | Коммит: …],
// чтобы модель не ссылалась на код из чужой ветки.
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
	"sync"
	"time"
)

// CodeSearch — имя инструмента в реестре (см. registry.go newTool).
const CodeSearch = "CodeSearch"

// RAGSearcher — минимальный интерфейс семантического поиска, используемый
// инструментом. Реализуется *rag.Client; выделен, чтобы hermetic-тесты
// работали с fake-реализацией без сети (ProjectInfo — статус индекса для
// RagIndexStatus, Ф-1 PLAN-2026-09-24-done-architect-intelligence.md).
type RAGSearcher interface {
	Ping(ctx context.Context) error
	Search(ctx context.Context, p rag.SearchParams) ([]rag.SearchResult, error)
	ProjectInfo(ctx context.Context, projectName string) (rag.ProjectInfo, error)
}

// codeSearchTool — обёртка инструмента CodeSearch в реестре.
type codeSearchTool struct {
	// searcher — клиент RAG (нил — инструмент деградирует в skipped).
	searcher RAGSearcher
	// ops — файловый контекст: имя проекта и ветка берутся из положения
	// OutputDir (temp/<имя>), чтобы поиск шёл по коду только своего проекта
	// и только своей ветки.
	ops *FileOps

	branchOnce sync.Once
	branch     string // кэш определения ветки (детерминирован в рамках жизни инструмента)
}

func (t *codeSearchTool) Name() string { return CodeSearch }
func (t *codeSearchTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name: CodeSearch,
		Description: "Используй этот инструмент, когда ищешь, ГДЕ в проекте реализована определённая функциональность " +
			"(например, «где валидируется токен сессии»), по смыслу, а не по именам файлов. Передай описание одной строкой в query " +
			"(и опционально scope — область проекта: server/frontend/...). Инструмент найдёт в векторной памяти кодовой базы топ " +
			"релевантных функций/методов/структур и вернёт их координаты (file, start_line–end_line), score релевантности и сниппет — " +
			"экономнее, чем читать файлы подряд. Поиск ВЕТКО-АОСОЗНАННЫЙ: по умолчанию берётся текущая ветка проекта (или branch), " +
			"поэтому в выдаче только код этой ветки и актуального main — изменений соседних веток/эпиков там нет; каждый результат " +
			"помчен строкой [Файл: … | Ветка: … | Коммит: …], не выдавай за существующий код того, чего нет в твоей ветке. " +
			"Если вернул status skipped (индекс не построен или Qdrant/эмбеддинги недоступны) — читай файлы через ReadMap/ReadFiles.",
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
				"branch": map[string]any{
					"type":        "string",
					"description": "Опционально: ветка, в контексте которой искать (например ai/epic/ARCH-01). Пусто — текущая ветка рабочего каталога агента (вне git — main). Выдача: код этой ветки + актуальный main, без перезаписанных в ветке фрагментов.",
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
	// Branch — необязательная ветка поиска; пусто — ветка рабочего каталога
	// агента (detectBranch), вне git — main.
	Branch string `json:"branch"`
}

// exec выполняет семантический поиск и возвращает JSON вида
// {"status":"success","branch":"…","results":[{file,start_line,end_line,score,snippet,branch,commit_sha,chunk_id}]}.
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

	// Ветка поиска: явный параметр агента, иначе текущая ветка рабочего
	// каталога (кэш — ветка в рамках шага не меняется).
	branch := strings.TrimSpace(p.Branch)
	if branch == "" {
		branch = t.detectBranch()
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
		Branch:   branch,
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
			"branch":  branch,
			"results": []rag.SearchResult{},
			"message": "по запросу ничего не найдено — сформулируй query иначе или расширь scope",
		}), nil
	}

	// Сниппеты помечаем происхождением: модель видит файл, ветку и коммит
	// каждого найденного куска и не считает чужую ветку своей.
	for i := range results {
		results[i].Snippet = codeSnippetHeader(results[i]) + "\n" + results[i].Snippet
	}

	return codeSearchJSON(map[string]any{
		"status":  "success",
		"branch":  branch,
		"results": results,
	}), nil
}

// codeSnippetHeader — маркер происхождения чанка над его кодом:
// [Файл: src/auth.go | Ветка: ai/epic/ARCH-01 | Коммит: a1b2c3d].
func codeSnippetHeader(r rag.SearchResult) string {
	commit := strings.TrimSpace(r.CommitSHA)
	if len(commit) > 8 {
		commit = commit[:8]
	}
	if commit == "" {
		commit = "-"
	}
	return "[Файл: " + r.File + " | Ветка: " + r.Branch + " | Коммит: " + commit + "]"
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

// detectBranch определяет ветку рабочего каталога агента для поиска по RAG
// (Р-6): вне git (или git недоступен) — main, поэтому поведение консольных
// проектов без gitflow не меняется. Результат кэшируется: ветка в рамках
// работы агента не меняется, а git-вызов на каждый CodeSearch не нужен.
func (t *codeSearchTool) detectBranch() string {
	t.branchOnce.Do(func() {
		dir := ""
		if t.ops != nil {
			dir = t.ops.OutputDir
		}
		branch, err := rag.DetectBranch(dir)
		if err != nil || branch == "" {
			t.branch = rag.MainBranch
			return
		}
		t.branch = branch
	})
	return t.branch
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
