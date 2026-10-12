package domain

import "unicode"

// EstimateTextTokens — грубая оценка количества токенов в тексте без словаря
// токенизатора (проект использует разные LLM — Ollama, Yandex, OpenAI-совместимые).
// Ориентир: ~4 символа на токен для ASCII/латиницы и более плотные языки
// (кириллица кодируется токенами чаще), поэтому оценка берёт максимум между
// «символьной» (len/4) и «словесной» (words × 1.25) эвристиками.
func EstimateTextTokens(s string) int {
	if s == "" {
		return 0
	}
	chars := 0
	words := 0
	inWord := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			inWord = false
			continue
		}
		chars++
		if !inWord {
			words++
			inWord = true
		}
	}
	if chars == 0 {
		return 0
	}
	byChars := chars/4 + 1
	byWords := words + words/4 + 1
	if byChars > byWords {
		return byChars
	}
	return byWords
}

// EstimateUsage оценивает токены раунда, когда провайдер не сообщает
// фактический usage: вход — вся история сообщений диалога (включая аргументы
// вызовов инструментов), выход — ответ модели (текст и аргументы tool_calls).
func EstimateUsage(messages []Message, reply *ModelReply) (in, out int) {
	for _, m := range messages {
		in += EstimateTextTokens(m.Content)
		if m.Role == "tool" {
			in += 4 // служебные токены tool-сообщения (tool_call_id и пр.)
		}
		for _, tc := range m.ToolCalls {
			in += EstimateTextTokens(tc.Name) + EstimateTextTokens(tc.Arguments)
		}
	}
	if reply != nil {
		out += EstimateTextTokens(reply.Content)
		for _, tc := range reply.ToolCalls {
			out += EstimateTextTokens(tc.Name) + EstimateTextTokens(tc.Arguments)
		}
	}
	return in, out
}
