package board

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config — параметры подключения к Redis, где живёт общая Kanban-доска
// проекта. Перенесён из agents/architect (срез H6a Этапа 8): конфигурация
// хранилища относится к infrastructure/board, а не к агенту.
type Config struct {
	RedisAddr     string
	RedisPassword string
	RedisDB       int
	RedisTTL      time.Duration
}

// LoadConfig читает настройки хранилища доски из переменных окружения
// (BOARD_REDIS_ADDR/BOARD_REDIS_PASSWORD/BOARD_REDIS_DB/BOARD_TTL).
func LoadConfig() Config {
	return Config{
		RedisAddr:     envString("BOARD_REDIS_ADDR", "localhost:56379"),
		RedisPassword: os.Getenv("BOARD_REDIS_PASSWORD"),
		RedisDB:       envInt("BOARD_REDIS_DB", 0),
		RedisTTL:      envDuration("BOARD_TTL", 0),
	}
}

// StoreConfig собирает конфигурацию хранилища доски для проекта.
func (c Config) StoreConfig(project string) StoreConfig {
	return StoreConfig{
		Addr:     c.RedisAddr,
		Password: c.RedisPassword,
		DB:       c.RedisDB,
		Project:  project,
		TTL:      c.RedisTTL,
	}
}

// envString возвращает значение переменной окружения или запасное значение.
func envString(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

// envInt парсит целочисленную переменную окружения с запасным значением.
func envInt(name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// envDuration парсит длительность (например "24h") с запасным значением.
func envDuration(name string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
