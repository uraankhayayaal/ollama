package models

import (
	"testing"
)

func TestResolveSettings(t *testing.T) {
	t.Run("legacy aliases", func(t *testing.T) {
		t.Setenv("OLLAMA_NUM_CTX", "4096")
		t.Setenv("OLLAMA_MAX_TOKENS", "2048")
		got := resolveSettings("OLLAMA", ModelSettings{})
		if got.InputTokens != 4096 || got.OutputTokens != 2048 || got.ThinkTokens != 0 {
			t.Fatalf("legacy aliases не применились: got %+v", got)
		}
	})

	t.Run("new vars приоритетнее legacy", func(t *testing.T) {
		t.Setenv("OLLAMA_INPUT_TOKENS", "16384")
		t.Setenv("OLLAMA_NUM_CTX", "4096")
		t.Setenv("OLLAMA_OUTPUT_TOKENS", "8192")
		t.Setenv("OLLAMA_MAX_TOKENS", "2048")
		t.Setenv("OLLAMA_THINK_TOKENS", "4000")
		got := resolveSettings("OLLAMA", ModelSettings{})
		if got.InputTokens != 16384 || got.OutputTokens != 8192 || got.ThinkTokens != 4000 {
			t.Fatalf("новые переменные должны выигрывать: got %+v", got)
		}
	})

	t.Run("fallback сохраняется, пустой env не перетирает", func(t *testing.T) {
		t.Setenv("TRIM_MAX_TOKENS", "")
		got := resolveSettings("TRIM", ModelSettings{InputTokens: 32000, OutputTokens: 4000})
		if got.InputTokens != 32000 || got.OutputTokens != 4000 {
			t.Fatalf("незаданный env не должен ломать fallback: got %+v", got)
		}
	})

	t.Run("нечисловое значение игнорируется", func(t *testing.T) {
		t.Setenv("TRIM_OUTPUT_TOKENS", "abc")
		got := resolveSettings("TRIM", ModelSettings{OutputTokens: 4000})
		if got.OutputTokens != 4000 {
			t.Fatalf("нечисловой env должен быть проигнорирован: got %+v", got)
		}
	})
}

func TestThinkLevelFromTokens(t *testing.T) {
	cases := []struct {
		tokens int
		want   string
	}{
		{0, ""},
		{-5, ""},
		{1, "low"},
		{2500, "low"},
		{3000, "medium"},
		{7999, "medium"},
		{8000, "high"},
		{11999, "high"},
		{12000, "max"},
	}
	for _, c := range cases {
		if got := thinkLevelFromTokens(c.tokens); got != c.want {
			t.Errorf("thinkLevelFromTokens(%d) = %q, want %q", c.tokens, got, c.want)
		}
	}
}
