package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// DebugEnabled — флаг из переменной окружения LLM_DEBUG ("1"/"true").
func DebugEnabled() bool {
	switch strings.ToLower(os.Getenv("LLM_DEBUG")) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func Debugf(format string, args ...any) {
	if DebugEnabled() {
		fmt.Printf("[DEBUG] "+format+"\n", args...)
	}
}

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
func Truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("...(%d байт всего)", len(s))
}

// DebugCheckEmpty — диагностика пустого ответа: помогает понять,
// почему модель вернула пустой контент.
func DebugCheckEmpty(provider, finishReason string, content string, toolCalls int, raw any) {
	if content != "" || toolCalls > 0 {
		return
	}

	Debugf(
		"!!! ПУСТОЙ ОТВЕТ от %s: finish_reason=%q, content='', tool_calls=%d",
		provider, finishReason, toolCalls,
	)
	DebugJSON(provider+": полный сырой ответ", raw)
}
