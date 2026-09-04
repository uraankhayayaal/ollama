package codereviewer

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"ai/forges"
)

// Модели (особенно YandexGPT) иногда вместо вызова инструментов пишут
// ревью текстом. Чтобы замечания не терялись, такой текст парсится в
// замечания и публикуется. Несуществующие файлы/строки затем отсекаются
// фильтром по диффу (filterCommentsByDiff), поэтому ложные срабатывания
// парсера безопасны.

// fileBlockRe находит строку "**Файл:** <path>".
var fileBlockRe = regexp.MustCompile(`(?i)\*\*Файл:\*\*\s*` + "`?([^\\n`]+)`?")

// lineNumberRe находит номер строки в абзаце: "**Строка:** 285",
// "**Строка:** ~285", "Строка: 285", "строка 285" и т.п. Устойчив к
// форматированию жирным (**), разделителям и символу "~" перед числом.
var lineNumberRe = regexp.MustCompile(`(?i)строка\D*?(\d+)`)

// textFieldRe находит содержимое текста замечания: "**Текст:** ...".
var textFieldRe = regexp.MustCompile(`(?i)\*\*Текст:\*\*\s*(.+)`)

// reviewCallStartRe находит начало псевдо-вызова инструмента ReviewMr —
// формат, который модели без реальных инструментов (например trim) пишут
// текстом вместо замечаний. Поддерживаются два вида аргументов:
//
//	ReviewMr(file_path="src/DTO.php", line=25, text="для заметки: ...")  — по имени
//	ReviewMr('src/DTO.php', 25, 'для заметки: ...')                      — позиционно
//
// Значения могут быть в одинарных или двойных кавычках, содержать
// экранированные символы, вложенные скобки и кавычки внутри text.
var reviewCallStartRe = regexp.MustCompile(`(?i)ReviewMr\s*\(`)

// reviewCallArgs названных аргументов вызова: file_path/line/text.
var (
	namedFilePathRe = regexp.MustCompile(`(?i)\bfile_path\s*=\s*([^,]+)`)
	namedLineRe     = regexp.MustCompile(`(?i)\bline\s*=\s*(\d+)`)
	namedTextRe     = regexp.MustCompile(`(?i)\btext\s*=\s*(.+)$`)
)

// scanParenArgs возвращает содержимое скобок, начиная с открывающей скобки
// по индексу open, с учётом вложенных скобок и кавычек.
func scanParenArgs(s string, open int) (string, bool) {
	depth := 0
	inStr := byte(0)
	for i := open; i < len(s); i++ {
		c := s[i]
		if inStr != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == inStr {
				inStr = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			inStr = c
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[open+1 : i], true
			}
		}
	}
	return "", false
}

// stripOuterQuotes снимает обрамляющие кавычки со строкового значения и
// раскрывает escape-последовательности (\' \" \\ \n \t \r). Для текста
// берутся первый и последний символы-кавычки, поэтому внутренние кавычки
// (например pluck('id') внутри '...') не мешают.
func stripOuterQuotes(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return strings.Trim(s, "`\t "), true
	}
	first := s[0]
	if first != '\'' && first != '"' {
		return s, true
	}
	last := len(s) - 1
	for last > 1 && (s[last] == ' ' || s[last] == '\t') {
		last--
	}
	if last <= 1 || s[last] != first {
		return "", false
	}
	return unescapeQuoted(first, s[1:last]), true
}

// unescapeQuoted раскрывает escape-последовательности строкового литерала.
// Неизвестный escape оставляется как есть (два символа), чтобы не портить текст.
func unescapeQuoted(delim byte, s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '\\', '\'', '"', '`':
			b.WriteByte(s[i])
		default:
			// Неизвестный escape — возвращаем оба символа без изменений.
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// parseReviewArgs разбирает аргументы одного вызова ReviewMr в замечание.
func parseReviewArgs(args string) (forges.ReviewComment, bool) {
	s := strings.TrimSpace(args)
	if s == "" {
		return forges.ReviewComment{}, false
	}

	var comment forges.ReviewComment

	if namedFilePathRe.MatchString(s) {
		// Названный формат: file_path=..., line=..., text=...
		if m := namedFilePathRe.FindStringSubmatch(s); len(m) > 1 {
			if v, ok := stripOuterQuotes(m[1]); ok {
				comment.FilePath = v
			}
		}
		if m := namedLineRe.FindStringSubmatch(s); len(m) > 1 {
			comment.Line, _ = strconv.Atoi(m[1])
		}
		if m := namedTextRe.FindStringSubmatch(s); len(m) > 1 {
			comment.Text, _ = stripOuterQuotes(m[1])
		}
	} else {
		// Позиционный формат: "path", line, "text".
		rest := s
		// Путь — до первого верхнего уровня запятой.
		path, after := readToken(rest)
		if path == "" || !findComma(after) {
			return forges.ReviewComment{}, false
		}
		comment.FilePath = path
		rest = strings.TrimSpace(after[findCommaIdx(after)+1:])

		// Номер строки — целое число до следующей запятой.
		tok, tail := readToken(rest)
		line, err := strconv.Atoi(strings.TrimPrefix(tok, "~"))
		if err != nil || !findComma(tail) {
			return forges.ReviewComment{}, false
		}
		comment.Line = line
		rest = strings.TrimSpace(tail[findCommaIdx(tail)+1:])

		// Всё остальное — текст замечания.
		comment.Text, _ = stripOuterQuotes(rest)
	}

	if comment.FilePath == "" || comment.Line == 0 || strings.TrimSpace(comment.Text) == "" {
		return forges.ReviewComment{}, false
	}
	return comment, true
}

// readToken читает первый токен списка аргументов: кавычкую строку (с учётом
// экранирования) или голое слово/число до первого разделителя.
func readToken(s string) (value, rest string) {
	s = strings.TrimLeft(s, " \t")
	if s == "" {
		return "", ""
	}
	q := s[0]
	if q == '\'' || q == '"' {
		for i := 1; i < len(s); i++ {
			if s[i] == '\\' {
				i++
				continue
			}
			if s[i] == q {
				// Конец строки: значение внутри кавычек.
				return s[1:i], s[i+1:]
			}
		}
		// Незакрытая строка.
		return s[1:], ""
	}
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			return strings.TrimSpace(s[:i]), s[i:]
		}
	}
	return strings.TrimSpace(s), ""
}

