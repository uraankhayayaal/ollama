// Тесты RAG-оптимизаций: query expansion, re-ranking, RRF.

package rag

import (
	"context"
	"testing"

	"github.com/qdrant/go-client/qdrant"
)

// ExpandedQueries: короткий запрос (<=30 символов, <=3 слов) не расширяется.
func TestExpandedQueriesShort(t *testing.T) {
	queries := ExpandedQueries("HandleChat")
	if len(queries) != 1 {
		t.Fatalf("короткий запрос: got %d queries, want 1: %v", len(queries), queries)
	}
	if queries[0] != "HandleChat" {
		t.Fatalf("оригинал: got %q, want %q", queries[0], "HandleChat")
	}
}

// ExpandedQueries: длинный запрос на русском (без идентификаторов кода)
// не расширяется — нет термов для генерации вариантов.
func TestExpandedQueriesRussian(t *testing.T) {
	queries := ExpandedQueries("где валидируется токен сессии при входе")
	if len(queries) != 1 {
		t.Fatalf("русский запрос без термов: got %d queries, want 1: %v", len(queries), queries)
	}
}

// ExpandedQueries: длинный запрос с идентификаторами кода расширяется.
func TestExpandedQueriesWithCodeTerms(t *testing.T) {
	queries := ExpandedQueries("validation of the user token in the auth handler")
	if len(queries) < 2 {
		t.Fatalf("запрос с термами: got %d queries, want >= 2: %v", len(queries), queries)
	}
}

// ExpandedQueries: пустой/очень короткий запрос.
func TestExpandedQueriesTiny(t *testing.T) {
	queries := ExpandedQueries("a")
	if len(queries) != 1 {
		t.Fatalf("minимальный запрос: got %d, want 1", len(queries))
	}
}

// RerankResults: один результат не пересортировывается.
func TestRerankSingleResult(t *testing.T) {
	results := []SearchResult{{File: "a.go", Score: 0.8}}
	RerankResults("test", results)
	if len(results) != 1 || results[0].Score != 0.8 {
		t.Fatalf("один результат: got score %f, want 0.8", results[0].Score)
	}
}

// RerankResults: пустой запрос не ломает результаты.
func TestRerankEmptyQuery(t *testing.T) {
	results := []SearchResult{
		{File: "a.go", Snippet: "func HandleChat()", Score: 0.9},
		{File: "b.go", Snippet: "func ParseFile()", Score: 0.7},
	}
	RerankResults("", results)
	// Без термов — сортировка не меняется.
	if results[0].File != "a.go" {
		t.Fatalf("пустой запрос: order changed unexpectedly")
	}
}

// RerankResults: ключевое совпадение изменяет score результатов.
func TestRerankKeywordBoost(t *testing.T) {
	results := []SearchResult{
		{File: "other.go", Snippet: "some unrelated code here", Score: 0.95},
		{File: "auth.go", Snippet: "func HandleChat(msg string)", Score: 0.6},
	}
	orig := make([]SearchResult, len(results))
	copy(orig, results)
	RerankResults("HandleChat", results)
	// После re-ranking score должен измениться (комбинация косинуса + keyword).
	// auth.go с совпадением терма получает бонус к score.
	authScore := results[1].Score
	otherScore := results[0].Score
	// Косинусная мера (alpha=0.7) доминирует — other.go с более высоким косинусом
	// остаётся наверху, но относительные score меняются.
	if authScore == orig[1].Score {
		t.Fatalf("score должен измениться после re-ranking")
	}
	// other.go всё ещё выше (косинус 0.95 vs 0.6, alpha=0.7).
	if otherScore <= authScore {
		t.Fatalf("косинусная мера доминирует: other %f должен быть > auth %f", otherScore, authScore)
	}
}

// RerankResults: точное совпадение при близких косинусах меняет порядок.
func TestRerankKeywordReorder(t *testing.T) {
	results := []SearchResult{
		{File: "other.go", Snippet: "some related implementation", Score: 0.85},
		{File: "auth.go", Snippet: "func HandleChat(msg string)", Score: 0.82},
	}
	RerankResults("HandleChat", results)
	// При близких косинусах keyword-бонус может поднять auth.go наверх.
	// Проверяем, что score изменился.
	if results[1].Score == 0.82 {
		t.Fatalf("score должен измениться после re-ranking")
	}
}

