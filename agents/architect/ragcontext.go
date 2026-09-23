package architect

// Подмешивание контекста RAG в системный промпт архитектора (Ф-1): семантическая
// выборка по тексту задачи из векторной памяти (rag.Search) с фильтром по
// проекту — блок «Релевантный код по задаче» (по образцу agents/chatassist/
// ragcontext.go, assistantRAGBlock). Архитектор должен знать релевантный код
// не только через инструмент CodeSearch, но и из контекста первого ответа.
//
// RAG опционален: без клиента (nil), при выключенном контексте или недоступном
// Qdrant/эмбеддингах — блока нет, архитектор работает как раньше (degrade).

import (
	"ai/rag"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// RAGSearcher — минимальный интерфейс семантического поиска для архитектора.
// Реализуется *rag.Client (совместим с tools.RAGSearcher); выделен, чтобы
// hermetic-тесты работали с fake без сети (как RAGSearcher в tools/codesearch.go).
type RAGSearcher interface {
	Search(ctx context.Context, p rag.SearchParams) ([]rag.SearchResult, error)
}

// projectNameFromOutputDir выводит имя проекта из пути OutputDir (базовое имя):
// temp/<имя> → "<имя>". Пустой путь — "" (блок RAG не строится).
func projectNameFromOutputDir(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Base(filepath.Clean(dir))
}

// architectRAGBlockChars — лимит текста блока «релевантный код» (как
// assistantRAGBlockChars у ассистента): суммарный объём выдачи не превышает
// лимит контекста.
const architectRAGBlockChars = 10_000

// architectRAGBlock — «релевантный код по задаче»: поиск по тексту задачи с
// фильтром ПО ПРОЕКТУ (scope пуст — весь проект). Возвращает "" при
// выключенном RAG-контексте, nil-поисковике, пустой задаче или неудаче
// поиска (degrade).
func architectRAGBlock(projectName, query string, r RAGSearcher) string {
	if !ragArchitectContextEnabled() || r == nil || strings.TrimSpace(projectName) == "" || strings.TrimSpace(query) == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), ragArchitectTimeout())
	defer cancel()

	results, err := r.Search(ctx, rag.SearchParams{
		Project:  strings.TrimSpace(projectName),
		Query:    strings.TrimSpace(query),
		Scope:    "",
		Limit:    ragArchitectLimit(),
		MaxTotal: ragArchitectMaxTotal(),
	})
	if err != nil || len(results) == 0 {
		return ""
	}
	return formatArchitectRAGBlock(results)
}

// formatArchitectRAGBlock форматирует найденные чанки в компактный блок:
// file:start-end (score) + сниппет. Суммарный объём блока не превышает
// architectRAGBlockChars — хвост отбрасывается с пометкой (лимиты контекста
// не превышаются).
func formatArchitectRAGBlock(results []rag.SearchResult) string {
	var b strings.Builder
	head := "Релевантный код по задаче (семантический поиск):\n"
	b.WriteString(head)
	chars := len(head)
	for i, r := range results {
		line := fmt.Sprintf("  [%d] %s:%d-%d (score %.2f)\n", i+1, r.File, r.StartLine, r.EndLine, r.Score)
		snip := strings.TrimRight(r.Snippet, "\n")
		if chars+len(line)+len(snip)+1 > architectRAGBlockChars {
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

// ragArchitectContextEnabled — активен ли контекст RAG у архитектора
// (RAG_ARCHITECT_CONTEXT). По умолчанию включён; 0/false/off/no — выключить.
func ragArchitectContextEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("RAG_ARCHITECT_CONTEXT"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// ragArchitectTimeout — лимит на поиск контекста архитектора
// (RAG_SEARCH_TIMEOUT), чтобы зависший Qdrant/эмбеддинги не вешали шаг.
func ragArchitectTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("RAG_SEARCH_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 30 * time.Second
}

// ragArchitectLimit — лимит чанков в контексте архитектора (RAG_MAX_RESULTS),
// по умолчанию rag.DefaultMaxResults.
func ragArchitectLimit() int {
	if v := strings.TrimSpace(os.Getenv("RAG_MAX_RESULTS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return rag.DefaultMaxResults
}

// ragArchitectMaxTotal — суммарный лимит текста блока (RAG_READ_MAX_TOTAL);
// <=0 — значение по умолчанию берёт сам rag.Search, а formatArchitectRAGBlock
// всё равно упирается в architectRAGBlockChars.
func ragArchitectMaxTotal() int {
	if v := strings.TrimSpace(os.Getenv("RAG_READ_MAX_TOTAL")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 0
}
