// Hermetic-тесты навигационных инструментов Ф-3: формат результата, лимит
// позиций и graceful degrade (языковой сервер недоступен). Клиент подменяется
// фейковым Navigator — процессы и сеть не запускаются.

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"ai/stackdetect"
	"ai/tools/lspclient"
)

type fakeNav struct {
	defs   []lspclient.Location
	refs   []lspclient.Location
	hover  *lspclient.Hover
	gotDir string
	gotRef bool
	file   string
	line   int
	col    int
}

func (f *fakeNav) Definition(_ context.Context, file string, line, col int) ([]lspclient.Location, error) {
	f.file, f.line, f.col = file, line, col
	return f.defs, nil
}
func (f *fakeNav) References(_ context.Context, file string, line, col int, include bool) ([]lspclient.Location, error) {
	f.gotRef = include
	return f.refs, nil
}
func (f *fakeNav) Hover(_ context.Context, file string, line, col int) (*lspclient.Hover, error) {
	return f.hover, nil
}

func withNavigator(t *testing.T, nav lspclient.Navigator, err error) {
	t.Helper()
	old := lspNavigator
	lspNavigator = func(context.Context, string, stackdetect.Kind) (lspclient.Navigator, error) {
		return nav, err
	}
	t.Cleanup(func() { lspNavigator = old })
}

func navProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package x\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLspDefinitionFormat(t *testing.T) {
	dir := navProject(t)
	nav := &fakeNav{defs: []lspclient.Location{{File: "lib.go", Line: 12, Col: 3, EndLine: 12, EndCol: 6}}}
	withNavigator(t, nav, nil)
	ops := &FileOps{OutputDir: dir}

	out, err := ops.LspDefinition(map[string]any{"file": "a.go", "line": 3, "col": 6})
	if err != nil {
		t.Fatalf("LspDefinition: %v", err)
	}
	var res struct {
		Status    string           `json:"status"`
		Count     int              `json:"count"`
		Locations []map[string]any `json:"locations"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, out)
	}
	if res.Status != "success" || res.Count != 1 || len(res.Locations) != 1 {
		t.Fatalf("res = %s", out)
	}
	if got := res.Locations[0]["file"]; got != "lib.go" {
		t.Fatalf("file = %v", got)
	}
	// 1-based позиция передана в клиент без изменений.
	if nav.file != "a.go" || nav.line != 3 || nav.col != 6 {
		t.Fatalf("client args = %s:%d:%d", nav.file, nav.line, nav.col)
	}
}

func TestLspReferencesDefaultsAndMonorepoPrefix(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "server")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "a.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	nav := &fakeNav{refs: []lspclient.Location{{File: "b.go", Line: 1, Col: 1}}}
	withNavigator(t, nav, nil)
	ops := &FileOps{OutputDir: root}

	out, err := ops.LspReferences(map[string]any{"file": "server/a.go", "line": 1, "col": 1})
	if err != nil {
		t.Fatalf("LspReferences: %v", err)
	}
	var res struct {
		Locations []map[string]any `json:"locations"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, out)
	}
	if len(res.Locations) != 1 || res.Locations[0]["file"] != "server/b.go" {
		t.Fatalf("locations = %s", out)
	}
	// include_declaration по умолчанию true.
	if !nav.gotRef {
		t.Fatal("include_declaration должен быть true по умолчанию")
	}
}

func TestLspHoverFormat(t *testing.T) {
	dir := navProject(t)
	nav := &fakeNav{hover: &lspclient.Hover{Contents: "func A()", Line: 3, Col: 6, EndLine: 3, EndCol: 7}}
	withNavigator(t, nav, nil)
	ops := &FileOps{OutputDir: dir}

	out, err := ops.LspHover(map[string]any{"file": "a.go", "line": 3, "col": 6})
	if err != nil {
		t.Fatalf("LspHover: %v", err)
	}
	var res struct {
		Status   string `json:"status"`
		Contents string `json:"contents"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, out)
	}
	if res.Status != "success" || res.Contents != "func A()" {
		t.Fatalf("res = %s", out)
	}
}

func TestLspNavDegrade(t *testing.T) {
	dir := navProject(t)
	withNavigator(t, nil, errors.New("языковой сервер не найден"))
	ops := &FileOps{OutputDir: dir}

	out, err := ops.LspDefinition(map[string]any{"file": "a.go", "line": 1, "col": 1})
	if err != nil {
		t.Fatalf("LspDefinition: %v", err)
	}
	var res struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, out)
	}
	if res.Status != "skipped" || res.Message == "" {
		t.Fatalf("res = %s", out)
	}
}

func TestLspNavMissingFile(t *testing.T) {
	dir := navProject(t)
	withNavigator(t, &fakeNav{}, nil)
	ops := &FileOps{OutputDir: dir}

	out, _ := ops.LspHover(map[string]any{"file": "nope.go", "line": 1, "col": 1})
	var res struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, out)
	}
	if res.Status != "skipped" {
		t.Fatalf("res = %s", out)
	}
}