func findComma(s string) bool {
	return strings.IndexByte(s, ',') >= 0
}

func findCommaIdx(s string) int {
	return strings.IndexByte(s, ',')
}

// parseReviewCalls извлекает замечания из псевдо-вызовов ReviewMr(...) в тексте.
// Если вызовов не найдено, возвращает nil.
func parseReviewCalls(content string) []forges.ReviewComment {
	locs := reviewCallStartRe.FindAllStringSubmatchIndex(content, -1)
	if len(locs) == 0 {
		return nil
	}

	var comments []forges.ReviewComment
	for _, loc := range locs {
		// loc[0] — начало "ReviewMr", loc[1] — сразу после "(".
		args, ok := scanParenArgs(content, loc[1]-1)
		if !ok {
			continue
		}
		if c, ok := parseReviewArgs(args); ok {
			comments = append(comments, c)
		}
	}
	if len(comments) == 0 {
		return nil
	}
	return comments
}

// parseTextReview разбирает текстовое ревью в слайс замечаний.
// Ожидаемые форматы:
//
//	1) Псевдо-вызовы ReviewMr(...) — модели без инструментов (trim):
//	   ReviewMr(file_path="core/AuthManager.php", line=285, text="...")
//
//	2) Заголовки вида (пример реального ответа YandexGPT):
//	   **Файл:** `core/.../AuthManager.php`
//	   1. **Строка:** ~285 (в блоке ...)
//	      **Текст:** `критично:` Проверка ...
//
// Возвращает замечания с file_path, line и text.
func parseTextReview(content string) []forges.ReviewComment {
	// Формат псевдо-вызовов ReviewMr(...) — основной для моделей без tools.
	if calls := parseReviewCalls(content); len(calls) > 0 {
		return calls
	}

	lines := strings.Split(content, "\n")
	var result []forges.ReviewComment

	curFile := ""
	pendingLine := 0 // номер строки из абзаца, ожидающий текст замечания

	for _, raw := range lines {
		line := strings.TrimSpace(raw)

		// Определяем текущий файл из заголовка "**Файл:** <path>".
		if m := fileBlockRe.FindStringSubmatch(line); m != nil {
			path := strings.Trim(m[1], "`\"'")
			path = strings.TrimSpace(strings.TrimPrefix(path, "Файл:"))
			if path != "" {
				curFile = strings.TrimSpace(path)
				// Новая секция файла — обнуляем ожидание прошлой строки.
				pendingLine = 0
			}
			continue
		}

		if curFile == "" {
			continue
		}

		// Запоминаем номер строки (если он в этой строке), ещё без текста.
		if m := lineNumberRe.FindStringSubmatch(line); m != nil {
			pendingLine, _ = strconv.Atoi(m[1])
		}

		// Ищем текст замечания (может быть в этой же или следующей строке).
		if m := textFieldRe.FindStringSubmatch(line); m != nil {
			text := strings.Trim(strings.TrimSpace(m[1]), "`")
			if pendingLine > 0 && text != "" {
				result = append(result, forges.ReviewComment{
					FilePath: curFile,
					Line:     pendingLine,
					Text:     text,
				})
				pendingLine = 0
			}
			continue
		}
	}

	return result
}

// PublishParsedReview публикует текстовое ревью, преобразуя его в комментарии
// через стандартный ReviewMr-путь (включая фильтр по diff и дедупек).
// Модели, склонные писать текст вместо вызова инструментов, теряют замечания:
// этот метод спасает результат. Возвращает число опубликованных замечаний.
func (cr *Codereviewer) PublishParsedReview(content string) int {
	if strings.TrimSpace(content) == "" {
		return 0
	}
	comments := parseTextReview(content)
	if len(comments) == 0 {
		return 0
	}
	// Кодируем в JSON-строку, т.к. parseComments принимает этот формат.
	b, err := json.Marshal(comments)
	if err != nil {
		return 0
	}
	// Используем ReviewMr, чтобы применить фильтр по diff и дедупликацию.
	cr.ReviewMr(map[string]any{"comments": string(b)})
	return cr.commentCount
}
