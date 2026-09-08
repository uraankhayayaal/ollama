package qaengineer

import (
	"os"
	"strconv"
)

// Config — настраиваемые параметры агента QA Engineer.
// Значения берутся из переменных окружения (см. .env.example).
type Config struct {
	// MaxFiles — максимальное число файлов, которое агент может записать
	// за один запуск. 0 или отрицательное — без лимита.
	MaxFiles int

	// NoOverwrite запрещает перезаписывать уже существующие файлы.
	// true — вместо перезаписи инструмент вернёт ошибку.
	NoOverwrite bool

	// ReportFile — имя файла-отчёта о тестировании, который агент пишет
	// после прогона тестов. Пусто — файл не создаётся.
	ReportFile string
}

// DefaultConfig возвращает конфиг со значениями по умолчанию.
func DefaultConfig() Config {
	return Config{
		NoOverwrite: false,
		ReportFile:  "TEST_REPORT.md",
	}
}

// LoadConfig читает конфиг из переменных окружения (QA_*),
// заполняя только те поля, которые заданы.
func LoadConfig() Config {
	cfg := DefaultConfig()

	if v := os.Getenv("QA_MAX_FILES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxFiles = n
		}
	}
	if v := os.Getenv("QA_NO_OVERWRITE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.NoOverwrite = b
		}
	}
	if v := os.Getenv("QA_REPORT_FILE"); v != "" {
		cfg.ReportFile = v
	}

	return cfg
}
