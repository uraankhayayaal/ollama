// Hermetic-тесты LSP-клиента Ф-3: рукопожатие, didOpen/didChange и навигация
// (definition/references/hover) против фейкового сервера. Сеть и реальные
// языковые серверы не используются:
//   - TestClientNavigation — in-memory пара потоков (jsonrpc2.NewChannelStreamPair);
//   - TestClientProcess — реальный stdio-транспорт через тестовый бинарник.

package lspclient

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"ai/stackdetect"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// fakeServer — минимальный языковой сервер для тестов. Отвечает фиксированными
// позициями и записывает полученные didOpen/didChange.
type fakeServer struct {
	protocol.UnimplementedServer
	root   string
	client protocol.Client

	mu      sync.Mutex
	opened  []string
	changed []string
}

func (s *fakeServer) Initialize(context.Context, *protocol.InitializeParams) (*protocol.InitializeResult, error) {
	return &protocol.InitializeResult{}, nil
}

// publish отправляет publishDiagnostics для файла (error + warning + info,
// последний должен быть отфильтрован клиентом).
func (s *fakeServer) publish(u uri.URI) {
	if s.client == nil {
		return
	}
	_ = s.client.PublishDiagnostics(context.Background(), &protocol.PublishDiagnosticsParams{
		URI: u,
		Diagnostics: []protocol.Diagnostic{
			{Range: protocol.Range{Start: protocol.Position{Line: 1, Character: 4}}, Severity: protocol.DiagnosticSeverityError, Message: protocol.String("undefined: Foo")},
			{Range: protocol.Range{Start: protocol.Position{Line: 2, Character: 0}}, Severity: protocol.DiagnosticSeverityWarning, Message: protocol.String("unused variable")},
			{Range: protocol.Range{Start: protocol.Position{Line: 3, Character: 0}}, Severity: protocol.DiagnosticSeverityInformation, Message: protocol.String("info hint")},
		},
	})
}

func (s *fakeServer) DidOpen(_ context.Context, p *protocol.DidOpenTextDocumentParams) error {
	s.mu.Lock()
	s.opened = append(s.opened, p.TextDocument.URI.FsPath())
	s.mu.Unlock()
	s.publish(p.TextDocument.URI)
	return nil
}

func (s *fakeServer) DidChange(_ context.Context, p *protocol.DidChangeTextDocumentParams) error {
	s.mu.Lock()
	s.changed = append(s.changed, p.TextDocument.URI.FsPath())
	s.mu.Unlock()
	s.publish(p.TextDocument.URI)
	return nil
}

func (s *fakeServer) Definition(context.Context, *protocol.DefinitionParams) (protocol.DefinitionResult, error) {
	return protocol.LocationSlice{{
		URI:   uri.File(filepath.Join(s.root, "lib.go")),
		Range: protocol.Range{Start: protocol.Position{Line: 4, Character: 2}, End: protocol.Position{Line: 4, Character: 8}},
	}}, nil
}

func (s *fakeServer) References(context.Context, *protocol.ReferenceParams) ([]protocol.Location, error) {
	return []protocol.Location{
		{
			URI:   uri.File(filepath.Join(s.root, "a.go")),
			Range: protocol.Range{Start: protocol.Position{Line: 9, Character: 0}},
		},
		// Путь вне проекта должен быть отфильтрован.
		{
			URI:   uri.File(filepath.Join(os.TempDir(), "outside", "dep.go")),
			Range: protocol.Range{Start: protocol.Position{Line: 0, Character: 0}},
		},
	}, nil
}

func (s *fakeServer) Hover(context.Context, *protocol.HoverParams) (*protocol.Hover, error) {
	r := protocol.Range{Start: protocol.Position{Line: 4, Character: 2}, End: protocol.Position{Line: 4, Character: 5}}
	return &protocol.Hover{Contents: &protocol.MarkupContent{Kind: protocol.MarkupKindMarkdown, Value: "func Foo()"}, Range: &r}, nil
}

func (s *fakeServer) Shutdown(context.Context) error { return nil }
func (s *fakeServer) Exit(context.Context) error     { return nil }

func writeProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return canonical(dir)
}

