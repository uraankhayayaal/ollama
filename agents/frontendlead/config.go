package frontendlead

import (
	"os"
	"strconv"
)

// Config — настраиваемые параметры агента Frontend Tech Lead.
// Значения берутся из переменных окружения (см. .env.example).
type Config struct {
	// MaxFiles — максимальное число файлов, которое агент может записать
	// за один запуск. 0 или отрицательное — без лимита.
	MaxFiles int

	// NoOverwrite запрещает перезаписывать уже существующие файлы.
	// true — вместо перезаписи инструмент вернёт ошибку.
	NoOverwrite bool

	// PlanFile — имя файла, в который агент сохраняет JSON-декомпозицию
	// задач для фронтенд-разработчиков. Пусто — файл не создаётся.
	PlanFile string
}

// DefaultConfig возвращает конфиг со значениями по умолчанию.
func DefaultConfig() Config {
	return Config{
		NoOverwrite: false,
		PlanFile:    "FRONTEND_PLAN.json",
	}
}

// LoadConfig читает конфиг из переменных окружения (FRONTEND_*),
// заполняя только те поля, которые заданы.
func LoadConfig() Config {
	cfg := DefaultConfig()

	if v := os.Getenv("FRONTEND_MAX_FILES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxFiles = n
		}
	}
	if v := os.Getenv("FRONTEND_NO_OVERWRITE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.NoOverwrite = b
		}
	}
	if v := os.Getenv("FRONTEND_PLAN_FILE"); v != "" {
		cfg.PlanFile = v
	}

	return cfg
}
