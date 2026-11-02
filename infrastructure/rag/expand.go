// Расширение запроса RAG: генерация альтернативных формулировок.
//
// Один запрос → один вектор может промахнуться, если формулировка пользователя
// не совпадает с терминологией кода. Query expansion генерирует 3–5
// альтернативных формулировок и объединяет результаты поиска через RRF
// (Reciprocal Rank Fusion).
//
// Подход текстовый (без вызова LLM): запрос разбирается на термы, для каждого
// генерируются варианты с добавлением контекста (имена функций, ключевые
// слова, префиксы). Это дешевле, чем вызов LLM, и работает без сети.

package rag

import (
	"fmt"
	"sort"
	"strings"
)

// ExpandedQueries — генерирует альтернативные формулировки запроса для
// расширения поиска. Возвращает оригинал + 1–4 варианта.
//
// Стратегия:
//   - Для запросов-ключевых слов: добавляет глагольные шаблоны
//     («как найти», «где реализовано», «реализация»).
//   - Для запросов-описаний: укорачивает до ключевых термов
//     («где валидируется токен сессии» → «валидация токена»).
//   - Для коротких запросов (< 30 символов): не расширяет (уже сжат).
func ExpandedQueries(query string) []string {
	query = strings.TrimSpace(query)
	if len(query) < 4 {
		return []string{query}
	}
	// Уже достаточно сжатый запрос: не расширяем.
	if len(query) <= 30 && countWords(query) <= 3 {
		return []string{query}
	}

	terms := extractTerms(query)
	if len(terms) == 0 {
		return []string{query}
	}

	// Удаляем стоп-слова для получения ключевых термов.
	keyTerms := filterStopWords(terms)
	if len(keyTerms) == 0 {
		keyTerms = terms
	}

	var expanded []string
	expanded = append(expanded, query) // оригинал — всегда первый

	// Вариант 1: ключевые термы без стоп-слов (укороченный запрос).
	if len(keyTerms) < len(terms) {
		expanded = append(expanded, strings.Join(keyTerms, " "))
	}

	// Вариант 2: добавляем контекстные префиксы.
	if len(keyTerms) >= 1 {
		for _, prefix := range codeSearchPrefixes {
			v := prefix + " " + strings.Join(keyTerms, " ")
			if v != query && len(expanded) < 5 {
				expanded = append(expanded, v)
			}
		}
	}

	// Вариант 3: комбинация 2-х термов (покрывает запросы из 3+ слов).
	if len(keyTerms) >= 3 && len(expanded) < 5 {
		for i := 0; i < len(keyTerms)-1 && len(expanded) < 5; i++ {
			v := keyTerms[i] + " " + keyTerms[i+1]
			if v != query {
				expanded = append(expanded, v)
			}
		}
	}

	return expanded
}

// codeSearchPrefixes — контекстные префиксы для генерации вариантов запроса.
var codeSearchPrefixes = []string{
	"реализация",
	"где",
	"функция",
}

// queryStopWords — слова-мусор, которые не несут семантики для поиска кода.
var queryStopWords = map[string]bool{
	"как":    true,
	"где":    true,
	"на":     true,
	"по":     true,
	"для":    true,
	"из":     true,
	"от":     true,
	"о":      true,
	"к":      true,
	"с":      true,
	"у":      true,
	"и":      true,
	"а":      true,
	"но":     true,
	"или":    true,
	"the":    true,
	"is":     true,
	"are":    true,
	"was":    true,
	"were":   true,
	"be":     true,
	"been":   true,
	"being":  true,
	"have":   true,
	"has":    true,
	"had":    true,
	"do":     true,
	"does":   true,
	"did":    true,
	"a":      true,
	"an":     true,
	"of":     true,
	"in":     true,
	"to":     true,
	"for":    true,
	"on":     true,
	"with":   true,
	"at":     true,
	"by":     true,
	"from":   true,
	"it":     true,
	"its":    true,
	"this":   true,
	"that":   true,
	"these":  true,
	"those":  true,
	"find":   true,
	"search": true,
	"look":   true,
	"show":   true,
}

// filterStopWords — удаляет стоп-слова из списка термов.
func filterStopWords(terms []string) []string {
	filtered := make([]string, 0, len(terms))
	for _, t := range terms {
		if !queryStopWords[t] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// countWords — приблизительное число слов в строке.
func countWords(s string) int {
	return len(strings.Fields(s))
}

// ReciprocalRankFusion — объединяет несколько списков результатов (от
// разных формулировок запроса) в один ранжированный список.
//
// RRF: каждый результат получает вес 1/(k + rank), где rank — позиция в
// списке (0-based), k — константа смещения (по умолчанию 60). Вес
// суммируется по всем спискам: результат, встречающийся в нескольких
// выдачах, получает больший итоговый вес.
//
// Результаты без совпадения по chunk_id сливаются: один чанк = один
// результат, даже если найден по нескольким запросам.
func ReciprocalRankFusion(results [][]SearchResult, k int) []SearchResult {
	if len(results) == 1 {
		return results[0]
	}
	if k <= 0 {
		k = 60
	}

	// Суммируем RRF-вес по chunk_id (или по File:Line без chunk_id).
	scores := map[string]float64{}
	resultsMap := map[string]SearchResult{}

	for _, rs := range results {
		seenInList := map[string]bool{}
		for rank, r := range rs {
			key := r.ChunkID
			if key == "" {
				key = fmt.Sprintf("%s:%d", r.File, r.StartLine)
			}
			if seenInList[key] {
				continue // дедуп внутри одного списка
			}
			seenInList[key] = true
			rrf := float64(1) / float64(k+rank)
			scores[key] += rrf
			if _, exists := resultsMap[key]; !exists {
				resultsMap[key] = r
			}
		}
	}

	// Собираем результаты, отсортированные по RRF-весу.
	out := make([]SearchResult, 0, len(scores))
	for key, r := range resultsMap {
		r.Score = float32(scores[key])
		out = append(out, r)
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out
}
