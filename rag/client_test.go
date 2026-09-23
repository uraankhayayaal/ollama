package rag

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/qdrant/go-client/qdrant"
)

// fakeStore — hermetic-реализация QdrantStore для тестов: никакой сети,
// только состояние в памяти (см. PLAN-qdrant.md, Ф-1: fake-клиент Qdrant).
type fakeStore struct {
	exists    bool
	existsErr error
	createErr error
	deleteErr error

	created     *qdrant.CreateCollection
	upsertCalls int
	deleteCalls int
	queryCalls  int
	countCalls  int
	countResult uint64
	countErr    error
	closed      bool
}

func (f *fakeStore) CollectionExists(ctx context.Context, collectionName string) (bool, error) {
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.exists, nil
}

func (f *fakeStore) CreateCollection(ctx context.Context, request *qdrant.CreateCollection) error {
	f.created = request
	if f.createErr != nil {
		// Имитация гонки: коллекцию параллельно создал другой процесс.
		f.exists = true
		return f.createErr
	}
	f.exists = true
	return nil
}

func (f *fakeStore) Upsert(ctx context.Context, request *qdrant.UpsertPoints) (*qdrant.UpdateResult, error) {
	f.upsertCalls++
	return &qdrant.UpdateResult{}, nil
}

func (f *fakeStore) Delete(ctx context.Context, request *qdrant.DeletePoints) (*qdrant.UpdateResult, error) {
	f.deleteCalls++
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return &qdrant.UpdateResult{}, nil
}

func (f *fakeStore) Query(ctx context.Context, request *qdrant.QueryPoints) ([]*qdrant.ScoredPoint, error) {
	f.queryCalls++
	return nil, nil
}

func (f *fakeStore) Count(ctx context.Context, request *qdrant.CountPoints) (*qdrant.CountResponse, error) {
	f.countCalls++
	if f.countErr != nil {
		return nil, f.countErr
	}
	return &qdrant.CountResponse{Result: &qdrant.CountResult{Count: f.countResult}}, nil
}

func (f *fakeStore) Close() error { f.closed = true; return nil }

// fakeEmbedder — hermetic-эмбеддер фиксированной размерности.
type fakeEmbedder struct {
	dim int
	err error
	// texts фиксирует все тексты, поданные на эмбеддинг.
	texts []string
}

func (e *fakeEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	e.texts = append(e.texts, text)
	if e.err != nil {
		return nil, e.err
	}
	vec := make([]float32, e.dim)
	for i := range vec {
		vec[i] = float32(i + 1)
	}
	return vec, nil
}

// NewClient создаёт коллекцию при первом обращении: размерность берётся
// пробой модели эмбеддингов, метрика — Distance_Cosine.
func TestEnsureCollectionCreates(t *testing.T) {
	store := &fakeStore{exists: false}
	embed := &fakeEmbedder{dim: 768}
	c := newClient(store, embed, DefaultCollectionName)

	dim, err := c.EnsureCollection(context.Background())
	if err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}
	if dim != 768 {
		t.Fatalf("размерность: got %d, want 768", dim)
	}
	if store.created == nil {
		t.Fatal("коллекция не создана")
	}
	if store.created.CollectionName != DefaultCollectionName {
		t.Fatalf("имя коллекции: got %q, want %q", store.created.CollectionName, DefaultCollectionName)
	}
	cfg := store.created.VectorsConfig.GetParams()
	if cfg == nil {
		t.Fatal("VectorsConfig без параметров")
	}
	if cfg.Size != 768 {
		t.Fatalf("размерность векторов в конфиге: got %d, want 768", cfg.Size)
	}
	if cfg.Distance != qdrant.Distance_Cosine {
		t.Fatalf("метрика: got %v, want Distance_Cosine", cfg.Distance)
	}
	// Размерность определилась пробой эмбеддера.
	if len(embed.texts) != 1 {
		t.Fatalf("эмбеддер должен быть вызван один раз (проба), got %d", len(embed.texts))
	}
}

// Существующая коллекция не пересоздаётся, а размерность отдаётся из кэша.
func TestEnsureCollectionExistsAndCachesDim(t *testing.T) {
	store := &fakeStore{exists: true}
	embed := &fakeEmbedder{dim: 384}
	c := newClient(store, embed, DefaultCollectionName)

	dim, err := c.EnsureCollection(context.Background())
	if err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}
	if store.created != nil {
		t.Fatal("CreateCollection не должен вызываться, коллекция уже есть")
	}
	// Повторный вызов не трогает эмбеддер — размерность закэширована.
	dim2, err := c.EnsureCollection(context.Background())
	if err != nil {
		t.Fatalf("EnsureCollection (второй): %v", err)
	}
	if dim != dim2 || dim2 != 384 {
		t.Fatalf("размерность: got %d/%d, want 384", dim, dim2)
	}
	if len(embed.texts) != 1 {
		t.Fatalf("эмбеддер должен вызываться один раз, got %d", len(embed.texts))
	}
}

