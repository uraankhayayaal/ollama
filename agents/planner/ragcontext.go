package planner

// Подмешивание контекста RAG в карту проекта и в промпты шагов
// (см. PLAN-qdrant.md, Ф-4).
//
// Планировщик получает «релевантный код по задаче» семантической выборкой из
// векторной памяти (rag.Search): поиск по тексту глобальной задачи с фильтром
// по проекту, блок добавляется к карте в рамках maxMapChars (не ломая текущий
// лимит). RAG опционален: без клиента (nil) или при недоступном Qdrant/
// эмбеддингах выдача становится обычной картой (degrade, как CodeSearch);
// управление — RAG_PLANNER_CONTEXT (по умолчанию включено).
//
// Scope-связка: scope шага плана (файлы/директории) маппится в фильтр области
// Qdrant (первый сегмент пути — payload scope, см. rag.ScopeForPath), чтобы
// контекст шага выбирался строго из его области — по образцу scopeSourceFiles
// из lspgate.go.

import (
	"ai/rag"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// RAGSearcher — минимальный интерфейс семантического поиска для планировщика.
// Реализуется *rag.Client; выделен, чтобы hermetic-тесты работали с fake без
// сети (как RAGSearcher в tools/codesearch.go).
type RAGSearcher interface {
	Search(ctx context.Context, p rag.SearchParams) ([]rag.SearchResult, error)
}

// BuildProjectMapRAG строит карту проекта (BuildProjectMap) и добавляет блок
// «Релевантный код по задаче»: семантическая выборка по тексту глобальной
// задачи с фильтром по проекту. Проект ещё не создан / RAG недоступен /
// выключен / пустая задача — обычная карта (фолбэк).
func BuildProjectMapRAG(projectName, taskText string, r RAGSearcher) string {
	m := BuildProjectMap(projectName)
	if strings.Contains(m, "ещё не существует") {
		return m
	}
	blk := ragProjectBlock(projectName, taskText, r)
	if blk == "" {
		return m
	}
	return m + "\n" + blk
}

// ragProjectBlock — «релевантный код по задаче»: поиск по тексту задачи с
// фильтром ПО ПРОЕКТУ (scope пуст — весь проект). Возвращает "" при выключенном
// RAG-контексте, nil-поисковике, пустой задаче или неудаче поиска (degrade).
func ragProjectBlock(projectName, taskText string, r RAGSearcher) string {
	if !ragPlannerContextEnabled() || r == nil || strings.TrimSpace(taskText) == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), ragPlannerTimeout())
	defer cancel()

	results, err := r.Search(ctx, rag.SearchParams{
		Project:  projectName,
		Query:    strings.TrimSpace(taskText),
		Scope:    "",
		Limit:    ragStepLimit(),
		MaxTotal: maxMapChars, // блок — строго в рамках maxMapChars
	})
	if err != nil || len(results) == 0 {
		return ""
	}
	return formatRAGBlock(results)
}

// formatRAGBlock форматирует найденные чанки в компактный блок: file:start-end
// (score) + сниппет. Суммарный объём блока не превышает maxMapChars — хвост
// отбрасывается с пометкой (лимиты контекста не превышаются).
func formatRAGBlock(results []rag.SearchResult) string {
	var b strings.Builder
	head := "Релевантный код по задаче (семантический поиск):\n"
	b.WriteString(head)
	chars := len(head)
	for i, r := range results {
		line := fmt.Sprintf("  [%d] %s:%d-%d (score %.2f)\n", i+1, r.File, r.StartLine, r.EndLine, r.Score)
		snip := strings.TrimRight(r.Snippet, "\n")
		if chars+len(line)+len(snip)+1 > maxMapChars {
			b.WriteString("  ... (блок обрезан по лимиту)\n")
			break
		}
		b.WriteString(line)
		b.WriteString("    ")
		b.WriteString(strings.ReplaceAll(snip, "\n", "\n    "))
		b.WriteString("\n")
		chars += len(line) + len(snip) + 1
	}
	return b.String()
}

// ragScopeFromScope выводит фильтр области Qdrant из scope шага плана: первый
// сегмент пути каждого вхождения (по rag.ScopeForPath) — server/frontend/root.
// Пустой scope (весь проект) или разнородные сегменты → "" (поиск по всему
// проекту): один фильтр их не покрывает.
//
//	["server/internal/auth/", "server/go.mod"] → "server"
//	["frontend/src/App.tsx"]                        → "frontend"
//	["README.md"]                                  → "root"
//	["server/…", "frontend/…"]                     → ""
//	[]                                             → ""
func ragScopeFromScope(scope []string) string {
	var first string
	for _, s := range scope {
		s = strings.TrimSpace(s)
		if s == "" || s == "." {
			continue
		}
		sc := rag.ScopeForPath(s)
		if first == "" {
			first = sc
		} else if first != sc {
			return ""
		}
	}
	return first
}

// stepRAGContext — блок «Релевантный код по шагу» для промпта шага: семантическая
// выборка по тексту шага (prompt, иначе description) с фильтром ОБЛАСТИ шага
// (ragScopeFromScope → scope Qdrant). RAG выключен / nil-поисковик / пустой шаг
// / ошибка поиска — "" (шаг не падает, исполняется как раньше).
func stepRAGContext(ctx context.Context, projectName string, s *Step, r RAGSearcher) string {
	if !ragPlannerContextEnabled() || r == nil || s == nil {
		return ""
	}
	query := strings.TrimSpace(s.Prompt)
	if query == "" {
		query = strings.TrimSpace(s.Description)
	}
	if query == "" {
		return ""
	}
	searchCtx, cancel := context.WithTimeout(ctx, ragPlannerTimeout())
	defer cancel()

	results, err := r.Search(searchCtx, rag.SearchParams{
		Project:  projectName,
		Query:    query,
		Scope:    ragScopeFromScope(s.Scope),
		Limit:    ragStepLimit(),
		MaxTotal: ragStepMaxTotal(),
	})
	if err != nil || len(results) == 0 {
		return ""
	}
	return formatRAGBlock(results)
}

// ragPlannerContextEnabled — активен ли контекст RAG у планировщика
// (RAG_PLANNER_CONTEXT). По умолчанию включён; 0/false/off/no — выключить.
func ragPlannerContextEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("RAG_PLANNER_CONTEXT"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// ragPlannerTimeout — лимит на поиск контекста планировщика (RAG_SEARCH_TIMEOUT),
// чтобы зависший Qdrant/эмбеддинги не вешали построение плана или шага.
func ragPlannerTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("RAG_SEARCH_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 30 * time.Second
}

// ragStepLimit — лимит чанков в контексте карты/шага (RAG_MAX_RESULTS),
// по умолчанию rag.DefaultMaxResults.
func ragStepLimit() int {
	if v := strings.TrimSpace(os.Getenv("RAG_MAX_RESULTS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return rag.DefaultMaxResults
}

// ragStepMaxTotal — суммарный лимит текста контекста шага (RAG_READ_MAX_TOTAL);
// <=0 — значение по умолчанию берёт сам rag.Search, а formatRAGBlock всё равно
// упирается в maxMapChars.
func ragStepMaxTotal() int {
	if v := strings.TrimSpace(os.Getenv("RAG_READ_MAX_TOTAL")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 0
}
