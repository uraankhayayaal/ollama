package services

import "ai/domain"

type Tool interface {
	Name() string
	Definition() domain.ToolDefinition
	Execute(args map[string]any) ([]byte, error)
}

type ToolSet interface {
	Definitions() []domain.ToolDefinition
	Execute(name string, args map[string]any) ([]byte, error)
}

type ToolSetBuilder func(projectDir string) ToolSet

type LSPDiag struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type LSPDiagnoser interface {
	Diagnostics(projectDir string, files []string) ([]LSPDiag, bool)
}

type Snapshot interface {
	FileCount() int
	Diff() (added, modified, removed []string, err error)
	Restore() (restored, removed []string, err error)
}

type SnapshotFactory interface {
	New(root string) (Snapshot, error)
	NewScoped(root string, scope []string, exclude ...string) (Snapshot, error)
}

type APIEntry struct {
	Kind string
	Name string
	Sign string
}

type PublicAPISnapshot struct {
	Entries []APIEntry
}

type PublicAPIAuditor interface {
	Snapshot(dir string) (*PublicAPISnapshot, error)
	Compare(before, after *PublicAPISnapshot) []string
}
