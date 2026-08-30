package repositories

import (
	"ai/models"
	"context"
)

// VectorItem представляет документ или объект, хранящийся в векторной БД
type VectorItem struct {
	ID      string         `json:"id"`
	Vector  []float32      `json:"vector"`
	Payload map[string]any `json:"payload"` // Дополнительные данные (текст, теги и т.д.)
	Score   float32        `json:"score"`   // Заполняется только при поиске (метрика близости)
}

// SearchFilter помогает фильтровать результаты по метаданным (payload)
type SearchFilter struct {
	Tags []string
}

// VectorRepository задает контракт для работы с векторным хранилищем
type VectorRepository interface {
	NewCollection(ctx context.Context, provider models.LLMProvider) error

	Insert(ctx context.Context, item *VectorItem) error

	BatchInsert(ctx context.Context, items []*VectorItem) error

	Search(ctx context.Context, targetVector []float32, limit int, filter *SearchFilter) ([]*VectorItem, error)

	Delete(ctx context.Context, id string) error
}
