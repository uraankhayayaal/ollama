package backendlead

import (
	"os"
	"strconv"
)

// Config — настраиваемые параметры агента Backend Tech Lead.
// Значения берутся из переменных окружения (см. .env.example).
type Config struct {
	// MaxFiles — максимальное число файлов, которое агент может записать
	// за один запуск. 0 или отрицательное — без лимита.
	MaxFiles int

	// NoOverwrite запрещает перезаписывать уже существующие файлы.
	// true — вместо перезаписи инструмент вернёт ошибку.
	NoOverwrite bool

	// PlanFile — имя файла, в который агент сохраняет JSON-декомпозицию
	// задач для бэкенд-разработчиков. Пусто — файл не создаётся.
	PlanFile string
}

// DefaultConfig возвращает конфиг со значениями по умолчанию.
func DefaultConfig() Config {
	return Config{
		NoOverwrite: false,
		PlanFile:    "BACKEND_PLAN.json",
	}
}

// LoadConfig читает конфиг из переменных окружения (BACKEND_*),
// заполняя только те поля, которые заданы.
func LoadConfig() Config {
	cfg := DefaultConfig()

	if v := os.Getenv("BACKEND_MAX_FILES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxFiles = n
		}
	}
	if v := os.Getenv("BACKEND_NO_OVERWRITE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.NoOverwrite = b
		}
	}
	if v := os.Getenv("BACKEND_PLAN_FILE"); v != "" {
		cfg.PlanFile = v
	}

	return cfg
}
