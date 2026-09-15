package tools

// Универсальный текстовый механизм SEARCH/REPLACE (работает с любым языком:
// Go, TypeScript/TSX/JSX, CSS). Субагент-разработчик возвращает точные куски
// существующего кода (SEARCH) и их замену (REPLACE). Бэкенд применяет замену
// ТОЛЬКО если SEARCH найден в файле дословно (один-в-один, включая отступы и
// переводы строк). Несовпадение — ошибка без записи: модель не может «тихо»
// переписать код, которого нет, или удалить чужой функционал.
//
// В отличие от семантического PatchGoFunction здесь не требуется, чтобы искомый
// фрагмент был функцией целиком — можно править отдельную строку, блок JSX,
// хук, пропсы компонента и т.п. (то, что не выражается узлом *ast.FuncDecl).

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// SearchReplacePatch — один блок замены: дословный поисковый фрагмент и замена.
type SearchReplacePatch struct {
	// Search — ТОЧНЫЙ кусок существующего кода (включая все табы/пробелы/переводы).
	Search string `json:"search"`
	// Replace — изменённый/новый код на место фрагмента Search.
	Replace string `json:"replace"`
}

// SearchReplaceFile — пакет патчей для одного файла.
type SearchReplaceFile struct {
	Filename string `json:"filename"`
	// Patches — упорядоченный список замен; применяются последовательно.
	Patches []SearchReplacePatch `json:"patches"`
	// Content — резервное поле: если модель передала «сырой» текст блоков
	// <<<<<<< SEARCH ... ======= ... >>>>>>> REPLACE целиком, он разбирается
	// на блоки автоматически (см. parseSearchReplaceBlocks).
	Content string `json:"content"`
}

// searchReplaceBlockRe выделяет блоки вида:
//
//	<<<<<<< SEARCH
//	<код из файла>
//	=======
//	<код-замена>
//	>>>>>>> REPLACE
var searchReplaceBlockRe = regexp.MustCompile(
	`(?s)<<<<<<< SEARCH[ \t]*\r?\n(.*?)\r?\n=======[ \t]*\r?\n(.*?)\r?\n>>>>>>> REPLACE[ \t]*`)

// parseSearchReplaceBlocks разбирает «сырой» текст блоков SEARCH/REPLACE,
// который модель иногда возвращает как один кусок, в список патчей.
func parseSearchReplaceBlocks(raw string) []SearchReplacePatch {
	ms := searchReplaceBlockRe.FindAllStringSubmatch(raw, -1)
	patches := make([]SearchReplacePatch, 0, len(ms))
	for _, m := range ms {
		patches = append(patches, SearchReplacePatch{Search: m[1], Replace: m[2]})
	}
	return patches
}

// ApplySearchReplace применяет упорядоченные блоки SEARCH/REPLACE к
// содержимому файла и возвращает итоговый текст. Каждый SEARCH обязан
// встретиться в текущем тексте дословно; заменяется СТРОГО первое вхождение,
// чтобы не сломать одинаковые конструкции в других местах.
//
// Ключевое свойство безопасности: если хоть один SEARCH не найден — вся
// операция завершается ошибкой, и вызывающий код НЕ записывает файл.
func ApplySearchReplace(content string, patches []SearchReplacePatch) (string, error) {
	if len(patches) == 0 {
		return "", errors.New("список патчей пуст: передай хотя бы один блок search/replace")
	}

	// Нормализуем переводы строк, чтобы поиск одинаково работал на CRLF/LF.
	content = strings.ReplaceAll(content, "\r\n", "\n")

	for i, p := range patches {
		if p.Search == "" {
			return "", fmt.Errorf("патч %d: search пуст — нельзя заменять пустой фрагмент", i+1)
		}
		search := strings.ReplaceAll(p.Search, "\r\n", "\n")
		replace := strings.ReplaceAll(p.Replace, "\r\n", "\n")
		idx := strings.Index(content, search)
		if idx < 0 {
			snippet := search
			if len(snippet) > 120 {
				snippet = snippet[:120] + "..."
			}
			return "", fmt.Errorf(
				"SEARCH-блок %d не найден в файле дословно (агент пытается править код, которого нет, либо переписывает чужой функционал). Искомый фрагмент: %q",
				i+1, snippet)
		}
		content = content[:idx] + replace + content[idx+len(search):]
	}
	return content, nil
}

// parseSearchReplaceFiles нормализует поле "files" инструмента SearchReplace.
// Модели передают массив по-разному (прямой массив, JSON-строка, []byte) —
// приводим все формы к []SearchReplaceFile, как parseFileItems для WriteFiles.
func parseSearchReplaceFiles(raw any) []SearchReplaceFile {
	switch v := raw.(type) {
	case string:
		var items []SearchReplaceFile
		if json.Unmarshal([]byte(v), &items) == nil {
			return completeSRPatches(items)
		}
	case []byte:
		return parseSearchReplaceFiles(string(v))
	default:
		if b, err := json.Marshal(v); err == nil {
			var items []SearchReplaceFile
			if json.Unmarshal(b, &items) == nil {
				return completeSRPatches(items)
			}
		}
	}
	return nil
}

// completeSRPatches доводит файлы до готовности: если patches пуст, а
// content (сырые блоки <<<<<<< SEARCH...>>>>>>> REPLACE) передан — разбираем
// его в блоки замены.
func completeSRPatches(items []SearchReplaceFile) []SearchReplaceFile {
	for i := range items {
		if len(items[i].Patches) == 0 && strings.TrimSpace(items[i].Content) != "" {
			items[i].Patches = parseSearchReplaceBlocks(items[i].Content)
		}
	}
	return items
}
