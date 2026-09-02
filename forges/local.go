package forges

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// KindLocal — локальная директория как «система ревью».
// Используется для self-review сгенерированного кодом проекта: дифф
// строится из файлов директории, а комментарии/сводка/апрув лишь
// накапливаются в памяти и печатаются (реального хостинга нет).
const (
	KindLocal kind = "local"

	// localSchemeLegacy — старый способ адресации, оставлен для совместимости.
	localSchemeLegacy = "local://"
)

// RegisterLocal регистрирует строителя локального провайдера.
func init() {
	Register(KindLocal, func(prURL string, token string) (Forge, error) {
		dir := strings.TrimPrefix(prURL, localSchemeLegacy)
		if dir == "" {
			return nil, fmt.Errorf("не указана директория для локального ревью")
		}
		return NewLocalForge(dir)
	})
}

// LocalForge — реализация Forge для локальной директории с кодом.
// Не требует сети: дифф строится по содержимому файлов, а публикация
// комментариев записывается в слайс Published.
type LocalForge struct {
	Dir       string
	Published []ReviewComment
	Summary   string
	Approved  bool
	expanded  map[string]string
}

// NewLocalForge создаёт LocalForge для директории.
func NewLocalForge(dir string) (*LocalForge, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("директория не существует: %s", abs)
	} else if !st.IsDir() {
		return nil, fmt.Errorf("не директория: %s", abs)
	}
	return &LocalForge{Dir: abs}, nil
}

// GetDiff строит единый унифицированный diff по всем файлам директории,
// считая их новыми (весь файл — новая версия). Возвращает его как текст.
func (lf *LocalForge) GetDiff() (string, error) {
	files, err := lf.listFiles()
	if err != nil {
		return "", err
	}

	var b strings.Builder
	for _, rel := range files {
		content, err := os.ReadFile(filepath.Join(lf.Dir, rel))
		if err != nil {
			continue
		}
		b.WriteString("diff --git a/")
		b.WriteString(rel)
		b.WriteString(" b/")
		b.WriteString(rel)
		b.WriteString("\n--- a/")
		b.WriteString(rel)
		b.WriteString("\n+++ b/")
		b.WriteString(rel)
		b.WriteString("\n@@ -0,0 +1,")
		fmt.Fprintf(&b, "%d", countLines(content))
		b.WriteString(" @@\n")
		for _, line := range strings.Split(string(content), "\n") {
			if line == "" {
				continue
			}
			b.WriteString("+")
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// PostComment накапливает комментарий к строке локального кода.
func (lf *LocalForge) PostComment(c ReviewComment) error {
	lf.Published = append(lf.Published, c)
	fmt.Printf("[LocalForge] замечание %s:%d: %s\n", c.FilePath, c.Line, c.Text)
	return nil
}

// PostSummary сохраняет сводку.
func (lf *LocalForge) PostSummary(summary string) error {
	lf.Summary = summary
	fmt.Printf("[LocalForge] сводка:\n%s\n", summary)
	return nil
}

// Approve фиксирует одобрение.
func (lf *LocalForge) Approve(summary string) error {
	lf.Approved = true
	fmt.Printf("[LocalForge] апрув: %s\n", summary)
	return nil
}

// listFiles возвращает отсортированный список относительных путей всех
// файлов в директории (рекурсивно), исключая скрытые и служебные.
func (lf *LocalForge) listFiles() ([]string, error) {
	var out []string
	err := filepath.WalkDir(lf.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(lf.Dir, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(path, ".") || strings.Contains(rel, "/.") {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out, err
}

func countLines(b []byte) int {
	n := 0
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		n++
	}
	return n
}
