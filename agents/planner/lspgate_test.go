// Тесты scope-гейта ЛСП по шагу (Ф-5). Точка входа к языковому серверу
// подменяется, реальные серверы не запускаются.

package planner

import (
	"ai/agents"
	"ai/projects"
	"ai/runner"
	"ai/tools"
	"context"
	"os"
	"path/filepath"
	"testing"
)

type lspGateProvider struct{}

func (p *lspGateProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	return &runner.AgentResponse{}, nil
}

func (p *lspGateProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

func withStepLSPFind(t *testing.T, diags []tools.LSPDiag, handled bool) {
	t.Helper()
	old := stepLSPFind
	stepLSPFind = func(_ string, _ []string) ([]tools.LSPDiag, bool) {
		return diags, handled
	}
	t.Cleanup(func() { stepLSPFind = old })
}

// Включение гейта: по умолчанию вкл., выключающие значения — вкл-выкл.
func TestStepLSPGateEnabled(t *testing.T) {
	for _, v := range []string{"", "1", "true", "on", "yes"} {
		t.Setenv("LSP_STEP_GATE", v)
		if !stepLSPGateEnabled() {
			t.Fatalf("LSP_STEP_GATE=%q должен включать гейт", v)
		}
	}
	for _, v := range []string{"0", "false", "off", "no", "False", "OFF"} {
		t.Setenv("LSP_STEP_GATE", v)
		if stepLSPGateEnabled() {
			t.Fatalf("LSP_STEP_GATE=%q должен выключать гейт", v)
		}
	}
}

// scopeSourceFiles: разворачивает файлы и директории scope, игнорирует
// служебные каталоги, уважает лимит, пропускает несуществующие пути.
func TestScopeSourceFiles(t *testing.T) {
	dir := t.TempDir()
	files := []string{
		"main.go",
		"internal/handler.go",
		"internal/helper.py",
		"web/App.tsx",
		"node_modules/lib.js",
		"docs/readme.md",
		"scripts/build.sh",
	}
	for _, f := range files {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	got := scopeSourceFiles(dir, []string{"main.go", "internal", "web/App.tsx", "нет такого"}, 100)
	want := []string{"main.go", "internal/handler.go", "internal/helper.py", "web/App.tsx"}
	if !sameStrings(got, want) {
		t.Fatalf("scope развёрнут неверно:\n got %#v\nwant %#v", got, want)
	}

	limited := scopeSourceFiles(dir, []string{"internal", "web"}, 1)
	if len(limited) != 1 {
		t.Fatalf("лимит не соблюдён: %#v", limited)
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Шаг завершился успешно, но ошибка уровня scope — шаг упал, его область
// откачена (созданные файлы удалены), план остановлен.
func TestStepLSPGateFailsStep(t *testing.T) {
	t.Setenv("CODEGEN_ROLLBACK_ON_FAIL", "1")
	ctx := context.Background()
	name := "LSPGateFail"
	root := projects.ProjectDir(name)
	defer os.RemoveAll(filepath.Clean(root))

	// Существующий до шага функционал.
	if err := os.MkdirAll(filepath.Join(root, "server"), 0755); err != nil {
		t.Fatal(err)
	}
	writePlanFile(t, filepath.Join(root, "server", "keep.go"), "package main\nfunc Keep() {}\n")

	plan := &Plan{
		ProjectName: name,
		Summary:     "гейт по scope",
		Steps: []Step{{
			ID: "g1", Agent: AgentBackendDev, Prompt: "сделай",
			Description: "генерация", Scope: []string{"server"},
		}},
	}
	withStepLSPFind(t, []tools.LSPDiag{
		{File: "server/handler.go", Line: 3, Col: 5, Severity: "error", Message: "undeclared name: Foo"},
	}, true)

	exec := NewExecutor(&lspGateProvider{}, plan)
	if err := exec.Run(ctx); err == nil {
		t.Fatal("гейт с ошибкой в scope должен остановить план")
	}
	if exec.completed["g1"] {
		t.Fatal("провалившийся гейтом шаг не может быть completed")
	}
	if _, err := os.Stat(filepath.Join(root, "server", "keep.go")); err != nil {
		t.Fatalf("чужой функционал повреждён: %v", err)
	}
}

func writePlanFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// В scope чисто либо есть только предупреждения — шаг проходит, план успешен.
func TestStepLSPGatePasses(t *testing.T) {
	t.Setenv("CODEGEN_ROLLBACK_ON_FAIL", "1")
	ctx := context.Background()
	name := "LSPGateOK"
	root := projects.ProjectDir(name)
	defer os.RemoveAll(filepath.Clean(root))
	writePlanFile(t, filepath.Join(root, "main.go"), "package main\nfunc X() {}\n")

	plan := &Plan{
		ProjectName: name,
		Summary:     "чистый гейт",
		Steps: []Step{{
			ID: "g2", Agent: AgentBackendDev, Prompt: "сделай",
			Description: "генерация", Scope: []string{"main.go"},
		}},
	}
	withStepLSPFind(t, []tools.LSPDiag{
		{File: "main.go", Line: 2, Severity: "warning", Message: "style"},
	}, true)

	exec := NewExecutor(&lspGateProvider{}, plan)
	if err := exec.Run(ctx); err != nil {
		t.Fatalf("предупреждения не должны останавливать гейт: %v", err)
	}
	if !exec.completed["g2"] {
		t.Fatal("шаг должен быть completed")
	}
}

// Гейт выключен — ошибки в scope не мешают шагу.
func TestStepLSPGateDisabled(t *testing.T) {
	t.Setenv("LSP_STEP_GATE", "0")
	t.Setenv("CODEGEN_ROLLBACK_ON_FAIL", "1")
	ctx := context.Background()
	name := "LSPGateOff"
	root := projects.ProjectDir(name)
	defer os.RemoveAll(filepath.Clean(root))
	writePlanFile(t, filepath.Join(root, "main.go"), "package main\nfunc X() {}\n")

	plan := &Plan{
		ProjectName: name,
		Summary:     "выключенный гейт",
		Steps: []Step{{
			ID: "g3", Agent: AgentBackendDev, Prompt: "сделай",
			Description: "генерация", Scope: []string{"main.go"},
		}},
	}
	withStepLSPFind(t, []tools.LSPDiag{
		{File: "main.go", Line: 5, Severity: "error", Message: "boom"},
	}, true)

	exec := NewExecutor(&lspGateProvider{}, plan)
	if err := exec.Run(ctx); err != nil {
		t.Fatalf("выключенный гейт не должен ронять план: %v", err)
	}
	if !exec.completed["g3"] {
		t.Fatal("шаг должен быть completed")
	}
}

// Сервера нет (handled=false) — гейт неактивен, деградация без ошибок.
func TestStepLSPGateNoServer(t *testing.T) {
	t.Setenv("CODEGEN_ROLLBACK_ON_FAIL", "1")
	ctx := context.Background()
	name := "LSPGateNoSrv"
	root := projects.ProjectDir(name)
	defer os.RemoveAll(filepath.Clean(root))
	writePlanFile(t, filepath.Join(root, "main.go"), "package main\nfunc X() {}\n")

	plan := &Plan{
		ProjectName: name,
		Summary:     "нет сервера",
		Steps: []Step{{
			ID: "g4", Agent: AgentBackendDev, Prompt: "сделай",
			Description: "генерация", Scope: []string{"main.go"},
		}},
	}
	withStepLSPFind(t, nil, false)

	exec := NewExecutor(&lspGateProvider{}, plan)
	if err := exec.Run(ctx); err != nil {
		t.Fatalf("без сервера гейт должен пропускать: %v", err)
	}
	if !exec.completed["g4"] {
		t.Fatal("шаг должен быть completed")
	}
}

// Scope не содержит исходников — файлов для проверки нет, гейт молчит.
func TestStepLSPGateSkipsNoSource(t *testing.T) {
	ctx := context.Background()
	name := "LSPGateNoSrc"
	root := projects.ProjectDir(name)
	defer os.RemoveAll(filepath.Clean(root))
	writePlanFile(t, filepath.Join(root, "README.md"), "# test\n")

	plan := &Plan{
		ProjectName: name,
		Summary:     "нет исходников",
		Steps: []Step{{
			ID: "g5", Agent: AgentBackendDev, Prompt: "сделай",
			Description: "генерация", Scope: []string{"README.md"},
		}},
	}
	withStepLSPFind(t, []tools.LSPDiag{
		{File: "x.go", Severity: "error", Message: "boom"},
	}, true)

	exec := NewExecutor(&lspGateProvider{}, plan)
	if err := exec.Run(ctx); err != nil {
		t.Fatalf("без исходников в scope гейт не срабатывает: %v", err)
	}
	if !exec.completed["g5"] {
		t.Fatal("шаг должен быть completed")
	}
}
