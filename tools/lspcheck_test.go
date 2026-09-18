// Hermetic-тесты LspCheck: парсеры вывода чекеров (gopls/tsc/pyright/ruff),
// каскад выбора команды, детект проекта/подпроекта, лимиты и формат результата.
// Реальные чекеры и сеть не используются: парсеры гоняем на фикстурах,
// degrade-ветки — на пустых/несуществующих бинарях.

package tools

import (
	"ai/stackdetect"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseGoplsFormat(t *testing.T) {
	out := "server/internal/user/service.go:41:9: undefined: User\n" +
		"server/internal/user/service.go:42:5: undeclared name: bad\n"
	ds := parseLSPOutput(stackdetect.KindGo, out, "/tmp/proj")
	if len(ds) != 2 {
		t.Fatalf("want 2 diagnostics, got %d", len(ds))
	}
	d := ds[0]
	if d.File != "server/internal/user/service.go" || d.Line != 41 || d.Col != 9 {
		t.Fatalf("got %+v", d)
	}
	if d.Severity != "error" {
		t.Fatalf("severity = %q, want error", d.Severity)
	}
}

func TestParseTscFormat(t *testing.T) {
	out := "/work/frontend/src/index.ts(12,5): error TS2322: Type 'string' is not assignable to type 'number'.\n"
	ds := parseLSPOutput(stackdetect.KindNode, out, "/work/frontend")
	if len(ds) != 1 {
		t.Fatalf("want 1 diagnostic, got %d", len(ds))
	}
	d := ds[0]
	if d.File != "src/index.ts" || d.Line != 12 || d.Col != 5 {
		t.Fatalf("got %+v", d)
	}
	if !strings.Contains(d.Message, "not assignable") {
		t.Fatalf("message = %q", d.Message)
	}
}

func TestParsePyrightJSON(t *testing.T) {
	out := `{"generalDiagnostics":[
	  {"file":"/work/app/main.py","severity":"error","message":"Undefined variable ` + "`missing`" + `","range":{"start":{"line":4,"character":10},"end":{"line":4,"character":17}}},
	  {"file":"/work/app/main.py","severity":"warning","message":"Unused import ` + "`os`" + `","range":{"start":{"line":2,"character":0},"end":{"line":2,"character":2}}},
	  {"file":"/work/app/main.py","severity":"information","message":"всё хорошо","range":{"start":{"line":1,"character":0},"end":{"line":1,"character":1}}}
	]}`
	ds := parseLSPOutput(stackdetect.KindPython, out, "/work/app")
	if len(ds) != 2 {
		t.Fatalf("want 2 diagnostics (info отбрасывается), got %d", len(ds))
	}
	if ds[0].Severity != "error" || ds[0].Line != 5 || ds[0].Col != 11 {
		t.Fatalf("got %+v", ds[0])
	}
	if ds[1].Severity != "warning" {
		t.Fatalf("got %+v", ds[1])
	}
}

func TestParseRuffFormat(t *testing.T) {
	out := "app/main.py:10:5: F401 'os' imported but unused\n"
	ds := parseLSPOutput(stackdetect.KindPython, out, "/work/app")
	if len(ds) != 1 || ds[0].Line != 10 || ds[0].Col != 5 {
		t.Fatalf("got %+v", ds)
	}
}

func TestParseLinesWithoutColsFiltered(t *testing.T) {
	// Строки вида "package: text" или "file:line: text" (без колонки) не дают
	// точечной диагностики и отфильтровываются.
	out := "server/internal/db: undefined: SomeType\nmain.go: This is a note\n"
	ds := parseLSPOutput(stackdetect.KindGo, out, "/work")
	if len(ds) != 0 {
		t.Fatalf("строки без :line:col: должны отфильтровываться, got %+v", ds)
	}
}

func TestSortDedupLSP(t *testing.T) {
	in := []lspDiagnostic{
		{File: "b.py", Line: 1, Col: 2, Severity: "warning", Message: "same"},
		{File: "a.go", Line: 2, Col: 3, Severity: "error", Message: "e1"},
		{File: "b.py", Line: 1, Col: 2, Severity: "warning", Message: "same"},
		{File: "a.go", Line: 2, Col: 3, Severity: "error", Message: "e1"},
		{File: "c.ts", Line: 0, Col: 0, Severity: "info", Message: "i1"},
	}
	out := sortDedupLSP(in)
	if len(out) != 3 {
		t.Fatalf("want 3 (после дедупа), got %d: %+v", len(out), out)
	}
	if out[0].Severity != "error" || out[1].Severity != "warning" || out[2].Severity != "info" {
		t.Fatalf("порядок серьёзности нарушен: %+v", out)
	}
}

func TestCleanLSPFiles(t *testing.T) {
	in := []string{"server/main.go", "../escape.go", "/abs.go", "./x.go", " server/ y.go ", ""}
	out := cleanLSPFiles(in)
	if len(out) != 2 {
		t.Fatalf("want 2, got %d: %v", len(out), out)
	}
	if out[0] != "server/main.go" || out[1] != "x.go" {
		t.Fatalf("got %v", out)
	}
}

func TestLSPCommandSelectionGo(t *testing.T) {
	dir := t.TempDir()
	writeMarker(t, dir, "go.mod")
	if cmd, checker, err := lspCheckerCommand(stackdetect.KindGo, dir, nil); err != nil {
		t.Fatalf("go: err=%v", err)
	} else if checker != "gopls" && checker != "go vet" {
		t.Fatalf("go: checker=%q, cmd=%q", checker, cmd)
	}
}

func TestLSPCommandSelectionNodeLocalTsc(t *testing.T) {
	dir := t.TempDir()
	writeMarker(t, dir, "package.json")
	writeMarker(t, dir, "node_modules/.bin/tsc")
	writeMarker(t, dir, "tsconfig.json")
	cmd, checker, err := lspCheckerCommand(stackdetect.KindNode, dir, nil)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if checker != "tsc" || !strings.Contains(cmd, "./node_modules/.bin/tsc") {
		t.Fatalf("checker=%q cmd=%q", checker, cmd)
	}
}

func TestLSPCommandSelectionNodeFallbackBuild(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"build":"echo ok"}}`), 0644)
	cmd, checker, err := lspCheckerCommand(stackdetect.KindNode, dir, nil)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if checker != "npm run build" || cmd != "npm run build" {
		t.Fatalf("checker=%q cmd=%q", checker, cmd)
	}
}

func TestLSPCommandSelectionDegrade(t *testing.T) {
	dir := t.TempDir()
	writeMarker(t, dir, "requirements.txt")
	_, _, err := lspCheckerCommand(stackdetect.KindPython, dir, nil)
	if err == nil {
		return // pyright/ruff установлены — нормальный путь
	}
	if !strings.Contains(err.Error(), "Run") {
		t.Fatalf("degrade не подсказывает Run: %v", err)
	}
}

func TestLSPProjectMonorepo(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "server"), 0755)
	os.MkdirAll(filepath.Join(dir, "frontend"), 0755)
	writeMarker(t, dir, "server/go.mod")
	writeMarker(t, dir, "frontend/package.json")

	proj, _ := lspProject(dir, []string{"frontend/src/index.ts"})
	if proj != filepath.Join(dir, "frontend") {
		t.Fatalf("по файлу должны выбрать frontend, got %q", proj)
	}
	if proj, _ := lspProject(dir, nil); proj != "" {
		t.Fatalf("неоднозначность должна вернуть '', got %q", proj)
	}
}

func TestLSPProjectSingleRoot(t *testing.T) {
	dir := t.TempDir()
	writeMarker(t, dir, "go.mod")
	proj, stack := lspProject(dir, nil)
	if proj != dir || stack != stackdetect.KindGo {
		t.Fatalf("got %q %v", proj, stack)
	}
}

func TestLSPLimitsEnv(t *testing.T) {
	t.Setenv("LSP_MAX_DIAGS", "2")
	t.Setenv("LSP_MAX_OUTPUT", "10")
	l := lspLimits()
	if l.maxDiags != 2 || l.maxOutput != 10 {
		t.Fatalf("got %+v", l)
	}
}

func TestLSPResultSkippedNoMarkers(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}
	out, err := ops.LspCheck(map[string]any{})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	s := string(out)
	if !strings.Contains(s, "skipped") || !strings.Contains(s, "Run") {
		t.Fatalf("got %s", s)
	}
	if !isValidJSON(t, s) {
		t.Fatalf("не JSON: %s", s)
	}
}

func TestLSPResultGoReal(t *testing.T) {
	if !commandAvailable("gopls") && !commandAvailable("go") {
		t.Skip("нет go/gopls — пропускаем E2E")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n\ngo 1.26\n"), 0644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() { undefined() }\n"), 0644)
	ops := &FileOps{OutputDir: dir}
	out, err := ops.LspCheck(map[string]any{"files": []string{"main.go"}})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	s := string(out)
	if !strings.Contains(s, `"status":"success"`) {
		t.Fatalf("want success, got %s", s)
	}
	// Диагностика должна указать на main.go (колонка 15 — undefined()).
	if !strings.Contains(s, "main.go") {
		t.Fatalf("нет пути к файлу: %s", s)
	}
	if !isValidJSON(t, s) {
		t.Fatalf("не JSON: %s", s)
	}
}

func writeMarker(t *testing.T, dir, rel string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("marker\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func isValidJSON(t *testing.T, s string) bool {
	t.Helper()
	var v any
	return json.Unmarshal([]byte(s), &v) == nil
}
