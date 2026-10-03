package tools

import (
	"encoding/json"
	"strings"
)

// ParseArguments разбирает JSON-строку аргументов в map.
//
// Разбор ТОЛЕРАНТНЫЙ к типичным дефектам, которые выдают локальные модели:
// висячие запятые, одинарные кавычки, неэкранированные переводы строк внутри
// строк, «мусор» после объекта (```-блок, пояснение после JSON). Раньше любой
// такой дефект убивал весь запуск («invalid character ']' looking for beginning
// of value» в логах оркестратора), то есть харнес ломался из-за ответа модели.
func ParseArguments(raw string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}, nil
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(trimmed), &args); err == nil {
		return args, nil
	}

	// 1. Вырезаем первый сбалансированный объект (убирает ```-обёртку и текст
	// после JSON), затем чиним синтаксис.
	if obj := balancedJSONObject(trimmed); obj != "" {
		if err := json.Unmarshal([]byte(relaxJSON(obj)), &args); err == nil {
			return args, nil
		}
	}

	// 2. Чиним синтаксис целиком (может пригодиться разбор не-объекта).
	if err := json.Unmarshal([]byte(relaxJSON(trimmed)), &args); err == nil {
		return args, nil
	}

	// 3. Отдаём исходную ошибку разбора — вызывающий код сообщит модели, что
	// нужно повторить вызов с валидным JSON.
	return nil, json.Unmarshal([]byte(trimmed), &args)
}

// balancedJSONObject возвращает первый сбалансированный {...}-фрагмент,
// учитывая кавычки и экранирование. Пустая строка — объекта нет.
func balancedJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\' && inString:
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
			// пропускаем содержимое строки
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// relaxJSON приводит почти-JSON к валидному JSON одним проходом:
//   - висячие запятые перед } и ] удаляются;
//   - одинарные кавычки вне строк становятся двойными;
//   - «сырые» управляющие символы внутри строк экранируются;
//   - «умные» двойные кавычки приводятся к ASCII (модели часто присылают “ ”).
func relaxJSON(s string) string {
	out := make([]byte, 0, len(s))
	inString := false
	escaped := false

	for i := 0; i < len(s); i++ {
		c := s[i]

		if inString {
			switch {
			case escaped:
				out = append(out, c)
				escaped = false
			case c == '\\':
				out = append(out, c)
				escaped = true
			case c == '"':
				out = append(out, c)
				inString = false
			case c == '\n' || c == '\r':
				// Неэкранированный перевод строки внутри строки — модель
				// «съела» экранирование; экранируем сами.
				out = append(out, '\\', 'n')
			case c == '\t':
				out = append(out, '\\', 't')
			case c < 0x20:
				out = append(out, escapeControl(c)...)
			default:
				out = append(out, c)
			}
			continue
		}

		// Вне строки.
		switch c {
		case '"':
			inString = true
			out = append(out, c)
		case '\'':
			out = append(out, '"')
		case '\n', '\r', '\t':
			out = append(out, ' ')
		case '}', ']':
			out = trimTrailingComma(out)
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return normalizeSmartQuotes(string(out))
}

// trimTrailingComma убирает висячую запятую (с возможными хвостовыми
// пробелами) перед закрывающей скобкой.
func trimTrailingComma(out []byte) []byte {
	end := len(out)
	for end > 0 && (out[end-1] == ' ' || out[end-1] == '\t') {
		end--
	}
	if end > 0 && out[end-1] == ',' {
		return append(out[:end-1], out[end:]...)
	}
	return out
}

// escapeControl возвращает \u00XX для управляющего байта.
func escapeControl(c byte) string {
	const hex = "0123456789abcdef"
	return `\u00` + string([]byte{hex[c>>4], hex[c&0x0f]})
}

// normalizeSmartQuotes заменяет типографские двойные кавычки на ASCII —
// иначе JSON не разбирается, а модель получает отказ вместо результата.
// Одинарные «умные» кавычки трогать нельзя: они легитимны внутри строк.
func normalizeSmartQuotes(s string) string {
	return strings.NewReplacer("“", `"`, "”", `"`).Replace(s)
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
	return json.Unmarshal([]byte(relaxJSON(raw)), out) == nil
}