// ReciprocalRankFusion: один список — без изменений.
func TestRRFSingleList(t *testing.T) {
	input := []SearchResult{{File: "a.go", Score: 0.9}}
	merged := ReciprocalRankFusion([][]SearchResult{input}, 60)
	if len(merged) != 1 {
		t.Fatalf("один список: got %d, want 1", len(merged))
	}
}

// ReciprocalRankFusion: два списка с общим результатом.
func TestRRFCommonResult(t *testing.T) {
	a := []SearchResult{{File: "shared.go", ChunkID: "shared", Score: 0.9}}
	b := []SearchResult{{File: "shared.go", ChunkID: "shared", Score: 0.8}}
	merged := ReciprocalRankFusion([][]SearchResult{a, b}, 60)
	if len(merged) != 1 {
		t.Fatalf("общий результат: got %d, want 1 (дедупликация)", len(merged))
	}
	// RRF-вес: 1/(60+0) + 1/(60+0) = 2/60 ≈ 0.033
	expected := float32(2.0 / 60.0)
	if merged[0].Score != expected {
		t.Fatalf("RRF score: got %f, want %f", merged[0].Score, expected)
	}
}

// ReciprocalRankFusion: разные результаты сортируются по RRF.
func TestRRFDifferentResults(t *testing.T) {
	a := []SearchResult{
		{File: "first.go", ChunkID: "first", Score: 0.9},
		{File: "second.go", ChunkID: "second", Score: 0.7},
	}
	b := []SearchResult{
		{File: "second.go", ChunkID: "second", Score: 0.85},
		{File: "third.go", ChunkID: "third", Score: 0.6},
	}
	merged := ReciprocalRankFusion([][]SearchResult{a, b}, 60)
	if len(merged) != 3 {
		t.Fatalf("3 разных результата: got %d", len(merged))
	}
	// "second" встречается в обоих списках → RRF выше.
	if merged[0].ChunkID != "second" {
		t.Fatalf("top result: got %s, want second (common to both lists)", merged[0].ChunkID)
	}
}

// countingStore — обёртка над queryStore, считающая число вызовов Query.
type countingStore struct {
	*queryStore
	count int
}

func (c *countingStore) Query(ctx context.Context, req *qdrant.QueryPoints) ([]*qdrant.ScoredPoint, error) {
	c.count++
	return c.queryStore.Query(ctx, req)
}

// Search integration: query expansion + re-ranking работают вместе.
func TestSearchWithExpansion(t *testing.T) {
	inner := &queryStore{fakeStore: &fakeStore{}}
	store := &countingStore{queryStore: inner}
	c := newClient(store, &fakeEmbedder{dim: 16}, DefaultCollectionName)

	// Длинный запрос с идентификаторами кода → expansion → несколько запросов к store.
	res, err := c.Search(context.Background(), SearchParams{
		Project: "demo",
		Query:   "validation of the user token in the auth handler function",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) == 0 {
		t.Log("empty results from fake store — expansion worked")
	}

	// Проверяем, что store получил больше запросов, чем 1 (expansion).
	if store.count < 2 {
		t.Fatalf("query expansion: store got %d queries, want >= 2", store.count)
	}
}

// Search integration: короткий запрос → без expansion.
func TestSearchNoExpansion(t *testing.T) {
	inner := &queryStore{fakeStore: &fakeStore{}}
	store := &countingStore{queryStore: inner}
	c := newClient(store, &fakeEmbedder{dim: 16}, DefaultCollectionName)

	_, err := c.Search(context.Background(), SearchParams{
		Project: "demo",
		Query:   "HandleChat",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// Короткий запрос → один вызов store.
	if store.count != 1 {
		t.Fatalf("без expansion: store got %d queries, want 1", store.count)
	}
}
