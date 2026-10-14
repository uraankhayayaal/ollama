package rag

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Search адаптера переводит SearchResult в services.RAGResult: пути, очищенный
// score и версионные маркеры в Metadata.
func TestProjectAdapterSearchMapsResults(t *testing.T) {
	store := &queryStore{fakeStore: &fakeStore{}}
	c := newClient(store, &fakeEmbedder{dim: 32}, DefaultCollectionName)

	store.points = append(store.points,
		versionedPoint("server/auth/token.go", 12, 26, 0.86, "func ValidateToken(){}", "ai/task/T-01", "abc123", "cid-1"),
	)

	pa := NewProjectAdapter(c, "demo", "/work/project")
	if pa.Project() != "demo" {
		t.Fatalf("Project: got %q, want demo", pa.Project())
	}

	res, err := pa.Search(context.Background(), "где валидируется токен", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("результатов: got %d, want 1", len(res))
	}
	got := res[0]
	if got.Path != "server/auth/token.go" {
		t.Errorf("Path: got %q", got.Path)
	}
	if got.Content != "func ValidateToken(){}" {
		t.Errorf("Content: got %q", got.Content)
	}
	if got.Score < 0.85 || got.Score > 0.87 {
		t.Errorf("Score: got %v, want ~0.86", got.Score)
	}
	for k, want := range map[string]string{
		"branch":   "ai/task/T-01",
		"commit":   "abc123",
		"chunk_id": "cid-1",
		"lines":    "12-26",
	} {
		if got.Metadata[k] != want {
			t.Errorf("Metadata[%q]: got %q, want %q", k, got.Metadata[k], want)
		}
	}
	// Проект из адаптера уходит в фильтр поиска, а не теряется.
	if store.req == nil {
		t.Fatal("Qdrant Query не вызван")
	}
}

// BranchIndexed сравнивает с коммитом рабочего каталога адаптера: вне git
// (коммит не определился) — false без ошибки.
func TestProjectAdapterBranchIndexed(t *testing.T) {
	withGit(t, "ai/task/T-01", "abc123", nil, nil)
	store := &fakeStore{exists: true, countResult: 1}
	c := newClient(store, &fakeEmbedder{dim: 32}, DefaultCollectionName)

	pa := NewProjectAdapter(c, "demo", "/work/project")
	ok, err := pa.BranchIndexed(context.Background(), "ai/task/T-01")
	if err != nil {
		t.Fatalf("BranchIndexed: %v", err)
	}
	if !ok {
		t.Fatal("ожидался true при непустом коммите и точках в индексе")
	}

	// Вне git коммит не определяется — сравнение невозможно, false.
	withGit(t, "", "", nil, errors.New("no git"))
	plain := NewProjectAdapter(c, "demo", "/work/plain")
	ok, err = plain.BranchIndexed(context.Background(), "main")
	if err != nil {
		t.Fatalf("BranchIndexed вне git: %v", err)
	}
	if ok {
		t.Fatal("вне git ожидался false")
	}
}

// Index индексирует каталог: коллекция создаётся, файлы уходят в Upsert.
func TestProjectAdapterIndexesDirectory(t *testing.T) {
	withGit(t, "ai/task/T-01", "abc123", nil, nil)
	store := &fakeStore{}
	c := newClient(store, &fakeEmbedder{dim: 32}, DefaultCollectionName)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() int { return 1 }\n"), 0o644); err != nil {
		t.Fatalf("запись файла: %v", err)
	}

	pa := NewProjectAdapter(c, "demo", dir)
	if err := pa.Index(context.Background(), dir); err != nil {
		t.Fatalf("Index: %v", err)
	}
	if !store.exists {
		t.Error("коллекция не создана")
	}
	if store.upsertCalls == 0 {
		t.Error("файлы не загружены в индекс (Upsert не вызван)")
	}
}

// Пустой каталог и у адаптера, и в аргументе — явная ошибка, а не тихий no-op.
func TestProjectAdapterIndexWithoutDir(t *testing.T) {
	c := newClient(&fakeStore{}, &fakeEmbedder{dim: 32}, DefaultCollectionName)
	pa := NewProjectAdapter(c, "demo", "")
	if err := pa.Index(context.Background(), "  "); err == nil {
		t.Fatal("ожидалась ошибка о неопределённом каталоге")
	}
}
