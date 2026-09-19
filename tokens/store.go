// Package tokens — счётчик потребления токенов проекта для Web UI.
//
// Проект может использовать разные LLM (Ollama, Yandex, OpenAI-совместимые),
// а каждый раунд агента/ассистента тратит входные и выходные токены. Данные
// аккумулируются за время жизни проекта в Redis-хэше tokens:<project> (поля
// "in"/"out"), накапливаются поверх перезапусков сервера и транслируются в
// реальном времени через WebSocket (type=tokens).
package tokens

import (
	"context"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"
)

// StoreConfig — параметры подключения Redis-хранилища счётчика токенов.
type StoreConfig struct {
	Addr     string // "host:port", по умолчанию "localhost:6379"
	Password string
	DB       int
	Project  string // префикс ключа (один счётчик на проект)
}

// Store — Redis-хэш счётчика токенов: атомарный инкремент (HINCRBY),
// чтение текущих тоталов (HGET).
type Store struct {
	client  *redis.Client
	project string
	key     string
}

// NewStore создаёт хранилище и проверяет доступность Redis.
func NewStore(ctx context.Context, cfg StoreConfig) (*Store, error) {
	if cfg.Project == "" {
		return nil, fmt.Errorf("tokens: имя проекта обязательно для счётчика токенов")
	}
	s := NewStoreNoCheck(cfg)
	if err := s.client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("tokens: redis недоступен (%s): %w", s.client.Options().Addr, err)
	}
	return s, nil
}

// NewStoreNoCheck создаёт хранилище без проверки соединения (для тестов).
func NewStoreNoCheck(cfg StoreConfig) *Store {
	addr := cfg.Addr
	if addr == "" {
		addr = "localhost:6379"
	}
	return &Store{
		client:  redis.NewClient(&redis.Options{Addr: addr, Password: cfg.Password, DB: cfg.DB}),
		project: cfg.Project,
		key:     "tokens:" + cfg.Project,
	}
}

// Close закрывает соединение с Redis.
func (s *Store) Close() error { return s.client.Close() }

// Client возвращает Redis-клиент (для низкоуровневых операций и тестов).
func (s *Store) Client() *redis.Client { return s.client }

// Project возвращает имя проекта, для которого ведётся счётчик.
func (s *Store) Project() string { return s.project }

// Key возвращает Redis-ключ счётчика.
func (s *Store) Key() string { return s.key }

// Add атомарно прибавляет порцию токенов и возвращает новые итоговые суммы
// (вход/выход) после инкремента.
func (s *Store) Add(ctx context.Context, in, out int64) (totIn, totOut int64, err error) {
	cmds, err := s.client.Pipelined(ctx, func(p redis.Pipeliner) error {
		p.HIncrBy(ctx, s.key, "in", in)
		p.HIncrBy(ctx, s.key, "out", out)
		return nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("tokens: инкремент %s: %w", s.key, err)
	}
	totIn = cmds[0].(*redis.IntCmd).Val()
	totOut = cmds[1].(*redis.IntCmd).Val()
	return totIn, totOut, nil
}

// Get возвращает текущие накопленные суммы токенов (вход/выход).
func (s *Store) Get(ctx context.Context) (int64, int64, error) {
	vals, err := s.client.HMGet(ctx, s.key, "in", "out").Result()
	if err != nil {
		return 0, 0, fmt.Errorf("tokens: чтение %s: %w", s.key, err)
	}
	return hashInt(vals[0]), hashInt(vals[1]), nil
}

// hashInt превращает значение поля хэша Redis (nil или строка) в int64.
func hashInt(v any) int64 {
	if v == nil {
		return 0
	}
	n, err := strconv.ParseInt(fmt.Sprint(v), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
