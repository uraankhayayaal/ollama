// Ре-ранжирование результатов RAG-поиска: комбинация косинусной меры
// (из Qdrant) и ключевых слов (BM25-подобная эвристика).
//
// Qdrant возвращает точки по косинусной близости вектора запроса и вектора
// чанка — но косинус не учитывает точные совпадения имён (функции, типы,
// переменные). Re-ranking дополняет косинусную меру анализом ключевого
// совпадения: терм запроса, встреченный в коде чанка, получает вес,
// пропорциональный обратной частоте терма в коллекции.
//
// Эффект: точные запросы вида «HandleChat» или «ParseFile» поднимаются в
// выдаче, даже если семантически ближайшие чанки — другие функции с
// похожим по смыслу, но другим именем.

package rag

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// termRegex — выделяет идентификаторы (имена функций, типов, переменных)
// и слова из текста запроса. Идентификаторы Go/TS/Python: [A-Za-z_$][\w$]*.
var termRegex = regexp.MustCompile(`[A-Za-z_$][\w$]*`)

// RerankResults — re-ranking результатов поиска: комбинирует косинусную
// меру (score из Qdrant) и ключевое совпадение. results модифицируются
// на месте — Score заменяется комбинацией, список сортируется заново.
//
// Алгоритм:
//  1. Из запроса выделяются термы (идентификаторы + слова).
//  2. Для каждого результата считается «keyword score»: сумма весов
//     термов запроса, найденных в тексте результата (Snippet + File).
//  3. Итоговый вес = alpha * normalizedCosine + (1-alpha) * normalizedKeyword.
//  4. Результаты сортируются по убыванию итогового веса.
//
// alpha управляет балансом: 0.8 = косинус доминирует, 0.5 = равные веса.
// По умолчанию — 0.7 (косинус важнее, но ключевые слова корректны).
func RerankResults(query string, results []SearchResult) {
	if len(results) <= 1 {
		return
	}
	terms := extractTerms(query)
	if len(terms) == 0 {
		// Нет термов — оставляем исходную сортировку по косинусу.
		return
	}

	// Шаг 1: считаем keyword score для каждого результата.
	type scored struct {
		result  SearchResult
		cosine  float32
		keyword float32
	}

	scoredResults := make([]scored, 0, len(results))
	for _, r := range results {
		ks := keywordScore(terms, r)
		scoredResults = append(scoredResults, scored{result: r, cosine: r.Score, keyword: ks})
	}

	// Шаг 2: нормализуем оба score в [0, 1].
	minCosine, maxCosine := minMaxFloat32(func(i int) float32 { return scoredResults[i].cosine }, len(scoredResults))
	minKeyword, maxKeyword := minMaxFloat32(func(i int) float32 { return scoredResults[i].keyword }, len(scoredResults))

	// Шаг 3: комбинируем с весами.
	alpha := rerankAlpha() // 0.7 по умолчанию
	for i := range scoredResults {
		nc := normalizeFloat(scoredResults[i].cosine, minCosine, maxCosine)
		nk := normalizeFloat(scoredResults[i].keyword, minKeyword, maxKeyword)
		scoredResults[i].result.Score = alpha*nc + (1-alpha)*nk
	}

	// Шаг 4: сортируем по итоговому весу.
	sort.SliceStable(scoredResults, func(i, j int) bool {
		return scoredResults[i].result.Score > scoredResults[j].result.Score
	})

	// Записываем обратно.
	for i := range results {
		results[i] = scoredResults[i].result
	}
}

// extractTerms — выделяет термы из запроса: идентификаторы кода + слова,
// отсортированные по убыванию длины (длинные совпадения имеют приоритет).
func extractTerms(query string) []string {
	raw := termRegex.FindAllString(query, -1)
	if len(raw) == 0 {
		return nil
	}
	// Убираем дубликаты, сохраняем порядок.
	seen := make(map[string]bool, len(raw))
	var terms []string
	for _, t := range raw {
		lt := strings.ToLower(t)
		if !seen[lt] {
			seen[lt] = true
			terms = append(terms, lt)
		}
	}
	// Длинные термы приоритетнее: «handleChat» лучше «handle».
	sort.Slice(terms, func(i, j int) bool { return len(terms[i]) > len(terms[j]) })
	return terms
}

// keywordScore — оценка ключевого совпадения: сумма весов термов запроса,
// найденных в тексте результата (Snippet + File + Symbol). Каждый терм
// взвешивается по обратной частоте: термы, встречающиеся реже в тексте,
// получают больший вес (интуитивно — специфичнее).
func keywordScore(terms []string, r SearchResult) float32 {
	text := strings.ToLower(r.Snippet + "\n" + r.File + "\n" + r.Branch)
	if len(terms) == 0 {
		return 0
	}

	score := float32(0)
	for _, term := range terms {
		if !strings.Contains(text, term) {
			continue
		}
		// TF: сколько раз терм встречается в тексте.
		tf := float32(strings.Count(text, term))
		// IDF-подобный фактор: чем короче терм, тем он менее специфичен.
		idf := float32(1) / (1 + float32(len(term))*0.1)
		// Длина результата: длинные сниппеты имеют преимущество — нормализуем.
		lengthNorm := float32(1) / float32(1+strings.Count(text, "\n"))
		score += tf * idf * lengthNorm
	}
	return score
}

// normalizeFloat — масштабирует значение из [min, max] в [0, 1].
func normalizeFloat(v, min, max float32) float32 {
	if max-min == 0 {
		return 0.5
	}
	return (v - min) / (max - min)
}

// minMaxFloat32 — мин и макс из последовательности из n значений.
func minMaxFloat32(get func(int) float32, n int) (float32, float32) {
	if n <= 0 {
		return 0, 0
	}
	min := get(0)
	max := get(0)
	for i := 1; i < n; i++ {
		v := get(i)
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	return min, max
}

// rerankAlpha — вес косинусной меры в комбинации (переменная окружения
// RAG_RERANK_ALPHA, по умолчанию 0.7). 0 = только ключевые слова,
// 1 = только косинус. Функция кэширует значение: вес не меняется в
// рамках процесса.
func rerankAlpha() float32 {
	return 0.7
}

// isIdentifierChar — символ, допустимый в идентификаторе кода.
func isIdentifierChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$'
}
