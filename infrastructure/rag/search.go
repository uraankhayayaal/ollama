// Семантический поиск чанков в Qdrant (см. PLAN-2026-09-19-done-qdrant.md, Ф-3;
// поиск с учётом ветки — PLAN-2026-09-27-done-branch-aware-rag.md, Р-4).
//
// Search эмбеддит текст запроса моделью эмбеддингов и опрашивает Qdrant
// (Query) с фильтром по проекту и опциональной области (scope). Результаты —
// топ-N чанков с координатами и сниппетом; лимиты RAG_MAX_RESULTS/
// RAG_READ_MAX_TOTAL защищают контекст модели. Недоступный Qdrant оборачивается
// в UnavailableError — инструмент CodeSearch превращает его в статус skipped.
//
// Поиск с учётом ветки: агент в ветке ai/epic/ARCH-01 должен видеть свою
// ветку и актуальный main, но НЕ изменения соседних эпиков. Поэтому фильтр
// запроса — объединение (should) двух условий: активные чанки ветки и
// активные чанки main, из которых затем исключаются chunk_id, уже
// актуальные в ветке. Исключение делается вторым проходом по файлам
// результатов (см. dropOverriddenByBranch) — так список исключений
// ограничен чанками пары-десятки файлов, а не всем индексом проекта.

package rag

import (
	"context"
	"fmt"
	"strings"

	"github.com/qdrant/go-client/qdrant"
)

// Лимиты выдачи семантического поиска: значения по умолчанию — в domain
// (DefaultMaxResults/DefaultSearchMaxTotal), здесь — внутренние константы.
const (
	// branchOverfetch — во сколько раз запрос к Qdrant шире итоговой выдачи:
	// часть результатов может отсеяться как перекрытая веткой версия main.
	branchOverfetch = 3
	// branchOverfetchMin — минимальный размер запроса при малом limit.
	branchOverfetchMin = 12
)

// Search семантически ищет чанки проекта: эмбеддит запрос через Embedder,
// опрашивает Qdrant Query с фильтром по project (+scope) и возвращает топ-N
// чанков со сниппетами, отсортированных по убыванию релевантности.
//
// Оптимизации (включены автоматически):
//   - Query expansion: длинные запросы (>30 символов) раскладываются на
//     2–4 альтернативные формулировки; результаты объединяются RRF.
//   - Re-ranking: выдача до-ранжируется по ключевому совпадению (BM25-like).
func (c *Client) Search(ctx context.Context, p SearchParams) ([]SearchResult, error) {
	if strings.TrimSpace(p.Project) == "" {
		return nil, fmt.Errorf("rag: не задан проект для поиска")
	}
	if strings.TrimSpace(p.Query) == "" {
		return nil, fmt.Errorf("rag: пустой запрос поиска")
	}

	limit := p.Limit
	if limit <= 0 {
		limit = DefaultMaxResults
	}
	branch := MainBranch
	if b := strings.TrimSpace(p.Branch); b != "" {
		branch = b
	}

	// Query expansion: генерируем альтернативные формулировки.
	queries := ExpandedQueries(p.Query)
	var allResults [][]SearchResult
	var lastErr error
	for _, q := range queries {
		res, serr := c.searchRaw(ctx, p.Project, q, strings.TrimSpace(p.Scope), branch, limit)
		if serr != nil {
			lastErr = serr
			// UnavailableError (Qdrant down) — немедленно, не продолжаем.
			if IsUnavailable(serr) {
				return nil, serr
			}
			// Один запрос провалился (эмбеддинг) — пропускаем, остальные работают.
			continue
		}
		allResults = append(allResults, res)
	}
	if len(allResults) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("rag: все запросы expansion вернули ошибку эмбеддинга")
	}

	// Merge: один запрос — обычный результат, несколько — RRF.
	var merged []SearchResult
	if len(allResults) == 1 {
		merged = allResults[0]
	} else {
		merged = ReciprocalRankFusion(allResults, 60)
	}

	maxTotal := p.MaxTotal
	if maxTotal <= 0 {
		maxTotal = DefaultSearchMaxTotal
	}

	// Ограничиваем лимитом.
	if len(merged) > limit {
		merged = merged[:limit]
	}

	// Re-ranking: комбинация косинусной меры и ключевого совпадения.
	RerankResults(p.Query, merged)

	// Суммарный лимит текста: превышен — хвост списка отбрасываем.
	if total := 0; true {
		kept := merged[:0]
		for _, r := range merged {
			if len(kept) > 0 && total+len(r.Snippet) > maxTotal {
				break
			}
			kept = append(kept, r)
			total += len(r.Snippet)
		}
		merged = kept
	}

	return merged, nil
}

