package repositories

import (
	"ai/models"
	"context"
	"fmt"

	"github.com/qdrant/go-client/qdrant"
)

type qdrantRepo struct {
	client     *qdrant.Client
	collection string
}

// NewQdrantRepository — конструктор репозитория
func NewQdrantRepository(client *qdrant.Client, collection string) VectorRepository {
	return &qdrantRepo{
		client:     client,
		collection: collection,
	}
}

func (r *qdrantRepo) NewCollection(ctx context.Context, provider models.LLMProvider) error {
	collectionName := provider.GetModelName(ctx)

	embedded, err := provider.GetEmbedded(ctx)
	if err == nil {
		return err
	}
	if len(embedded[0]) == 0 {
		return fmt.Errorf("Ошибка, модель не вернул вектор!")
	}

	// Создаем коллекцию (например, под эмбеддинги размерностью 1024)
	return r.client.CreateCollection(ctx, &qdrant.CreateCollection{
		CollectionName: collectionName,
		VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{
			Size:     uint64(len(embedded[0])), // Укажите размерность вашей модели эмбеддингов
			Distance: qdrant.Distance_Cosine,   // Косинусное сходство — стандарт для RAG
		}),
	})
}

func (r *qdrantRepo) Insert(ctx context.Context, item *VectorItem) error {
	// Логика трансформации структуры VectorItem в формат Qdrant
	// и вызов r.client.Upsert(...)
	return nil
}

func (r *qdrantRepo) BatchInsert(ctx context.Context, items []*VectorItem) error {
	// Пакетная вставка
	return nil
}

func (r *qdrantRepo) Search(ctx context.Context, targetVector []float32, limit int, filter *SearchFilter) ([]*VectorItem, error) {
	// 1. Формирование запроса к БД с учетом лимита и фильтров
	// 2. Вызов r.client.Search(...)
	// 3. Маппинг ответа БД обратно в []*VectorItem
	return nil, nil
}

func (r *qdrantRepo) Delete(ctx context.Context, id string) error {
	// Удаление точки
	return nil
}
