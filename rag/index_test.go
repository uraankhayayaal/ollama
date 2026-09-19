package rag

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/qdrant/go-client/qdrant"
)

// recStore — fakeStore с записью вызванных upsert/delete для проверки
// содержимого точек и инкрементальности.
type recStore struct {
	*fakeStore
	upserted [][]*qdrant.PointStruct
	deleted  []*qdrant.DeletePoints
}

func (r *recStore) Upsert(ctx context.Context, req *qdrant.UpsertPoints) (*qdrant.UpdateResult, error) {
	r.upserted = append(r.upserted, req.Points)
	return r.fakeStore.Upsert(ctx, req)
}

func (r *recStore) Delete(ctx context.Context, req *qdrant.DeletePoints) (*qdrant.UpdateResult, error) {
	r.deleted = append(r.deleted, req)
	return r.fakeStore.Delete(ctx, req)
}

func allPoints(r *recStore) []*qdrant.PointStruct {
	var out []*qdrant.PointStruct
	for _, batch := range r.upserted {
		out = append(out, batch...)
	}
	return out
}

func pointPayload(pt *qdrant.PointStruct, key string) string {
	if v, ok := pt.Payload[key]; ok {
		return v.GetStringValue()
	}
	return ""
}

func pointInt(pt *qdrant.PointStruct, key string) int64 {
	if v, ok := pt.Payload[key]; ok {
		return v.GetIntegerValue()
	}
	return 0
}

const indexSample = `package example

// Sum складывает два числа.
func Sum(a, b int) int {
	return a + b
}
`

// IndexProject чистит точки проекта, нарезает файлы на чанки и грузит их с
// полным payload (project_name/file_path/start_line/end_line/code_content/scope),
// векторы — размерности модели эмбеддингов.
func TestIndexProjectPayload(t *testing.T) {
	store := &recStore{fakeStore: &fakeStore{exists: true}}
	embed := &fakeEmbedder{dim: 768}
	c := newClient(store, embed, DefaultCollectionName)

	res, err := c.IndexProject(context.Background(), "demo", []IndexItem{
		{Path: "internal/math/calc.go", Scope: "internal", Content: indexSample},
		{Path: "main.go", Scope: "root", Content: "package main\n\nfunc main() {}\n"},
	})
	if err != nil {
		t.Fatalf("IndexProject: %v", err)
	}
	if res.Files != 2 || res.Chunks != 2 {
		t.Fatalf("сводка: files=%d chunks=%d, want 2/2", res.Files, res.Chunks)
	}

	// Перед загрузкой — очистка точек проекта фильтром по project_name.
	if len(store.deleted) != 1 {
		t.Fatalf("delete-вызовов: got %d, want 1", len(store.deleted))
	}
	if !deleteFilterMatches(store.deleted[0], "project_name", "demo") {
		t.Fatalf("фильтр удаления должен содержать project_name=demo")
	}

	pts := allPoints(store)
	if len(pts) != 2 {
		t.Fatalf("точек загружено: got %d, want 2", len(pts))
	}
	// id не нулевые.
	for _, p := range pts {
		if p.Id == nil || p.Id.GetNum() == 0 {
			t.Fatal("точка без числового ID")
		}
	}

	foundCalc := false
	foundMain := false
	for _, p := range pts {
		if pointPayload(p, "project_name") != "demo" {
			t.Fatalf("project_name: got %q", pointPayload(p, "project_name"))
		}
		if pointInt(p, "start_line") <= 0 || pointInt(p, "end_line") < pointInt(p, "start_line") {
			t.Fatalf("координаты строк: %d-%d", pointInt(p, "start_line"), pointInt(p, "end_line"))
		}
		if pointPayload(p, "code_content") == "" {
			t.Fatalf("code_content пуст")
		}
		if v := p.Vectors.GetVector().GetDense().GetData(); len(v) != 768 {
			t.Fatalf("вектор не размерности 768 (len=%d)", len(v))
		}
		switch pointPayload(p, "file_path") {
		case "internal/math/calc.go":
			foundCalc = true
			if pointPayload(p, "scope") != "internal" {
				t.Fatalf("scope calc.go: got %q", pointPayload(p, "scope"))
			}
		case "main.go":
			foundMain = true
			if pointPayload(p, "scope") != "root" {
				t.Fatalf("scope main.go: got %q", pointPayload(p, "scope"))
			}
		}
	}
	if !foundCalc || !foundMain {
		t.Fatalf("нет точек обоих файлов: calc=%v main=%v", foundCalc, foundMain)
	}
}

