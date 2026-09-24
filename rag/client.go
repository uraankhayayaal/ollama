// Клиент векторной памяти RAG поверх Qdrant (gRPC, см. PLAN-2026-09-19-done-qdrant.md, Ф-1).
//
// Инфраструктурный слой: инициализация клиента и create-if-not-exists
// коллекции. Размерность векторов определяется пробой модели эмбеддингов
// (EMBEDDING_MODEL), метрика — косинусная (Distance_Cosine). Qdrant может
// отсутствовать: соединение ленивое, недоступность всплывает в первом же
// вызове (Ping / EnsureCollection) и обрабатывается наружу как graceful
// degrade (инструменты вернут skipped, а не упадут — как ЛСП-серверы).

package rag

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"

	"ai/logging"

	"github.com/qdrant/go-client/qdrant"
)

// Значения по умолчанию и переменные окружения.
const (
	// DefaultAddr — адрес Qdrant gRPC по умолчанию (QDRANT_ADDR).
	DefaultAddr = "localhost:6334"
	// DefaultCollectionName — имя коллекции по умолчанию (QDRANT_COLLECTION_NAME).
	DefaultCollectionName = "project_code_base"
)

// probeText — короткий текст для определения размерности модели эмбеддингов
// (длина вектора известна только самой модели).
const probeText = "Этот текст проганяется через модель эмбеддингов, чтобы узнать размерность вектора."

// Config — настройки клиента RAG-памяти. Пустые поля добираются из переменных
// окружения: QDRANT_ADDR, QDRANT_COLLECTION_NAME, EMBEDDING_MODEL.
type Config struct {
	// Addr — адрес Qdrant вида "host:port" (gRPC, порт 6334).
	Addr string
	// CollectionName — имя коллекции чанков проекта.
	CollectionName string
	// EmbeddingModel — модель эмбеддингов Ollama.
	EmbeddingModel string
	// Embedder — источник эмбеддингов; пусто — OllamaEmbedder по умолчанию.
	Embedder Embedder
}

// QdrantStore — минимальный набор операций Qdrant gRPC, используемых RAG.
// Выделен в интерфейс, чтобы hermetic-тесты работали с fake-реализацией
// без сети (см. PLAN-2026-09-19-done-qdrant.md, Ф-1: «fake-клиент Qdrant (интерфейс)»).
type QdrantStore interface {
	CollectionExists(ctx context.Context, collectionName string) (bool, error)
	CreateCollection(ctx context.Context, request *qdrant.CreateCollection) error
	Upsert(ctx context.Context, request *qdrant.UpsertPoints) (*qdrant.UpdateResult, error)
	Delete(ctx context.Context, request *qdrant.DeletePoints) (*qdrant.UpdateResult, error)
	Query(ctx context.Context, request *qdrant.QueryPoints) ([]*qdrant.ScoredPoint, error)
	Count(ctx context.Context, request *qdrant.CountPoints) (uint64, error)
	Close() error
}

// Client — единая точка доступа к векторной памяти проекта.
type Client struct {
	store      QdrantStore
	embed      Embedder
	collection string

	mu  sync.Mutex
	dim int // кэш размерности векторов после первого определения
}

// Collection возвращает имя коллекции (для логов и фильтров поиска).
func (c *Client) Collection() string { return c.collection }

// NewClient создаёт клиент RAG поверх реальных сервисов: Qdrant (gRPC) и
// эмбеддинги Ollama. Подключение к Qdrant откладывается (grpc.NewClient
// ленив), поэтому недоступный Qdrant не роняет запуск: ошибка появится при
// первом же обращении (Ping / EnsureCollection / Search) и обрабатывается
// как graceful degrade.
func NewClient(cfg Config) (*Client, error) {
	if cfg.Embedder == nil {
		model := cfg.EmbeddingModel
		if model == "" {
			model = strings.TrimSpace(os.Getenv("EMBEDDING_MODEL"))
		}
		embed, err := NewOllamaEmbedder(model)
		if err != nil {
			return nil, err
		}
		cfg.Embedder = embed
	}

	host, port, err := parseAddr(cfg.Addr)
	if err != nil {
		return nil, err
	}

	collection := cfg.CollectionName
	if collection == "" {
		collection = strings.TrimSpace(os.Getenv("QDRANT_COLLECTION_NAME"))
	}
	if collection == "" {
		collection = DefaultCollectionName
	}

	store, err := qdrant.NewClient(&qdrant.Config{
		Host: host, Port: port,
		PoolSize:               1,
		SkipCompatibilityCheck: true, // проверка версии ходит в сеть — не нужна
	})
	if err != nil {
		return nil, fmt.Errorf("rag: создание клиента Qdrant: %w", err)
	}

	return &Client{store: store, embed: cfg.Embedder, collection: collection}, nil
}

// newClient собирает Client из готовых компонентов. Используется тестами
// с fake-реализациями QdrantStore/Embedder (hermetic, без сети).
func newClient(store QdrantStore, embed Embedder, collection string) *Client {
	return &Client{store: store, embed: embed, collection: collection}
}

