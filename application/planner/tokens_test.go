package planner

import (
	"ai/domain"
	"strings"
	"testing"
)

// TestEstimateContextSize проверяет, что оценка токенов корректна
// для типичных размеров промптов.
func TestEstimateContextSize(t *testing.T) {
	// Системный промпт архитектора ~40KB
	// 1 токен ≈ 4 символа
	t.Run("system prompt 40KB", func(t *testing.T) {
		text := strings.Repeat("a", 40*1024)
		tokens := domain.EstimateTextTokens(text)
		if tokens <= 0 {
			t.Fatal("оценка токенов должна быть > 0")
		}
		// 40KB / 4 ≈ 10000 токенов
		if tokens < 8000 || tokens > 12000 {
			t.Fatalf("ожидаем ~10000 токенов для 40KB, got %d", tokens)
		}
	})

	t.Run("maxInputTokens threshold", func(t *testing.T) {
		// 85K токенов — лимит
		text := strings.Repeat("a", maxInputTokens*4) // 1 токен ≈ 4 символа
		tokens := domain.EstimateTextTokens(text)
		// Оценка должна быть ~maxInputTokens
		if tokens <= 0 {
			t.Fatal("оценка токенов должна быть > 0")
		}
		// Разница между оценкой и лимитом должна быть < 10%
		diff := tokens - maxInputTokens
		if diff < 0 {
			diff = -diff
		}
		if diff > maxInputTokens/10 {
			t.Fatalf("оценка %d сильно отличается от лимита %d (diff %d)", tokens, maxInputTokens, diff)
		}
	})

	t.Run("empty", func(t *testing.T) {
		tokens := domain.EstimateTextTokens("")
		if tokens != 0 {
			t.Fatalf("ожидаем 0 токенов для пустой строки, got %d", tokens)
		}
	})
}
