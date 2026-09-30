package models

import (
	"testing"
)

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

func TestProviderConfig_ResolveModel(t *testing.T) {
	cfg := ProviderConfig{
		Models:       []string{"model-a", "model-b"},
		DefaultModel: "model-a",
	}

	if got := cfg.ResolveModel(""); got != "model-a" {
		t.Errorf("ResolveModel с пустым env должен вернуть default_model, got %q", got)
	}

	if got := cfg.ResolveModel("model-c"); got != "model-c" {
		t.Errorf("ResolveModel с env MODEL должен вернуть значение из env, got %q", got)
	}

	cfgNoDefault := ProviderConfig{
		Models: []string{"model-x"},
	}
	if got := cfgNoDefault.ResolveModel(""); got != "model-x" {
		t.Errorf("ResolveModel без default_model должен вернуть первую модель, got %q", got)
	}
}
