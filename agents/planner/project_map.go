package planner

import (
	"ai/forges"
	"ai/projects"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxMapFiles и maxMapChars ограничивают размер карты проекта, чтобы
// планировщику передавалась компактная сводка структуры, а не всё содержимое.
const (
	maxMapFiles = 200
	maxMapChars = 10_000
)

// BuildProjectMap собирает компактную карту проекта для планировщика:
// список файлов с размерами (без содержимого), распределение языков и
// наличие ключевых манифестов. Помогает планировщику декомпозировать задачу
// на шаги с точным scope и не переполнять контекст чтением всех файлов.
//
// Если проект ещё не создан — возвращает соответствующую пометку.
func BuildProjectMap(projectName string) string {
	dir := projects.ProjectDir(projectName)

	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return "Проект ещё не существует — будет создан с нуля (текущий каталог: " + dir + ")."
	}

	langCount := map[string]int{}
	var files []string // "rel (size B)"
	var total int64
	fileNum := 0

	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == dir {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") || forges.IsIgnoredDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(rel, ".") || strings.Contains(rel, "/.") {
			return nil
		}
		fileNum++
		if fileNum > maxMapFiles {
			return filepath.SkipAll
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		total += info.Size()
		files = append(files, fmt.Sprintf("%s (%d B)", rel, info.Size()))
		if ext := filepath.Ext(rel); ext != "" {
			langCount[strings.TrimPrefix(ext, ".")]++
		}
		return nil
	})
	if walkErr != nil && walkErr != filepath.SkipAll {
		// Не падаем из-за ошибок обхода (могут быть недоступные каталоги).
	}

	var b strings.Builder
	b.WriteString("Текущая структура проекта:\n")
	if len(files) == 0 {
		b.WriteString("  (пустой каталог)\n")
	} else {
		chars := 0
		truncated := false
		for _, f := range files {
			if chars+len(f) > maxMapChars {
				truncated = true
				break
			}
			b.WriteString("  ")
			b.WriteString(f)
			b.WriteString("\n")
			chars += len(f)
		}
		if truncated {
			b.WriteString("  ... (список обрезан по лимиту)\n")
		}
	}

	if len(langCount) > 0 {
		b.WriteString("Языки по расширениям: ")
		exts := make([]string, 0, len(langCount))
		for ext := range langCount {
			exts = append(exts, ext)
		}
		sort.Strings(exts)
		for i, ext := range exts {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, ".%s=%d", ext, langCount[ext])
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "Всего файлов: ~%d, суммарный размер: ~%d Б.\n", fileNum, total)
	return b.String()
}
