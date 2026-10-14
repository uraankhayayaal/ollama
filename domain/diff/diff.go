// Разбор unified-диффа git на per-file блоки: метаданные (путь, статус,
// +/-) и патч конкретного файла. Никаких git-команд и файловой системы —
// только строки (stdlib-only), поэтому пакет безопасен для домена.
//
// Зачем отдельный слой: полный `git diff <base>` может быть огромным, а Web UI
// нужен список файлов с метаданными и патч конкретного файла — по запросу. Здесь
// разбор, который кэшируется сервером; сам git-вызов и кэш — в server.
package diff

import (
	"path/filepath"
	"strconv"
	"strings"
)

// Status — тип изменения файла в диффе.
type Status string

const (
	Added     Status = "added"
	Modified  Status = "modified"
	Removed   Status = "removed"
	Renamed   Status = "renamed"
	Submodule Status = "submodule"
)

// FileEntry — один файл в диффе (метаданные, без патча).
type FileEntry struct {
	Path    string `json:"path"`
	Status  Status `json:"status"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
}

// Diff — разобранный unified-дифф: метаданные файлов и патчи по путям.
type Diff struct {
	Files []FileEntry
	File  map[string]string
}

// Parse разбирает вывод `git diff <base>` на per-file блоки.
func Parse(raw string) *Diff {
	d := &Diff{File: map[string]string{}}
	for _, b := range Split(raw) {
		if strings.TrimSpace(b) == "" {
			continue
		}
		f, patch := ParseBlock(b)
		if f == nil {
			continue
		}
		d.Files = append(d.Files, *f)
		d.File[f.Path] = patch
	}
	return d
}

// Split делит unified-дифф на блоки по строкам `diff --git`.
func Split(raw string) []string {
	lines := strings.Split(raw, "\n")
	var blocks []string
	var cur []string
	for _, ln := range lines {
		if strings.HasPrefix(ln, "diff --git ") || strings.HasPrefix(ln, "diff --cc ") {
			if len(cur) > 0 {
				blocks = append(blocks, strings.Join(cur, "\n"))
			}
			cur = []string{ln}
			continue
		}
		cur = append(cur, ln)
	}
	if len(cur) > 0 {
		blocks = append(blocks, strings.Join(cur, "\n"))
	}
	return blocks
}

// ParseBlock разбирает блок одного файла: метаданные и патч. Возвращает nil,
// если путь файла не удалось определить (заголовок без `---`/`+++`).
func ParseBlock(block string) (*FileEntry, string) {
	f := &FileEntry{Status: Modified}
	lines := strings.Split(block, "\n")

	var old, new string
	for _, ln := range lines {
		switch {
		case strings.HasPrefix(ln, "--- "):
			old = PathOf(ln[4:])
		case strings.HasPrefix(ln, "+++ "):
			new = PathOf(ln[4:])
		}
		if strings.HasPrefix(ln, "rename to ") {
			f.Path = strings.TrimSpace(ln[len("rename to "):])
			f.Status = Renamed
		}
	}

	// Статус по заголовкам файлов.
	switch {
	case old == "/dev/null" && new != "/dev/null" && new != "":
		f.Status = Added
	case new == "/dev/null" && old != "":
		f.Status = Removed
	}
	if strings.Contains(block, "Subproject commit ") || strings.Contains(block, "160000") || strings.Contains(block, "Submodule ") {
		f.Status = Submodule
	}
	if f.Path == "" {
		switch {
		case f.Status == Added && new != "":
			f.Path = new
		case f.Status == Removed && old != "":
			f.Path = old
		case new != "":
			f.Path = new
		case old != "":
			f.Path = old
		}
	}
	if f.Path == "" {
		return nil, block
	}

	// Подсчёт +/- по строкам хунков (исключая заголовки ---/+++).
	for _, ln := range lines {
		switch {
		case strings.HasPrefix(ln, "+") && !strings.HasPrefix(ln, "+++"):
			f.Added++
		case strings.HasPrefix(ln, "-") && !strings.HasPrefix(ln, "---"):
			f.Deleted++
		}
	}

	patch := strings.Join(lines, "\n")
	return f, patch
}

// PathOf обрезает префиксы a/ и b/ у путей файлов диффа и снимает git-кавычки.
func PathOf(p string) string {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "a/") {
		p = p[2:]
	} else if strings.HasPrefix(p, "b/") {
		p = p[2:]
	}
	if strings.HasPrefix(p, `"`) {
		if u, err := strconv.Unquote(p); err == nil {
			p = u
		}
	}
	return p
}

// PrefixPath добавляет префикс каталога к путям в патче (для сабмодулей: патч
// дочернего проекта монтируется под путём сабмодуля в родителе).
func PrefixPath(patch, prefix string) string {
	prefix = filepath.ToSlash(prefix)
	lines := strings.Split(patch, "\n")
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --git a/"):
			line = strings.ReplaceAll(line, "diff --git a/", "diff --git a/"+prefix+"/")
			line = strings.ReplaceAll(line, " b/", " b/"+prefix+"/")
		case strings.HasPrefix(line, "--- a/"):
			line = strings.ReplaceAll(line, "--- a/", "--- a/"+prefix+"/")
		case strings.HasPrefix(line, "+++ b/"):
			line = strings.ReplaceAll(line, "+++ b/", "+++ b/"+prefix+"/")
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}
