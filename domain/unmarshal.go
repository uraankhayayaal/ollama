package domain

import (
	"encoding/json"
	"strings"
)

// UnmarshalBacklog разбирает JSON-ответ/аргументы Системного архитектора
// (backlog с полем tasks) в Backlog. Устойчив к markdown-обёрткам и типовому
// мусору модели (экранирование строк внутри текста).
func UnmarshalBacklog(data string) (*Backlog, error) {
	data = extractJSON(data)
	var b Backlog
	if err := json.Unmarshal([]byte(sanitizeJSON(data)), &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// UnmarshalTasks разбирает JSON-ответ лида направления (декомпозиция с полем
// tasks) в список задач. Лиды используют разные корневые ключи
// (frontend_lead_summary, backend_lead_summary и т.п.), поэтому разбираем их
// толерантно, извлекая только массив tasks.
func UnmarshalTasks(data string) ([]TaskSpec, error) {
	root := struct {
		Tasks []TaskSpec `json:"tasks"`
	}{}
	// Объект-обёртка проходит только если в нём есть реальные задачи: объекты
	// без ключа "tasks" (например, элемент декомпозиции без обёртки) парсятся
	// lenient'но в пустой список, и такой ответ не должен считаться успехом.
	if err := json.Unmarshal([]byte(sanitizeJSON(extractJSON(data))), &root); err == nil && len(root.Tasks) > 0 {
		return root.Tasks, nil
	}
	// Некоторые модели возвращают голый массив задач вместо объекта
	// {"tasks":[...]} — пробуем извлечь JSON-массив от первой "[" до
	// последней "]" и разобрать задачи напрямую.
	if arr := extractJSONArray(data); arr != nil {
		var tasks []TaskSpec
		if err := json.Unmarshal(arr, &tasks); err == nil && len(tasks) > 0 {
			return tasks, nil
		}
	}
	// Оба варианта не прошли — возвращаем ошибку разбора объекта
	// (для диагностики модели), а не отвлечённую ошибку второго шага.
	rootErr := json.Unmarshal([]byte(sanitizeJSON(extractJSON(data))), &root)
	return nil, rootErr
}

// extractJSON вырезает из текста модели первый JSON-объект (открывающаяся
// '{' до последней '}'), отбрасывая markdown-обёртки и лишний текст.
func extractJSON(s string) string {
	start := -1
	for i, c := range s {
		if c == '{' {
			start = i
			break
		}
	}
	if start < 0 {
		return s
	}
	end := -1
	for i := len(s) - 1; i >= start; i-- {
		if s[i] == '}' {
			end = i
			break
		}
	}
	if end < 0 {
		return s
	}
	return s[start : end+1]
}

// extractJSONArray вырезает из текста модели первый JSON-массив (от первой
// '[' до последней ']'), отбрасывая markdown-обёртки и лишний текст. Возвращает
// nil, если в тексте нет символа '['.
func extractJSONArray(s string) []byte {
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '[' {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	end := -1
	for i := len(s) - 1; i >= start; i-- {
		if s[i] == ']' {
			end = i
			break
		}
	}
	if end < 0 {
		return nil
	}
	return []byte(sanitizeJSON(s[start : end+1]))
}

// sanitizeJSON чинит типовые ошибки модели: реальные управляющие символы внутри
// JSON-строк (\n, \r) экранируются, вне строк \r убирается (аналог логики
// планировщика).
func sanitizeJSON(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inString := false
	escaped := false

	for _, r := range s {
		if inString {
			switch {
			case escaped:
				b.WriteRune(r)
				escaped = false
			case r == '\\':
				b.WriteRune(r)
				escaped = true
			case r == '"':
				b.WriteRune(r)
				inString = false
			case r < 0x20:
				writeUnicodeEscape(&b, r)
			default:
				b.WriteRune(r)
			}
			continue
		}
		switch r {
		case '"':
			b.WriteRune(r)
			inString = true
		case '\r':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// writeUnicodeEscape записывает r в \uXXXX-форме.
func writeUnicodeEscape(b *strings.Builder, r rune) {
	const hex = "0123456789abcdef"
	b.WriteString(`\u`)
	for shift := 12; shift >= 0; shift -= 4 {
		b.WriteByte(hex[(r>>shift)&0xf])
	}
}
