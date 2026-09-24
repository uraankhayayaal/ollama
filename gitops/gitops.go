// Package gitops — git-операции поверх рабочего проекта (Ф-2-2).
//
// Гарантирует изоляцию работы агента:
//
//   - Worktree — фича-ветка в отдельном git worktree (основной клон не
//     трогаем); это предпочитаемый режим для git-проектов.
//   - BranchInPlace — fallback: если worktree недоступен/нежелателен,
//     фича-ветка создаётся прямо в клоне (branch-in-place).
//   - Commit/Push — фиксируют изменения в фича-ветке и пушат её в remote.
//   - RejectBranch — откат: удаляет фича-ветку на remote и локально, снимает
//     worktree. Используется при отклонении результата человеком (HITL).
//   - Diff — git diff между точкой отхода и текущей HEAD.
//
// Все операции выполняются через абстракцию Executor: реальная реализация
// зовёт git CLI, в тестах подставляются dry-run-исполнители (команды
// записываются, но не выполняются), что делает тесты hermetic и связывает
// поведение с перечнем команд без зависимостей от окружения.
package gitops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Executor выполняет команды в рабочем каталоге.
type Executor interface {
	// Exec запускает argv в каталоге dir и возвращает stdout.
	// Для git-операций argv начинается с "git".
	Exec(ctx context.Context, dir string, argv ...string) (string, error)
}

// ExecutorFunc — функциональная реализация Executor (для подстановки в тестах
// и композиций, когда одним исполнителем нельзя описать все вызовы).
type ExecutorFunc func(ctx context.Context, dir string, argv ...string) (string, error)

func (f ExecutorFunc) Exec(ctx context.Context, dir string, argv ...string) (string, error) {
	return f(ctx, dir, argv...)
}

// Repo описывает изолированную рабочую копию фича-ветки проекта.
type Repo struct {
	// Remote — URL remote (например git@gitlab.com:g/r.git).
	Remote string
	// Root — основной клон/worktree проекта (рабочий каталог агента).
	Root string
	// Branch — имя фича-ветки.
	Branch string
	// Base — точка отхода (SHA/ref), от которой ведётся дифф.
	Base string

	// Submodules — git-сабмодули проекта (Ф-1, PLAN-2026-09-22-todo-submodule):
	// заполняется после клона/открытия через ListSubmodules+EnsureSubmodules
	// (лениво — в Push/PushTo/Diff, если не заполнен заранее) и позволяет
	// родителю пушить сабмодули ДО себя: remote родителя ссылается на gitlink,
	// поэтому SHA сабмодуля обязан лечь на remote раньше.
	Submodules []Submodule

	ex Executor
}

// Worktree создаёт изолированный worktree для фича-ветки branch, отходящей
// от base. Возвращает Repo с Root = каталог worktree. Основной клон остается
// нетронутым; его путь передаётся в mainRoot.
func Worktree(ctx context.Context, ex Executor, mainRoot, branch, base, worktreePath string) (*Repo, error) {
	if ex == nil {
		return nil, fmt.Errorf("gitops: не задан исполнитель")
	}
	if strings.TrimSpace(branch) == "" || strings.TrimSpace(base) == "" {
		return nil, fmt.Errorf("gitops: required branch и base")
	}
	if _, err := ex.Exec(ctx, mainRoot, "git", "rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("gitops: %s не git-клон: %w", mainRoot, err)
	}

	argv := []string{"git", "worktree", "add", "-b", branch, worktreePath, base}
	if _, err := ex.Exec(ctx, mainRoot, argv...); err != nil {
		return nil, fmt.Errorf("gitops: создание worktree %q: %w", worktreePath, err)
	}

	remote, err := remoteOf(ctx, ex, worktreePath)
	if err != nil {
		return nil, err
	}
	return &Repo{Remote: remote, Root: worktreePath, Branch: branch, Base: base, ex: ex}, nil
}

// BranchInPlace создаёт фича-ветку прямо в клоне root (fallback, когда
// worktree недоступен). Возвращает Repo, работающий в том же каталоге.
func BranchInPlace(ctx context.Context, ex Executor, root, branch, base string) (*Repo, error) {
	if ex == nil {
		return nil, fmt.Errorf("gitops: не задан исполнитель")
	}
	if _, err := ex.Exec(ctx, root, "git", "checkout", "-b", branch, base); err != nil {
		return nil, fmt.Errorf("gitops: создание ветки в клоне: %w", err)
	}
	remote, err := remoteOf(ctx, ex, root)
	if err != nil {
		return nil, err
	}
	return &Repo{Remote: remote, Root: root, Branch: branch, Base: base, ex: ex}, nil
}

