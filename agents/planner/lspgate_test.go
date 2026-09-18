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

// lspWriteProvider пишет файл в проект и возвращает пустой ответ — по
// окончании снимок (snap) видит его как добавленный, гейт проверяет.
type lspWriteProvider struct {
	filename string
	content  string
}

func (p *lspWriteProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	_, err := agent.CallFunction("WriteFiles", map[string]any{
		"files": []map[string]any{
			{"filename": p.filename, "content": p.content},
		},
	})
	if err != nil {
		return nil, err
	}
	return &runner.AgentResponse{}, nil
}

func (p *lspWriteProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

// lspNoopProvider не меняет ничего — гейт видит пустой diff и пропускается.
type lspNoopProvider struct{}

func (p *lspNoopProvider) Generate(_ context.Context, _ agents.Agent) (*runner.AgentResponse, error) {
	return &runner.AgentResponse{}, nil
}
func (p *lspNoopProvider) ChatOnce(_ context.Context, _ agents.Agent, _ []runner.Message) (*runner.ModelReply, error) {
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

func writePlanFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// Субагент записал файл в scope — гейт видит ошибку → шаг падает, откат
// удаляет файл, «чужой» функционал сохраняется.
func TestStepLSPGateFailsOnChangedFile(t *testing.T) {
	t.Setenv("CODEGEN_ROLLBACK_ON_FAIL", "1")
	ctx := context.Background()
	name := "LSPGateFailChanged"
	root := projects.ProjectDir(name)
	defer os.RemoveAll(filepath.Clean(root))
	writePlanFile(t, filepath.Join(root, "server", "keep.go"), "package main\nfunc Keep() {}\n")

	plan := &Plan{
		ProjectName: name,
		Summary:     "гейт по изменённым",
		Steps: []Step{{
			ID: "g1", Agent: AgentBackendDev, Prompt: "сделай",
			Description: "генерация", Scope: []string{"server"},
		}},
	}
	withStepLSPFind(t, []tools.LSPDiag{
		{File: "server/hack.go", Line: 1, Severity: "error", Message: "undeclared name: Foo"},
	}, true)

	exec := NewExecutor(&lspWriteProvider{filename: "server/hack.go", content: "package main\nfunc Hack() {}\n"}, plan)
	if err := exec.Run(ctx); err == nil {
		t.Fatal("гейт с ошибкой в изменённом файле должен остановить план")
	}
	if exec.completed["g1"] {
		t.Fatal("провалившийся гейтом шаг не может быть completed")
	}
	if _, err := os.Stat(filepath.Join(root, "server", "hack.go")); err == nil {
		t.Fatal("файл упавшего субагента должен быть откачен")
	}
	if _, err := os.Stat(filepath.Join(root, "server", "keep.go")); err != nil {
		t.Fatalf("чужой функционал повреждён при откате: %v", err)
	}
}

// Без изменений файлов — diff пуст, гейт пропускается (субагент ничего не менял).
func TestStepLSPGateSkipsNoChanges(t *testing.T) {
	ctx := context.Background()
	name := "LSPGateSkipNoChanges"
	root := projects.ProjectDir(name)
	defer os.RemoveAll(filepath.Clean(root))
	writePlanFile(t, filepath.Join(root, "main.go"), "package main\nfunc X() {}\n")

	plan := &Plan{
		ProjectName: name,
		Summary:     "пустой diff",
		Steps: []Step{{
			ID: "g2", Agent: AgentBackendDev, Prompt: "сделай",
			Description: "генерация", Scope: []string{"main.go"},
		}},
	}
	withStepLSPFind(t, []tools.LSPDiag{{File: "x.go", Severity: "error", Message: "boom"}}, true)

	exec := NewExecutor(&lspNoopProvider{}, plan)
	if err := exec.Run(ctx); err != nil {
		t.Fatalf("без изменений гейт должен пропускать: %v", err)
	}
	if !exec.completed["g2"] {
		t.Fatal("шаг должен быть completed")
	}
}

// Предупреждения в изменённом файле — гейт не падает.
func TestStepLSPGatePassesWarnings(t *testing.T) {
	t.Setenv("CODEGEN_ROLLBACK_ON_FAIL", "1")
	ctx := context.Background()
	name := "LSPGatePassWarnings"
	root := projects.ProjectDir(name)
	defer os.RemoveAll(filepath.Clean(root))
	writePlanFile(t, filepath.Join(root, "main.go"), "package main\nfunc X() {}\n")

	plan := &Plan{
		ProjectName: name,
		Summary:     "только ворнинги",
		Steps: []Step{{
			ID: "g3", Agent: AgentBackendDev, Prompt: "сделай",
			Description: "генерация", Scope: []string{"."},
		}},
	}
	withStepLSPFind(t, []tools.LSPDiag{
		{File: "main.go", Line: 2, Severity: "warning", Message: "style"},
	}, true)

	exec := NewExecutor(&lspWriteProvider{filename: "app.go", content: "package main\nfunc App() {}\n"}, plan)
	if err := exec.Run(ctx); err != nil {
		t.Fatalf("предупреждения не должны останавливать гейт: %v", err)
	}
	if !exec.completed["g3"] {
		t.Fatal("шаг должен быть completed")
	}
}

// Гейт выключен — ошибки в изменённых файлах не мешают шагу.
func TestStepLSPGateDisabled(t *testing.T) {
	t.Setenv("LSP_STEP_GATE", "0")
	t.Setenv("CODEGEN_ROLLBACK_ON_FAIL", "1")
	ctx := context.Background()
	name := "LSPGateDisabled"
	root := projects.ProjectDir(name)
	defer os.RemoveAll(filepath.Clean(root))
	writePlanFile(t, filepath.Join(root, "main.go"), "package main\nfunc X() {}\n")

	plan := &Plan{
		ProjectName: name,
		Summary:     "выключенный гейт",
		Steps: []Step{{
			ID: "g4", Agent: AgentBackendDev, Prompt: "сделай",
			Description: "генерация", Scope: []string{"."},
		}},
	}
	withStepLSPFind(t, []tools.LSPDiag{
		{File: "main.go", Line: 5, Severity: "error", Message: "boom"},
	}, true)

	exec := NewExecutor(&lspWriteProvider{filename: "bad.go", content: "bad"}, plan)
	if err := exec.Run(ctx); err != nil {
		t.Fatalf("выключенный гейт не должен ронять план: %v", err)
	}
	if !exec.completed["g4"] {
		t.Fatal("шаг должен быть completed")
	}
}

// Сервера нет (handled=false) — гейт неактивен, деградация без ошибок.
func TestStepLSPGateNoServer(t *testing.T) {
	t.Setenv("CODEGEN_ROLLBACK_ON_FAIL", "1")
	ctx := context.Background()
	name := "LSPGateNoServer"
	root := projects.ProjectDir(name)
	defer os.RemoveAll(filepath.Clean(root))
	writePlanFile(t, filepath.Join(root, "main.go"), "package main\nfunc X() {}\n")

	plan := &Plan{
		ProjectName: name,
		Summary:     "нет сервера",
		Steps: []Step{{
			ID: "g5", Agent: AgentBackendDev, Prompt: "сделай",
			Description: "генерация", Scope: []string{"."},
		}},
	}
	withStepLSPFind(t, nil, false)

	exec := NewExecutor(&lspWriteProvider{filename: "a.go", content: "package main\n"}, plan)
	if err := exec.Run(ctx); err != nil {
		t.Fatalf("без сервера гейт должен пропускать: %v", err)
	}
	if !exec.completed["g5"] {
		t.Fatal("шаг должен быть completed")
	}
}

// Изменённые файлы не являются исходниками (README, картинки) — гейт молчит.
func TestStepLSPGateIgnoresNonSource(t *testing.T) {
	t.Setenv("CODEGEN_ROLLBACK_ON_FAIL", "1")
	ctx := context.Background()
	name := "LSPGateNonSource"
	root := projects.ProjectDir(name)
	defer os.RemoveAll(filepath.Clean(root))
	writePlanFile(t, filepath.Join(root, "main.go"), "package main\nfunc X() {}\n")

	plan := &Plan{
		ProjectName: name,
		Summary:     "не-исходники",
		Steps: []Step{{
			ID: "g6", Agent: AgentBackendDev, Prompt: "сделай",
			Description: "генерация", Scope: []string{"."},
		}},
	}
	withStepLSPFind(t, []tools.LSPDiag{
		{File: "x.go", Severity: "error", Message: "boom"},
	}, true)

	// Субагент пишет только не-исходник — diff его видит, но gate отфильтрует.
	exec := NewExecutor(&lspWriteProvider{filename: "docs/notes.md", content: "# notes\n"}, plan)
	if err := exec.Run(ctx); err != nil {
		t.Fatalf("не-исходники не должны вызывать гейт: %v", err)
	}
	if !exec.completed["g6"] {
		t.Fatal("шаг должен быть completed")
	}
}
