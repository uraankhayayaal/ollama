// Тесты Ф-4: нативные диагностики (publishDiagnostics) как источник LspCheck.
// Реальный языковой сервер не запускается — провайдер подменяется.

package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai/stackdetect"
	"ai/tools/lspclient"
)

// fakeDiagProvider — подменный источник нативных диагностик.
type fakeDiagProvider struct {
	ds    []lspclient.Diagnostic
	err   error
	files []string
	calls int
}

func (f *fakeDiagProvider) Diagnostics(_ context.Context, files []string) ([]lspclient.Diagnostic, error) {
	f.calls++
	f.files = files
	return f.ds, f.err
}

func withDiagProvider(t *testing.T, p lspclient.DiagnosticsProvider, err error) {
	t.Helper()
	old := lspDiagProvider
	lspDiagProvider = func(context.Context, string, stackdetect.Kind) (lspclient.DiagnosticsProvider, error) {
		return p, err
	}
	t.Cleanup(func() { lspDiagProvider = old })
}

// LspCheck берёт диагностики нативно, когда провайдер доступен: CLI-чекер не
// запускается, checker = "lsp".
func TestLspCheckUsesNativeDiagnostics(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n\ngo 1.26\n"), 0644)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package m\n"), 0644)

	fake := &fakeDiagProvider{ds: []lspclient.Diagnostic{
		{File: "a.go", Line: 2, Col: 15, Severity: "error", Message: "undefined: Foo"},
		{File: "a.go", Line: 3, Col: 1, Severity: "warning", Message: "unused variable"},
	}}
	withDiagProvider(t, fake, nil)

	ops := &FileOps{OutputDir: dir}
	out, err := ops.LspCheck(map[string]any{"files": []string{"a.go"}})
	if err != nil {
		t.Fatalf("LspCheck: %v", err)
	}
	s := string(out)
	for _, want := range []string{`"status":"success"`, `"checker":"lsp"`, `"undefined: Foo"`, `"unused variable"`, `"file":"a.go"`, `"line":2`, `"col":15`} {
		if !strings.Contains(s, want) {
			t.Fatalf("нет %s в %s", want, s)
		}
	}
	if fake.calls != 1 {
		t.Fatalf("ожидали 1 вызов провайдера, got %d", fake.calls)
	}
	if len(fake.files) != 1 || fake.files[0] != "a.go" {
		t.Fatalf("провайдеру передан неверный список файлов: %#v", fake.files)
	}
}

// Нативный режим деградирует к CLI, если сервер недоступен (ошибка провайдера).
func TestLspNativeDiagnosticsDegrade(t *testing.T) {
	dir := t.TempDir()
	withDiagProvider(t, nil, errors.New("server unavailable"))
	ops := &FileOps{OutputDir: dir}
	if _, handled := ops.lspNativeDiagnostics(dir, stackdetect.KindGo, []string{"a.go"}); handled {
		t.Fatal("при ошибке провайдера handled должен быть false (fallback на CLI)")
	}
}

// LSP_NATIVE=0 полностью выключает нативный путь, не трогая провайдер.
func TestLspNativeDisabledByEnv(t *testing.T) {
	t.Setenv("LSP_NATIVE", "0")
	dir := t.TempDir()
	fake := &fakeDiagProvider{ds: []lspclient.Diagnostic{{File: "a.go", Line: 1, Col: 1, Severity: "error", Message: "x"}}}
	withDiagProvider(t, fake, nil)
	ops := &FileOps{OutputDir: dir}
	if _, handled := ops.lspNativeDiagnostics(dir, stackdetect.KindGo, []string{"a.go"}); handled {
		t.Fatal("при LSP_NATIVE=0 нативный путь должен быть выключен")
	}
	if fake.calls != 0 {
		t.Fatalf("провайдер не должен вызываться при LSP_NATIVE=0, got %d", fake.calls)
	}
}

// Без файлов нативный путь не используется (нечего открывать).
func TestLspNativeWithoutFiles(t *testing.T) {
	dir := t.TempDir()
	fake := &fakeDiagProvider{}
	withDiagProvider(t, fake, nil)
	ops := &FileOps{OutputDir: dir}
	if _, handled := ops.lspNativeDiagnostics(dir, stackdetect.KindGo, nil); handled {
		t.Fatal("без файлов нативный путь не нужен")
	}
	if fake.calls != 0 {
		t.Fatalf("провайдер не должен вызываться без файлов, got %d", fake.calls)
	}
}

// Монорепо: пути диагностик от корня подпроекта дополняются префиксом до
// OutputDir, а провайдеру передаются пути уже относительно подпроекта.
func TestLspNativeMonorepoPrefix(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "server")
	os.MkdirAll(sub, 0755)
	os.WriteFile(filepath.Join(sub, "go.mod"), []byte("module server\n\ngo 1.26\n"), 0644)
	os.WriteFile(filepath.Join(sub, "a.go"), []byte("package server\n"), 0644)

	fake := &fakeDiagProvider{ds: []lspclient.Diagnostic{
		{File: "a.go", Line: 1, Col: 1, Severity: "error", Message: "boom"},
	}}
	withDiagProvider(t, fake, nil)

	ops := &FileOps{OutputDir: root}
	out, err := ops.LspCheck(map[string]any{"files": []string{"server/a.go"}})
	if err != nil {
		t.Fatalf("LspCheck: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `"project":"server"`) || !strings.Contains(s, `"file":"server/a.go"`) {
		t.Fatalf("пути не приведены к OutputDir: %s", s)
	}
	if len(fake.files) != 1 || fake.files[0] != "a.go" {
		t.Fatalf("провайдеру передан неверный путь: %#v", fake.files)
	}
}

// lspProjectFiles переводит OutputDir-относительные пути в proj-относительные.
func TestLspProjectFiles(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "server")
	os.MkdirAll(sub, 0755)
	got := lspProjectFiles(dir, sub, []string{"server/a.go", "server/pkg/b.go", "../evil.go"})
	if len(got) != 2 || got[0] != "a.go" || got[1] != "pkg/b.go" {
		t.Fatalf("lspProjectFiles = %#v", got)
	}
	// Корневой проект: список не меняется.
	same := lspProjectFiles(dir, dir, []string{"main.go"})
	if len(same) != 1 || same[0] != "main.go" {
		t.Fatalf("для корня список не должен меняться: %#v", same)
	}
}
