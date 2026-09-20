package chatassist

// Подмешивание контекста RAG в системный промпт ассистента (Ф-1):
// семантическая выборка по тексту вопроса из векторной памяти (rag.Search)
// с фильтром по проекту — блок «Релевантный код по вопросу» (по образцу
// agents/planner/ragcontext.go, ragProjectBlock). Ассистент должен знать
// релевантный код не только через инструмент CodeSearch, но и из контекста
// первого ответа.
//
// RAG опционален: без клиента (nil), при выключенном контексте, пустом
// вопросе или недоступном Qdrant/эмбеддингах — блока нет, ассистент работает
// как раньше (degrade, как в планировщике).

import (
	"ai/rag"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// RAGSearcher — минимальный интерфейс семантического поиска для ассистента.
// Реализуется *rag.Client; выделен, чтобы hermetic-тесты работали с fake без
// сети (как RAGSearcher в tools/codesearch.go).
type RAGSearcher interface {
	Search(ctx context.Context, p rag.SearchParams) ([]rag.SearchResult, error)
}

// assistantRAGBlockChars — лимит текста блока «релевантный код» (как maxMapChars
// у планировщика): суммарный объём выдачи не превышает лимит контекста.
const assistantRAGBlockChars = 10_000

// assistantRAGBlock — «релевантный код по вопросу»: поиск по тексту вопроса с
// фильтром ПО ПРОЕКТУ (scope пуст — весь проект). Возвращает "" при выключенном
// RAG-контексте, nil-поисковике, пустом вопросе или неудаче поиска (degrade).
func assistantRAGBlock(projectName, query string, r RAGSearcher) string {
	if !ragAssistantContextEnabled() || r == nil || strings.TrimSpace(projectName) == "" || strings.TrimSpace(query) == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), ragAssistantTimeout())
	defer cancel()

	results, err := r.Search(ctx, rag.SearchParams{
		Project:  strings.TrimSpace(projectName),
		Query:    strings.TrimSpace(query),
		Scope:    "",
		Limit:    ragAssistantLimit(),
		MaxTotal: ragAssistantMaxTotal(),
	})
	if err != nil || len(results) == 0 {
		return ""
	}
	return formatAssistantRAGBlock(results)
}

// formatAssistantRAGBlock форматирует найденные чанки в компактный блок:
// file:start-end (score) + сниппет. Суммарный объём блока не превышает
// assistantRAGBlockChars — хвост отбрасывается с пометкой (лимиты контекста
// не превышаются).
func formatAssistantRAGBlock(results []rag.SearchResult) string {
	var b strings.Builder
	head := "Релевантный код по вопросу (семантический поиск):\n"
	b.WriteString(head)
	chars := len(head)
	for i, r := range results {
		line := fmt.Sprintf("  [%d] %s:%d-%d (score %.2f)\n", i+1, r.File, r.StartLine, r.EndLine, r.Score)
		snip := strings.TrimRight(r.Snippet, "\n")
		if chars+len(line)+len(snip)+1 > assistantRAGBlockChars {
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

// ragAssistantContextEnabled — активен ли контекст RAG у ассистента
// (RAG_ASSISTANT_CONTEXT). По умолчанию включён; 0/false/off/no — выключить.
func ragAssistantContextEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("RAG_ASSISTANT_CONTEXT"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// ragAssistantTimeout — лимит на поиск контекста ассистента (RAG_SEARCH_TIMEOUT),
// чтобы зависший Qdrant/эмбеддинги не вешали ответ ассистента.
func ragAssistantTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("RAG_SEARCH_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 30 * time.Second
}

// ragAssistantLimit — лимит чанков в контексте ассистента (RAG_MAX_RESULTS),
// по умолчанию rag.DefaultMaxResults.
func ragAssistantLimit() int {
	if v := strings.TrimSpace(os.Getenv("RAG_MAX_RESULTS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return rag.DefaultMaxResults
}

// ragAssistantMaxTotal — суммарный лимит текста блока (RAG_READ_MAX_TOTAL);
// <=0 — значение по умолчанию берёт сам rag.Search, а formatAssistantRAGBlock
// всё равно упирается в assistantRAGBlockChars.
func ragAssistantMaxTotal() int {
	if v := strings.TrimSpace(os.Getenv("RAG_READ_MAX_TOTAL")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 0
}
