package developer

import (
	"ai/forges"
	"ai/logging"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Finalize пишет в OutputDir файл-отчёт SUMMARY.md (если включено конфигом)
// со структурой проекта после работы разработчика. Вызывается из main.go после
// цикла генерации/саморемонта, заполняя отчёт реально созданными файлами.
func (d *base) Finalize() {
	if d.Config.SummaryFile == "" {
		return
	}

	full, err := d.ResolvePath(d.Config.SummaryFile)
	if err != nil {
		return
	}

	lines := []string{
		"# Проект",
		"",
		"- Язык: " + d.Config.Language,
		"- Задание: " + d.Prompt,
		"",
		"## Файлы",
		"",
	}

	_ = filepath.Walk(d.OutputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(d.OutputDir, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			// Не спускаемся в зависимости/билды/кеши (node_modules и т.п.).
			if rel != "." && (strings.HasPrefix(info.Name(), ".") || forges.IsIgnoredDir(info.Name())) {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == d.Config.SummaryFile {
			return nil
		}
		lines = append(lines, "- "+rel)
		return nil
	})

	content := strings.Join(lines, "\n") + "\n"
	_ = os.WriteFile(full, []byte(content), 0644)
	logging.Infof("[%s] написан отчёт: %s", d.label, full)

	// Гарантируем наличие README.md с инструкциями по установке, запуску и
	// использованию. Если модель уже создала его — не трогаем (не перезапишем).
	d.EnsureREADME()
}

// EnsureREADME создаёт README.md в OutputDir, если он ещё не существует,
// с инструкцией по установке, запуску и использованию, собранной из
// реально созданных файлов. Если README уже есть (его написала модель) —
// файл не перезаписывается, чтобы не портить авторский текст.
func (d *base) EnsureREADME() {
	const name = "README.md"

	full, err := d.ResolvePath(name)
	if err != nil {
		return
	}
	if _, err := os.Stat(full); err == nil {
		// README уже есть (например, его создала модель) — не трогаем.
		logging.Detailf("[%s] README.md уже существует, пропускаю", d.label)
		return
	}

	files := d.listProjectFiles()
	content := d.buildReadme(files)
	if content == "" {
		return
	}
	_ = os.WriteFile(full, []byte(content), 0644)
	logging.Infof("[%s] README.md создан: %s", d.label, full)
}

// listProjectFiles возвращает относительные пути всех файлов OutputDir
// (рекурсивно), кроме собственных README/SUMMARY, отсортированные.
func (d *base) listProjectFiles() []string {
	var out []string
	_ = filepath.Walk(d.OutputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(d.OutputDir, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			// Пропускаем зависимости/билды/кеши (node_modules и т.п.).
			if rel != "." && (strings.HasPrefix(info.Name(), ".") || forges.IsIgnoredDir(info.Name())) {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == "README.md" || rel == d.Config.SummaryFile {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out
}

// buildReadme собирает текст README.md из имеющихся файлов: определяет
// модуль и точку входа (main.go, cmd/), Go-версию, наличие тестов и
// формирует разделы "Установка", "Запуск", "Использование".
func (d *base) buildReadme(files []string) string {
	if len(files) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("# Проект\n\n")
	b.WriteString("Проект создан агентом-разработчиком.\n\n")

	module := d.Config.Module
	goVersion := d.goVersionFromFiles(files)
	hasTests := hasGoTests(files)

	if module != "" {
		fmt.Fprintf(&b, "- Модуль: `%s`\n", module)
	}
	if goVersion != "" {
		fmt.Fprintf(&b, "- Go: `%s`\n", goVersion)
	}
	if hasTests {
		b.WriteString("- Тесты: есть\n")
	}

	b.WriteString("\n## Установка\n\n")
	if module != "" || goVersion != "" {
		b.WriteString("```bash\ngo mod download\n```\n\n")
	}
	if goVersion != "" {
		fmt.Fprintf(&b, "Требуется Go %s или новее.\n\n", goVersion)
	}

	b.WriteString("## Запуск\n\n")
	if mainPath := findMainGo(files); mainPath != "" {
		fmt.Fprintf(&b, "```bash\ngo run %s\n```\n\n", mainPath)
	} else {
		b.WriteString("```bash\ngo run .\n```\n\n")
	}

	if hasTests {
		b.WriteString("## Тесты\n\n```bash\ngo test ./...\n```\n\n")
	}

	b.WriteString("## Использование\n\n")
	if mainPath := findMainGo(files); mainPath != "" {
		fmt.Fprintf(&b, "После запуска (`go run %s`) программа выполнит основной сценарий из задания.\n", mainPath)
	} else if module != "" {
		fmt.Fprintf(&b, "Публичные пакеты/функции проекта используются через импорт: `import %q/…`.\n", module)
	} else {
		b.WriteString("Публичные пакеты/функции проекта используются через импорт из корня модуля.\n")
	}

	b.WriteString("\n## Структура\n\n```\n")
	for _, f := range files {
		fmt.Fprintf(&b, "%s\n", f)
	}
	b.WriteString("```\n")

	return b.String()
}

// findMainGo возвращает путь к файлу point-of-entry (main.go или первый
// файл под cmd/), подходящий для команды "go run". Иначе "".
func findMainGo(files []string) string {
	if len(files) == 0 {
		return ""
	}
	// Отдаём предпочтение корневому main.go.
	for _, f := range files {
		if f == "main.go" || f == "./main.go" {
			return "main.go"
		}
	}
	for _, f := range files {
		if filepath.Base(f) == "main.go" {
			return f
		}
	}
	return ""
}

// hasGoTests сообщает, содержит ли список файлов Go-тесты (*_test.go).
func hasGoTests(files []string) bool {
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			return true
		}
	}
	return false
}

// goVersionFromFiles ищет строку "go x.y.z" в go.mod и возвращает её,
// иначе "".
func (d *base) goVersionFromFiles(files []string) string {
	for _, f := range files {
		if filepath.Base(f) != "go.mod" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(d.OutputDir, f))
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "go ") {
				return strings.TrimPrefix(line, "go ")
			}
		}
	}
	return ""
}
