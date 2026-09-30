package models

// ModelSettings — ключевые настройки модели провайдера, ограничивающие
// потребление токенов. Значения задаются в providers.json в секции settings
// каждого провайдера.
//
//   - ThinkTokens — бюджет цепочки рассуждения (thinking) в токенах.
//     Провайдеры не принимают количество токенов напрямую, поэтому бюджет
//     переводится в уровень рассуждения: Ollama — think key ("low"/"medium"/
//     "high"/"max"), OpenAI-совместимые (Yandex, Trim) — reasoning_effort
//     ("low"/"medium"/"high") — см. thinkLevelFromTokens.
//   - InputTokens — максимальный размер входного контекста (окно контекста / num_ctx).
//     0 = значение провайдера по умолчанию.
//   - OutputTokens — максимальный размер выходного контекста (ответа).
//     0 = значение провайдера по умолчанию.
type ModelSettings struct {
	// ThinkTokens — бюджет токенов на цепочку рассуждения (thinking).
	// 0 = не задан (модель работает как настроена провайдером).
	ThinkTokens int `json:"think_tokens"`
	// InputTokens — максимальный размер входного контекста (в токенах).
	// 0 = не задан.
	InputTokens int `json:"input_tokens"`
	// OutputTokens — максимальный размер выходного контекста (в токенах).
	// 0 = не задан.
	OutputTokens int `json:"output_tokens"`
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
