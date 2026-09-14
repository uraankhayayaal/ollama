package tools

import (
	"strings"
	"testing"
	"time"
)

// TestRunCommandTimeout — долгоживущая команда (npm run dev, сервер) не должна
// вешать шаг навсегда: runCommand ограничен по времени, процесс и его группа
// убиваются, а в результате есть понятное сообщение о таймауте.
func TestRunCommandTimeout(t *testing.T) {
	t.Setenv("CODEGEN_RUN_TIMEOUT", "300ms")

	start := time.Now()
	out, err := runCommand("sleep 5", t.TempDir())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("runCommand: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("команда не была прервана по таймауту, прошло %s", elapsed)
	}
	if out["status"] != "error" {
		t.Fatalf("ожидался статус error, got %q", out["status"])
	}
	if !strings.Contains(out["message"], "timeout") {
		t.Fatalf("ожидалось сообщение о таймауте, got %q", out["message"])
	}
	if out["exit_error"] == "" {
		t.Fatal("ожидалась информация об ошибке завершения (signal: killed)")
	}
}

// TestRunCommandSuccess — быстрая команда завершается успешно и без артефактов
// таймаута (сообщения timeout быть не должно).
func TestRunCommandSuccess(t *testing.T) {
	out, err := runCommand("echo hello", t.TempDir())
	if err != nil {
		t.Fatalf("runCommand: %v", err)
	}
	if out["status"] != "success" {
		t.Fatalf("ожидался статус success, got %q", out["status"])
	}
	if !strings.Contains(out["stdout"], "hello") {
		t.Fatalf("stdout = %q, ожидалось hello", out["stdout"])
	}
	if out["message"] != "" {
		t.Fatalf("успешная команда не должна нести сообщение о таймауте, got %q", out["message"])
	}
}