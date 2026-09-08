package tools

import "encoding/json"

// ParseArguments разбирает JSON-строку аргументов в map.
func ParseArguments(raw string) (map[string]any, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, err
	}
	return args, nil
}

// unmarshalLikeModel пытается разобрать JSON-значение, приходящее от модели.
// Модели (например, qwen) могут сериализовать массив (files, filenames, comments)
// в JSON-строку — это уже поддерживалось. К моменту, когда аргументы попадают в
// инструмент, один слой экранирования такой строки часто уже снят (Ollama
// structpb + ParseArguments): внутри файла остаются реальные переводы строк и
// кавычки содержимого, поэтому прямой json.Unmarshal не срабатывает. Функция
// возвращает true, если разбор удался хотя бы через один из путей.
func unmarshalLikeModel(raw string, out any) bool {
	return json.Unmarshal([]byte(raw), out) == nil
}
