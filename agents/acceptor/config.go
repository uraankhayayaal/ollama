package acceptor

import (
	"os"
	"strconv"
	"time"
)

// Config — настраиваемые параметры агента-приёмщика (acceptor).
// Значения берутся из переменных окружения ACCEPT_* (см. .env.example).
type Config struct {
	// BuildCmd — команда сборки, выполняется через sh -c в директории
	// проекта. Пустая — автодетект по типу проекта (go.mod / package.json /
	// requirements.txt). Задаётся ACCEPT_BUILD_CMD.
	BuildCmd string

	// RunCmd — команда запуска приложения. Пустая — автодетект.
	// Задаётся ACCEPT_RUN_CMD.
	RunCmd string

	// BuildTimeout — максимальное время сборки (по умолчанию 2m).
	BuildTimeout time.Duration

	// RunTimeout — сколько времени приложение работает во время приёмки
	// до принудительного завершения (по умолчанию 10s). Для серверов этого
	// достаточно, чтобы проверить, что процесс стартовал и не упал.
	RunTimeout time.Duration

	// MaxRounds — сколько циклов «приёмка → планировщик исправлений →
	// приёмка» допускается в исполнителе плана. 0 или меньше — фиксации по
	// приёмке не происходит (только отчёт).
	MaxRounds int

	// CheckFormat — выполнять ли проверку стилизатора (gofmt/prettier/black).
	// Нарушения формата — предупреждения, вердикт не меняют. По умолчанию true.
	CheckFormat bool

	// CheckAnalyze — выполнять ли проверку анализатора (go vet/eslint/ruff).
	// Находки анализатора — ошибки: ведут к вердикту reject. По умолчанию true.
	CheckAnalyze bool

	// FormatCmd — команда проверки стилизатора. Пустая — автодетект по типу
	// проекта. Задаётся ACCEPT_FORMAT_CMD.
	FormatCmd string

	// AnalyzeCmd — команда проверки анализатора. Пустая — автодетект по типу
	// проекта. Задаётся ACCEPT_ANALYZE_CMD.
	AnalyzeCmd string

	// InstallDeps — устанавливать ли зависимости проекта перед сборкой
	// (go mod download / npm ci / pip install). Недоступный инструмент
	// установки — не ошибка, шаг пропускается. По умолчанию true.
	InstallDeps bool

	// InstallCmd — команда установки зависимостей. Пустая — автодетект по типу
	// проекта. Задаётся ACCEPT_INSTALL_CMD.
	InstallCmd string

	// InstallTimeout — таймаут установки зависимостей. Установка обычно дольше
	// сборки, по умолчанию 5m.
	InstallTimeout time.Duration

	// MaxLog — максимальное число символов вывода сборки/запуска, которое
	// попадает в отчёт (защита контекста от переполнения).
	MaxLog int
}

// DefaultConfig возвращает конфиг со значениями по умолчанию.
func DefaultConfig() Config {
	return Config{
		BuildTimeout:   2 * time.Minute,
		RunTimeout:     10 * time.Second,
		MaxRounds:      3,
		CheckFormat:    true,
		CheckAnalyze:   true,
		InstallDeps:    true,
		InstallTimeout: 5 * time.Minute,
		MaxLog:         6000,
	}
}

// LoadConfig читает конфиг из переменных окружения (ACCEPT_*),
// заполняя только те поля, которые заданы. Остальные остаются по умолчанию.
func LoadConfig() Config {
	cfg := DefaultConfig()

	if v := os.Getenv("ACCEPT_BUILD_CMD"); v != "" {
		cfg.BuildCmd = v
	}
	if v := os.Getenv("ACCEPT_RUN_CMD"); v != "" {
		cfg.RunCmd = v
	}
	if v := os.Getenv("ACCEPT_FORMAT_CMD"); v != "" {
		cfg.FormatCmd = v
	}
	if v := os.Getenv("ACCEPT_ANALYZE_CMD"); v != "" {
		cfg.AnalyzeCmd = v
	}
	if v := os.Getenv("ACCEPT_INSTALL_CMD"); v != "" {
		cfg.InstallCmd = v
	}
	if v := os.Getenv("ACCEPT_INSTALL_DEPS"); v != "" {
		cfg.InstallDeps = parseBool(v, true)
	}
	if v := os.Getenv("ACCEPT_INSTALL_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.InstallTimeout = d
		}
	}
	if v := os.Getenv("ACCEPT_FORMAT"); v != "" {
		cfg.CheckFormat = parseBool(v, true)
	}
	if v := os.Getenv("ACCEPT_ANALYZE"); v != "" {
		cfg.CheckAnalyze = parseBool(v, true)
	}
	if v := os.Getenv("ACCEPT_BUILD_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.BuildTimeout = d
		}
	}
	if v := os.Getenv("ACCEPT_RUN_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.RunTimeout = d
		}
	}
	if v := os.Getenv("ACCEPT_MAX_ROUNDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxRounds = n
		}
	}
	if v := os.Getenv("ACCEPT_MAX_LOG"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxLog = n
		}
	}

	return cfg
}

// parseBool разбирает булево значение переменной окружения; при ошибке —
// возвращает заданное значение по умолчанию.
func parseBool(v string, def bool) bool {
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
