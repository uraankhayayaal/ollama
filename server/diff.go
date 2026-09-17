// Дифф git-проектов (Ф-3, ленивая загрузка): полный `git diff <base>`
// разбирается один раз на per-file записи (метаданные + патч), список файлов
// отдаётся метаданными, а патч конкретного файла — отдельным запросом
// `GET /diff?file=<path>`. Это избавляет Web UI от передачи/парсинга огромных
// unified-диффов целиком.

package server

import (
	"context"
	"strconv"
	"strings"

	"ai/gitops"
	"ai/workspace"
)

// diffFileStatus — тип изменения файла в диффе.
type diffFileStatus string

const (
	diffAdded    diffFileStatus = "added"
	diffModified diffFileStatus = "modified"
	diffRemoved  diffFileStatus = "removed"
	diffRenamed  diffFileStatus = "renamed"
)

// diffFileEntry — один файл в диффе git-проекта (метаданные, без патча).
type diffFileEntry struct {
	Path    string         `json:"path"`
	Status  diffFileStatus `json:"status"`
	Added   int            `json:"added"`
	Deleted int            `json:"deleted"`
}

// cachedDiff — разобранный дифф git-проекта: метаданные файлов и патчи по
// путям. Заполняется gitProjectDiff и живёт в кэше сервера.
type cachedDiff struct {
	Files  []diffFileEntry
	File   map[string]string
	Branch string
	Base   string
	Remote string
}

// gitProjectDiff возвращает разобранный дифф git-проекта из кэша или строит
// его заново (git add -N -A + git diff <base>). Кэш пересоздаётся на каждый
// запрос списка (/diff без ?file=), чтобы отражать актуальные правки агентов;
// per-file патчи отдаются из последнего кэша — они дешёвые.
func (s *Server) gitProjectDiff(ctx context.Context, inf workspace.Info) (*cachedDiff, error) {
	s.diffMu.Lock()
	defer s.diffMu.Unlock()
	if c, ok := s.diffs[inf.Name]; ok {
		return c, nil
	}
	repo := gitops.RepoFromState(s.gitExec, inf.Root, inf.GitRemote, inf.GitBranch, inf.GitBase)
	raw, err := repo.Diff(ctx)
	if err != nil {
		return nil, err
	}
	c := parseUnifiedDiff(raw)
	c.Branch = inf.GitBranch
	c.Base = inf.GitBase
	c.Remote = inf.GitRemote
	s.diffs[inf.Name] = c
	return c, nil
}

// parseUnifiedDiff разбирает вывод `git diff <base>` на per-file блоки.
func parseUnifiedDiff(raw string) *cachedDiff {
	c := &cachedDiff{File: map[string]string{}}
	blocks := splitDiffBlocks(raw)
	for _, b := range blocks {
		if strings.TrimSpace(b) == "" {
			continue
		}
		f, patch := parseDiffBlock(b)
		if f == nil {
			continue
		}
		c.Files = append(c.Files, *f)
		c.File[f.Path] = patch
	}
	return c
}

// splitDiffBlocks делит unified-дифф на блоки по строкам `diff --git`.
func splitDiffBlocks(raw string) []string {
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

// parseDiffBlock разбирает блок одного файла: метаданные и патч.
func parseDiffBlock(block string) (*diffFileEntry, string) {
	f := &diffFileEntry{Status: diffModified}
	lines := strings.Split(block, "\n")

	var old, new string
	for _, ln := range lines {
		switch {
		case strings.HasPrefix(ln, "--- "):
			old = diffPathOf(ln[4:])
		case strings.HasPrefix(ln, "+++ "):
			new = diffPathOf(ln[4:])
		}
		if strings.HasPrefix(ln, "rename to ") {
			f.Path = strings.TrimSpace(ln[len("rename to "):])
			f.Status = diffRenamed
		}
	}

	// Статус по заголовкам файлов.
	switch {
	case old == "/dev/null" && new != "/dev/null" && new != "":
		f.Status = diffAdded
	case new == "/dev/null" && old != "":
		f.Status = diffRemoved
	}
	if f.Path == "" {
		switch {
		case f.Status == diffAdded && new != "":
			f.Path = new
		case f.Status == diffRemoved && old != "":
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

// diffPathOf обрезает префиксы a/ и b/ у путей файлов диффа и снимает git-кавычки.
func diffPathOf(p string) string {
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