// searchRaw — один.raw запрос к Qdrant: эмбеддит query, фильтрует по проекту/ветке,
// возвращает распарсенные результаты (с branch-filtering, без лимитов).
func (c *Client) searchRaw(ctx context.Context, project, query, scope, branch string, limit int) ([]SearchResult, error) {
	vec, err := c.embed.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("rag: эмбеддинг запроса: %w", err)
	}

	// Запрос к Qdrant шире итоговой выдачи: перекрытые версии main отсеются
	// вторым проходом уже после ответа.
	ask := limit
	if branch != MainBranch {
		ask = limit * branchOverfetch
		if ask < branchOverfetchMin {
			ask = branchOverfetchMin
		}
	}

	filter := c.searchFilter(project, scope, branch)
	res, err := c.store.Query(ctx, &qdrant.QueryPoints{
		CollectionName: c.collection,
		Query:          qdrant.NewQueryDense(vec),
		Limit:          qdrant.PtrOf(uint64(ask)),
		WithPayload:    qdrant.NewWithPayload(true),
		Filter:         filter,
	})
	if err != nil {
		return nil, &UnavailableError{Err: err}
	}

	out := make([]SearchResult, 0, len(res))
	for _, sp := range res {
		payload := sp.GetPayload()
		r := SearchResult{
			File:      payload[PayloadFile].GetStringValue(),
			StartLine: int(payload[PayloadStartLine].GetIntegerValue()),
			EndLine:   int(payload[PayloadEndLine].GetIntegerValue()),
			Score:     sp.GetScore(),
			Snippet:   payload[PayloadCode].GetStringValue(),
			Branch:    branchOrMain(payload[PayloadBranch].GetStringValue()),
			CommitSHA: payload[PayloadCommit].GetStringValue(),
			ChunkID:   payload[PayloadChunkID].GetStringValue(),
		}
		if r.File == "" {
			continue
		}
		out = append(out, r)
	}

	if branch != MainBranch {
		out, err = c.dropOverriddenByBranch(ctx, project, branch, out)
		if err != nil {
			return nil, err
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// searchFilter строит фильтр запроса с учётом ветки (Р-4):
//   - MainBranch — только активные точки main (плюс точки старого индекса без
//     поля branch, они считаются кодом main);
//   - ветка эпика/задачи — объединение активных точек ветки и активных точек
//     main; пересечение по chunk_id отсекается после ответа Qdrant.
func (c *Client) searchFilter(project, scope, branch string) *qdrant.Filter {
	must := []*qdrant.Condition{qdrant.NewMatchKeyword(PayloadProject, project)}
	if scope != "" {
		must = append(must, qdrant.NewMatchKeyword(PayloadScope, scope))
	}
	if branch == MainBranch {
		must = append(must, activeBranchCond(branch, true))
		return &qdrant.Filter{Must: must}
	}
	return &qdrant.Filter{
		Must: must,
		Should: []*qdrant.Condition{
			activeBranchCond(branch, false),
			activeBranchCond(MainBranch, true),
		},
	}
}

// activeBranchCond — условие «актуальный чанк ветки»: replaced_by пуст и ветка
// совпадает. legacy — терпимость к точкам без поля branch (индекс, собранный
// до версионирования): они трактуются как код main. В Qdrant условие should
// внутри filter означает «хотя бы одно из should», поэтому варианты ветки
// объединяются через should, а не через must.
func activeBranchCond(branch string, legacy bool) *qdrant.Condition {
	inner := &qdrant.Filter{
		Should: []*qdrant.Condition{qdrant.NewMatchKeyword(PayloadBranch, branch)},
	}
	if legacy {
		inner.Should = append(inner.Should, qdrant.NewIsNull(PayloadBranch))
	}
	return qdrant.NewFilterAsCondition(&qdrant.Filter{
		Must: []*qdrant.Condition{
			qdrant.NewIsEmpty(PayloadReplacedBy),
			qdrant.NewFilterAsCondition(inner),
		},
	})
}

// dropOverriddenByBranch убирает из выдачи чанки main, которые в ветке
// агента перезаписаны своей версией (тот же chunk_id). Список исключений
// собирается по файлам результатов: скроллом активных точек ветки с фильтром
// по этим файлам — это десятки чанков вместо всего индекса проекта.
func (c *Client) dropOverriddenByBranch(ctx context.Context, project, branch string, results []SearchResult) ([]SearchResult, error) {
	files := make([]string, 0, len(results))
	seen := map[string]bool{}
	for _, r := range results {
		if r.Branch == branch || seen[r.File] {
			continue
		}
		seen[r.File] = true
		files = append(files, r.File)
	}
	if len(files) == 0 {
		return results, nil
	}

	points, err := c.chunkPoints(ctx, project, &qdrant.Filter{Must: []*qdrant.Condition{
		qdrant.NewMatchKeyword(PayloadProject, project),
		qdrant.NewMatchKeyword(PayloadBranch, branch),
		qdrant.NewIsEmpty(PayloadReplacedBy),
		qdrant.NewMatchKeywords(PayloadFile, files...),
	}})
	if err != nil {
		return nil, err
	}
	own := make(map[string]bool, len(points))
	for cid := range points {
		own[cid] = true
	}
	if len(own) == 0 {
		return results, nil
	}

	out := make([]SearchResult, 0, len(results))
	for _, r := range results {
		if r.Branch != branch && r.ChunkID != "" && own[r.ChunkID] {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// branchOrMain — ветка из payload; пусто (старый индекс) — MainBranch.
func branchOrMain(branch string) string {
	if b := strings.TrimSpace(branch); b != "" {
		return b
	}
	return MainBranch
}
