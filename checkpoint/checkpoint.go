// Package checkpoint хранит состояние выполнения плана планировщика в Redis,
// позволяя возобновлять работу с места остановки (resume).
//
// Пакет намеренно не зависит от types планировщика: план сохраняется как
// непрозрачный JSON (PlanJSON), а исполнение описывается картой завершённых
// шагов и статусами. Это исключает цикл импортов (planner -> checkpoint ->
// planner) и позволяет менять структуру Plan без миграций хранилища.
package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNotFound — чекпоинт для ключа не найден.
var ErrNotFound = errors.New("чекпоинт не найден")

// StepStatus — статус шага плана.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusDone      = "done"
	StatusFailed    = "failed"
	StatusSkipped   = "skipped"
)

// Snapshot — состояние выполнения плана в момент контрольной точки.
type Snapshot struct {
	// ProjectName — имя проекта, к которому относится план.
	ProjectName string `json:"project_name"`
	// Summary — короткое описание плана (для логов/UI).
	Summary string `json:"summary"`
	// PlanJSON — план (сериализованный в JSON), как его вернул планировщик.
	PlanJSON json.RawMessage `json:"plan"`
	// Completed — шаги, успешно завершённые к моменту контрольной точки.
	Completed map[string]bool `json:"completed"`
	// Statuses — детальный статус каждого шага (pending/running/done/failed).
	Statuses map[string]string `json:"statuses"`
	// Waves — шаги, сгруппированные по волнам параллельности (для resume).
	Waves [][]string `json:"waves,omitempty"`
	// UpdatedAt — время последнего обновления чекпоинта (RFC3339).
	UpdatedAt string `json:"updated_at"`
}

// Store — Redis-хранилище чекпоинтов.
type Store struct {
	client *redis.Client
	ttl    time.Duration
	key    string
}

// StoreConfig — параметры подключения к Redis.
type StoreConfig struct {
	// Addr — адрес Redis вида "host:port". По умолчанию "localhost:6379".
	Addr string
	// Password — пароль Redis (пусто — без пароля).
	Password string
	// DB — номер базы Redis.
	DB int
	// Key — ключ, под которым хранится чекпоинт (обычно "checkpoint:<project>").
	Key string
	// TTL — время жизни чекпоинта. 0 — без истечения.
	TTL time.Duration
}

// NewStore создаёт хранилище чекпоинтов с заданными настройками.
// Возвращает ошибку, если Redis недоступен (проверка соединения).
func NewStore(ctx context.Context, cfg StoreConfig) (*Store, error) {
	addr := cfg.Addr
	if addr == "" {
		addr = "localhost:6379"
	}
	key := cfg.Key
	if key == "" {
		key = "checkpoint"
	}

	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis недоступен (%s): %w", addr, err)
	}

	return &Store{client: client, ttl: cfg.TTL, key: key}, nil
}

// NewStoreNoCheck создаёт хранилище без проверки соединения (для тестов).
func NewStoreNoCheck(cfg StoreConfig) *Store {
	addr := cfg.Addr
	if addr == "" {
		addr = "localhost:6379"
	}
	key := cfg.Key
	if key == "" {
		key = "checkpoint"
	}
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})
	return &Store{client: client, ttl: cfg.TTL, key: key}
}

// Client возвращает Redis-клиент (для тестов и низкоуровневых операций).
func (s *Store) Client() *redis.Client { return s.client }

// Key возвращает ключ, под которым хранится чекпоинт.
func (s *Store) Key() string { return s.key }

// Close закрывает соединение с Redis.
func (s *Store) Close() error { return s.client.Close() }

// Save записывает снапшот в Redis (с TTL, если задан).
func (s *Store) Save(ctx context.Context, snap *Snapshot) error {
	snap.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if snap.Completed == nil {
		snap.Completed = map[string]bool{}
	}
	if snap.Statuses == nil {
		snap.Statuses = map[string]string{}
	}

	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("сериализация чекпоинта: %w", err)
	}

	var ttl time.Duration
	if s.ttl > 0 {
		ttl = s.ttl
	}
	return s.client.Set(ctx, s.key, data, ttl).Err()
}

// Load читает снапшот из Redis. Если чекпоинта нет — возвращает ErrNotFound.
func (s *Store) Load(ctx context.Context) (*Snapshot, error) {
	data, err := s.client.Get(ctx, s.key).Bytes()
	if err == redis.Nil {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("чтение чекпоинта: %w", err)
	}

	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("разбор чекпоинта: %w", err)
	}
	return &snap, nil
}

// MarkStatus обновляет статус шага и, если done, отмечает его завершённым.
func (s *Store) MarkStep(ctx context.Context, snap *Snapshot, stepID, status string) error {
	if snap.Statuses == nil {
		snap.Statuses = map[string]string{}
	}
	if snap.Completed == nil {
		snap.Completed = map[string]bool{}
	}

	snap.Statuses[stepID] = status
	if status == StatusDone {
		snap.Completed[stepID] = true
	}
	return s.Save(ctx, snap)
}

// IsCompleted сообщает, завершён ли шаг (сравнение без записи в Redis).
func (s *Store) IsCompleted(snap *Snapshot, stepID string) bool {
	if snap == nil || snap.Completed == nil {
		return false
	}
	return snap.Completed[stepID]
}