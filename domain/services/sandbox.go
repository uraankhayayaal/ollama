package services

import "context"

type SandboxEngine interface {
	Start(ctx context.Context, config SandboxConfig) (*SandboxInstance, error)
	Stop(ctx context.Context, id string) error
	Exec(ctx context.Context, id string, cmd []string) (SandboxResult, error)
	CopyIn(ctx context.Context, id string, src string, dst string) error
	CopyOut(ctx context.Context, id string, src string, dst string) error
}

type SandboxConfig struct {
	Image   string
	Workdir string
	Mounts  []SandboxMount
	Env     map[string]string
}

type SandboxMount struct {
	Source   string
	Target   string
	ReadOnly bool
}

type SandboxInstance struct {
	ID      string
	Workdir string
}

type SandboxResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}
