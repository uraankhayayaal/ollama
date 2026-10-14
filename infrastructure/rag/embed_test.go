package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

// OllamaEmbedder ходит в /api/embeddings с JSON-запросом {model, prompt} и
// конвертирует float64-ответ в float32-вектор для Qdrant.
func TestOllamaEmbedderRoute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" {
			t.Fatalf("путь: got %q, want /api/embeddings", r.URL.Path)
		}
		var req api.EmbeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("тело запроса не JSON: %v", err)
		}
		if req.Model != "test-model" {
			t.Fatalf("model: got %q, want test-model", req.Model)
		}
		if req.Prompt != "привет мир" {
			t.Fatalf("prompt: got %q, want привет мир", req.Prompt)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"embedding":[0.5, -1.25, 3]}`)
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	embed := &OllamaEmbedder{client: api.NewClient(u, http.DefaultClient), model: "test-model"}

	vec, err := embed.Embed(context.Background(), "привет мир")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	want := []float32{0.5, -1.25, 3}
	if len(vec) != len(want) {
		t.Fatalf("длина: got %d, want %d", len(vec), len(want))
	}
	for i := range want {
		if vec[i] != want[i] {
			t.Fatalf("vec[%d]: got %v, want %v", i, vec[i], want[i])
		}
	}
}

// Ошибка эмбеддингов (5xx) пробрасывается как ошибка с контекстом модели.
func TestOllamaEmbedderServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"model not found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	embed := &OllamaEmbedder{client: api.NewClient(u, http.DefaultClient), model: "missing-model"}

	_, err := embed.Embed(context.Background(), "текст")
	if err == nil {
		t.Fatal("ожидалась ошибка обращения к Ollama")
	}
	msg := err.Error()
	if !strings.Contains(msg, "missing-model") {
		t.Fatalf("ошибка должна упоминать модель: %v", err)
	}
}

// Пустая модель заменяется на значение по умолчанию при конструировании.
func TestNewOllamaEmbedderDefaultModel(t *testing.T) {
	embed, err := NewOllamaEmbedder("")
	if err != nil {
		t.Fatalf("NewOllamaEmbedder: %v", err)
	}
	if embed.model != DefaultEmbeddingModel {
		t.Fatalf("model: got %q, want %q", embed.model, DefaultEmbeddingModel)
	}
}
