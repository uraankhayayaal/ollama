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

	// CriticalOnly — публиковать только критические замечания: ревьювер должен
	// указывать лишь на реальные дефекты (баги, падения, уязвимости, гонки,
	// утечки ресурсов), а несущественные/шумовые замечания («для заметки:»,
	// «можно упростить», «стоит проверить» и т.п.) молча отсекаются.
	// true — по умолчанию.
	CriticalOnly bool

	// BlockOnCritical запрещает апрув, если модель пометила хотя бы одно
	// замечание как критичное ("критично:"). true — блокировать.
	BlockOnCritical bool

	// SkipGenerated отсекает сгенерированные и бинарные файлы из анализа.
	SkipGenerated bool

	// ChunkSize — порог (в символах) размера диффа, после которого дифф
	// разбивается на несколько частей и ревьюится по частям, чтобы не
	// переполнять контекст модели и снижать галлюцинации. 0 — без разбиения.
	ChunkSize int

	// MaxRounds — максимальное число циклов «ревью → исправление → ревью»
	// в планировщике (runReviewLoop) для одного шага codereviewer.
	// Замечания ревью превращаются в шаги разработчиков, после исправлений ревью
	// повторяется. 1 — один проход ревью без цикла исправлений, 0 или
	// отрицательное — цикл отключён.
	MaxRounds int
}

// DefaultConfig возвращает конфиг со значениями по умолчанию.
func DefaultConfig() Config {
	return Config{
		MaxComments:     10,
		CriticalOnly:    true,
		BlockOnCritical: true,
		SkipGenerated:   true,
		ChunkSize:       14000,
		MaxRounds:       3,
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
	if v := os.Getenv("REVIEW_CRITICAL_ONLY"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.CriticalOnly = b
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
	if v := os.Getenv("REVIEW_CHUNK_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.ChunkSize = n
		}
	}
	if v := os.Getenv("REVIEW_FIX_ROUNDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxRounds = n
		}
	}

	return cfg
}
