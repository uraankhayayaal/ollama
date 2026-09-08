package devopslead

import (
	"ai/agents/devops"
)

// Config — настраиваемые параметры агента DevOps Lead.
// Переиспользует конфигурацию агента devops (общие переменные окружения
// DEVOPS_*): оба инфраструктурных агента работают с одними и теми же
// файловыми инструментами и лимитами записи.
type Config = devops.Config

// LoadConfig читает конфиг из переменных окружения (DEVOPS_*).
func LoadConfig() Config {
	return devops.LoadConfig()
}
