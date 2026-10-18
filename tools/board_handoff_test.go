package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandoffGateSkipsWithoutContext(t *testing.T) {
	if err := handoffGate(nil); err != nil {
		t.Fatalf("nil FileOps должен пропускать гейт: %v", err)
	}
	if err := handoffGate(&FileOps{}); err != nil {
		t.Fatalf("пустая рабочая директория должна пропускать гейт: %v", err)
	}
	if err := handoffGate(&FileOps{OutputDir: filepath.Join(t.TempDir(), "does-not-exist")}); err != nil {
		t.Fatalf("несуществующая директория должна пропускать гейт: %v", err)
	}
}

func TestHandoffGateDisabledByEnv(t *testing.T) {
	t.Setenv("CODEGEN_HANDOFF_GATE", "off")
	dir := t.TempDir()
	// Грязная директория + подменённый раннер, который бы сблокировал.
	stubHandoffRun(t, func(cmd, dir, project string) (map[string]string, error) {
		return map[string]string{"status": "success", "stdout": " M main.go"}, nil
	})
	if err := handoffGate(&FileOps{OutputDir: dir}); err != nil {
		t.Fatalf("CODEGEN_HANDOFF_GATE=off должен выключать гейт: %v", err)
	}
}

func TestHandoffBuildCommand(t *testing.T) {
	t.Setenv("CODEGEN_HANDOFF_CMD", "")
	goDir := t.TempDir()
	writeHandoffFile(t, goDir, "go.mod", "module x\n")
	if got := handoffBuildCommand(goDir); got != "go build ./..." {
		t.Fatalf("go.mod → %q, ожидалось go build ./...", got)
	}
	nodeDir := t.TempDir()
	writeHandoffFile(t, nodeDir, "package.json", "{}")
	if got := handoffBuildCommand(nodeDir); got != "npm run build --if-present" {
		t.Fatalf("package.json → %q", got)
	}
	emptyDir := t.TempDir()
	if got := handoffBuildCommand(emptyDir); got != "" {
		t.Fatalf("без маркеров → %q, ожидалось пусто", got)
	}
	t.Setenv("CODEGEN_HANDOFF_CMD", "make build")
	if got := handoffBuildCommand(emptyDir); got != "make build" {
		t.Fatalf("CODEGEN_HANDOFF_CMD не применён: %q", got)
	}
}

// TestHandoffGateBlocksDirtyTrackedChanges: незакоммиченные правки
// отслеживаемых файлов блокируют передачу в приёмку.
func TestHandoffGateBlocksDirtyTrackedChanges(t *testing.T) {
	dir := t.TempDir()
	stubHandoffRun(t, func(cmd, dir, project string) (map[string]string, error) {
		if strings.Contains(cmd, "git status") {
			return map[string]string{"status": "success", "stdout": " M app.go\n"}, nil
		}
		return map[string]string{"status": "success"}, nil
	})
	err := handoffGate(&FileOps{OutputDir: dir})
	if err == nil || !strings.Contains(err.Error(), "незакоммиченные") {
		t.Fatalf("ожидался отказ по незакоммиченным изменениям, got %v", err)
	}
}

// TestHandoffGateBlocksBrokenBuild: сломанная сборка блокирует передачу.
func TestHandoffGateBlocksBrokenBuild(t *testing.T) {
	dir := t.TempDir()
	writeHandoffFile(t, dir, "go.mod", "module x\n")
	stubHandoffRun(t, func(cmd, dir, project string) (map[string]string, error) {
		if strings.Contains(cmd, "git status") {
			return map[string]string{"status": "success"}, nil
		}
		return map[string]string{
			"status":     "error",
			"exit_error": "exit status 1",
			"stderr":     "./app.go:3:1: syntax error",
		}, nil
	})
	err := handoffGate(&FileOps{OutputDir: dir})
	if err == nil || !strings.Contains(err.Error(), "сборка не проходит") {
		t.Fatalf("ожидался отказ по сборке, got %v", err)
	}
}

// TestHandoffGateDegradesOnInfra: инфраструктурный сбой проверки не блокирует.
func TestHandoffGateDegradesOnInfra(t *testing.T) {
	dir := t.TempDir()
	writeHandoffFile(t, dir, "go.mod", "module x\n")
	stubHandoffRun(t, func(cmd, dir, project string) (map[string]string, error) {
		return map[string]string{
			"status":  "error",
			"message": "песочница не настроена",
		}, nil
	})
	if err := handoffGate(&FileOps{OutputDir: dir}); err != nil {
		t.Fatalf("инфра-деград не должен блокировать гейт: %v", err)
	}
}

// TestHandoffGatePassesCleanBuild: чистое дерево и зелёная сборка пропускают.
func TestHandoffGatePassesCleanBuild(t *testing.T) {
	dir := t.TempDir()
	writeHandoffFile(t, dir, "go.mod", "module x\n")
	stubHandoffRun(t, func(cmd, dir, project string) (map[string]string, error) {
		return map[string]string{"status": "success", "stdout": ""}, nil
	})
	if err := handoffGate(&FileOps{OutputDir: dir}); err != nil {
		t.Fatalf("чистое дерево + сборка должны пропускать: %v", err)
	}
}

func stubHandoffRun(t *testing.T, fn func(cmd, dir, project string) (map[string]string, error)) {
	t.Helper()
	orig := handoffRunCommand
	handoffRunCommand = fn
	t.Cleanup(func() { handoffRunCommand = orig })
}

func writeHandoffFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
