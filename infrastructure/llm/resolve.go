package llm

import (
	"ai/domain/services"
	"fmt"
	"os"
	"strings"
)

// ProviderName — имя провайдера для особенностей поведения (noChunk и т.п.).
type ProviderName string

const (
	ProviderOllama   ProviderName = "ollama"
	ProviderAlisa    ProviderName = "yandex"
	ProviderTrim     ProviderName = "trim"
	ProviderReg      ProviderName = "reg"
	ProviderLlamaCpp ProviderName = "llama.cpp"
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

// Resolved — созданные провайдеры (малый и большой слои) и ФАКТИЧЕСКИ
// выбранные модели (после применения дефолтов из providers.json). Нужен, чтобы
// логи и Web UI показывали реально работающую модель, а не только запрошенную.
//
// Полный агентский цикл и эскалацию малого слоя на большой собирает
// application-слой (usecases.NewEscalating) из Small и Large — инфраструктура
// отдаёт только чат-провайдеров (реализации порта services.ChatProvider).
type Resolved struct {
	Small      services.ChatProvider
	Large      services.ChatProvider // nil, если большая модель не задана/совпадает
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
//   - MODEL_LARGE — большая модель для эскалации (если не задано, используется large_model из конфига)
//
// Возвращает созданные слои и имя провайдера.
func ResolveProvider() (*Resolved, ProviderName, error) {
	r, err := ResolveSelection(EnvSelection())
	name := ProviderName(strings.TrimSpace(os.Getenv("LLM_PROVIDER")))
	if r != nil {
		name = r.Name
	}
	if err != nil {
		return r, name, err
	}
	return r, r.Name, nil
}

// ResolveSelection создаёт провайдеров по ЯВНОМУ выбору (провайдер + модели).
// Пустые Model/LargeModel берутся из providers.json. В отличие от старого
// ResolveProvider не мутирует os.Environ — вызов из Web UI меняет модель на
// лету, не затрагивая остальной процесс.
//
// Большая модель создаётся как ОТДЕЛЬНЫЙ провайдер (Large) и не оборачивается
// здесь в маршрутизацию: политику «когда эскалировать» задаёт application-слой.
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

	switch sel.Provider {
	case ProviderOllama:
		small, serr := NewOllamaProvider(model, providerCfg)
		if serr != nil {
			return out, serr
		}
		out.Small = small
		if largeModel != "" && largeModel != model {
			large, lerr := NewOllamaProvider(largeModel, providerCfg)
			if lerr != nil {
				return out, lerr
			}
			out.Large = large
			out.LargeModel = largeModel
		}

	case ProviderAlisa, ProviderTrim, ProviderReg, ProviderLlamaCpp:
		p, perr := NewOpenAIProvider(sel.Provider, model, providerCfg)
		if perr != nil {
			return out, perr
		}
		out.Small = p

	default:
		return nil, fmt.Errorf("unknown provider: %s. Use 'ollama', 'yandex', 'trim', 'reg' or 'llama.cpp'", sel.Provider)
	}

	return out, nil
}