// remoteOf извлекает origin из git config каталога root.
func remoteOf(ctx context.Context, ex Executor, root string) (string, error) {
	out, err := ex.Exec(ctx, root, "git", "remote", "get-url", "origin")
	if err != nil {
		// Нет origin — локальный проект без remote (например, bare/локальный
		// репозиторий, из которого только клонируют). Это не ошибка для
		// branch-in-place.
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

// Commit фиксирует все изменения в рабочем каталоге ro в фича-ветке.
// message — сообщение коммита; author при необходимости можно задать через
// env (AI_GIT_AUTHOR_NAME/EMAIL), иначе используется конфиг git по умолчанию.
func (r *Repo) Commit(ctx context.Context, message string) error {
	if r == nil || r.Root == "" {
		return fmt.Errorf("gitops: пустой Repo")
	}
	msg := strings.TrimSpace(message)
	if msg == "" {
		return fmt.Errorf("gitops: пустое сообщение коммита")
	}
	for _, sub := range r.Submodules {
		dirty, err := r.SubmoduleDirty(ctx, sub)
		if err != nil {
			return err
		}
		if dirty {
			if err := r.SubmoduleCommit(ctx, sub, msg); err != nil {
				return err
			}
		}
	}
	if _, err := r.ex.Exec(ctx, r.Root, "git", "add", "-A"); err != nil {
		return fmt.Errorf("gitops: git add: %w", err)
	}
	if _, err := r.ex.Exec(ctx, r.Root, "git", "commit", "-m", msg); err != nil {
		return fmt.Errorf("gitops: git commit: %w", err)
	}
	return nil
}

// CommitAllowEmpty фиксирует изменения, даже если индекс пуст (рабочая копия
// совпадает с HEAD). Используется для завершения merge-процесса, когда резолв
// оставляет дерево равным HEAD-дереву: MERGE_HEAD требует merge-коммит, а
// без --allow-empty git откажется его создать.
func (r *Repo) CommitAllowEmpty(ctx context.Context, message string) error {
	if r == nil || r.Root == "" {
		return fmt.Errorf("gitops: пустой Repo")
	}
	msg := strings.TrimSpace(message)
	if msg == "" {
		return fmt.Errorf("gitops: пустое сообщение коммита")
	}
	if _, err := r.ex.Exec(ctx, r.Root, "git", "commit", "--allow-empty", "-m", msg); err != nil {
		return fmt.Errorf("gitops: git commit --allow-empty: %w", err)
	}
	return nil
}

// Push пушит фича-ветку в remote (origin) с upstream.
// При отсутствии remote (локальный проект без origin) — ничего не делает.
func (r *Repo) Push(ctx context.Context) error {
	return r.PushToWith(ctx, "", nil)
}

// PushToWith пушит сначала вложенные сабмодули, затем текущий репозиторий.
// pushFn при наличии позволяет вызывающему слою выбирать credentialed URL
// отдельно для remote каждого сабмодуля.
func (r *Repo) PushToWith(ctx context.Context, remoteURL string, pushFn func(context.Context, *Repo, string) error) error {
	if r == nil || r.Root == "" {
		return fmt.Errorf("gitops: пустой Repo")
	}
	if r.Remote == "" && remoteURL == "" {
		return nil // нет push-цели — изолированная ветка остаётся локальной
	}
	for _, sub := range r.Submodules {
		if sub.Branch == "" || sub.Remote == "" {
			continue
		}
		child := &Repo{Root: sub.Root, Remote: sub.Remote, Branch: sub.Branch, Base: sub.Base, ex: r.ex}
		var err error
		if pushFn != nil {
			err = pushFn(ctx, child, sub.Remote)
		} else {
			err = child.Push(ctx)
		}
		if err != nil {
			return fmt.Errorf("gitops: push сабмодуля %s: %w", sub.Path, err)
		}
	}
	argv := []string{"push", "-u", "origin", r.Branch}
	if remoteURL != "" {
		argv = []string{"push", remoteURL, r.Branch}
	}
	if _, err := r.ex.Exec(ctx, r.Root, append([]string{"git"}, argv...)...); err != nil {
		return fmt.Errorf("gitops: git push %s: %w", r.Branch, err)
	}
	return nil
}

// PushTo пушит фича-ветку в явно указанный remote-URL (без -u: upstream на
// временный URL не настраивается). Используется для HTTPS-remotes GitHub/
// GitLab, где нет credentialed credential-helper: токен встраивается в URL
// (https://x-access-token:<token>@host/…), чтобы push прошёл в headless-среде
// без интерактива. Токен никуда не сохраняется — URL живёт только в argv.
func (r *Repo) PushTo(ctx context.Context, remoteURL string) error {
	if r == nil || r.Root == "" {
		return fmt.Errorf("gitops: пустой Repo")
	}
	if strings.TrimSpace(remoteURL) == "" {
		return fmt.Errorf("gitops: пустой push-URL")
	}
	return r.PushToWith(ctx, remoteURL, nil)
}

// Clone клонирует удалённый репозиторий remote в новый каталог dest и создаёт
// в нём фича-ветку branch от ветки по умолчанию (base). Используется Web UI
// при открытии git-проекта по URL (Ф-2-3): фича-ветка создаётся сразу, чтобы
// агенты работали в изоляции, а приёмка (accept/reject) шла от этой ветки.
//
// Режим — branch-in-place: репозиторий уже принадлежит проекту, отдельный
// worktree не создаётся (якорь — сам свежий клон в temp/<проект>).
func Clone(ctx context.Context, ex Executor, remoteURL, branch, dest string) (*Repo, error) {
	if ex == nil {
		return nil, fmt.Errorf("gitops: не задан исполнитель")
	}
	if strings.TrimSpace(remoteURL) == "" {
		return nil, fmt.Errorf("gitops: пустой remote")
	}
	if strings.TrimSpace(branch) == "" {
		return nil, fmt.Errorf("gitops: пустая фича-ветка")
	}
	if strings.TrimSpace(dest) == "" {
		return nil, fmt.Errorf("gitops: пустой путь клона")
	}

	parent := filepath.Dir(dest)
	cloneArgs := []string{"clone"}
	cloneArgs = append(cloneArgs, remoteURL, dest)
	argv := append([]string{"git"}, cloneArgs...)
	if _, err := ex.Exec(ctx, parent, argv...); err != nil {
		return nil, fmt.Errorf("gitops: git clone %s: %w", remoteURL, err)
	}
	if os.Getenv("GITOPS_SUBMODULES") != "0" {
		if _, err := EnsureSubmodules(ctx, ex, dest); err != nil {
			return nil, fmt.Errorf("gitops: сабмодули %s: %w", remoteURL, err)
		}
	}

	// Ветка по умолчанию (точка отхода базы) — до создания фича-ветки.
	base, err := ex.Exec(ctx, dest, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("gitops: определение ветки по умолчанию: %w", err)
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return nil, fmt.Errorf("gitops: не удалось определить ветку по умолчанию клона %s", remoteURL)
	}

	if _, err := ex.Exec(ctx, dest, "git", "checkout", "-b", branch); err != nil {
		return nil, fmt.Errorf("gitops: создание фича-ветки %s: %w", branch, err)
	}
	var subs []Submodule
	if os.Getenv("GITOPS_SUBMODULES") != "0" {
		subs, err = PrepareSubmodules(ctx, ex, dest, branch)
		if err != nil {
			return nil, fmt.Errorf("gitops: подготовка сабмодулей: %w", err)
		}
	}

	return &Repo{
		Remote:     strings.TrimSpace(remoteURL),
		Root:       dest,
		Branch:     branch,
		Base:       base,
		Submodules: subs,
		ex:         ex,
	}, nil
}

// RepoFromState восстанавливает Repo по сохранённому состоянию (реестр
// workspace: root/remote/branch/base). Сервер Web UI пересоздаёт Repo из
// записи реестра при каждом запросе diff/accept/reject — состояние git не
// хранится в памяти, а читается из реестра заново.
func RepoFromState(ex Executor, root, remote, branch, base string) *Repo {
	return &Repo{Root: root, Remote: remote, Branch: branch, Base: base, ex: ex}
}

// Dirty сообщает, есть ли незакоммиченные изменения в фича-ветке
// (git status --porcelain непустой). Используется приёмкой (accept/ПМР),
// чтобы не коммитить пустое состояние.
func (r *Repo) Dirty(ctx context.Context) (bool, error) {
	if r == nil || r.Root == "" {
		return false, fmt.Errorf("gitops: пустой Repo")
	}
	out, err := r.ex.Exec(ctx, r.Root, "git", "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("gitops: git status: %w", err)
	}
	return strings.TrimSpace(out) != "", nil
}

// RejectBranch удаляет фича-ветку: на remote (если он есть) и локально,
// снимая worktree, если Repo был создан через Worktree. После отката рабочая
// копия возвращается в состояние базы (checkout base). Используется HITL-затвором
// при отказе принять результат.
func (r *Repo) RejectBranch(ctx context.Context) error {
	if r == nil || r.Root == "" {
		return fmt.Errorf("gitops: пустой Repo")
	}

	// 1) Если это worktree — снимаем его и удаляем ветку. Проверяем тип `.git`:
	//    у изолированного worktree файл `.git` — это файл-указатель на основной
	//    репозиторий; у обычного клона (branch-in-place) `.git` — это каталог.
	isWorktree := false
	if st, err := os.Stat(r.Root + "/.git"); err == nil && !st.IsDir() {
		isWorktree = true
	}

	if r.Remote != "" {
		// Удаляем ветку на remote, чтобы не оставлять «висящей» после reject.
		if _, err := r.ex.Exec(ctx, r.Root, "git", "push", "origin", "--delete", r.Branch); err != nil {
			// Если ветки на remote нет (никогда не пушилась) — не критично.
			if _, err := r.ex.Exec(ctx, r.Root, "git", "rev-parse", "--verify", "refs/remotes/origin/"+r.Branch); err != nil {
				_ = err
			} else {
				return fmt.Errorf("gitops: удаление remote-ветки %s: %w", r.Branch, err)
			}
		}
	}

	if isWorktree {
		// Переходим на базу в основном клоне не нужно: worktree отдельный.
		base := r.Base
		if base != "" {
			if _, err := r.ex.Exec(ctx, r.Root, "git", "checkout", base); err != nil {
				return fmt.Errorf("gitops: checkout %s: %w", base, err)
			}
		}
		// Ветка принадлежит worktree: удаляем ветку из основного репозитория.
		if _, err := r.ex.Exec(ctx, r.Root, "git", "branch", "-D", r.Branch); err != nil {
			return fmt.Errorf("gitops: git branch -D %s: %w", r.Branch, err)
		}
	} else {
		// Branch-in-place: отбрасываем незакоммиченные правки (reset --hard),
		// переходим на базу и удаляем фича-ветку. Удалить нельзя текущую
		// (checked-out) ветку, поэтому сначала переключаемся на базу.
		if r.Base != "" {
			if _, err := r.ex.Exec(ctx, r.Root, "git", "reset", "--hard", r.Base); err != nil {
				return fmt.Errorf("gitops: reset --hard %s: %w", r.Base, err)
			}
			if _, err := r.ex.Exec(ctx, r.Root, "git", "checkout", r.Base); err != nil {
				return fmt.Errorf("gitops: checkout %s: %w", r.Base, err)
			}
		}
		if _, err := r.ex.Exec(ctx, r.Root, "git", "branch", "-D", r.Branch); err != nil {
			return fmt.Errorf("gitops: git branch -D %s: %w", r.Branch, err)
		}
	}
	return nil
}

// Diff возвращает изменения от точки отхода Base до рабочего каталога:
// сюда входят и незакоммиченные правки агентов, и уже закоммиченные. Новые
// (untracked) файлы помечаются git add -N (intent-to-add), иначе git diff их
// не покажет. Это то же состояние, которое Accept зафиксирует через
// git add -A + commit, поэтому дифф ревью до accept совпадает с содержимым
// будущего коммита; после commit отдача не меняется (рабочий каталог = HEAD).
// Intent-to-add безопасен: содержимое файлов не меняет, Accept перезатрёт
// index, а Reject — сбросит через git reset --hard.
// Для локальных (не-git) проектов этот пакет не используется — там дифф
// строится по Snap.Diff() в server (см. PLAN Ф-2 (diff-вью, loc)).
func (r *Repo) Diff(ctx context.Context) (string, error) {
	if r == nil || r.Root == "" {
		return "", fmt.Errorf("gitops: пустой Repo")
	}
	if _, err := r.ex.Exec(ctx, r.Root, "git", "add", "-N", "-A"); err != nil {
		return "", fmt.Errorf("gitops: git add -N: %w", err)
	}
	out, err := r.ex.Exec(ctx, r.Root, "git", "-c", "diff.submodule=log", "diff", r.Base)
	if err != nil {
		return "", fmt.Errorf("gitops: git diff: %w", err)
	}
	return strings.TrimSpace(out), nil
}
