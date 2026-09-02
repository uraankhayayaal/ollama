package codegenerator

import (
	"os"
	"strconv"
)

// Config — настраиваемые параметры агента-генератора кода.
// Значения берутся из переменных окружения (см. .env.example).
type Config struct {
	// Language — целевой язык программирования. Влияет на промпт агента.
	Language string

	// Module — имя модуля Go для go.mod (например "example.com/gen").
	// Пусто — модель придумывает сама.
	Module string

	// MaxFiles — максимальное число файлов, которое агент может записать
	// за один запуск. 0 или отрицательное — без лимита.
	MaxFiles int

	// NoOverwrite запрещает перезаписывать уже существующие файлы.
	// true — вместо перезаписи инструмент вернёт ошибку.
	NoOverwrite bool

	// SummaryFile — имя файла-отчёта, который пишется после генерации.
	// Пусто — файл не создаётся.
	SummaryFile string
}

// DefaultConfig возвращает конфиг со значениями по умолчанию.
func DefaultConfig() Config {
	return Config{
		Language:    "Go",
		NoOverwrite: false,
		SummaryFile: "SUMMARY.md",
	}
}

// LoadConfig читает конфиг из переменных окружения (CODEGEN_*),
// заполняя только те поля, которые заданы.
func LoadConfig() Config {
	cfg := DefaultConfig()

	if v := os.Getenv("CODEGEN_LANG"); v != "" {
		cfg.Language = v
	}
	if v := os.Getenv("CODEGEN_MODULE"); v != "" {
		cfg.Module = v
	}
	if v := os.Getenv("CODEGEN_MAX_FILES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxFiles = n
		}
	}
	if v := os.Getenv("CODEGEN_NO_OVERWRITE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.NoOverwrite = b
		}
	}
	if v := os.Getenv("CODEGEN_SUMMARY_FILE"); v != "" {
		cfg.SummaryFile = v
	}

	return cfg
}
