package tools

import (
	"ai/domain"
	"encoding/json"
)

// ParseArguments разбирает JSON-строку аргументов инструмента толерантно к
// типовым дефектам моделей. Реализация принадлежит домену (domain.ParseArguments):
// парсер чистый и нужен провайдерам моделей без зависимости от пакета tools.
func ParseArguments(raw string) (map[string]any, error) {
	return domain.ParseArguments(raw)
}

// unmarshalLikeModel пытается разобрать JSON-значение, приходящее от модели.
// Модели (например, qwen) могут сериализовать массив (files, filenames,
// comments) в JSON-строку — это уже поддерживалось. К моменту, когда аргументы
// попадают в инструмент, один слой экранирования такой строки часто уже снят
// (Ollama structpb + ParseArguments): внутри файла остаются реальные переводы
// строк и кавычки содержимого, поэтому прямой json.Unmarshal не срабатывает.
// Функция возвращает true, если разбор удался хотя бы через один из путей.
func unmarshalLikeModel(raw string, out any) bool {
	if json.Unmarshal([]byte(raw), out) == nil {
		return true
	}
	return json.Unmarshal([]byte(domain.RelaxJSON(raw)), out) == nil
}
