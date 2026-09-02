package codereviewer

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"ai/forges"
)

// hunkHeaderRe разбирает заголовок ханка: "@@ -a,b +c,d @@".
// Вторая группа — стартовый номер строки в новой версии (c).
var hunkHeaderRe = regexp.MustCompile(`@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// diffNewLines строит индекс: путь файла -> множество номеров строк
// в новой версии, реально присутствующих в diff (контекст и добавления).
func diffNewLines(diff string) map[string]map[int]bool {
	index := map[string]map[int]bool{}
	lines := strings.Split(diff, "\n")

	curFile := ""
	nextNew := -1 // следующий ожидаемый номер строки новой версии

	for _, line := range lines {
		if m := fileHeaderRe.FindStringSubmatch(line); m != nil {
			curFile = strings.TrimLeft(m[1], "b/")
			index[curFile] = map[int]bool{}
			nextNew = -1
			continue
		}

		if curFile == "" {
			continue
		}

		// Заголовок ханка задаёт стартовый номер новой строки.
		if m := hunkHeaderRe.FindStringSubmatch(line); m != nil {
			nextNew, _ = strconv.Atoi(m[1])
			continue
		}

		if nextNew < 0 || len(line) == 0 {
			continue
		}

		switch line[0] {
		case ' ', '+':
			// Контекст и добавление — строка существует в новой версии.
			index[curFile][nextNew] = true
			nextNew++
		case '-', '\\':
			// Удалённая строка или "\ No newline at end of file" — нет номера.
		default:
			// Вне ханка — сбрасываем ожидание номера.
			nextNew = -1
		}
	}

	return index
}

// filterCommentsByDiff отбрасывает комментарии, привязанные к файлам или
// строкам, которых нет в diff (защита от галлюцинаций модели).
// Возвращает валидные комментарии и список отклонённых причин.
func filterCommentsByDiff(diff string, comments []forges.ReviewComment) (valid []forges.ReviewComment, rejected []string) {
	index := diffNewLines(diff)

	presentFiles := map[string]bool{}
	for f := range index {
		presentFiles[f] = true
	}

	for _, c := range comments {
		switch {
		case !presentFiles[c.FilePath]:
			rejected = append(rejected, fmt.Sprintf(
				"%s:%d — файл не присутствует в diff", c.FilePath, c.Line))
		case !index[c.FilePath][c.Line]:
			rejected = append(rejected, fmt.Sprintf(
				"%s:%d — строка не присутствует в diff", c.FilePath, c.Line))
		default:
			valid = append(valid, c)
		}
	}

	return valid, rejected
}
