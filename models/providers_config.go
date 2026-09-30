package models

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	configMu    sync.Mutex
	configCache *ProvidersConfig
	configPath  string
	configStamp string
)

// LoadProvidersConfig загружает providers.json из корня проекта.
//
// Результат кэшируется по пути файла и его отпечатку (mtime + размер): повторные
// вызовы не читают файл заново, но правка providers.json (добавили модель в
// Web UI без перезапуска) подхватывается сразу. Путь можно задать явно через
// PROVIDERS_CONFIG — иначе файл ищется в текущем каталоге и выше.
func LoadProvidersConfig() (*ProvidersConfig, error) {
	path, err := providersConfigPath()
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("providers.json (%s) недоступен: %w", path, err)
	}
	stamp := fmt.Sprintf("%d:%d", st.ModTime().UnixNano(), st.Size())

	configMu.Lock()
	defer configMu.Unlock()
	if configCache != nil && configPath == path && configStamp == stamp {
		return configCache, nil
	}
	cfg, err := loadProvidersConfigFile(path)
	if err != nil {
		return nil, err
	}
	configCache, configPath, configStamp = cfg, path, stamp
	return cfg, nil
}

// providersConfigPath определяет путь к providers.json: PROVIDERS_CONFIG,
// затем поиск в текущем каталоге и выше (до корня).
func providersConfigPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("PROVIDERS_CONFIG")); p != "" {
		return p, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("определение рабочей директории: %w", err)
	}
	for {
		candidate := filepath.Join(dir, "providers.json")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("providers.json не найден. Создайте его из providers.json.example")
}

func loadProvidersConfigFile(path string) (*ProvidersConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("чтение %s: %w", path, err)
	}

	var cfg ProvidersConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("разбор %s: %w", path, err)
	}

	if len(cfg.Providers) == 0 {
		return nil, fmt.Errorf("%s не содержит провайдеров", path)
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
