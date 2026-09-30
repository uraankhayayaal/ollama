package models

import (
	"fmt"
	"os"
	"strings"
)

// ProviderName — имя провайдера для особенностей поведения (noChunk и т.п.).
type ProviderName string

const (
	ProviderOllama ProviderName = "ollama"
	ProviderAlisa  ProviderName = "yandex"
	ProviderTrim   ProviderName = "trim"
	ProviderReg    ProviderName = "reg"
)

// Selection — выбор провайдера и моделей. Пустые поля означают «взять из
// providers.json» (default_model / large_model). Используется двумя путями:
//   - EnvSelection — чтение переменных окружения (CLI и режим сервера по умолчанию);
//   - явный выбор из Web UI (server/resolve.go), который не требует перезапуска
//     процесса и не мутирует окружение.
type Selection struct {
	Provider   ProviderName
	Model      string
	LargeModel string
}

// Resolved — созданный провайдер и ФАКТИЧЕСКИ выбранные модели (после
// применения дефолтов из providers.json). Нужен, чтобы логи и Web UI
// показывали реально работающую модель, а не только запрошенную.
type Resolved struct {
	Provider   LLMProvider
	Name       ProviderName
	Model      string
	LargeModel string
}

// Describe — человекочитаемое описание выбора для логов и UI, например
// "ollama/qwen3-coder:30b (large=qwen3.6:35b-a3b)". Слой large печатается,
// только если он реально включён (задан и отличается от основного).
func (r *Resolved) Describe() string {
	if r == nil {
		return "провайдер не создан"
	}
	base := fmt.Sprintf("%s/%s", r.Name, orDash(r.Model))
	if r.LargeModel != "" && r.LargeModel != r.Model {
		return fmt.Sprintf("%s (large=%s)", base, r.LargeModel)
	}
	return base
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "модель не задана"
	}
	return s
}

// EnvSelection — выбор из окружения: LLM_PROVIDER, MODEL, MODEL_LARGE.
func EnvSelection() Selection {
	return Selection{
		Provider:   ProviderName(strings.TrimSpace(os.Getenv("LLM_PROVIDER"))),
		Model:      strings.TrimSpace(os.Getenv("MODEL")),
		LargeModel: strings.TrimSpace(os.Getenv("MODEL_LARGE")),
	}
}

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
	r, err := ResolveSelection(EnvSelection())
	if err != nil {
		name := ProviderName(strings.TrimSpace(os.Getenv("LLM_PROVIDER")))
		if r != nil {
			name = r.Name
		}
		return nil, name, err
	}
	return r.Provider, r.Name, nil
}

// ResolveSelection создаёт провайдера по ЯВНОМУ выбору (провайдер + модели).
// Пустые Model/LargeModel берутся из providers.json. В отличие от старого
// ResolveProvider не мутирует os.Environ — вызов из Web UI меняет модель на
// лету, не затрагивая остальной процесс.
func ResolveSelection(sel Selection) (*Resolved, error) {
	cfg, err := LoadProvidersConfig()
	if err != nil {
		return nil, err
	}

	providerCfg, err := cfg.GetProviderConfig(sel.Provider)
	if err != nil {
		return nil, err
	}

	model := providerCfg.ResolveModel(sel.Model)
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("у провайдера %q не задана модель: укажите models/default_model в providers.json", sel.Provider)
	}

	out := &Resolved{Name: sel.Provider, Model: model}

	largeModel := sel.LargeModel
	if largeModel == "" {
		largeModel = providerCfg.LargeModel
	}

	var provider LLMProvider

	switch sel.Provider {
	case ProviderOllama:
		// Двухслойная маршрутизация по размеру модели: если задана большая
		// модель — быстрые шаги идут на маленькую, тяжёлые (лиды/ревью) и
		// эскалации — на большую.
		var largeProvider LLMProvider
		provider, err = NewOllamaProvider(model, providerCfg)
		if err == nil && largeModel != "" && largeModel != model {
			largeProvider, err = NewOllamaProvider(largeModel, providerCfg)
			if err == nil {
				out.LargeModel = largeModel
			}
		}
		if err == nil {
			provider = NewLayeredProvider(provider, largeProvider)
		}

	case ProviderAlisa, ProviderTrim, ProviderReg:
		provider, err = NewOpenAIProvider(sel.Provider, model, providerCfg)

	default:
		return nil, fmt.Errorf("unknown provider: %s. Use 'ollama', 'yandex', 'trim' or 'reg'", sel.Provider)
	}

	if err != nil {
		return out, err
	}
	out.Provider = provider
	return out, nil
}
