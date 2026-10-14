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
	"strings"

	"ai/domain/diff"
	"ai/infrastructure/git"
	"ai/workspace"
)

// diffModified — статус «изменён» для файла, не найденного в метаданных диффа.
const diffModified = diff.Modified

// diffFileEntry — алиас метаданных файла диффа из domain/diff.
type diffFileEntry = diff.FileEntry

// cachedDiff — разобранный дифф git-проекта: метаданные файлов и патчи по
// путям. Заполняется gitProjectDiff и живёт в кэше сервера.
type cachedDiff struct {
	Files  []diff.FileEntry
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
	repo := git.RepoFromState(s.gitExec, inf.Root, inf.GitRemote, inf.GitBranch, inf.GitBase)
	if inf.Parent == "" {
		if subs, err := git.ListSubmodules(ctx, s.gitExec, inf.Root); err != nil {
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
		childRepo := git.RepoFromState(s.gitExec, child.Root, child.GitRemote, child.GitBranch, child.GitBase)
		childRaw, err := childRepo.Diff(ctx)
		if err != nil {
			return nil, err
		}
		childDiff := parseUnifiedDiff(childRaw)
		for _, f := range childDiff.Files {
			f.Path = filepath.ToSlash(sub.Path) + "/" + f.Path
			c.Files = append(c.Files, f)
			c.File[f.Path] = diff.PrefixPath(childDiff.File[strings.TrimPrefix(f.Path, filepath.ToSlash(sub.Path)+"/")], sub.Path)
		}
	}
	c.Branch = inf.GitBranch
	c.Base = inf.GitBase
	c.Remote = inf.GitRemote
	s.diffs[key] = c
	return c, nil
}

// parseUnifiedDiff — тонкий адаптер: разбор в domain/diff, здесь добавляются
// поля кэша (branch/base/remote).
func parseUnifiedDiff(raw string) *cachedDiff {
	d := diff.Parse(raw)
	return &cachedDiff{Files: d.Files, File: d.File}
}
