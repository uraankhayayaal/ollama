// Типы-порты жизненного цикла worktree (срез H3c Этапа 8): application-слой не
// импортирует infrastructure/git напрямую. Реализуются адаптером на сервере
// поверх *git.Repo и git-исполнителя.
package gitflow

import "context"

// WorktreeEntry — запись `git worktree list` (ветка и путь рабочей копии).
type WorktreeEntry struct {
	Branch string
	Path   string
}

// WorktreeRepo — операции рабочей копии (клона/worktree), нужные менеджеру
// worktree: список worktree, снятие, rebase на ветку, checkout ветки, создание
// worktree. Реализуется адаптером поверх *git.Repo.
type WorktreeRepo interface {
	// Root — каталог рабочей копии.
	Root() string
	// WorktreeList — список worktree репозитория (включая основной).
	WorktreeList(ctx context.Context) ([]WorktreeEntry, error)
	// RemoveWorktree — снять worktree по пути.
	RemoveWorktree(ctx context.Context, path string) error
	// Rebase — rebase ветки branch на ветку onto.
	Rebase(ctx context.Context, branch, onto string) error
	// Checkout — переключить рабочую копию на ветку.
	Checkout(ctx context.Context, branch string) error
	// AddWorktree — создать worktree ветки branch по пути и вернуть его ручку.
	AddWorktree(ctx context.Context, path, branch string) (WorktreeRepo, error)
}

// Submodule — сабмодуль проекта (путь относительно родителя и его корень).
type Submodule struct {
	Path string
	Root string
}
