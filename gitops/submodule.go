// Работа с git submodule (Ф-1, PLAN-2026-09-22-todo-submodule).
//
// Сабмодуль — самостоятельный git-репозиторий, «приклеенный» к родителю
// gitlink-записью в index (mode 160000 + SHA). Появляется в диффе родителя как
// смена одного SHA (`Subproject commit <sha>`), поэтому содержимое правок
// агента нужно смотреть в самом сабмодуле. Этот файл добавляет субмодулям
// изоляцию работы, как у основного клона:
//
//   - ListSubmodules/EnsureSubmodules — чтение `.gitmodules` и инициализация
//     прямых submodule-ов (`git submodule update --init`);
//   - task worktree сабмодуля создаётся сервером через gitops.Worktree;
//   - SubmoduleCommit и Repo.Diff фиксируют и показывают изменения сабмодуля;
//   - Push — сабмодули пушатся ДО родителя: родительский gitlink поднимается
//     только после того, как на remote сабмодуля лёг новый SHA, иначе
//     remote родителя будет ссылаться на несуществующий gitlink.
//
// Сабмодули, как и сам родитель, регистрируются в реестре workspace как
// отдельные git-проекты с именами `<родитель>--<путь>` (см. Ф-1, Реестр).
package gitops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Submodule описывает запись сабмодуля родительского репозитория.
type Submodule struct {
	// Path — путь сабмодуля относительно Root родителя (из .gitmodules).
	Path string
	// URL — remote сабмодуля (из .gitmodules).
	URL string
	// Root — абсолютный путь сабмодуля (Root родителя + Path). Заполняется
	// после EnsureSubmodules; пуст — сабмодуль не инициализирован.
	Root          string
	Remote        string
	Base          string
	Branch        string
	DefaultBranch string
}

// ListSubmodules читает .gitmodules в root и возвращает записи сабмодулей.
// Отсутствующий .gitmodules — пустой список, не ошибка.
func ListSubmodules(ctx context.Context, ex Executor, root string) ([]Submodule, error) {
	if ex == nil {
		return nil, fmt.Errorf("gitops: не задан исполнитель")
	}
	gm := filepath.Join(root, ".gitmodules")
	if _, err := os.Stat(gm); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("gitops: stat .gitmodules: %w", err)
	}
	// git config -f .gitmodules --list выдаёт строки вида
	// submodule.<name>.path=<path>, submodule.<name>.url=<url>,
	// submodule.<name>.branch=<branch>. Секция `submodule` на верхнем уровне
	// (без вложенного имени) git не отдаёт — её можно безопасно игнорировать.
	out, err := ex.Exec(ctx, root, "git", "config", "-f", ".gitmodules", "--list")
	if err != nil {
		return nil, fmt.Errorf("gitops: git config -f .gitmodules: %w", err)
	}
	subs := parseSubmodulesConfig(out)
	for _, sub := range subs {
		clean := filepath.Clean(filepath.FromSlash(sub.Path))
		if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("gitops: небезопасный путь сабмодуля %q", sub.Path)
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		absSub := filepath.Clean(filepath.Join(absRoot, clean))
		if !hasPathPrefix(absSub, absRoot) {
			return nil, fmt.Errorf("gitops: путь сабмодуля %q выходит за корень проекта", sub.Path)
		}
		resolvedRoot, err := filepath.EvalSymlinks(absRoot)
		if err != nil {
			return nil, fmt.Errorf("gitops: resolve root %s: %w", absRoot, err)
		}
		probe := absSub
		for {
			if _, err := os.Lstat(probe); err == nil {
				break
			}
			parent := filepath.Dir(probe)
			if parent == probe {
				break
			}
			probe = parent
		}
		resolvedProbe, err := filepath.EvalSymlinks(probe)
		if err != nil {
			return nil, fmt.Errorf("gitops: resolve submodule path %q: %w", sub.Path, err)
		}
		if !hasPathPrefix(resolvedProbe, resolvedRoot) && resolvedProbe != resolvedRoot {
			return nil, fmt.Errorf("gitops: путь сабмодуля %q проходит через symlink за пределы проекта", sub.Path)
		}
	}
	return subs, nil
}

func hasPathPrefix(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// parseSubmodulesConfig разбирает вывод `git config -f .gitmodules --list`
// на записи submodule`ов. Ключи вида submodule.<name>.path/url/branch.
func parseSubmodulesConfig(configOutput string) []Submodule {
	mu := make(map[string]*Submodule) // name → запись
	order := []string{}
	for _, ln := range strings.Split(configOutput, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		k, v, ok := strings.Cut(ln, "=")
		if !ok {
			continue
		}
		fields := strings.Split(strings.TrimSpace(k), ".")
		// submodule.<name>.path / .url / .branch
		if len(fields) != 3 || fields[0] != "submodule" {
			continue
		}
		name := fields[1]
		key := fields[2]
		if _, seen := mu[name]; !seen {
			order = append(order, name)
			mu[name] = &Submodule{}
		}
		switch key {
		case "path":
			mu[name].Path = strings.TrimSpace(v)
		case "url":
			mu[name].URL = strings.TrimSpace(v)
		case "branch":
			mu[name].Branch = strings.TrimSpace(v)
		}
	}
	out := make([]Submodule, 0, len(order))
	for _, name := range order {
		s := mu[name]
		if s.Path == "" || s.URL == "" {
			continue // неполная запись — пропускаем
		}
		out = append(out, *s)
	}
	return out
}

// EnsureSubmodules инициализирует прямые сабмодули репозитория и возвращает
// заполненные записи (Root = filepath.Join(root, Path)), которые нужно
// зарегистрировать в реестре workspace. Отсутствие .gitmodules — пустой список.
func EnsureSubmodules(ctx context.Context, ex Executor, root string) ([]Submodule, error) {
	subs, err := ListSubmodules(ctx, ex, root)
	if err != nil {
		return nil, err
	}
	if len(subs) == 0 {
		return nil, nil
	}
	args := []string{"git", "submodule", "update", "--init"}
	if depth := strings.TrimSpace(os.Getenv("GITOPS_SUBMODULE_DEPTH")); depth != "" {
		n, err := strconv.Atoi(depth)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("gitops: GITOPS_SUBMODULE_DEPTH must be a positive integer")
		}
		args = append(args, "--depth", strconv.Itoa(n))
	}
	if _, err := ex.Exec(ctx, root, args...); err != nil {
		return nil, fmt.Errorf("gitops: git submodule update --init: %w", err)
	}
	for i := range subs {
		subs[i].Root = filepath.Join(root, subs[i].Path)
		if remote, err := ex.Exec(ctx, subs[i].Root, "git", "remote", "get-url", "origin"); err == nil {
			subs[i].Remote = strings.TrimSpace(remote)
		} else {
			subs[i].Remote = strings.TrimSpace(subs[i].URL)
		}
	}
	return subs, nil
}

