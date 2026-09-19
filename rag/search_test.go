package rag

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/qdrant/go-client/qdrant"
)

// queryStore — fakeStore с настраиваемым ответом на Query (запись запроса).
type queryStore struct {
	*fakeStore
	points []*qdrant.ScoredPoint
	err    error
	req    *qdrant.QueryPoints
}

func (q *queryStore) Query(ctx context.Context, req *qdrant.QueryPoints) ([]*qdrant.ScoredPoint, error) {
	q.req = req
	if q.err != nil {
		return nil, q.err
	}
	return q.points, nil
}

func scoredPoint(file string, start, end int, score float32, code string) *qdrant.ScoredPoint {
	return &qdrant.ScoredPoint{
		Score: score,
		Payload: qdrant.NewValueMap(map[string]any{
			PayloadFile:      file,
			PayloadStartLine: start,
			PayloadEndLine:   end,
			PayloadCode:      code,
		}),
	}
}

// Search эмбеддит запрос, строит фильтр project (+scope) и разбирает точки.
func TestSearchBuildsFilterAndParsesResults(t *testing.T) {
	store := &queryStore{fakeStore: &fakeStore{}}
	embed := &fakeEmbedder{dim: 32}
	c := newClient(store, embed, DefaultCollectionName)

	store.points = []*qdrant.ScoredPoint{
		scoredPoint("server/auth/token.go", 12, 26, 0.86, "func ValidateToken(...) {...}"),
		scoredPoint("server/auth/session.go", 4, 9, 0.72, "func NewSession(...) {...}"),
	}

	res, err := c.Search(context.Background(), SearchParams{
		Project: "demo", Query: "где валидируется токен сессии", Scope: "server", Limit: 5,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("результатов: got %d, want 2", len(res))
	}

	// Запрос к Qdrant: коллекция, фильтр по project_name+scope, лимит, вектор.
	req := store.req
	if req == nil {
		t.Fatal("QdrantQuery не вызван")
	}
	if req.CollectionName != DefaultCollectionName {
		t.Fatalf("коллекция: got %q", req.CollectionName)
	}
	if req.GetLimit() != 5 {
		t.Fatalf("лимит запроса: got %d, want 5", req.GetLimit())
	}
	f := req.GetFilter()
	if f == nil || !filterHasMatch(f, PayloadProject, "demo") || !filterHasMatch(f, PayloadScope, "server") {
		t.Fatal("фильтр должен содержать project_name и scope")
	}
	if v := req.GetQuery().GetNearest().GetDense().GetData(); len(v) != 32 {
		t.Fatalf("вектор запроса: got len %d, want 32", len(v))
	}
	if len(embed.texts) != 1 || !strings.Contains(embed.texts[0], "токен") {
		t.Fatalf("запрос не подан на эмбеддинг: %v", embed.texts)
	}

	// Результаты отсортированы по убыванию score и содержат payload.
	if res[0].File != "server/auth/token.go" || res[0].Score != 0.86 ||
		res[0].StartLine != 12 || res[0].EndLine != 26 ||
		!strings.Contains(res[0].Snippet, "ValidateToken") {
		t.Fatalf("первый результат: %+v", res[0])
	}
	if res[1].Score != 0.72 {
		t.Fatalf("второй результат должен быть с меньшим score: %+v", res[1])
	}
}

// Без scope фильтр содержит только project_name.
func TestSearchWithoutScope(t *testing.T) {
	store := &queryStore{fakeStore: &fakeStore{}}
	c := newClient(store, &fakeEmbedder{dim: 16}, DefaultCollectionName)

	_, err := c.Search(context.Background(), SearchParams{Project: "demo", Query: "тест"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	f := store.req.GetFilter()
	if !filterHasMatch(f, PayloadProject, "demo") {
		t.Fatal("фильтр должен содержать project_name")
	}
	if f.Must != nil {
		for _, cond := range f.Must {
			if cond.GetField().GetKey() == PayloadScope {
				t.Fatal("фильтр не должен содержать scope без параметра")
			}
		}
	}
}

// RAG_READ_MAX_TOTAL обрезает хвост списка по суммарному объёму сниппетов.
func TestSearchMaxTotalTruncates(t *testing.T) {
	store := &queryStore{fakeStore: &fakeStore{}}
	c := newClient(store, &fakeEmbedder{dim: 16}, DefaultCollectionName)

	long := strings.Repeat("x", 30)
	store.points = []*qdrant.ScoredPoint{
		scoredPoint("a.go", 1, 1, 0.9, long),
		scoredPoint("b.go", 1, 1, 0.8, long),
		scoredPoint("c.go", 1, 1, 0.7, long),
	}

	res, err := c.Search(context.Background(), SearchParams{
		Project: "demo", Query: "тест", Limit: 3, MaxTotal: 80,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("лимит MaxTotal=80 при сниппетах по 30: got %d, want 2", len(res))
	}
}

// Предел results по умолчанию — DefaultMaxResults.
func TestSearchDefaultLimit(t *testing.T) {
	store := &queryStore{fakeStore: &fakeStore{}}
	c := newClient(store, &fakeEmbedder{dim: 16}, DefaultCollectionName)

	_, err := c.Search(context.Background(), SearchParams{Project: "demo", Query: "тест"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := store.req.GetLimit(); got != uint64(DefaultMaxResults) {
		t.Fatalf("лимит по умолчанию: got %d, want %d", got, uint64(DefaultMaxResults))
	}
}

// Недоступный Qdrant — UnavailableError (инструмент → skipped).
func TestSearchQdrantDown(t *testing.T) {
	store := &queryStore{fakeStore: &fakeStore{}, err: errors.New("connection refused")}
	c := newClient(store, &fakeEmbedder{dim: 16}, DefaultCollectionName)

	_, err := c.Search(context.Background(), SearchParams{Project: "demo", Query: "тест"})
	if !IsUnavailable(err) {
		t.Fatalf("ошибка должна быть UnavailableError, got %T: %v", err, err)
	}
}

// Пустой запрос и пустой проект — ошибки валидации до обращения к хранилищу.
func TestSearchValidation(t *testing.T) {
	c := newClient(&queryStore{fakeStore: &fakeStore{}}, &fakeEmbedder{dim: 16}, DefaultCollectionName)

	if _, err := c.Search(context.Background(), SearchParams{Project: "", Query: "тест"}); err == nil {
		t.Fatal("пустой проект должен быть ошибкой")
	}
	if _, err := c.Search(context.Background(), SearchParams{Project: "demo", Query: "  "}); err == nil {
		t.Fatal("пустой запрос должен быть ошибкой")
	}
}

// filterHasMatch проверяет наличие keyword-условия field=value в фильтре.
func filterHasMatch(f *qdrant.Filter, field, value string) bool {
	if f == nil {
		return false
	}
	for _, cond := range f.Must {
		fc := cond.GetField()
		if fc == nil || fc.GetKey() != field {
			continue
		}
		if fc.GetMatch().GetKeyword() == value {
			return true
		}
	}
	return false
}
