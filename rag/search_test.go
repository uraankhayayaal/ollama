package rag

import (
	"context"
	"errors"
	"sort"
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

// scoredPoint — точка ответа Qdrant с полями payload выдачи. Версия с branch
// и chunk_id нужна для проверки ветко-зависимого поиска.
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

// versionedPoint — точка с версионными маркерами (как её пишет IndexFile).
func versionedPoint(file string, start, end int, score float32, code, branch, commit, chunkID string) *qdrant.ScoredPoint {
	return &qdrant.ScoredPoint{
		Score: score,
		Payload: qdrant.NewValueMap(map[string]any{
			PayloadFile:      file,
			PayloadStartLine: start,
			PayloadEndLine:   end,
			PayloadCode:      code,
			PayloadBranch:    branch,
			PayloadCommit:    commit,
			PayloadChunkID:   chunkID,
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
	// Точки старого индекса (без branch) трактуются как код main.
	if res[0].Branch != MainBranch {
		t.Fatalf("ветка результата: got %q, want %q", res[0].Branch, MainBranch)
	}
}

// Поиск из ветки эпика видит свою ветку и main, но не чужие ветки (Р-4):
// ответ Qdrant с разными branch в payload уходит в выдачу как есть, а фильтр
// запроса содержит объединение branch=ветка/main.
func TestSearchBranchUnionFilter(t *testing.T) {
	store := &queryStore{fakeStore: &fakeStore{}}
	c := newClient(store, &fakeEmbedder{dim: 16}, DefaultCollectionName)

	store.points = []*qdrant.ScoredPoint{
		versionedPoint("server/api.go", 3, 9, 0.9, "func Handler()", "ai/epic/ARCH-01", "e1", "cid-1"),
		versionedPoint("server/other.go", 1, 4, 0.8, "func Other()", MainBranch, "m1", "cid-2"),
	}

	res, err := c.Search(context.Background(), SearchParams{
		Project: "demo", Query: "тест", Scope: "server", Limit: 5, Branch: "ai/epic/ARCH-01",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("результатов: got %d, want 2", len(res))
	}
	if res[0].Branch != "ai/epic/ARCH-01" || res[0].CommitSHA != "e1" || res[0].ChunkID != "cid-1" {
		t.Fatalf("версионные маркеры результата: %+v", res[0])
	}
	f := store.req.GetFilter()
	if !filterHasMatch(f, PayloadProject, "demo") || !filterHasMatch(f, PayloadScope, "server") {
		t.Fatal("фильтр должен содержать project_name и scope")
	}
	if len(f.Should) != 2 {
		t.Fatalf("фильтр ветки должен объединять 2 условия (ветка и main), got %d", len(f.Should))
	}
	var branches []string
	for _, c := range f.Should {
		for _, m := range branchMatches(c) {
			branches = append(branches, m)
		}
	}
	sort.Strings(branches)
	if len(branches) != 2 || branches[0] != "ai/epic/ARCH-01" || branches[1] != MainBranch {
		t.Fatalf("условия фильтра по ветке: %v", branches)
	}
	// Запрос шире выдачи: часть результатов может отсеяться как перекрытая.
	if store.req.GetLimit() < 5 {
		t.Fatalf("лимит запроса: got %d, want >= 5 (overfetch)", store.req.GetLimit())
	}
}

// Перекрытая версия main не попадает в выдачу: если в ветке агента есть своя
// активная версия чанка (тот же chunk_id), версия main отбрасывается.
func TestSearchDropsOverriddenMainVersion(t *testing.T) {
	store := newMemStore()
	c := newClient(store, &textEmbedder{dim: 16}, DefaultCollectionName)
	project := "demo"
	file := "server/api.go"
	code := "func Handler() { serve() }"

	// main и ветка эпика проиндексировали один и тот же чанк.
	if _, err := c.IndexFile(context.Background(), project, file, "server", code,
		IndexOptions{Branch: MainBranch, CommitSHA: "m1"}); err != nil {
		t.Fatalf("IndexFile main: %v", err)
	}
	branchCode := code + " // правка эпика"
	if _, err := c.IndexFile(context.Background(), project, file, "server", branchCode,
		IndexOptions{Branch: "ai/epic/ARCH-01", CommitSHA: "e1"}); err != nil {
		t.Fatalf("IndexFile epic: %v", err)
	}
	// Чужой ветке main видна, а эпику — нет.
	if _, err := c.IndexFile(context.Background(), project, "web/app.tsx", "web", "export const App = () => null",
		IndexOptions{Branch: "ai/epic/ARCH-02", CommitSHA: "e2"}); err != nil {
		t.Fatalf("IndexFile другого эпика: %v", err)
	}

	res, err := c.Search(context.Background(), SearchParams{
		Project: project, Query: "Handler serve", Limit: 10, Branch: "ai/epic/ARCH-01",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	var own, foreign int
	for _, r := range res {
		if r.File == file {
			own++
			if !strings.Contains(r.Snippet, "правка эпика") {
				t.Fatalf("в выдаче должна быть версия ветки, а не main: %q", r.Snippet)
			}
			if r.Branch != "ai/epic/ARCH-01" {
				t.Fatalf("ветка результата: %q", r.Branch)
			}
		}
		if r.Branch == "ai/epic/ARCH-02" {
			foreign++
		}
	}
	if own != 1 {
		t.Fatalf("результатов по перекрытому файлу: %d, want 1 (версия main отсечена)", own)
	}
	if foreign != 0 {
		t.Fatalf("код чужой ветки попал в выдачу: %d результатов", foreign)
	}
}

// Поиск из main не видит изменений веток эпиков (Р-4).
func TestSearchMainIgnoresFeatureBranch(t *testing.T) {
	store := newMemStore()
	c := newClient(store, &textEmbedder{dim: 16}, DefaultCollectionName)

	if _, err := c.IndexFile(context.Background(), "demo", "server/api.go", "server",
		"func Handler() { serve() }", IndexOptions{Branch: MainBranch, CommitSHA: "m1"}); err != nil {
		t.Fatalf("IndexFile main: %v", err)
	}
	if _, err := c.IndexFile(context.Background(), "demo", "server/api.go", "server",
		"func Handler() { serve_v2() }", IndexOptions{Branch: "ai/epic/ARCH-01", CommitSHA: "e1"}); err != nil {
		t.Fatalf("IndexFile epic: %v", err)
	}

	res, err := c.Search(context.Background(), SearchParams{
		Project: "demo", Query: "Handler serve", Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("результатов из main: got %d, want 1", len(res))
	}
	if res[0].Branch != MainBranch || !strings.Contains(res[0].Snippet, "serve()") {
		t.Fatalf("main должен видеть только свою версию: %+v", res[0])
	}
}

// branchMatches — значения branch из keyword-условий в (вложенном) фильтре.
func branchMatches(c *qdrant.Condition) []string {
	var out []string
	var walk func(f *qdrant.Filter)
	walk = func(f *qdrant.Filter) {
		if f == nil {
			return
		}
		for _, cond := range append(append([]*qdrant.Condition{}, f.Must...), f.Should...) {
			switch {
			case cond.GetFilter() != nil:
				walk(cond.GetFilter())
			case cond.GetField() != nil && cond.GetField().GetKey() == PayloadBranch:
				if kw := cond.GetField().GetMatch().GetKeyword(); kw != "" {
					out = append(out, kw)
				}
			}
		}
	}
	walk(c.GetFilter())
	return out
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