// PrepareSubmodules создаёт изолированные feature-ветки сабмодулей и
// сохраняет их базовый commit SHA, чтобы последующие commit/push/reject
// выполнялись независимо от ветки родителя.
func PrepareSubmodules(ctx context.Context, ex Executor, root, branchPrefix string) ([]Submodule, error) {
	subs, err := EnsureSubmodules(ctx, ex, root)
	if err != nil {
		return nil, err
	}
	for i := range subs {
		subs[i].DefaultBranch = subs[i].Branch
		base, err := ex.Exec(ctx, subs[i].Root, "git", "rev-parse", "HEAD")
		if err != nil {
			return nil, fmt.Errorf("gitops: сабмодуль %s base: %w", subs[i].Path, err)
		}
		subs[i].Base = strings.TrimSpace(base)
		if subs[i].DefaultBranch == "" {
			if ref, err := ex.Exec(ctx, subs[i].Root, "git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
				subs[i].DefaultBranch = strings.TrimPrefix(strings.TrimSpace(ref), "origin/")
			}
		}
		branch := branchPrefix + "/submodule/" + SanitizeBranchName(filepath.ToSlash(subs[i].Path))
		subs[i].Branch = branch
		if _, err := ex.Exec(ctx, subs[i].Root, "git", "checkout", "-B", branch); err != nil {
			return nil, fmt.Errorf("gitops: сабмодуль %s feature branch: %w", subs[i].Path, err)
		}
		if _, err := ex.Exec(ctx, subs[i].Root, "git", "checkout", "--detach", branch); err != nil {
			return nil, fmt.Errorf("gitops: detach сабмодуля %s: %w", subs[i].Path, err)
		}
	}
	return subs, nil
}

// SubmoduleTarget возвращает gitops.Repo для работы с сабмодулем: Root — его
// каталог, Branch — фича-ветка задачи (если задана), Remote родителя остаётся
// у родителя. Если у сабмодуля нет своей ветки — Branch остаётся пустой и
// операции идут прямо в его рабочей копии.
func (r *Repo) SubmoduleTarget(ctx context.Context, sub Submodule) (*Repo, error) {
	if r == nil || r.Root == "" {
		return nil, fmt.Errorf("gitops: пустой Repo")
	}
	if sub.Root == "" {
		return nil, fmt.Errorf("gitops: пустой каталог сабмодуля")
	}
	return &Repo{Root: sub.Root, Branch: "", Base: "", ex: r.ex}, nil
}

// SubmoduleCommit фиксирует изменения в рабочей копии сабмодуля. Это отдельный
// git-репозиторий, поэтому commit идёт в нём (Root сабмодуля), а gitlink в
// родителе поднимется отдельным коммитом родителя.
func (r *Repo) SubmoduleCommit(ctx context.Context, sub Submodule, message string) error {
	if sub.Root == "" {
		return fmt.Errorf("gitops: пустой каталог сабмодуля")
	}
	if _, err := r.ex.Exec(ctx, sub.Root, "git", "add", "-A"); err != nil {
		return fmt.Errorf("gitops: сабмодуль %s: git add: %w", sub.Path, err)
	}
	if _, err := r.ex.Exec(ctx, sub.Root, "git", "commit", "-m", message); err != nil {
		return fmt.Errorf("gitops: сабмодуль %s: git commit: %w", sub.Path, err)
	}
	if sub.Branch != "" {
		if _, err := r.ex.Exec(ctx, sub.Root, "git", "branch", "-f", sub.Branch, "HEAD"); err != nil {
			return fmt.Errorf("gitops: сабмодуль %s: обновление ветки %s: %w", sub.Path, sub.Branch, err)
		}
	}
	return nil
}

// SubmoduleDirty сообщает, есть ли в сабмодуле незакоммиченные изменения.
func (r *Repo) SubmoduleDirty(ctx context.Context, sub Submodule) (bool, error) {
	if sub.Root == "" {
		return false, fmt.Errorf("gitops: пустой каталог сабмодуля")
	}
	out, err := r.ex.Exec(ctx, sub.Root, "git", "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("gitops: сабмодуль %s: git status: %w", sub.Path, err)
	}
	return strings.TrimSpace(out) != "", nil
}
