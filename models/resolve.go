package models

import (
	"fmt"
	"os"
)

// ProviderName — имя провайдера для особенностей поведения (noChunk и т.п.).
type ProviderName string

const (
	ProviderOllama ProviderName = "ollama"
	ProviderAlisa  ProviderName = "yandex"
	ProviderTrim   ProviderName = "trim"
	ProviderReg    ProviderName = "reg"
)

// ResolveProvider выбирает провайдера по переменной окружения LLM_PROVIDER
// ("ollama", "yandex", "trim" или "reg"). Одна функция для общих точек входа:
// CLI (main.go) и WebUI (server) — чтобы набор провайдеров не расходился.
//
// Конфигурация провайдеров (API-ключи, base URL, модели, лимиты) читается
// из providers.json (см. LoadProvidersConfig). Переменные окружения:
//   - LLM_PROVIDER — выбор провайдера
//   - MODEL — выбор модели (если не задано, используется default_model из конфига)
//   - MODEL_LARGE — большая модель для LayeredProvider (если не задано, используется large_model из конфига)
//
// Возвращает провайдера, имя провайдера и ошибку.
func ResolveProvider() (LLMProvider, ProviderName, error) {
	name := ProviderName(os.Getenv("LLM_PROVIDER"))

	cfg, err := LoadProvidersConfig()
	if err != nil {
		return nil, name, err
	}

	providerCfg, err := cfg.GetProviderConfig(name)
	if err != nil {
		return nil, name, err
	}

	model := providerCfg.ResolveModel(os.Getenv("MODEL"))

	var (
		provider LLMProvider
	)

	switch name {
	case ProviderOllama:
		// Двухслойная маршрутизация по размеру модели: если задан MODEL_LARGE
		// или large_model в конфиге — быстрые шаги идут на маленькую модель,
		// тяжёлые (лиды/ревью) и эскалации — на большую.
		largeModel := os.Getenv("MODEL_LARGE")
		if largeModel == "" {
			largeModel = providerCfg.LargeModel
		}

		var smallProvider, largeProvider LLMProvider
		smallProvider, err = NewOllamaProvider(model, providerCfg)
		if err == nil && largeModel != "" && largeModel != model {
			largeProvider, err = NewOllamaProvider(largeModel, providerCfg)
		}
		if err == nil {
			provider = NewLayeredProvider(smallProvider, largeProvider)
		}

	case ProviderAlisa, ProviderTrim, ProviderReg:
		provider, err = NewOpenAIProvider(name, model, providerCfg)

	default:
		return nil, name, fmt.Errorf("unknown provider: %s. Use 'ollama', 'yandex', 'trim' or 'reg'", name)
	}

	return provider, name, err
}