// Недоступная модель эмбеддингов — понятная ошибка с подсказкой, без попытки
// лезть в Qdrant.
func TestEnsureCollectionDimensionProbeError(t *testing.T) {
	store := &fakeStore{exists: true}
	embed := &fakeEmbedder{err: errors.New("connection refused")}
	c := newClient(store, embed, DefaultCollectionName)

	_, err := c.EnsureCollection(context.Background())
	if err == nil {
		t.Fatal("ожидалась ошибка определения размерности")
	}
	if !strings.Contains(err.Error(), "размерность") || !strings.Contains(err.Error(), "EMBEDDING_MODEL") {
		t.Fatalf("ошибка не содержат подсказку: %v", err)
	}
	if store.created != nil {
		t.Fatal("коллекция не должна создаваться без размерности")
	}
}

// Недоступный Qdrant — graceful degrade: различимая ошибка UnavailableError,
// которую инструменты превратят в статус skipped.
func TestEnsureCollectionQdrantDown(t *testing.T) {
	store := &fakeStore{existsErr: errors.New("connect: connection refused")}
	embed := &fakeEmbedder{dim: 768}
	c := newClient(store, embed, DefaultCollectionName)

	_, err := c.EnsureCollection(context.Background())
	if !IsUnavailable(err) {
		t.Fatalf("ошибка должна быть UnavailableError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "QDRANT_ADDR") {
		t.Fatalf("ошибка должна подсказывать про QDRANT_ADDR: %v", err)
	}
}

// Гонка двух процессов на создание коллекции не считается ошибкой: если после
// ошибки Create коллекция уже существует — успех.
func TestEnsureCollectionCreateRace(t *testing.T) {
	store := &fakeStore{exists: false, createErr: errors.New("already exists")}
	embed := &fakeEmbedder{dim: 256}
	c := newClient(store, embed, DefaultCollectionName)

	dim, err := c.EnsureCollection(context.Background())
	if err != nil {
		t.Fatalf("гонка создания коллекции не должна быть ошибкой: %v", err)
	}
	if dim != 256 {
		t.Fatalf("размерность: got %d, want 256", dim)
	}
}

// Ping проверяет Qdrant без обращения к эмбеддеру (лёгкий деградационный
// контроль для инструментов).
func TestPing(t *testing.T) {
	store := &fakeStore{exists: true}
	embed := &fakeEmbedder{dim: 768}
	c := newClient(store, embed, DefaultCollectionName)

	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping при живом Qdrant: %v", err)
	}
	if len(embed.texts) != 0 {
		t.Fatalf("Ping не должен трогать эмбеддер, got %d вызовов", len(embed.texts))
	}

	store.existsErr = errors.New("unavailable")
	if err := c.Ping(context.Background()); !IsUnavailable(err) {
		t.Fatalf("Ping при выключенном Qdrant должен дать UnavailableError, got %T: %v", err, err)
	}
}

// Close закрывает соединение.
func TestClose(t *testing.T) {
	store := &fakeStore{}
	c := newClient(store, &fakeEmbedder{dim: 8}, DefaultCollectionName)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !store.closed {
		t.Fatal("Close не дошёл до хранилища")
	}
}

// Collection отдаёт имя коллекции.
func TestCollectionName(t *testing.T) {
	c := newClient(&fakeStore{}, &fakeEmbedder{dim: 8}, "custom")
	if got := c.Collection(); got != "custom" {
		t.Fatalf("Collection: got %q, want custom", got)
	}
}

// parseAddr разбирает "host:port" и значения по умолчанию из окружения.
func TestParseAddr(t *testing.T) {
	t.Setenv("QDRANT_ADDR", "")
	cases := []struct {
		in   string
		host string
		port int
	}{
		{"", "localhost", 6334},
		{"qdrant.local:6334", "qdrant.local", 6334},
		{"http://qdrant.local:6334", "qdrant.local", 6334},
		{":1234", "localhost", 1234},
	}
	for _, tc := range cases {
		host, port, err := parseAddr(tc.in)
		if err != nil {
			t.Fatalf("parseAddr(%q): %v", tc.in, err)
		}
		if host != tc.host || port != tc.port {
			t.Fatalf("parseAddr(%q): got %s:%d, want %s:%d", tc.in, host, port, tc.host, tc.port)
		}
	}

	if _, _, err := parseAddr("not-an-addr"); err == nil {
		t.Fatal("parseAddr должен отвергнуть строку без порта")
	}
	if _, _, err := parseAddr("host:99999"); err == nil {
		t.Fatal("parseAddr должен отвергнуть порт вне диапазона")
	}
}
