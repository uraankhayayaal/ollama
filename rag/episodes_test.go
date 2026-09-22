package rag

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// IndexEpisode грузит эпизод под псевдо-проектом @episodes:<project>: текст
// нарезается (линейно), payload отдаётся в upsert. Пустой текст — no-op.
func TestIndexEpisode(t *testing.T) {
	store := &fakeStore{exists: false}
	c := newClient(store, &fakeEmbedder{dim: 4}, DefaultCollectionName)

	err := c.EvictionEpisodes(context.Background(), "proj-a", "dialog", strings.Repeat("фрагмент диалога\n", 300))
	if err != nil {
		t.Fatalf("IndexEpisode: %v", err)
	}
	if store.created == nil {
		t.Fatal("коллекция должна создаться при первом эпизоде")
	}
	if store.upsertCalls == 0 {
		t.Fatal("эпизод должен быть загружен upsert'ом")
	}

	// Пустой текст — no-op без взаимодействия.
	before := store.upsertCalls
	if err := c.EvictionEpisodes(context.Background(), "proj-a", "dialog", "   \n  "); err != nil {
		t.Fatalf("IndexEpisode(пустой): %v", err)
	}
	if store.upsertCalls != before {
		t.Fatal("пустой эпизод не должен ничего грузить")
	}
}

// Недоступные эмбеддинги — понятная ошибка (фича отключается на уровне runner);
// недоступный Qdrant при живых эмбеддингах — UnavailableError.
func TestIndexEpisodeEmbedError(t *testing.T) {
	c := newClient(&fakeStore{exists: true}, &fakeEmbedder{dim: 4, err: errors.New("ollama down")}, DefaultCollectionName)
	if err := c.EvictionEpisodes(context.Background(), "p", "dialog", "текст"); err == nil {
		t.Fatal("ошибка эмбеддера должна всплыть")
	}

	store := &fakeStore{exists: true, existsErr: errors.New("qdrant down")}
	c2 := newClient(store, &fakeEmbedder{dim: 4}, DefaultCollectionName)
	if err := c2.EvictionEpisodes(context.Background(), "p", "dialog", "текст"); !IsUnavailable(err) {
		t.Fatalf("ошибка Qdrant должна быть UnavailableError, got %T: %v", err, err)
	}
}

// Similarity — косинусные меры в исходном порядке; ошибка эмбеддера всплывает.
func TestSimilarity(t *testing.T) {
	embed := &fakeEmbedder{dim: 3}
	c := newClient(&fakeStore{exists: true}, embed, DefaultCollectionName)

	scores, err := c.Similarity(context.Background(), "запрос", []string{"один", "два"})
	if err != nil {
		t.Fatalf("Similarity: %v", err)
	}
	if len(scores) != 2 {
		t.Fatalf("должно быть 2 меры, got %d", len(scores))
	}
	// Векторы эмбеддера идентичны во всех вызовах → мера должна быть 1 (норма 1).
	for i, s := range scores {
		if s < 0.99 {
			t.Fatalf("косинус идентичных векторов должен быть ~1, got %f (idx %d)", s, i)
		}
	}
	// Запрос + 2 кандидата = 3 вызова эмбеддера (пробы размерности нет —
	// dimension кэшируется только EnsureCollection).
	if len(embed.texts) != 3 {
		t.Fatalf("ожидали 3 вызова эмбеддера (query + 2), got %d", len(embed.texts))
	}

	embed.err = errors.New("ollama down")
	if _, err := c.Similarity(context.Background(), "запрос", []string{"x"}); err == nil {
		t.Fatal("ошибка эмбеддера должна всплыть")
	}

	if _, err := c.Similarity(context.Background(), " ", []string{"x"}); err == nil {
		t.Fatal("пустой запрос должны отвергаться")
	}
}

// cosineSimilarity: ноль при нулевых нормах, симметричность.
func TestCosineSimilarity(t *testing.T) {
	if got := cosineSimilarity([]float32{1, 2, 3}, []float32{4, 5, 6}); got <= 0 {
		t.Fatalf("косинус сонаправленных векторов должен быть >0, got %f", got)
	}
	if got := cosineSimilarity([]float32{}, []float32{1, 2}); got != 0 {
		t.Fatalf("нулевая норма должна давать 0, got %f", got)
	}
	// Разная длина — вычисляем до общей длины и не падаем.
	if got := cosineSimilarity([]float32{1, 2, 3, 4}, []float32{1, 2, 3}); got == 0 {
		t.Fatalf("косинус по общей части не должен быть 0, got %f", got)
	}
}