func TestClientNavigation(t *testing.T) {
	dir := writeProject(t)

	left, right := jsonrpc2.NewChannelStreamPair(0)
	srv := &fakeServer{root: dir}
	_, sconn, sclient := protocol.NewServer(context.Background(), srv, right)
	srv.client = sclient
	defer sconn.Close()

	ctx := context.Background()
	c, err := newClient(ctx, left, dir, stackdetect.KindGo)
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	defer c.Close()

	defs, err := c.Definition(ctx, "main.go", 3, 7)
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(defs) != 1 || defs[0].File != "lib.go" || defs[0].Line != 5 || defs[0].Col != 3 {
		t.Fatalf("definition = %+v", defs)
	}

	refs, err := c.References(ctx, "main.go", 3, 7, true)
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	if len(refs) != 1 || refs[0].File != "a.go" || refs[0].Line != 10 || refs[0].Col != 1 {
		t.Fatalf("references = %+v", refs)
	}

	h, err := c.Hover(ctx, "main.go", 3, 7)
	if err != nil {
		t.Fatalf("Hover: %v", err)
	}
	if h == nil || h.Contents != "func Foo()" || h.Line != 5 {
		t.Fatalf("hover = %+v", h)
	}

	// Документ открыт один раз; повторный запрос без правок didChange не шлёт.
	if _, err := c.Definition(ctx, "main.go", 3, 7); err != nil {
		t.Fatalf("Definition#2: %v", err)
	}
	srv.mu.Lock()
	if len(srv.opened) != 1 {
		t.Fatalf("didOpen count = %d, want 1", len(srv.opened))
	}
	if len(srv.changed) != 0 {
		t.Fatalf("didChange count = %d, want 0", len(srv.changed))
	}
	srv.mu.Unlock()

	// После правки файла отправляется didChange с новым содержимым.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() { _ = 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Definition(ctx, "main.go", 3, 7); err != nil {
		t.Fatalf("Definition#3: %v", err)
	}
	srv.mu.Lock()
	if len(srv.changed) != 1 {
		t.Fatalf("didChange count = %d, want 1", len(srv.changed))
	}
	srv.mu.Unlock()
}

func TestClientRejectsPathOutsideProject(t *testing.T) {
	dir := writeProject(t)
	left, right := jsonrpc2.NewChannelStreamPair(0)
	srv := &fakeServer{root: dir}
	_, sconn, sclient := protocol.NewServer(context.Background(), srv, right)
	srv.client = sclient
	defer sconn.Close()

	c, err := newClient(context.Background(), left, dir, stackdetect.KindGo)
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	defer c.Close()

	if _, err := c.Definition(context.Background(), "../secret.go", 1, 1); err == nil {
		t.Fatal("ожидалась ошибка для пути вне проекта")
	}
}

// TestClientDiagnostics проверяет нативный сбор publishDiagnostics: severity
// error/warning попадают в результат, info отфильтровывается, позиции 1-based.
func TestClientDiagnostics(t *testing.T) {
	dir := writeProject(t)
	left, right := jsonrpc2.NewChannelStreamPair(0)
	srv := &fakeServer{root: dir}
	_, sconn, sclient := protocol.NewServer(context.Background(), srv, right)
	srv.client = sclient
	defer sconn.Close()

	ctx := context.Background()
	c, err := newClient(ctx, left, dir, stackdetect.KindGo)
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	defer c.Close()

	ds, err := c.Diagnostics(ctx, []string{"main.go"})
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if len(ds) != 2 {
		t.Fatalf("diagnostics = %+v", ds)
	}
	if ds[0].File != "main.go" || ds[0].Severity != "error" || ds[0].Line != 2 || ds[0].Col != 5 {
		t.Fatalf("diag[0] = %+v", ds[0])
	}
	if ds[1].Severity != "warning" || ds[1].Line != 3 {
		t.Fatalf("diag[1] = %+v", ds[1])
	}
}

// TestHelperProcess — фейковый LSP-сервер в дочернем процессе: запускается
// только когда GO_LSP_HELPER=1 (см. TestClientProcess).
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_LSP_HELPER") != "1" {
		return
	}
	rwc := stdioRWC{in: os.Stdin, out: os.Stdout}
	srv := &fakeServer{}
	if wd, err := os.Getwd(); err == nil {
		srv.root = wd
	}
	_, conn, sclient := protocol.NewServer(context.Background(), srv, jsonrpc2.NewStream(rwc))
	srv.client = sclient
	<-conn.Done()
	os.Exit(0)
}

func TestClientProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stdio-транспорт тестируется на Unix")
	}
	dir := writeProject(t)

	ctx := context.Background()
	c, err := Start(ctx, Config{
		Kind:    stackdetect.KindGo,
		Dir:     dir,
		Command: []string{os.Args[0], "-test.run=TestHelperProcess"},
		Env:     []string{"GO_LSP_HELPER=1"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	defs, err := c.Definition(ctx, "main.go", 3, 7)
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(defs) != 1 || defs[0].File != "lib.go" {
		t.Fatalf("definition = %+v", defs)
	}
}

type stdioRWC struct {
	in  io.Reader
	out io.Writer
}

func (s stdioRWC) Read(p []byte) (int, error)  { return s.in.Read(p) }
func (s stdioRWC) Write(p []byte) (int, error) { return s.out.Write(p) }
func (s stdioRWC) Close() error                { return nil }
