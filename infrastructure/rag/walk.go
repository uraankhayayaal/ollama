// Обход дерева проекта для индексации (Ф-2) и частичной переиндексации (Ф-5).
//
// Правила отбора файлов повторяют политику контекста ревью/List: исключаются
// служебные каталоги (.git, node_modules, build и т.п. — forges.IsIgnoredDir),
// скрытые файлы, бинарные файлы (NUL-байт в префиксе) и записи .gitignore
// (с не-typical поддержкой «!»-инверсии и каталогов/шаблонов).

package rag

import (
	"ai/forges"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// maxIndexFileSize — файлы крупнее этого объёма не читаются целиком и
// пропускаются индексатором (защита от утечки памяти на вендоренных блоках).
const maxIndexFileSize = 2_000_000

// binaryProbeBytes — сколько байт префикса проверяется на NUL для отсева
// бинарных файлов.
const binaryProbeBytes = 1024

// WalkProject обходит дерево проекта и возвращает относительные slash-пути
// текстовых исходников для индексации (отсортировано, детерминированно).
func WalkProject(dir string) ([]string, error) {
	gm, err := collectGitignores(dir)
	if err != nil {
		return nil, err
	}

	var out []string
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Недоступные каталоги не роняют обход (как и карта проекта).
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
			if gm.Match(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}

		// Скрытые файлы (.env, .gitignore, dot-файлы) не индексируются.
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if gm.Match(rel, false) {
			return nil
		}

		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if info.Size() > maxIndexFileSize || info.Size() == 0 {
			return nil
		}
		if isBinary(filepath.Join(dir, filepath.FromSlash(rel))) {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// isBinary определяет по NUL-байту в binaryProbeBytes префикса, бинарный ли
// файл. Текстовые кодировки (UTF-8/UTF-16/ASCII) NUL в префиксе не содержат.
func isBinary(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	buf := make([]byte, binaryProbeBytes)
	n, _ := f.Read(buf)
	for _, b := range buf[:n] {
		if b == 0 {
			return true
		}
	}
	return false
}

// gitignoreMatcher — набор .gitignore-файлов, собранных по дереву проекта.
type gitignoreMatcher struct {
	files []gitignoreFile
}

// Match проверяет, игнорируется ли путь (dir — признак каталога). Каждый
// .gitignore применяется только к путям внутри своего корня.
func (m *gitignoreMatcher) Match(rel string, dir bool) bool {
	for _, gi := range m.files {
		if !inside(rel, gi.root) {
			continue
		}
		if gi.match(rel, dir) {
			return true
		}
	}
	return false
}

// inside проверяет, что rel находится внутри root (или равен ему).
// Пустой root — корневой .gitignore, применяется ко всему дереву.
func inside(rel, root string) bool {
	if root == "" {
		return true
	}
	return rel == root || strings.HasPrefix(rel, root+"/")
}

// gitignoreFile — один .gitignore с нормализованными шаблонами.
type gitignoreFile struct {
	root     string // каталог, где лежит файл (slash-путь от корня проекта)
	patterns []gitPattern
}

// gitPattern — скомпилированный шаблон .gitignore.
type gitPattern struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

// match проверяет путь по шаблонам файла: сработавшая «!»-инверсия отменяет
// игнор, обычное совпадение — игнорирует.
func (g gitignoreFile) match(rel string, dir bool) bool {
	rel = strings.TrimPrefix(rel, g.root)
	rel = strings.Trim(rel, "/")
	ignored := false
	for _, p := range g.patterns {
		if p.dirOnly && !dir {
			continue
		}
		if p.re.MatchString(rel) {
			ignored = !p.negate
		}
	}
	return ignored
}

// collectGitignores находит все .gitignore в дереве и парсит их шаблоны.
func collectGitignores(dir string) (*gitignoreMatcher, error) {
	m := &gitignoreMatcher{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			rel, rerr := filepath.Rel(dir, path)
			if rerr == nil && rel != "." && (strings.HasPrefix(d.Name(), ".") || forges.IsIgnoredDir(d.Name())) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != ".gitignore" {
			return nil
		}
		root, _ := filepath.Rel(dir, filepath.Dir(path))
		root = filepath.ToSlash(root)
		if root == "." {
			root = ""
		}
		gi, perr := parseGitignore(path, root)
		if perr == nil && len(gi.patterns) > 0 {
			m.files = append(m.files, gi)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

// parseGitignore читает и компилирует один файл .gitignore.
func parseGitignore(path, root string) (gitignoreFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return gitignoreFile{}, err
	}
	gi := gitignoreFile{root: root}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := gitPattern{}
		if strings.HasPrefix(line, "!") {
			p.negate = true
			line = strings.TrimSpace(line[1:])
		}
		if strings.HasSuffix(line, "/") {
			p.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		if line == "" {
			continue
		}
		re, rerr := gitPatternRe(line)
		if rerr != nil {
			continue
		}
		p.re = re
		gi.patterns = append(gi.patterns, p)
	}
	// Порядок важен: последний сработавший шаблон побеждает.
	return gi, nil
}

// gitPatternRe превращает glob-шаблон .gitignore в регулярное выражение.
// Шаблон со слэшем — относительный путь (анкор). Без слэша — компонент имени
// на любой глубине. Поддерживаются * (не через /), ? и **.
func gitPatternRe(pattern string) (*regexp.Regexp, error) {
	anchored := strings.Contains(pattern, "/")
	var b strings.Builder
	if anchored {
		b.WriteString("^")
	} else {
		b.WriteString("(?:^|.*/)")
	}
	parts := strings.Split(pattern, "/")
	for i, part := range parts {
		if i > 0 {
			b.WriteString("/")
		}
		compileGlobPart(&b, part)
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// compileGlobPart переводит один сегмент шаблона: "**" — любые пути,
// "*" — любые символы кроме "/", "?" — один символ.
func compileGlobPart(b *strings.Builder, part string) {
	if part == "**" {
		b.WriteString(".*")
		return
	}
	for _, r := range part {
		switch r {
		case '*':
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '^', '$', '[', ']', '\\', '{', '}':
			b.WriteRune('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
}