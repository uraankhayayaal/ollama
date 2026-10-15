// Тесты ЛСП-диагностики приёмки (Ф-5): разворачивание scope-файлов, маппинг
// диагностик в analyze-замечания, деградация при отсутствии языкового сервера.
// Реальные языковые серверы не запускаются — точка входа подменяется.

package acceptor

import (
	"ai/tools"
	"os/exec"
	"strings"
	"testing"
)

func withAcceptorLSPDiags(t *testing.T, diags []tools.LSPDiag, handled bool) {
	t.Helper()
	old := acceptorLSPDiags
	acceptorLSPDiags = func(_ string, _ []string) ([]tools.LSPDiag, string, bool) {
		return diags, "lsp", handled
	}
	t.Cleanup(func() { acceptorLSPDiags = old })
}

// projectSourceFiles: собирает исходники, игнорирует служебные директории,
// уважает лимит.
func TestProjectSourceFiles(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"main.go", "server/handler.go", "frontend/App.tsx", "src/user.php", "view.phtml", "vendor/x.go", "vendor/x.php", "node_modules/y.js", "README.md", "script.sh"} {
		writeTestFile(t, dir, f, "")
	}
	files := projectSourceFiles(dir, 100)
	if len(files) != 5 {
		t.Fatalf("ожидали 5 исходников, got %#v", files)
	}
	for _, want := range []string{"main.go", "server/handler.go", "frontend/App.tsx", "src/user.php", "view.phtml"} {
		found := false
		for _, f := range files {
			if f == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("исходник %s не найден в %#v", want, files)
		}
	}

	limited := projectSourceFiles(dir, 2)
	if len(limited) != 2 {
		t.Fatalf("лимит не соблюдён: %#v", limited)
	}
}

// lspDiagsToIssues: error-замечания ведут к !OK и analyze-ошибкам,
// warning остаются в отчёте, но вердикт не меняют.
func TestLSPDiagsToIssues(t *testing.T) {
	res, issues := lspDiagsToIssues(nil)
	if !res.OK || len(issues) != 0 {
		t.Fatalf("пустые диагностики должны быть OK: %+v %#v", res, issues)
	}

	res, issues = lspDiagsToIssues([]tools.LSPDiag{
		{File: "main.go", Line: 3, Col: 5, Severity: "error", Message: "undefined: Foo"},
		{File: "main.go", Line: 9, Severity: "warning", Message: "unused"},
	})
	if res.OK {
		t.Fatal("error-диагностика должна давать !OK")
	}
	if len(issues) != 2 {
		t.Fatalf("ожидали 2 замечания, got %#v", issues)
	}
	if issues[0].Stage != StageAnalyze || issues[0].Severity != "error" ||
		issues[0].File != "main.go" || issues[0].Line != 3 || issues[0].Text != "undefined: Foo" {
		t.Fatalf("error-замечание искажено: %+v", issues[0])
	}
	if issues[1].Severity != "warning" {
		t.Fatalf("warning-замечание искажено: %+v", issues[1])
	}

	res, _ = lspDiagsToIssues([]tools.LSPDiag{
		{File: "main.go", Line: 2, Severity: "warning", Message: "style"},
	})
	if !res.OK {
		t.Fatal("только предупреждения — вердикт не должен меняться")
	}
}

// runLSPCheck: сервер недоступен (handled=false) — Skipped с предупреждением,
// без ошибок приёмки. Диагностики сервера — Issues.
func TestRunLSPCheck(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module m\n")
	writeTestFile(t, dir, "main.go", "package m\n")

	withAcceptorLSPDiags(t, nil, false)
	res, issues := runLSPCheck(dir)
	if !res.Skipped {
		t.Fatalf("без сервера проверка должна быть пропущена: %+v", res)
	}
	if len(issues) != 1 || issues[0].Severity != "warning" {
		t.Fatalf("ожидали предупреждение о пропуске: %#v", issues)
	}

	withAcceptorLSPDiags(t, []tools.LSPDiag{
		{File: "main.go", Line: 3, Col: 1, Severity: "error", Message: "undeclared name: Foo"},
	}, true)
	res, issues = runLSPCheck(dir)
	if res.Skipped || res.OK {
		t.Fatalf("сервер есть и нашёл ошибки: res=%+v", res)
	}
	if len(issues) != 1 || issues[0].Severity != "error" || issues[0].File != "main.go" {
		t.Fatalf("ошибки не попали в замечания: %#v", issues)
	}
}

// End-to-end через Accept: ЛСП-замечание само по себе (сборка/запуск в порядке)
// ведёт к reject и попадает в Issues — планировщик получит точный файл для
// scope фикс-шага.
func TestAcceptRejectsOnLSPFindings(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go не установлен")
	}
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module lspbad\n")
	writeTestFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")

	withAcceptorLSPDiags(t, []tools.LSPDiag{
		{File: "main.go", Line: 4, Col: 1, Severity: "error", Message: "unreachable code"},
	}, true)

	cfg := DefaultConfig()
	cfg.BuildTimeout = 2 * testTimeout(t)
	cfg.RunTimeout = testTimeout(t)

	rep := Accept(dir, cfg)
	if rep.Verdict != VerdictReject {
		t.Fatalf("ЛСП-ошибка должна давать reject, got %s (%s)", rep.Verdict, rep.Summary)
	}
	if rep.LSP == nil || rep.LSP.OK || rep.LSP.Skipped {
		t.Fatalf("ЛСП-проверка должна выполниться с ошибками: %+v", rep.LSP)
	}
	found := false
	for _, iss := range rep.Issues {
		if iss.Stage == StageAnalyze && iss.Severity == "error" && iss.File == "main.go" && strings.Contains(iss.Text, "unreachable") {
			found = true
		}
	}
	if !found {
		t.Fatalf("ЛСП-замечание не попало в Issues: %#v", rep.Issues)
	}
	if files := rep.IssueFiles(); len(files) != 1 || files[0] != "main.go" {
		t.Fatalf("IssueFiles должны дать точный scope для фикса: %#v", files)
	}
	// Пропущенный провайдер (degrade) не должен менять approve-вердикт сборки.
	withAcceptorLSPDiags(t, nil, false)
	rep = Accept(dir, cfg)
	if rep.Verdict != VerdictApprove {
		t.Fatalf("пропуск ЛСП не должен менять вердикт, got %s", rep.Verdict)
	}
	if rep.LSP == nil || !rep.LSP.Skipped {
		t.Fatalf("ожидали пропущенную ЛСП-проверку: %+v", rep.LSP)
	}
}

// Scope-наборы подпроектов монорепо попадают в IssueFiles с префиксом каталога.
func TestAcceptManyLSPIssuesPrefixed(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "frontend/package.json", `{"scripts": {"build": "echo ok"}}`)
	writeTestFile(t, root, "server/go.mod", "module server\n")
	writeTestFile(t, root, "server/main.go", "package main\n\nfunc main() {}\n")

	withAcceptorLSPDiags(t, []tools.LSPDiag{
		{File: "main.go", Line: 2, Col: 3, Severity: "error", Message: "bad"},
	}, true)

	cfg := DefaultConfig()
	cfg.BuildTimeout = 2 * testTimeout(t)
	cfg.RunTimeout = testTimeout(t)

	rep := Accept(root, cfg)
	if rep.Verdict != VerdictReject {
		t.Fatalf("ЛСП-ошибка в подпроекте должна давать reject, got %s", rep.Verdict)
	}
	if files := rep.IssueFiles(); len(files) != 1 || files[0] != "server/main.go" {
		t.Fatalf("префикс подкаталога не добавлен: %#v", files)
	}
}
