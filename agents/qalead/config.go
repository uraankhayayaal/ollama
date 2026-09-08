package qalead

import (
	"ai/agents/qaengineer"
)

// Config — настраиваемые параметры агента QA Lead.
// Переиспользует конфигурацию агента qaengineer (общие переменные окружения
// QA_*): оба QA-агента работают с одними и теми же файловыми инструментами
// и лимитами записи.
type Config = qaengineer.Config

// LoadConfig читает конфиг из переменных окружения (QA_*).
func LoadConfig() Config {
	return qaengineer.LoadConfig()
}
