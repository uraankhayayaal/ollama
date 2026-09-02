package codereviewer

import (
	"os"
	"strconv"
)

// Config — настраиваемые параметры агента код-ревью.
// Значения берутся из переменных окружения (см. .env.example).
type Config struct {
	// MaxComments ограничивает число публикуемых замечаний за одно ревью.
	// 0 или отрицательное — без лимита.
	MaxComments int

	// BlockOnCritical запрещает апрув, если модель пометила хотя бы одно
	// замечание как критичное ("критично:"). true — блокировать.
	BlockOnCritical bool

	// SkipGenerated отсекает сгенерированные и бинарные файлы из анализа.
	SkipGenerated bool
}

// DefaultConfig возвращает конфиг со значениями по умолчанию.
func DefaultConfig() Config {
	return Config{
		MaxComments:     10,
		BlockOnCritical: true,
		SkipGenerated:   true,
	}
}

// LoadConfig читает конфиг из переменных окружения, заполняя только те
// поля, которые заданы. Остальные остаются на значениях по умолчанию.
func LoadConfig() Config {
	cfg := DefaultConfig()

	if v := os.Getenv("REVIEW_MAX_COMMENTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxComments = n
		}
	}
	if v := os.Getenv("REVIEW_BLOCK_ON_CRITICAL"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.BlockOnCritical = b
		}
	}
	if v := os.Getenv("REVIEW_SKIP_GENERATED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.SkipGenerated = b
		}
	}

	return cfg
}
