package rag

import (
	"context"
	"errors"
	"sort"
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

// IndexProject чистит точки старого индекса (без поля branch), нарезает файлы
// на чанки и грузит их с полным payload (project_name/file_path/start_line/
// end_line/code_content/scope + branch/commit_sha/chunk_id), векторы —
// размерности модели эмбеддингов.
func TestIndexProjectPayload(t *testing.T) {
	store := &recStore{fakeStore: &fakeStore{exists: true}}
	embed := &fakeEmbedder{dim: 768}
	c := newClient(store, embed, DefaultCollectionName)

	res, err := c.IndexProject(context.Background(), "demo", []IndexItem{
		{Path: "internal/math/calc.go", Scope: "internal", Content: indexSample},
		{Path: "main.go", Scope: "root", Content: "package main\n\nfunc main() {}\n"},
	}, IndexOptions{Branch: MainBranch, CommitSHA: "c1"})
	if err != nil {
		t.Fatalf("IndexProject: %v", err)
	}
	if res.Files != 2 || res.Chunks != 2 {
		t.Fatalf("сводка: files=%d chunks=%d, want 2/2", res.Files, res.Chunks)
	}

	// Перед загрузкой — чистка точек старого индекса (без поля branch).
	if len(store.deleted) != 1 {
		t.Fatalf("delete-вызовов: got %d, want 1", len(store.deleted))
	}
	if !deleteFilterMatches(store.deleted[0], "project_name", "demo") {
		t.Fatalf("фильтр удаления должен содержать project_name=demo")
	}
	if !deleteFilterHasIsNull(store.deleted[0], "branch") {
		t.Fatal("фильтр удаления должен отбирать точки без branch (старый индекс)")
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
		// Версионные поля (Р-1).
		if pointPayload(p, PayloadBranch) != MainBranch {
			t.Fatalf("branch: got %q", pointPayload(p, PayloadBranch))
		}
		if pointPayload(p, PayloadCommit) != "c1" {
			t.Fatalf("commit_sha: got %q", pointPayload(p, PayloadCommit))
		}
		if pointPayload(p, PayloadChunkID) == "" {
			t.Fatal("chunk_id пуст")
		}
		if _, ok := p.Payload[PayloadReplacedBy]; ok {
			t.Fatal("у новой точки не должно быть replaced_by (она актуальна)")
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

// Повторный прогон с тем же коммитом и тем же содержимым идемпотентен: чанки
// не переэмбеддятся и не создают новых точек (Р-3).
func TestIndexProjectSameCommitIdempotent(t *testing.T) {
	store := newMemStore()
	c := newClient(store, &textEmbedder{dim: 16}, DefaultCollectionName)

	items := []IndexItem{{Path: "a.go", Scope: "root", Content: indexSample}}
	opts := IndexOptions{Branch: MainBranch, CommitSHA: "c1"}

	res, err := c.IndexProject(context.Background(), "demo", items, opts)
	if err != nil {
		t.Fatalf("первый прогон: %v", err)
	}
	if res.Chunks != 1 {
		t.Fatalf("загружено чанков: got %d, want 1", res.Chunks)
	}
	first := len(store.points)

	res, err = c.IndexProject(context.Background(), "demo", items, opts)
	if err != nil {
		t.Fatalf("второй прогон: %v", err)
	}
	if res.Chunks != 0 {
		t.Fatalf("повторный прогон загрузил чанки заново: got %d, want 0", res.Chunks)
	}
	if len(store.points) != first {
		t.Fatalf("повторный прогон добавил лишние точки: %d → %d", first, len(store.points))
	}
}

// Незакоммиченная правка агента (тот же HEAD, другое содержимое) обязана попасть
// в индекс: иначе переиндексация после мутации (Ф-5) молча оставляла бы
// агенту устаревший код (Р-3).
func TestIndexFileSameCommitNewContentReindexed(t *testing.T) {
	store := newMemStore()
	c := newClient(store, &textEmbedder{dim: 16}, DefaultCollectionName)
	opts := IndexOptions{Branch: "ai/task/T-01", CommitSHA: "t1"}

	if _, err := c.IndexFile(context.Background(), "demo", "a.go", "root", indexSample, opts); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}
	cid := ChunkID("demo", "a.go", "Sum")
	if !strings.Contains(store.codeOf("ai/task/T-01", cid), "return a + b") {
		t.Fatalf("первая версия не в индексе: %q", store.codeOf("ai/task/T-01", cid))
	}

	// Тот же коммит, но мутация ещё не закоммичена.
	changed := strings.Replace(indexSample, "return a + b", "return a * b", 1)
	if _, err := c.IndexFile(context.Background(), "demo", "a.go", "root", changed, opts); err != nil {
		t.Fatalf("повторный IndexFile: %v", err)
	}
	if got := store.codeOf("ai/task/T-01", cid); !strings.Contains(got, "return a * b") {
		t.Fatalf("незакоммиченная правка не попала в индекс: %q", got)
	}
	if ids := store.activeChunkIDs(); len(ids) != 1 {
		t.Fatalf("активных версий должно остаться 1, got %d (%v)", len(ids), ids)
	}
}

// Новый коммит той же ветки: прежние точки получают replaced_by, активной
// остаётся ровно одна версия чанка (Р-3).
func TestIndexFileVersioningReplacesOldChunk(t *testing.T) {
	store := newMemStore()
	c := newClient(store, &textEmbedder{dim: 16}, DefaultCollectionName)

	if _, err := c.IndexFile(context.Background(), "demo", "server/api.go", "server", indexSample,
		IndexOptions{Branch: MainBranch, CommitSHA: "c1"}); err != nil {
		t.Fatalf("IndexFile c1: %v", err)
	}
	cid := ChunkID("demo", "server/api.go", "Sum")
	if got := store.codeOf(MainBranch, cid); got == "" {
		t.Fatal("активная точка чанка Sum не найдена")
	}
	if len(store.points) != 1 {
		t.Fatalf("точек после первого прогона: %d, want 1", len(store.points))
	}

	// Изменение функции на следующем коммите.
	changed := strings.Replace(indexSample, "return a + b", "return a + b*2", 1)
	if _, err := c.IndexFile(context.Background(), "demo", "server/api.go", "server", changed,
		IndexOptions{Branch: MainBranch, CommitSHA: "c2"}); err != nil {
		t.Fatalf("IndexFile c2: %v", err)
	}
	if len(store.points) != 2 {
		t.Fatalf("точек после смены версии: %d, want 2 (новая + прежняя)", len(store.points))
	}
	if !strings.Contains(store.codeOf(MainBranch, cid), "a + b*2") {
		t.Fatalf("активной осталась не новая версия: %q", store.codeOf(MainBranch, cid))
	}
	if len(store.setPayload) == 0 {
		t.Fatal("прежняя версия должна получить replaced_by через SetPayload")
	}
	if got := store.setPayload[0][PayloadReplacedBy].GetStringValue(); got != "c2" {
		t.Fatalf("replaced_by: got %q, want c2", got)
	}

	// Третья версия: предыдущая след-версия (c2) подчищается, история не растёт.
	if _, err := c.IndexFile(context.Background(), "demo", "server/api.go", "server", indexSample,
		IndexOptions{Branch: MainBranch, CommitSHA: "c3"}); err != nil {
		t.Fatalf("IndexFile c3: %v", err)
	}
	if len(store.points) != 2 {
		t.Fatalf("после третьей версии точек: %d, want 2 (актуальная + предыдущая)", len(store.points))
	}
}

// Изоляция веток: переиндексация файла в ветке эпика не трогает точки main и
// не выдаёт их за код ветки (Р-3).
func TestIndexFileBranchIsolation(t *testing.T) {
	store := newMemStore()
	c := newClient(store, &textEmbedder{dim: 16}, DefaultCollectionName)

	if _, err := c.IndexFile(context.Background(), "demo", "server/api.go", "server", indexSample,
		IndexOptions{Branch: MainBranch, CommitSHA: "m1"}); err != nil {
		t.Fatalf("IndexFile main: %v", err)
	}
	epicChanged := strings.Replace(indexSample, "return a + b", "return a - b", 1)
	if _, err := c.IndexFile(context.Background(), "demo", "server/api.go", "server", epicChanged,
		IndexOptions{Branch: "ai/epic/ARCH-01", CommitSHA: "e1"}); err != nil {
		t.Fatalf("IndexFile epic: %v", err)
	}
	// main не тронут: его точка осталась активной, точка ветки — отдельная.
	cid := ChunkID("demo", "server/api.go", "Sum")
	if got := store.codeOf(MainBranch, cid); !strings.Contains(got, "a + b") {
		t.Fatalf("версия main должна остаться активной: %q", got)
	}
	if got := store.codeOf("ai/epic/ARCH-01", cid); !strings.Contains(got, "a - b") {
		t.Fatalf("активной должна быть версия ветки эпика: %q", got)
	}
	if len(store.points) != 2 {
		t.Fatalf("ожидались 2 точки (main + ветка), got %d", len(store.points))
	}
	// Точка ветки помечена веткой, main — своей.
	var branches []string
	for _, p := range store.points {
		branches = append(branches, p.payload[PayloadBranch].GetStringValue())
	}
	sort.Strings(branches)
	if len(branches) != 2 || branches[0] != "ai/epic/ARCH-01" || branches[1] != MainBranch {
		t.Fatalf("ветки точек: %v", branches)
	}
}

// IndexFile при переиндексации одного файла помечает прежние точки файла
// устаревшими (replaced_by) и грузит новые.
func TestIndexFileReplacesOldChunks(t *testing.T) {
	store := &recStore{fakeStore: &fakeStore{exists: true}}
	c := newClient(store, &fakeEmbedder{dim: 32}, DefaultCollectionName)

	n, err := c.IndexFile(context.Background(), "demo", "server/api.go", "server", indexSample,
		IndexOptions{Branch: MainBranch})
	if err != nil {
		t.Fatalf("IndexFile: %v", err)
	}
	if n != 1 {
		t.Fatalf("чанков: got %d, want 1", n)
	}
	if len(allPoints(store)) != 1 {
		t.Fatalf("точек загружено: got %d, want 1", len(allPoints(store)))
	}
}

// Без коммита (проект без git) прежние точки файла удаляются целиком, а новая
// версия грузится в ветку main (обратная совместимость, решение №7).
func TestIndexFileWithoutCommitHardDeletes(t *testing.T) {
	store := newMemStore()
	c := newClient(store, &textEmbedder{dim: 16}, DefaultCollectionName)

	if _, err := c.IndexFile(context.Background(), "demo", "a.go", "root", indexSample, IndexOptions{}); err != nil {
		t.Fatalf("первый IndexFile: %v", err)
	}
	if _, err := c.IndexFile(context.Background(), "demo", "a.go", "root", indexSample, IndexOptions{}); err != nil {
		t.Fatalf("второй IndexFile: %v", err)
	}
	// Удаление прежних точек — по списку ID (не фильтром).
	var byID int
	for _, d := range store.deleted {
		if d.GetPoints().GetPoints() != nil {
			byID++
		}
	}
	if byID == 0 {
		t.Fatal("без коммита прежние точки должны удаляться по списку ID")
	}
	if len(store.setPayload) != 0 {
		t.Fatal("без коммита replaced_by не проставляется")
	}
}

// Файл без чанков (пустой) ничего не грузит.
func TestIndexFileEmptyContent(t *testing.T) {
	store := &recStore{fakeStore: &fakeStore{exists: true}}
	c := newClient(store, &fakeEmbedder{dim: 32}, DefaultCollectionName)

	n, err := c.IndexFile(context.Background(), "demo", "empty.go", "root", "",
		IndexOptions{Branch: MainBranch, CommitSHA: "c1"})
	if err != nil {
		t.Fatalf("IndexFile: %v", err)
	}
	if n != 0 {
		t.Fatalf("чанков: got %d, want 0", n)
	}
	if len(allPoints(store)) != 0 {
		t.Fatal("пустой файл не должен ничего загружать")
	}
}

// Удаление файла (Р-8): активные точки файла в ветке получают replaced_by, и
// поиск их больше не возвращает.
func TestDeleteFileMarksSuperseded(t *testing.T) {
	store := newMemStore()
	c := newClient(store, &textEmbedder{dim: 16}, DefaultCollectionName)
	opts := IndexOptions{Branch: "ai/task/T-01", CommitSHA: "t1"}

	if _, err := c.IndexFile(context.Background(), "demo", "server/api.go", "server", indexSample, opts); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}
	if len(store.activeChunkIDs()) != 1 {
		t.Fatalf("активных чанков: %d, want 1", len(store.activeChunkIDs()))
	}

	if err := c.DeleteFile(context.Background(), "demo", "server/api.go",
		IndexOptions{Branch: "ai/task/T-01", CommitSHA: "t2"}); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if got := store.activeChunkIDs(); len(got) != 0 {
		t.Fatalf("после удаления файла активных чанков: %d, want 0 (%v)", len(got), got)
	}
	// Точка сохранена как история версий с меткой замены.
	if len(store.points) != 1 {
		t.Fatalf("точек после удаления: %d, want 1 (история версий)", len(store.points))
	}
	if got := store.setPayload[0][PayloadReplacedBy].GetStringValue(); got != "t2" {
		t.Fatalf("replaced_by после удаления: got %q, want t2", got)
	}
}

// Полный прогон ветки: файлы, исчезнувшие из индекса, тоже помечаются
// устаревшими (файл удалён мутацией).
func TestIndexProjectVanishedFileSuperseded(t *testing.T) {
	store := newMemStore()
	c := newClient(store, &textEmbedder{dim: 16}, DefaultCollectionName)
	opts := IndexOptions{Branch: "ai/epic/ARCH-02", CommitSHA: "e1"}

	items := []IndexItem{
		{Path: "a.go", Scope: "root", Content: indexSample},
		{Path: "gone.go", Scope: "root", Content: indexSample},
	}
	if _, err := c.IndexProject(context.Background(), "demo", items, opts); err != nil {
		t.Fatalf("первый IndexProject: %v", err)
	}
	if len(store.activeChunkIDs()) != 2 {
		t.Fatalf("активных чанков: %d, want 2", len(store.activeChunkIDs()))
	}

	// gone.go больше не в проекте.
	if _, err := c.IndexProject(context.Background(), "demo", items[:1],
		IndexOptions{Branch: "ai/epic/ARCH-02", CommitSHA: "e2"}); err != nil {
		t.Fatalf("второй IndexProject: %v", err)
	}
	if len(store.activeChunkIDs()) != 1 {
		t.Fatalf("активных чанков после удаления файла: %d, want 1", len(store.activeChunkIDs()))
	}
	// Точка исчезнувшего файла помечена устаревшей (не удалена — история).
	var stale bool
	for _, p := range store.points {
		if p.payload[PayloadFile].GetStringValue() == "gone.go" &&
			p.payload[PayloadReplacedBy].GetStringValue() == "e2" {
			stale = true
		}
	}
	if !stale {
		t.Fatal("точки исчезнувшего файла должны получить replaced_by")
	}
}

// Недоступный Qdrant при индексации — та же graceful-ошибка UnavailableError.
func TestIndexProjectQdrantDown(t *testing.T) {
	store := &recStore{fakeStore: &fakeStore{exists: true, deleteErr: errors.New("unavailable")}}
	c := newClient(store, &fakeEmbedder{dim: 32}, DefaultCollectionName)

	_, err := c.IndexProject(context.Background(), "demo", []IndexItem{{Path: "a.go", Scope: "root", Content: indexSample}},
		IndexOptions{Branch: MainBranch})
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

// deleteFilterHasIsNull — в фильтре удаления есть условие is_null по полю.
func deleteFilterHasIsNull(del *qdrant.DeletePoints, field string) bool {
	sel := del.GetPoints().GetFilter()
	if sel == nil {
		return false
	}
	for _, c := range sel.Must {
		if c.GetIsNull() != nil && c.GetIsNull().GetKey() == field {
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

// pointID детерминирован и различает файлы/строки/ветки/коммиты.
func TestPointIDDeterministic(t *testing.T) {
	a := pointID("demo", "a.go", MainBranch, "c1", 3, "h1")
	b := pointID("demo", "a.go", MainBranch, "c1", 3, "h1")
	if a != b {
		t.Fatalf("детерминированность: %d != %d", a, b)
	}
	if a == pointID("demo", "a.go", MainBranch, "c1", 4, "h1") {
		t.Fatal("разные строки не должны давать один ID")
	}
	if a == pointID("demo", "b.go", MainBranch, "c1", 3, "h1") {
		t.Fatal("разные файлы не должны давать один ID")
	}
	if a == pointID("demo", "a.go", "ai/epic/ARCH-01", "c1", 3, "h1") {
		t.Fatal("разные ветки не должны давать один ID")
	}
	if a == pointID("demo", "a.go", MainBranch, "c2", 3, "h1") {
		t.Fatal("разные коммиты не должны давать один ID (иначе теряется история версий)")
	}
	if a == pointID("demo", "a.go", MainBranch, "c1", 3, "h2") {
		t.Fatal("разное содержимое не должно давать один ID (иначе незакоммиченная правка потерялась бы)")
	}
	if strings.TrimSpace(chunkEmbedText("a.go", Chunk{Content: "code", StartLine: 1, EndLine: 2})) == "" {
		t.Fatal("chunkEmbedText пуст")
	}
}
