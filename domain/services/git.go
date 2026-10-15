package services

import "context"

// Executor выполняет команды в рабочем каталоге (реализуется
// infrastructure/git). Узкий порт для мест, которым нужен прямой запуск
// git-команд, но не весь API рабочей копии.
type Executor interface {
	// Exec запускает argv в каталоге dir и возвращает stdout.
	Exec(ctx context.Context, dir string, argv ...string) (string, error)
}

// GitOperations — порт рабочей копии Git, которой пользуется приложение
// (операции с ветками, коммитами, слияниями и состоянием дерева). Реализуется
// инфраструктурным `*infrastructure/git.Repo`; application-слой зависит только
// от этого интерфейса и не тянет git-CLI.
//
// Порт намеренно повторяет подпись методов `git.Repo` (тот же порядок
// аргументов и результатов), чтобы репозиторий реализовывал его напрямую, без
// адаптера. Он не покрывает весь богатый API gitops: операции, возвращающие
// инфраструктурные типы (MergeResult, WorktreeInfo, Submodule) или требующие
// Executor, остаются у конкретного `*git.Repo` и подключаются на месте
// потребления (см. `infrastructure/git/port.go`).
type GitOperations interface {
	// Состояние рабочей копии.
	CurrentBranch(ctx context.Context) (string, error)
	HeadSHA(ctx context.Context) (string, error)
	// InTaskBranch сообщает, что текущая ветка — рабочая ветка задачи/эпика
	// (не базовая), т.е. её можно коммитить и пушить.
	InTaskBranch(ctx context.Context) (bool, error)
	Dirty(ctx context.Context) (bool, error)
	DirtyPaths(ctx context.Context) ([]string, error)
	BranchExists(ctx context.Context, branch string) (bool, error)

	// История и сравнение версий.
	MergeBase(ctx context.Context, a, b string) (string, error)
	MergedInto(ctx context.Context, into, feat string) (bool, error)
	CountCommits(ctx context.Context, base, branch string) (int, error)
	Diff(ctx context.Context) (string, error)

	// Изменения: фиксация и публикация.
	Commit(ctx context.Context, message string) error
	// CommitIfDirty коммитит только непустые изменения и возвращает SHA
	// созданного коммита (ok=false, если дерево чистое).
	CommitIfDirty(ctx context.Context, message string) (sha string, ok bool, err error)
	Stage(ctx context.Context, paths []string) error
	Push(ctx context.Context) error

	// Ветки.
	CreateBranch(ctx context.Context, branch, base string) error
	Checkout(ctx context.Context, branch string) error
	// RejectBranch удаляет рабочую ветку (локально и на remote).
	RejectBranch(ctx context.Context) error

	// Конфликты слияния.
	ConflictingFiles(ctx context.Context, other string) ([]string, error)
	UnmergedFiles(ctx context.Context) ([]string, error)
	ConflictDiff(ctx context.Context, paths []string) (string, error)
}
