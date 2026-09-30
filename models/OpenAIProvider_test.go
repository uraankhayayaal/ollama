package models

import (
	"ai/runner"
	"testing"
)

func TestNewOpenAIProvider(t *testing.T) {
	cfg := ProviderConfig{
		BaseURL: "https://ai.reg.cloud/v1",
		APIKey:  "test-key",
		Models:  []string{"qwen-3.8-27b"},
		Settings: ModelSettings{
			InputTokens:  262144,
			OutputTokens: 32768,
		},
	}

	provider, err := NewOpenAIProvider(ProviderReg, "qwen-3.8-27b", cfg)
	if err != nil {
		t.Fatalf("Failed to create OpenAIProvider: %v", err)
	}

	if provider.model != "qwen-3.8-27b" {
		t.Errorf("Expected model 'qwen-3.8-27b', got %s", provider.model)
	}

	limits := provider.ModelLimits()
	if limits.InputTokens != 262144 {
		t.Errorf("Expected InputTokens 262144, got %d", limits.InputTokens)
	}
	if limits.OutputTokens != 32768 {
		t.Errorf("Expected OutputTokens 32768, got %d", limits.OutputTokens)
	}
}

func TestNewOpenAIProvider_MissingAPIKey(t *testing.T) {
	cfg := ProviderConfig{
		BaseURL: "https://ai.reg.cloud/v1",
		APIKey:  "",
		Models:  []string{"qwen-3.8-27b"},
	}

	_, err := NewOpenAIProvider(ProviderReg, "qwen-3.8-27b", cfg)
	if err == nil {
		t.Fatal("Expected error when API key is not set")
	}
}

func TestNewOpenAIProvider_ModelPrefix(t *testing.T) {
	cfg := ProviderConfig{
		BaseURL:     "https://ai.api.cloud.yandex.net/v1",
		APIKey:      "test-key",
		FolderID:    "test-folder",
		ModelPrefix: "gpt://test-folder/",
		Models:      []string{"qwen3.6-35b-a3b/latest"},
	}

	provider, err := NewOpenAIProvider(ProviderAlisa, "qwen3.6-35b-a3b/latest", cfg)
	if err != nil {
		t.Fatalf("Failed to create OpenAIProvider: %v", err)
	}

	expected := "gpt://test-folder/qwen3.6-35b-a3b/latest"
	if provider.model != expected {
		t.Errorf("Expected model %q, got %q", expected, provider.model)
	}
}

func TestNewOpenAIProvider_NoPrefix(t *testing.T) {
	cfg := ProviderConfig{
		BaseURL: "https://ai.reg.cloud/v1",
		APIKey:  "test-key",
		Models:  []string{"qwen-3.8-27b"},
	}

	provider, err := NewOpenAIProvider(ProviderReg, "qwen-3.8-27b", cfg)
	if err != nil {
		t.Fatalf("Failed to create OpenAIProvider: %v", err)
	}

	if provider.model != "qwen-3.8-27b" {
		t.Errorf("Expected model 'qwen-3.8-27b', got %q", provider.model)
	}
}

func TestHasToolResult(t *testing.T) {
	msgs := []runner.Message{
		{Role: "user", Content: "test"},
	}
	if hasToolResult(msgs) {
		t.Error("Expected false for messages without tool result")
	}

	msgs = append(msgs, runner.Message{Role: "tool", Content: "result"})
	if !hasToolResult(msgs) {
		t.Error("Expected true for messages with tool result")
	}
}
