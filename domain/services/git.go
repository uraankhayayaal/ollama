package services

import "context"

type GitOperations interface {
	Clone(ctx context.Context, remote string, dir string) error
	Commit(ctx context.Context, dir string, message string) (string, error)
	Push(ctx context.Context, dir string, branch string) error
	CreateBranch(ctx context.Context, dir string, branch string, base string) error
	Merge(ctx context.Context, dir string, branch string) error
	Diff(ctx context.Context, dir string, base string, head string) ([]FileDiff, error)
	Status(ctx context.Context, dir string) (GitStatus, error)
}

type FileDiff struct {
	Path      string
	Status    string
	Additions int
	Deletions int
}

type GitStatus struct {
	Branch      string
	Clean       bool
	Ahead       int
	Behind      int
	Uncommitted []string
}
