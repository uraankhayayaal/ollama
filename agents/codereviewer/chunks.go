package codereviewer

import (
	"fmt"
	"regexp"
	"strings"

	"ai/forges"
)

// splitDiffChunks разбивает diff на чанки так, чтобы не разрезать ханк
// по середине (целый файл всегда попадает в один чанк), а суммарный размер
// каждого чанка не превышал примерно maxChars. Возвращает один элемент,
// если дифф меньше лимита.
func splitDiffChunks(diff string, maxChars int) []string {
	if maxChars <= 0 || len(diff) <= maxChars {
		return []string{diff}
	}

	// Границей файла служит строка "diff --git ". Каждый блок — от неё
	// до следующей (заголовок + ханки одного файла).
	lines := strings.Split(diff, "\n")
	diffGitRe := regexp.MustCompile(`^diff --git `)

	// Файлы: каждый элемент — строки одного файла.
	var files [][]string
	cur := []string{}
	for _, line := range lines {
		if diffGitRe.MatchString(line) && len(cur) > 0 {
			files = append(files, cur)
			cur = []string{}
		}
		cur = append(cur, line)
	}
	if len(cur) > 0 {
		files = append(files, cur)
	}

	// Собираем файлы в чанки по суммарному размеру.
	var chunks []string
	var buf []string
	size := 0
	flush := func() {
		chunks = append(chunks, strings.Join(buf, "\n"))
		buf = nil
		size = 0
	}

	for _, f := range files {
		block := strings.Join(f, "\n")
		if len(buf) > 0 && size+len(block) > maxChars {
			flush()
		}
		buf = append(buf, f...)
		size += len(block)
	}
	if len(buf) > 0 {
		flush()
	}

	return chunks
}

// chunkLabel возвращает подпись чанка вида "1 из 3" или "" для одного чанка.
func chunkLabel(idx, total int) string {
	if total <= 1 {
		return ""
	}
	return fmt.Sprintf("%d из %d", idx+1, total)
}

// commentKey формирует сигнатуру замечания: уникальная локация + текст.
// Дедуп идёт в первую очередь по локации (файл:строка); текст нормализуем,
// чтобы повторное размещение того же текста на той же строке отбрасывалось.
func commentKey(c forges.ReviewComment) string {
	text := strings.ToLower(strings.TrimSpace(c.Text))
	text = strings.TrimPrefix(text, "критично:")
	text = strings.Join(strings.Fields(text), " ")
	return fmt.Sprintf("%s:%d|%s", c.FilePath, c.Line, text)
}

// dedupComments оставляет одно замечание на каждую уникальную локацию
// (файл:строка) — защита от того, чтобы модель повторно публиковала одно и
// то же замечание в одном месте (в т.ч. с отличающимся регистром). Разные
// строки сохраняются, даже если текст совпадает: полезно отмечать одну и ту
// же проблему в нескольких местах. Порядок сохраняется. seen — постоянная
// карта, накапливаемая между раундами ревью.
func dedupComments(comments []forges.ReviewComment, seen map[string]bool) []forges.ReviewComment {
	out := make([]forges.ReviewComment, 0, len(comments))
	for _, c := range comments {
		key := commentKey(c)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}
