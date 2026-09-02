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

// parseTextReview разбирает текстовое ревью в слайс замечаний.
// Ожидаемый формат (пример реального ответа YandexGPT):
//
//	**Файл:** `core/.../AuthManager.php`
//	1. **Строка:** ~285 (в блоке ...)
//	   **Текст:** `критично:` Проверка ...
//
// Возвращает замечания с file_path, line и text.
func parseTextReview(content string) []forges.ReviewComment {
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
