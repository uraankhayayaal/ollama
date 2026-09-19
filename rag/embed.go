// Модуль эмбеддингов векторной памяти RAG (см. PLAN-qdrant.md, Ф-1).
//
// Embedder превращает текст в вектор ([]float32) для семантического поиска
// в Qdrant. Реализация по умолчанию — нативный Ollama HTTP (POST
// /api/embeddings), без отдельного сервиса эмбеддингов (решение пользователя №2).

package rag

import (
	"context"
	"fmt"

	"github.com/ollama/ollama/api"
)

// DefaultEmbeddingModel — модель эмбеддингов по умолчанию.
const DefaultEmbeddingModel = "nomic-embed-text"

// Embedder — источник эмбеддингов текста.
type Embedder interface {
	// Embed возвращает векторное представление текста.
	Embed(ctx context.Context, text string) ([]float32, error)
}

// OllamaEmbedder — эмбеддер через HTTP Ollama (/api/embeddings). Клиент берётся
// из окружения (OLLAMA_HOST) — той же локальной Ollama, что и основной
// провайдер моделей; модель задаётся EMBEDDING_MODEL.
type OllamaEmbedder struct {
	client *api.Client
	model  string
}

// NewOllamaEmbedder создаёт эмбеддер для модели model. Пустая модель заменяется
// на DefaultEmbeddingModel.
func NewOllamaEmbedder(model string) (*OllamaEmbedder, error) {
	client, err := api.ClientFromEnvironment()
	if err != nil {
		return nil, fmt.Errorf("rag: инициализация клиента Ollama: %w", err)
	}
	if model == "" {
		model = DefaultEmbeddingModel
	}
	return &OllamaEmbedder{client: client, model: model}, nil
}

// Embed вызывает Ollama /api/embeddings и возвращает вектор float32.
func (e *OllamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	resp, err := e.client.Embeddings(ctx, &api.EmbeddingRequest{Model: e.model, Prompt: text})
	if err != nil {
		return nil, fmt.Errorf("rag: Ollama /api/embeddings (%s): %w", e.model, err)
	}
	// Qdrant хранит векторы float32, а Ollama /api/embeddings возвращает float64.
	vec := make([]float32, len(resp.Embedding))
	for i := range resp.Embedding {
		vec[i] = float32(resp.Embedding[i])
	}
	return vec, nil
}
