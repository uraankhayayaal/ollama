package models

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ProviderConfig — конфигурация одного провайдера из providers.json.
type ProviderConfig struct {
	// BaseURL — базовый URL API (например, "https://ai.api.cloud.yandex.net/v1").
	BaseURL string `json:"base_url"`
	// APIKey — ключ доступа (для Ollama может быть пустым).
	APIKey string `json:"api_key"`
	// FolderID — идентификатор каталога Yandex Cloud (только для yandex).
	FolderID string `json:"folder_id,omitempty"`
	// ModelPrefix — префикс для имени модели (например, "gpt://folder/" для Yandex).
	ModelPrefix string `json:"model_prefix,omitempty"`
	// Models — список доступных моделей.
	Models []string `json:"models"`
	// DefaultModel — модель по умолчанию (если не указана через env MODEL).
	DefaultModel string `json:"default_model,omitempty"`
	// LargeModel — большая модель для LayeredProvider (опционально).
	LargeModel string `json:"large_model,omitempty"`
	// Settings — лимиты токенов и настройки thinking.
	Settings ModelSettings `json:"settings"`
}

// ProvidersConfig — корневой конфиг провайдеров.
type ProvidersConfig struct {
	Providers map[string]ProviderConfig `json:"providers"`
}

var (
	configCache *ProvidersConfig
	configOnce  sync.Once
	configErr   error
)

// LoadProvidersConfig загружает providers.json из корня проекта.
// Кэширует результат — повторные вызовы возвращают тот же указатель.
func LoadProvidersConfig() (*ProvidersConfig, error) {
	configOnce.Do(func() {
		configCache, configErr = loadProvidersConfig()
	})
	return configCache, configErr
}

func loadProvidersConfig() (*ProvidersConfig, error) {
	// Ищем providers.json в текущей директории и выше (до корня)
	dir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("определение рабочей директории: %w", err)
	}

	var path string
	for {
		candidate := filepath.Join(dir, "providers.json")
		if _, err := os.Stat(candidate); err == nil {
			path = candidate
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	if path == "" {
		return nil, fmt.Errorf("providers.json не найден. Создайте его из providers.json.example")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("чтение providers.json: %w", err)
	}

	var cfg ProvidersConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("разбор providers.json: %w", err)
	}

	if len(cfg.Providers) == 0 {
		return nil, fmt.Errorf("providers.json не содержит провайдеров")
	}

	return &cfg, nil
}

// GetProviderConfig возвращает конфиг конкретного провайдера.
func (c *ProvidersConfig) GetProviderConfig(name ProviderName) (ProviderConfig, error) {
	cfg, ok := c.Providers[string(name)]
	if !ok {
		return ProviderConfig{}, fmt.Errorf("провайдер %q не найден в providers.json", name)
	}
	return cfg, nil
}

// ResolveModel определяет итоговое имя модели: из env MODEL, либо default_model из конфига.
func (c *ProviderConfig) ResolveModel(envModel string) string {
	if envModel != "" {
		return envModel
	}
	if c.DefaultModel != "" {
		return c.DefaultModel
	}
	if len(c.Models) > 0 {
		return c.Models[0]
	}
	return ""
}
