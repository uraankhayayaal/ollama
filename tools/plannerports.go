package tools

import "ai/domain/services"

func PlannerToolSet(projectDir string) services.ToolSet {
	return Select(
		[]string{"List", "ReadFiles", LspDefinition, LspReferences, LspHover},
		Deps{FileOps: &FileOps{OutputDir: projectDir}},
	)
}

type LSPDiagnoserAdapter struct{}

func (LSPDiagnoserAdapter) Diagnostics(projectDir string, files []string) ([]services.LSPDiag, bool) {
	diags, _, handled := (&FileOps{OutputDir: projectDir}).LSPDiagnostics(files)
	return diags, handled
}

type SnapshotAdapterFactory struct{}

func (SnapshotAdapterFactory) New(root string) (services.Snapshot, error) {
	return NewSnap(root)
}

func (SnapshotAdapterFactory) NewScoped(root string, scope []string, exclude ...string) (services.Snapshot, error) {
	return NewSnapScoped(root, scope, exclude...)
}

type PublicAPIAuditorAdapter struct{}

func (PublicAPIAuditorAdapter) Snapshot(dir string) (*services.PublicAPISnapshot, error) {
	return BuildPublicAPISnapshot(dir)
}

func (PublicAPIAuditorAdapter) Compare(before, after *services.PublicAPISnapshot) []string {
	return ComparePublicAPI(before, after)
}
