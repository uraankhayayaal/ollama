package devops

import (
	"os"
	"strconv"
)

// Config — настраиваемые параметры агента DevOps Engineer.
// Значения берутся из переменных окружения (см. .env.example).
type Config struct {
	// MaxFiles — максимальное число файлов, которое агент может записать
	// за один запуск. 0 или отрицательное — без лимита.
	MaxFiles int

	// NoOverwrite запрещает перезаписывать уже существующие файлы.
	// true — вместо перезаписи инструмент вернёт ошибку.
	NoOverwrite bool
}

// DefaultConfig возвращает конфиг со значениями по умолчанию.
func DefaultConfig() Config {
	return Config{
		NoOverwrite: false,
	}
}

// LoadConfig читает конфиг из переменных окружения (DEVOPS_*),
// заполняя только те поля, которые заданы.
func LoadConfig() Config {
	cfg := DefaultConfig()

	if v := os.Getenv("DEVOPS_MAX_FILES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxFiles = n
		}
	}
	if v := os.Getenv("DEVOPS_NO_OVERWRITE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.NoOverwrite = b
		}
	}

	return cfg
}
