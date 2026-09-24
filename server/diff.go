// Дифф git-проектов (Ф-3, ленивая загрузка): полный `git diff <base>`
// разбирается один раз на per-file записи (метаданные + патч), список файлов
// отдаётся метаданными, а патч конкретного файла — отдельным запросом
// `GET /diff?file=<path>`. Это избавляет Web UI от передачи/парсинга огромных
// unified-диффов целиком.

package server

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"ai/gitops"
	"ai/workspace"
)

// diffFileStatus — тип изменения файла в диффе.
type diffFileStatus string

const (
	diffAdded     diffFileStatus = "added"
	diffModified  diffFileStatus = "modified"
	diffRemoved   diffFileStatus = "removed"
	diffRenamed   diffFileStatus = "renamed"
	diffSubmodule diffFileStatus = "submodule"
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

// branchDiff строит diff зарегистрированной ветки относительно base. Ветки
// резолвятся до вызова по side-реестру, поэтому сюда не попадают пути клиента.
func (s *Server) branchDiff(ctx context.Context, inf workspace.Info, ref workspace.BranchRef, base string) (*cachedDiff, error) {
	if strings.TrimSpace(ref.Branch) == "" {
		return nil, fmt.Errorf("ветка не создана")
	}
	root := inf.Root
	worktree := strings.TrimSpace(ref.Worktree)
	if worktree != "" {
		root = worktree
		// Worktree может быть уже снят после завершения задачи, пока запись
		// реестра ещё не обновилась. Не маскируем это как пустой diff.
		if _, err := s.gitExec.Exec(ctx, root, "git", "rev-parse", "--git-dir"); err != nil {
			return nil, fmt.Errorf("worktree ветки %s недоступен: %w", ref.Branch, err)
		}
		mergeBase, err := s.gitExec.Exec(ctx, inf.Root, "git", "merge-base", base, ref.Branch)
		if err != nil {
			return nil, fmt.Errorf("не удалось найти общую базу %s и %s: %w", base, ref.Branch, err)
		}
		// -N включает новые файлы, сохраняя их содержимое в worktree.
		if _, err := s.gitExec.Exec(ctx, root, "git", "add", "-N", "-A"); err != nil {
			return nil, fmt.Errorf("git add -N в worktree: %w", err)
		}
		base = strings.TrimSpace(mergeBase)
	}
	var raw string
	var err error
	if worktree != "" {
		// Для worktree diff от merge-base должен включать и рабочее дерево.
		// Трёхточечная форма напрямую сравнивает коммиты, поэтому используем
		// найденный merge-base как обычную точку сравнения.
		raw, err = s.gitExec.Exec(ctx, root, "git", "-c", "diff.submodule=log", "diff", base)
	} else {
		raw, err = s.gitExec.Exec(ctx, root, "git", "-c", "diff.submodule=log", "diff", base+"..."+ref.Branch)
	}
	if err != nil {
		return nil, fmt.Errorf("git diff ветки %s: %w", ref.Branch, err)
	}
	c := parseUnifiedDiff(raw)
	c.Branch, c.Base, c.Remote = ref.Branch, inf.GitBase, inf.GitRemote
	return c, nil
}

// invalidateDiffs удаляет все варианты кэша веток и проекта.
func (s *Server) invalidateDiffs(project string) {
	s.diffMu.Lock()
	defer s.diffMu.Unlock()
	prefix := project + "\x00"
	for key := range s.diffs {
		if strings.HasPrefix(key, prefix) {
			delete(s.diffs, key)
		}
	}
}

// gitProjectDiff возвращает разобранный дифф проекта или зарегистрированной
// ветки. Кэш пересоздаётся на каждый запрос списка, а ленивые патчи берутся из
// последнего результата с тем же ключом (проект, ref, vs).
func diffCacheKey(project, ref, base string) string {
	return project + "\x00" + ref + "\x00" + base
}

func (s *Server) gitProjectDiff(ctx context.Context, inf workspace.Info, ref, base string, refresh bool) (*cachedDiff, error) {
	s.diffMu.Lock()
	defer s.diffMu.Unlock()
	key := diffCacheKey(inf.Name, ref, base)
	if !refresh {
		if c := s.diffs[key]; c != nil {
			return c, nil
		}
	}
	if ref != "" {
		var branch workspace.BranchRef
		found := false
		if inf.GitBranches != nil {
			for _, candidate := range inf.GitBranches.Epics {
				if candidate.Branch == ref {
					branch, found = candidate, true
					break
				}
			}
			if !found {
				for _, candidate := range inf.GitBranches.Tasks {
					if candidate.Branch == ref {
						branch, found = candidate, true
						break
					}
				}
			}
		}
		if !found {
			return nil, fmt.Errorf("ветка %q не зарегистрирована в проекте", ref)
		}
		c, err := s.branchDiff(ctx, inf, branch, base)
		if err != nil {
			return nil, err
		}
		s.diffs[key] = c
		return c, nil
	}
	repo := gitops.RepoFromState(s.gitExec, inf.Root, inf.GitRemote, inf.GitBranch, inf.GitBase)
	if inf.Parent == "" {
		if subs, err := gitops.ListSubmodules(ctx, s.gitExec, inf.Root); err != nil {
			return nil, err
		} else {
			repo.Submodules = subs
		}
	}
	raw, err := repo.Diff(ctx)
	if err != nil {
		return nil, err
	}
	c := parseUnifiedDiff(raw)
	for _, sub := range repo.Submodules {
		child, err := s.reg.Get(submoduleProjectName(inf.Name, sub.Path))
		if err != nil {
			continue
		}
		childRepo := gitops.RepoFromState(s.gitExec, child.Root, child.GitRemote, child.GitBranch, child.GitBase)
		childRaw, err := childRepo.Diff(ctx)
		if err != nil {
			return nil, err
		}
		childDiff := parseUnifiedDiff(childRaw)
		for _, f := range childDiff.Files {
			f.Path = filepath.ToSlash(sub.Path) + "/" + f.Path
			c.Files = append(c.Files, f)
			c.File[f.Path] = prefixDiffPath(childDiff.File[strings.TrimPrefix(f.Path, filepath.ToSlash(sub.Path)+"/")], sub.Path)
		}
	}
	c.Branch = inf.GitBranch
	c.Base = inf.GitBase
	c.Remote = inf.GitRemote
	s.diffs[key] = c
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

func prefixDiffPath(patch, prefix string) string {
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
	if strings.Contains(block, "Subproject commit ") || strings.Contains(block, "160000") || strings.Contains(block, "Submodule ") {
		f.Status = diffSubmodule
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
