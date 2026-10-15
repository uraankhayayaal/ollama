package runner

import (
	"ai/application/usecases"
	"ai/shared/logging"
	"encoding/json"
)

// Детектор петель живёт в слое application (usecases) и не знает про логгер
// инфраструктуры — подключаем его при инициализации пакета.
func init() { usecases.Debugf = Debugf }

// DebugEnabled — флаг из переменной окружения LLM_DEBUG ("1"/"true").
func DebugEnabled() bool { return logging.DebugEnabled() }

func Debugf(format string, args ...any) { logging.Debugf(format, args...) }

func DebugJSON(label string, v any) {
	if !DebugEnabled() {
		return
	}

	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		Debugf("%s: (не удалось сериализовать: %v)", label, err)
		return
	}

	Debugf("%s:\n%s", label, string(b))
}

// Truncate обрезает длинные строки для компактного лога.
func Truncate(s string, max int) string { return logging.Truncate(s, max) }

// DebugCheckEmpty — диагностика пустого ответа: помогает понять,
// почему модель вернула пустой контент.
func DebugCheckEmpty(provider, finishReason string, content string, toolCalls int, raw any) {
	logging.DebugCheckEmpty(provider, finishReason, content, toolCalls, raw)
}
