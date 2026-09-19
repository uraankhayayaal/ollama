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
)

// ResolveProvider выбирает провайдера по переменной окружения LLM_PROVIDER
// ("ollama", "yandex" или "trim"). Одна функция для общих точек входа: CLI
// (main.go) и WebUI (server) — чтобы набор провайдеров не расходился.
//
// Возвращает таппера, имя провайдера и ошибку. Для ollama учитывает
// двухслойную маршрутизацию OLLAMA_MODEL / OLLAMA_MODEL_LARGE.
func ResolveProvider() (LLMProvider, ProviderName, error) {
	name := ProviderName(os.Getenv("LLM_PROVIDER"))

	var (
		provider LLMProvider
		err      error
	)

	switch name {
	case ProviderOllama:
		model := os.Getenv("OLLAMA_MODEL") // например, "llama3"
		if model == "" {
			model = "llama3"
		}
		// Двухслойная маршрутизация по размеру модели (Скорость 2): если задан
		// OLLAMA_MODEL_LARGE — быстрые шаги идут на OLLAMA_MODEL, тяжёлые
		// (лиды/ревью) и эскалации — на большую модель. Без OLLAMA_MODEL_LARGE
		// поведение не меняется.
		var smallProvider, largeProvider LLMProvider
		smallProvider, err = NewOllamaProvider(model)
		if largeModel := os.Getenv("OLLAMA_MODEL_LARGE"); largeModel != "" && largeModel != model {
			largeProvider, err = NewOllamaProvider(largeModel)
		}
		if err == nil {
			provider = NewLayeredProvider(smallProvider, largeProvider)
		}

	case ProviderAlisa:
		provider = NewAlisaProvider()

	case ProviderTrim:
		provider, err = NewTrimProvider()

	default:
		return nil, name, fmt.Errorf("unknown provider: %s. Use 'ollama', 'yandex' or 'trim'", name)
	}

	return provider, name, err
}
