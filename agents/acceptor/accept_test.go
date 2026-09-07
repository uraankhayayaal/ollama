package acceptor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectKind(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, dir, "go.mod", "module x\n")
	if got := DetectKind(dir); got != KindGo {
		t.Fatalf("go.mod: got %q, want go", got)
	}
	os.Remove(filepath.Join(dir, "go.mod"))

	writeTestFile(t, dir, "package.json", "{}")
	if got := DetectKind(dir); got != KindNode {
		t.Fatalf("package.json: got %q, want node", got)
	}
	os.Remove(filepath.Join(dir, "package.json"))

	writeTestFile(t, dir, "main.py", "print(1)")
	if got := DetectKind(dir); got != KindPython {
		t.Fatalf("main.py: got %q, want python", got)
	}
	os.Remove(filepath.Join(dir, "main.py"))

	if got := DetectKind(dir); got != KindUnknown {
		t.Fatalf("пустая директория: got %q, want unknown", got)
	}
}

func TestRunCommand(t *testing.T) {
	out, code, timedOut, err := runCommand(t.TempDir(), `echo "hello word" && printf 'пока'`, DefaultConfig().BuildTimeout)
	if err != nil {
		t.Fatalf("runCommand: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}
	if timedOut {
		t.Fatal("не должно быть таймаута")
	}
	if out != "hello word\nпока" {
		t.Fatalf("output: got %q", out)
	}
}

func TestRunCommandExitCode(t *testing.T) {
	_, code, _, _ := runCommand(t.TempDir(), "exit 3", DefaultConfig().BuildTimeout)
	if code != 3 {
		t.Fatalf("exit code: got %d, want 3", code)
	}
}

func TestRunCommandTimeout(t *testing.T) {
	_, _, timedOut, _ := runCommand(t.TempDir(), "sleep 60", 200)
	if !timedOut {
		t.Fatal("ожидали таймаут")
	}
}

func TestAnalyzeOutput(t *testing.T) {
	out := `main.go:9:2: undefined: missingFunc
panic: runtime error: nil pointer dereference
`

	issues, critical := analyzeOutput(StageRun, out)
	if !critical {
		t.Fatal("ожидали критичный маркер panic")
	}
	if len(issues) == 0 {
		t.Fatal("ожидали найденные issues")
	}
	found := false
	for _, iss := range issues {
		if iss.File == "main.go" && iss.Line == 9 {
			found = true
		}
		if strings.Contains(iss.Text, "panic") && iss.Severity != "error" {
			t.Fatalf("severity %q для паники, ожидали error", iss.Severity)
		}
	}
	if !found {
		t.Fatalf("не найден issue с main.go:9, issues=%#v", issues)
	}
}

func TestTrimOutput(t *testing.T) {
	out := strings.Repeat("x", 100)
	got := trimOutput(out, 20)
	if !strings.Contains(got, "обрезан") {
		t.Fatalf("ожидали пометку обрезания, got %q", got)
	}
}

// Стоп сломанного Go-проекта: сборка не компилируется → вердикт reject.
func TestAcceptRejectsBrokenGoProject(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go не установлен")
	}
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module broken\n")
	writeTestFile(t, dir, "main.go", `package main

func main() {
	var x int
	x = "не число"
	_ = x
}
`)

	cfg := DefaultConfig()
	cfg.BuildTimeout = 2 * testTimeout(t)
	cfg.RunTimeout = testTimeout(t)

	rep := Accept(dir, cfg)
	if rep.Verdict != VerdictReject {
		t.Fatalf("сломанный проект должен быть отвергнут, got %s", rep.Verdict)
	}
	if !rep.Build.OK {
		if len(rep.Issues) == 0 {
			t.Fatal("сломанная сборка должна давать хотя бы одно замечание")
		}
	}
}

// Рабочий Go-проект: собрался, запустился, завершился нулевым кодом → approve.
func TestAcceptApprovesWorkingGoProject(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go не установлен")
	}
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module good\n")
	writeTestFile(t, dir, "main.go", `package main

func main() { println("привет") }
`)

	cfg := DefaultConfig()
	cfg.BuildTimeout = 2 * testTimeout(t)
	cfg.RunTimeout = testTimeout(t)

	rep := Accept(dir, cfg)
	if rep.Verdict != VerdictApprove {
		t.Fatalf("рабочий проект должен быть принят, got %s\n%+v", rep.Verdict, rep.Summary)
	}
	if rep.Run == nil || rep.Run.Command != "go run ." {
		t.Fatalf("ожидали запуск go run ., got %#v", rep.Run)
	}
}

