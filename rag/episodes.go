// Эпизоды векторной памяти (Ф-7/Ф-8 сжатия контекста).
//
// IndexEpisode сохраняет вытесненный из окна диалог («эпизод») как обычные
// точки, но под псевдо-проектом @episodes:<project>: фильтр поиска CodeSearch
// по project_name их не зацепит, а переиндексация проекта их не стирает.
// Similarity — внеколлекционная мера семантического ранжирования кандидатов
// на выброс (косинус запроса и текстов эмбеддерами Ollama, без Qdrant).

package rag

import (
	"context"
	"fmt"
	"math"
	"strings"
)

// EpisodeProjectPrefix — префикс payload-проектов эпизодов: изолирует память
// ушедших фрагментов диалога от кода проекта (см. шапку файла).
const EpisodeProjectPrefix = "@episodes:"

// IndexEpisode сохраняет эпизод диалога в векторную память: text нарезается
// (линейно, без парсинга кода) и грузится под псевдо-файлом @episode/<source>.
// Точки детерминированные — повторный прогон перезаписывает, не дублирует.
func (c *Client) IndexEpisode(ctx context.Context, pseudoProject, source, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if _, err := c.EnsureCollection(ctx); err != nil {
		return err
	}
	rel := "@episode/" + source + ".txt"
	chunks := ChunkFile(rel, text)
	if len(chunks) == 0 {
		return nil
	}
	if err := c.upsertChunks(ctx, pseudoProject, rel, "@episode", chunks); err != nil {
		return fmt.Errorf("rag: индексация эпизода %s: %w", source, err)
	}
	return nil
}

// EvictionEpisodes — удобная обёртка над IndexEpisode с изоляцией проектов:
// псевдо-проект = EpisodeProjectPrefix + projectName.
func (c *Client) EvictionEpisodes(ctx context.Context, projectName, source, text string) error {
	return c.IndexEpisode(ctx, EpisodeProjectPrefix+projectName, source, text)
}

// Similarity оценивает семантическую близость texts к query: эмбеддит запрос
// и каждый кандидат и возвращает косинусные меры в ТОМ ЖЕ порядке, что texts.
// Используется ранжировщиком сжатия (Ф-8) для выбора жертв выброса. Ошибка —
// только недоступность эмбеддеров; меры в пределах [-1, 1].
func (c *Client) Similarity(ctx context.Context, query string, texts []string) ([]float32, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("rag: пустой запрос ранжирования")
	}
	qvec, err := c.embed.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("rag: эмбеддинг запроса ранжирования: %w", err)
	}
	scores := make([]float32, len(texts))
	for i, t := range texts {
		vec, err := c.embed.Embed(ctx, t)
		if err != nil {
			return nil, fmt.Errorf("rag: эмбеддинг кандидата %d: %w", i, err)
		}
		scores[i] = cosineSimilarity(qvec, vec)
	}
	return scores, nil
}

// cosineSimilarity — косинусная мера двух векторов (0 при нулевых нормах).
func cosineSimilarity(a, b []float32) float32 {
	var dot, na, nb float64
	for i := range a {
		na += float64(a[i]) * float64(a[i])
		if i < len(b) {
			dot += float64(a[i]) * float64(b[i])
			nb += float64(b[i]) * float64(b[i])
		}
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}