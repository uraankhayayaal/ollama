package models

import (
	"os"
	"strconv"
)

// ModelSettings — ключевые настройки модели провайдера, ограничивающие
// потребление токенов. Значения задаются переменными окружения с префиксом
// провайдера (OLLAMA_/YANDEX_/TRIM_):
//
//   - <PREFIX>_THINK_TOKENS — бюджет цепочки рассуждения (thinking) в токенах.
//     Провайдеры не принимают количество токенов напрямую, поэтому бюджет
//     переводится в уровень рассуждения: Ollama — think key ("low"/"medium"/
//     "high"/"max"), OpenAI-совместимые (Yandex, Trim) — reasoning_effort
//     ("low"/"medium"/"high") — см. thinkLevelFromTokens.
//   - <PREFIX>_INPUT_TOKENS — максимальный размер входного контекста (окно
//     контекста / num_ctx). 0 = значение провайдера по умолчанию.
//   - <PREFIX>_OUTPUT_TOKENS — максимальный размер выходного контекста (ответа).
//     0 = значение провайдера по умолчанию.
//
// Для обратной совместимости также читаются legacy-аналоги:
// <PREFIX>_MAX_TOKENS (выход, как у Yandex/Trim) и <PREFIX>_NUM_CTX (вход,
// как у Ollama). Явные новые переменные имеют приоритет.
type ModelSettings struct {
	// ThinkTokens — бюджет токенов на цепочку рассуждения (thinking).
	// 0 = не задан (модель работает как настроена провайдером).
	ThinkTokens int
	// InputTokens — максимальный размер входного контекста (в токенах).
	// 0 = не задан.
	InputTokens int
	// OutputTokens — максимальный размер выходного контекста (в токенах).
	// 0 = не задан.
	OutputTokens int
}

// envInt читает целочисленную переменную окружения; 0, если она не задана
// или не является положительным числом.
func envInt(name string) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// resolveSettings собирает настройки модели из переменных окружения с
// префиксом prefix. Поля fallback используются как значения по умолчанию,
// явно заданная среда имеет приоритет.
func resolveSettings(prefix string, fallback ModelSettings) ModelSettings {
	if n := envInt(prefix + "_THINK_TOKENS"); n > 0 {
		fallback.ThinkTokens = n
	}
	if n := envInt(prefix + "_INPUT_TOKENS"); n > 0 {
		fallback.InputTokens = n
	} else if n := envInt(prefix + "_NUM_CTX"); n > 0 {
		fallback.InputTokens = n
	}
	if n := envInt(prefix + "_OUTPUT_TOKENS"); n > 0 {
		fallback.OutputTokens = n
	} else if n := envInt(prefix + "_MAX_TOKENS"); n > 0 {
		fallback.OutputTokens = n
	}
	return fallback
}

// thinkLevelFromTokens переводит бюджет thinking-токенов в уровень рассуждения
// провайдера. Ни Ollama (think: bool|"low"|"medium"|"high"|"max"), ни
// OpenAI-совместимые API (reasoning_effort: "low"|"medium"|"high") не
// принимают бюджет в токенах — только уровень, поэтому числевой бюджет
// отображается на знакомые уровни. 0 и меньше — "" (не задано).
func thinkLevelFromTokens(tokens int) string {
	switch {
	case tokens >= 12000:
		return "max"
	case tokens >= 8000:
		return "high"
	case tokens >= 3000:
		return "medium"
	case tokens > 0:
		return "low"
	default:
		return ""
	}
}