// Неизвестный тип проекта: reject с указанием, что маркеры не найдены.
func TestAcceptUnknownProject(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "README.md", "no markers here")

	rep := Accept(dir, DefaultConfig())
	if rep.Verdict != VerdictReject {
		t.Fatalf("неизвестный проект должен быть отвергнут, got %s", rep.Verdict)
	}
}

// Явно заданные команды через конфиг работают даже без маркеров проекта.
func TestAcceptOverriddenCommands(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "run.sh", `#!/bin/sh
echo started
exit 0
`)

	cfg := DefaultConfig()
	cfg.BuildCmd = "true"
	cfg.RunCmd = "sh run.sh"
	cfg.RunTimeout = testTimeout(t)

	rep := Accept(dir, cfg)
	if rep.Verdict != VerdictApprove {
		t.Fatalf("проект с заданными командами должен быть принят, got %s (%s)", rep.Verdict, rep.Summary)
	}
	if rep.Tool != string(KindUnknown) {
		t.Fatalf("tool: got %q, want unknown", rep.Tool)
	}
}

func TestReportFixPrompt(t *testing.T) {
	rep := &Report{
		Project: "p",
		Tool:    "go",
		Verdict: VerdictReject,
		Build:   BuildResult{Command: "go build ./...", Output: "main.go:4:6: undefined: foo"},
		Issues:  []Issue{{Stage: StageBuild, Severity: "error", File: "main.go", Line: 4, Text: "undefined: foo"}},
	}
	p := rep.FixPrompt()
	for _, want := range []string{"go build ./...", "main.go:4", "undefined: foo"} {
		if !strings.Contains(p, want) {
			t.Fatalf("FixPrompt не содержит %q:\n%s", want, p)
		}
	}
	if !strings.Contains(p, "ОШИБКА") {
		t.Fatalf("FixPrompt должен показывать ошибку сборки")
	}
}

// testTimeout подбирает короткий таймаут, но достаточный для локальных go run.
func testTimeout(t *testing.T) time.Duration {
	return 60 * time.Second
}

// Автодетект команды установки зависимостей по типу проекта.
func TestInstallCommandByKind(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module x\n")
	if cmd, tool := KindGo.installCommand(dir); cmd != "go mod download" {
		t.Fatalf("go: got %q (tool=%s)", cmd, tool)
	}

	dir = t.TempDir()
	writeTestFile(t, dir, "package.json", "{}")
	if cmd, _ := KindNode.installCommand(dir); cmd != "npm install" {
		t.Fatalf("node без lock: got %q", cmd)
	}
	writeTestFile(t, dir, "package-lock.json", "{}")
	if cmd, _ := KindNode.installCommand(dir); cmd != "npm ci" {
		t.Fatalf("node с lock: got %q", cmd)
	}

	dir = t.TempDir()
	writeTestFile(t, dir, "requirements.txt", "# пусто\n")
	if cmd, _ := KindPython.installCommand(dir); !strings.Contains(cmd, "pip install -r requirements.txt") {
		t.Fatalf("python: got %q", cmd)
	}

	dir = t.TempDir()
	if cmd, _ := KindUnknown.installCommand(dir); cmd != "" {
		t.Fatalf("unknown: got %q, want ''", cmd)
	}
}

// go mod download на проекте без внешних зависимостей не требует сети и
// выполняется мгновенно: шаг установки считается успешным до сборки.
func TestAcceptInstallsDepsForGo(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go не установлен")
	}
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module deps\n")
	writeTestFile(t, dir, "main.go", `package main

func main() { println("hi") }
`)

	cfg := DefaultConfig()
	cfg.BuildTimeout = 2 * testTimeout(t)
	cfg.RunTimeout = testTimeout(t)

	rep := Accept(dir, cfg)
	if rep.Install == nil || rep.Install.Skipped {
		t.Fatalf("ожидали выполненную установку, got %#v", rep.Install)
	}
	if !rep.Install.OK {
		t.Fatalf("go mod download на проекте без зависимостей должен пройти, got %+v", rep.Install)
	}
	if rep.Install.Command != "go mod download" {
		t.Fatalf("command: got %q, want go mod download", rep.Install.Command)
	}
}

// Упавшая установка зависимостей ведёт к вердикту reject.
func TestAcceptInstallFailureRejects(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "package.json", "{}")
	writeTestFile(t, dir, "index.js", "console.log('ok')\n")

	cfg := DefaultConfig()
	cfg.InstallCmd = "false"
	cfg.RunTimeout = testTimeout(t)

	rep := Accept(dir, cfg)
	if rep.Install == nil || rep.Install.OK || rep.Install.Skipped {
		t.Fatalf("ожидали проваленную установку, got %#v", rep.Install)
	}
	if rep.Verdict != VerdictReject {
		t.Fatalf("провал установки должен давать reject, got %s (%s)", rep.Verdict, rep.Summary)
	}
	found := false
	for _, iss := range rep.Issues {
		if iss.Stage == StageInstall && iss.Severity == "error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ошибка установки не попала в отчёт, issues=%#v", rep.Issues)
	}
}