// NewClientSafe создаёт клиент RAG, но при некорректной конфигурации (битый
// QDRANT_ADDR/EMBEDDING_MODEL и т.п.) возвращает nil вместо ошибки: клиент
// опционален (tools.Deps.RAG), инструменты без него просто деградируют в
// skipped. Само подключение к Qdrant по-прежнему откладывается до первого
// вызова — недоступный Qdrant на этом этапе не ошибка.
func NewClientSafe(cfg Config) *Client {
	c, err := NewClient(cfg)
	if err != nil {
		logging.Warnf("rag: клиент RAG не создан (%v) — CodeSearch будет возвращать skipped", err)
		return nil
	}
	return c
}

// parseAddr разбирает адрес Qdrant "host:port" из QDRANT_ADDR. Пустая строка —
// DefaultAddr. Допустим лишний scheme ("http://…") — адрес скопирован с REST-
// документации, gRPC-порт от этого не меняется.
func parseAddr(addr string) (host string, port int, err error) {
	if strings.TrimSpace(addr) == "" {
		addr = os.Getenv("QDRANT_ADDR")
	}
	if strings.TrimSpace(addr) == "" {
		addr = DefaultAddr
	}
	addr = strings.TrimSpace(addr)
	if i := strings.Index(addr, "://"); i >= 0 {
		addr = addr[i+3:]
	}
	h, p, serr := net.SplitHostPort(addr)
	if serr != nil {
		return "", 0, fmt.Errorf("rag: неверный QDRANT_ADDR %q (ожидается host:port): %w", addr, serr)
	}
	port, perr := strconv.Atoi(p)
	if perr != nil || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("rag: неверный порт в QDRANT_ADDR %q: %q", addr, p)
	}
	if h == "" {
		h = "localhost"
	}
	return h, port, nil
}

// Ping проверяет доступность Qdrant одним лёгким вызовом, не трогая эмбеддер.
// Инструменты используют его для graceful degrade (skipped), когда Qdrant
// выключен: падение любых RPC даёт различимую ошибку-подсказку.
func (c *Client) Ping(ctx context.Context) error {
	if _, err := c.store.CollectionExists(ctx, c.collection); err != nil {
		return &UnavailableError{Err: err}
	}
	return nil
}

// EnsureCollection создаёт коллекцию, если её ещё нет (create-if-not-exists),
// и возвращает размерность векторов. Размерность определяется пробой модели
// эмбеддингов и кэшируется. Вызывается при полной индексации и авто-обновлении.
func (c *Client) EnsureCollection(ctx context.Context) (int, error) {
	dim, err := c.dimension(ctx)
	if err != nil {
		return 0, err
	}

	exists, err := c.store.CollectionExists(ctx, c.collection)
	if err != nil {
		return dim, &UnavailableError{Err: err}
	}
	if exists {
		return dim, nil
	}

	if cerr := c.store.CreateCollection(ctx, &qdrant.CreateCollection{
		CollectionName: c.collection,
		VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{
			Size:     uint64(dim),
			Distance: qdrant.Distance_Cosine,
		}),
	}); cerr != nil {
		// Гонка: коллекцию параллельно создал другой процесс между проверкой
		// и Create. Повторная проверка отличает её от настоящей ошибки.
		if ok, okErr := c.store.CollectionExists(ctx, c.collection); okErr == nil && ok {
			return dim, nil
		}
		return dim, fmt.Errorf("rag: не удалось создать коллекцию %s: %w", c.collection, cerr)
	}

	return dim, nil
}

// dimension определяет (и кэширует) размерность векторов модели эмбеддингов:
// прогоняет probeText через Embed и замеряет длину вектора.
func (c *Client) dimension(ctx context.Context) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dim != 0 {
		return c.dim, nil
	}
	probe, err := c.embed.Embed(ctx, probeText)
	if err != nil {
		return 0, fmt.Errorf("rag: не удалось определить размерность эмбеддингов (%v): проверь EMBEDDING_MODEL и доступность Ollama", err)
	}
	if len(probe) == 0 {
		return 0, fmt.Errorf("rag: модель эмбеддингов вернула пустой вектор")
	}
	c.dim = len(probe)
	return c.dim, nil
}

// Close закрывает соединение с Qdrant.
func (c *Client) Close() error { return c.store.Close() }

// UnavailableError — признак того, что Qdrant недоступен: инструменты и
// индексатор превращают его в "skipped" с подсказкой (graceful degrade),
// не считая это ошибкой шага.
type UnavailableError struct{ Err error }

func (e *UnavailableError) Error() string {
	return fmt.Sprintf("rag: Qdrant недоступен (%v): проверь QDRANT_ADDR и docker compose up qdrant", e.Err)
}

// Unwrap возвращает исходную ошибку gRPC.
func (e *UnavailableError) Unwrap() error { return e.Err }

// IsUnavailable отличает недоступность Qdrant от прочих ошибок RAG
// (инструменты переводят её в статус skipped).
func IsUnavailable(err error) bool {
	var u *UnavailableError
	return errors.As(err, &u)
}
