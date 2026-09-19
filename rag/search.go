// Семантический поиск чанков в Qdrant (см. PLAN-qdrant.md, Ф-3).
//
// Search эмбеддит текст запроса моделью эмбеддингов и опрашивает Qdrant
// (Query) с фильтром по проекту и опциональной области (scope). Результаты —
// топ-N чанков с координатами и сниппетом; лимиты RAG_MAX_RESULTS/
// RAG_READ_MAX_TOTAL защищают контекст модели. Недоступный Qdrant оборачивается
// в UnavailableError — инструмент CodeSearch превращает его в статус skipped.

package rag

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/qdrant/go-client/qdrant"
)

// Лимиты выдачи семантического поиска (настраиваются в инструменте CodeSearch
// через RAG_MAX_RESULTS/RAG_READ_MAX_TOTAL; здесь — значения по умолчанию).
const (
	// DefaultMaxResults — максимум чанков в выдаче (RAG_MAX_RESULTS).
	DefaultMaxResults = 3
	// DefaultSearchMaxTotal — суммарный лимит символов сниппетов
	// (RAG_READ_MAX_TOTAL): избыток хвоста отбрасывается.
	DefaultSearchMaxTotal = 150_000
)

// SearchResult — один найденный чанк кода.
type SearchResult struct {
	File      string  `json:"file"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Score     float32 `json:"score"`
	Snippet   string  `json:"snippet"`
}

// SearchParams — параметры семантического поиска.
type SearchParams struct {
	// Project — имя проекта (фильтр payload project_name). Обязателен:
	// поиск всегда ограничен кодом одного проекта.
	Project string
	// Query — текст запроса («где валидируется токен сессии»).
	Query string
	// Scope — опциональный фильтр по области (payload scope, первый сегмент
	// пути: server/frontend/...). Пусто — по всему проекту.
	Scope string
	// Limit — максимум результатов; <=0 — DefaultMaxResults.
	Limit int
	// MaxTotal — суммарный лимит символов сниппетов; <=0 — DefaultSearchMaxTotal.
	MaxTotal int
}

// Search семантически ищет чанки проекта: эмбеддит запрос через Embedder,
// опрашивает Qdrant Query с фильтром по project (+scope) и возвращает топ-N
// чанков со сниппетами, отсортированных по убыванию релевантности.
func (c *Client) Search(ctx context.Context, p SearchParams) ([]SearchResult, error) {
	if strings.TrimSpace(p.Project) == "" {
		return nil, fmt.Errorf("rag: не задан проект для поиска")
	}
	if strings.TrimSpace(p.Query) == "" {
		return nil, fmt.Errorf("rag: пустой запрос поиска")
	}

	vec, err := c.embed.Embed(ctx, p.Query)
	if err != nil {
		return nil, fmt.Errorf("rag: эмбеддинг запроса: %w", err)
	}

	limit := p.Limit
	if limit <= 0 {
		limit = DefaultMaxResults
	}

	filter := &qdrant.Filter{Must: []*qdrant.Condition{
		qdrant.NewMatchKeyword(PayloadProject, p.Project),
	}}
	if scope := strings.TrimSpace(p.Scope); scope != "" {
		filter.Must = append(filter.Must, qdrant.NewMatchKeyword(PayloadScope, scope))
	}

	res, err := c.store.Query(ctx, &qdrant.QueryPoints{
		CollectionName: c.collection,
		Query:          qdrant.NewQueryDense(vec),
		Limit:          qdrant.PtrOf(uint64(limit)),
		WithPayload:    qdrant.NewWithPayload(true),
		Filter:         filter,
	})
	if err != nil {
		return nil, &UnavailableError{Err: err}
	}

	maxTotal := p.MaxTotal
	if maxTotal <= 0 {
		maxTotal = DefaultSearchMaxTotal
	}

	out := make([]SearchResult, 0, len(res))
	total := 0
	for _, sp := range res {
		payload := sp.GetPayload()
		r := SearchResult{
			File:      payload[PayloadFile].GetStringValue(),
			StartLine: int(payload[PayloadStartLine].GetIntegerValue()),
			EndLine:   int(payload[PayloadEndLine].GetIntegerValue()),
			Score:     sp.GetScore(),
			Snippet:   payload[PayloadCode].GetStringValue(),
		}
		if r.File == "" {
			continue
		}
		// Суммарный лимит текста: превышен — хвост списка (уже отсортирован
		// по релевантности) отбрасываем, оставляя набранное.
		if len(out) > 0 && total+len(r.Snippet) > maxTotal {
			break
		}
		out = append(out, r)
		total += len(r.Snippet)
	}

	// Qdrant уже вернул точки по убыванию score; сортировка — страховка для
	// согласованного формата выдачи при любом поведении хранилища.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}