// Недоступный инструмент установки — не ошибка: шаг пропускается,
// вердикт не меняется.
func TestAcceptInstallToolMissingSkips(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "package.json", "{}")
	writeTestFile(t, dir, "index.js", "console.log('ok')\n")
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node не установлен")
	}

	cfg := DefaultConfig()
	cfg.InstallCmd = "no-such-installer-xyz"
	cfg.RunTimeout = testTimeout(t)

	rep := Accept(dir, cfg)
	if rep.Install == nil || !rep.Install.Skipped {
		t.Fatalf("ожидали пропущенную установку, got %#v", rep.Install)
	}
	if rep.Verdict != VerdictApprove {
		t.Fatalf("пропуск установки не должен менять вердикт, got %s (%s)", rep.Verdict, rep.Summary)
	}
}

// Проект собирается и запускается, но go vet находит ошибку → вердикт reject.
func TestAcceptAnalyzerRejects(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go не установлен")
	}
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module vetbad\n")
	writeTestFile(t, dir, "main.go", `package main

import "fmt"

func main() {
	fmt.Printf("%d\n")
}
`)

	cfg := DefaultConfig()
	cfg.BuildTimeout = 2 * testTimeout(t)
	cfg.RunTimeout = testTimeout(t)

	rep := Accept(dir, cfg)
	if rep.Analyze == nil || rep.Analyze.Skipped {
		t.Fatalf("ожидали выполненную проверку анализатора, got %#v", rep.Analyze)
	}
	if rep.Analyze.OK {
		t.Fatal("анализатор должен найти проблему с Printf")
	}
	if rep.Verdict != VerdictReject {
		t.Fatalf("проект с ошибками go vet должен быть отвергнут, got %s (%s)", rep.Verdict, rep.Summary)
	}
	found := false
	for _, iss := range rep.Issues {
		if iss.Stage == StageAnalyze && strings.Contains(iss.Text, "Printf") {
			found = true
		}
	}
	if !found {
		t.Fatalf("замечание go vet не попало в отчёт, issues=%#v", rep.Issues)
	}
}

// Проект собирается и запускается, но gofmt находит неотформатированный файл.
// Нарушения формата — предупреждения: вердикт остаётся approve.
func TestAcceptFormatWarning(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go не установлен")
	}
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module fmtbad\n")
	writeTestFile(t, dir, "main.go", "package main\n\nfunc main(){\tprintln(\"a\",\"b\") }\n")

	cfg := DefaultConfig()
	cfg.BuildTimeout = 2 * testTimeout(t)
	cfg.RunTimeout = testTimeout(t)

	rep := Accept(dir, cfg)
	if rep.Format == nil || rep.Format.Skipped {
		t.Fatalf("ожидали выполненную проверку стилизатора, got %#v", rep.Format)
	}
	if rep.Format.OK {
		t.Fatal("стилизатор должен отметить неотформатированный файл")
	}
	if rep.Verdict != VerdictApprove {
		t.Fatalf("нарушение формата не должно менять вердикт, got %s (%s)", rep.Verdict, rep.Summary)
	}
	found := false
	for _, iss := range rep.Issues {
		if iss.Stage == StageFormat {
			found = true
		}
	}
	if !found {
		t.Fatalf("предупреждение формата не попало в отчёт, issues=%#v", rep.Issues)
	}
}

// У проекта нет node_modules, prettier/eslint не настроены: проверки помечаются
// как пропущенные, вердикт — approve (отсутствие инструментов не ошибка).
func TestAcceptSkipsMissingToolsForNode(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node не установлен")
	}
	dir := t.TempDir()
	writeTestFile(t, dir, "package.json", `{}`)
	writeTestFile(t, dir, "index.js", "console.log('ok')\n")

	cfg := DefaultConfig()
	cfg.RunTimeout = testTimeout(t)

	rep := Accept(dir, cfg)
	if rep.Format == nil || !rep.Format.Skipped {
		t.Fatalf("ожидали пропущенный стилизатор, got %#v", rep.Format)
	}
	if rep.Analyze == nil || !rep.Analyze.Skipped {
		t.Fatalf("ожидали пропущенный анализатор, got %#v", rep.Analyze)
	}
	if rep.Verdict != VerdictApprove {
		t.Fatalf("отсутствие инструментов не должно влиять на вердикт, got %s (%s)", rep.Verdict, rep.Summary)
	}
}