// Повторный прогон идемпотентен: сначала чистится проект, а id точек
// детерминированные — те же чанки не дублируются.
func TestIndexProjectIncrementalNoDuplicates(t *testing.T) {
	store := &recStore{fakeStore: &fakeStore{exists: true}}
	c := newClient(store, &fakeEmbedder{dim: 64}, DefaultCollectionName)

	items := []IndexItem{{Path: "a.go", Scope: "root", Content: indexSample}}

	if _, err := c.IndexProject(context.Background(), "demo", items); err != nil {
		t.Fatalf("первый прогон: %v", err)
	}
	first := pointIDs(allPoints(store))

	// Второй прогон по тем же файлам.
	if _, err := c.IndexProject(context.Background(), "demo", items); err != nil {
		t.Fatalf("второй прогон: %v", err)
	}
	second := pointIDs(allPoints(store))

	// Повторный прогон загрузил те же точки (id детерминированный) — новых
	// уникальных id нет, дубликаты векторов не появляются.
	if len(second) != len(first)*2 {
		t.Fatalf("повторный прогон добавил лишние точки: %d → %d", len(first), len(second))
	}
	uniq := map[uint64]bool{}
	for _, id := range second {
		uniq[id] = true
	}
	if len(uniq) != len(first) {
		t.Fatalf("появились новые id после повторного прогона: %d", len(uniq))
	}
	// Проект чистился на каждом прогоне.
	if len(store.deleted) != 2 {
		t.Fatalf("delete-вызовов: got %d, want 2", len(store.deleted))
	}
}

// IndexFile при переиндексации одного файла удаляет его прежние точки
// (фильтром по project_name+file_path) и грузит новые.
func TestIndexFileDeletesOldChunks(t *testing.T) {
	store := &recStore{fakeStore: &fakeStore{exists: true}}
	c := newClient(store, &fakeEmbedder{dim: 32}, DefaultCollectionName)

	n, err := c.IndexFile(context.Background(), "demo", "server/api.go", "server", indexSample)
	if err != nil {
		t.Fatalf("IndexFile: %v", err)
	}
	if n != 1 {
		t.Fatalf("чанков: got %d, want 1", n)
	}
	if len(store.deleted) != 1 {
		t.Fatalf("delete-вызовов: got %d, want 1", len(store.deleted))
	}
	del := store.deleted[0]
	if !deleteFilterMatches(del, "project_name", "demo") || !deleteFilterMatches(del, "file_path", "server/api.go") {
		t.Fatalf("фильтр удаления должен содержать project_name и file_path")
	}
	if len(allPoints(store)) != 1 {
		t.Fatalf("точек загружено: got %d, want 1", len(allPoints(store)))
	}
}

// Файл без чанков (пустой) удаляется, но ничего не грузит.
func TestIndexFileEmptyContent(t *testing.T) {
	store := &recStore{fakeStore: &fakeStore{exists: true}}
	c := newClient(store, &fakeEmbedder{dim: 32}, DefaultCollectionName)

	n, err := c.IndexFile(context.Background(), "demo", "empty.go", "root", "")
	if err != nil {
		t.Fatalf("IndexFile: %v", err)
	}
	if n != 0 {
		t.Fatalf("чанков: got %d, want 0", n)
	}
	if len(store.deleted) != 1 {
		t.Fatalf("старые точки должны удаляться и при пустом содержимом")
	}
	if len(allPoints(store)) != 0 {
		t.Fatal("пустой файл не должен ничего загружать")
	}
}

// Недоступный Qdrant при индексации — та же graceful-ошибка UnavailableError.
func TestIndexProjectQdrantDown(t *testing.T) {
	store := &recStore{fakeStore: &fakeStore{exists: true, deleteErr: errors.New("unavailable")}}
	c := newClient(store, &fakeEmbedder{dim: 32}, DefaultCollectionName)

	_, err := c.IndexProject(context.Background(), "demo", []IndexItem{{Path: "a.go", Scope: "root", Content: indexSample}})
	if !IsUnavailable(err) {
		t.Fatalf("ошибка должна быть UnavailableError, got %T: %v", err, err)
	}
}

// deleteFilterMatches проверяет условие фильтра удаления по payload-полям.
func deleteFilterMatches(del *qdrant.DeletePoints, field, value string) bool {
	sel := del.GetPoints().GetFilter()
	if sel == nil {
		return false
	}
	for _, c := range sel.Must {
		fc := c.GetField()
		if fc == nil || fc.GetKey() != field {
			continue
		}
		if fc.GetMatch().GetKeyword() == value {
			return true
		}
	}
	return false
}

func pointIDs(pts []*qdrant.PointStruct) []uint64 {
	out := make([]uint64, 0, len(pts))
	for _, p := range pts {
		out = append(out, p.Id.GetNum())
	}
	return out
}

// pointID детерминирован и различает файлы/строки.
func TestPointIDDeterministic(t *testing.T) {
	a := pointID("demo", "a.go", 3)
	b := pointID("demo", "a.go", 3)
	if a != b {
		t.Fatalf("детерминированность: %d != %d", a, b)
	}
	if a == pointID("demo", "a.go", 4) {
		t.Fatal("разные строки не должны давать один ID")
	}
	if a == pointID("demo", "b.go", 3) {
		t.Fatal("разные файлы не должны давать один ID")
	}
	if strings.TrimSpace(chunkEmbedText("a.go", Chunk{Content: "code", StartLine: 1, EndLine: 2})) == "" {
		t.Fatal("chunkEmbedText пуст")
	}
}