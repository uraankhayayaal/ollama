package tools

import (
	"context"
	"os/exec"
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
	out, err := runCommand("sleep 5", t.TempDir(), "")
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
	out, err := runCommand("echo hello", t.TempDir(), "")
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

// TestLongRunningHintBlocked — запуск приложения/сервера распознаётся как
// долгоживущая команда и возвращает хинт; сборка и автотесты — проходят.
func TestLongRunningHintBlocked(t *testing.T) {
	blocked := []string{
		"go run server/main.go",
		"go run .",
		"go run ./cmd/server",
		"npm run dev",
		"npm start",
		"pnpm run dev",
		"yarn dev",
		"yarn start",
		"next dev",
		"vite dev",
		"ng serve",
		"uvicorn app:app --host 0.0.0.0",
		"python main.py",
		"docker compose up -d",
	}
	for _, cmd := range blocked {
		hint, ok := longRunningHint(cmd)
		if !ok || !strings.Contains(hint, "завис") {
			t.Errorf("команда должно быть заблокирована: %q (hint=%q)", cmd, hint)
		}
	}

	allowed := []string{
		"go build ./...",
		"go vet ./...",
		"go test ./...",
		"go run ./scripts/check.go",
		"npm run build",
		"yarn build",
		"pnpm build",
		"npm test",
		"docker compose config",
		"go run server/main.go & sleep 2 && curl localhost:8080",
	}
	for _, cmd := range allowed {
		if hint, ok := longRunningHint(cmd); ok {
			t.Errorf("команда не должна блокироваться: %q (hint=%q)", cmd, hint)
		}
	}
}

// TestRunCommandMissingToolHint — команда несуществующим в окружении хоста
// инструментом (cargo, модуль Python) не должна оставлять модель в догадках:
// в результате появляется подсказка выполнять проверки в окружении проекта
// (контейнер), иначе агент тратит раунды на which/find/pip install.
func TestRunCommandMissingToolHint(t *testing.T) {
	out, err := runCommand("definitely-not-installed-xyz build", t.TempDir(), "")
	if err != nil {
		t.Fatalf("runCommand: %v", err)
	}
	if out["status"] != "error" {
		t.Fatalf("ожидался статус error, got %q", out["status"])
	}
	hint := out["hint"]
	if !strings.Contains(hint, "docker compose run") {
		t.Fatalf("ожидалась подсказка про окружение проекта, got %q", hint)
	}
	if !strings.Contains(hint, "definitely-not-installed-xyz") {
		t.Fatalf("подсказка должна называть команду, got %q", hint)
	}
	// Ф-4 PLAN-2026-09-24-todo-makefile.md: при наличии Makefile-контракта
	// подсказка направляет на инфра-цели 'make infra.<цель>'.
	if !strings.Contains(hint, "make infra.<цель>") {
		t.Fatalf("ожидалась подсказка про инфра-цели Makefile, got %q", hint)
	}

	// Успешная команда подсказки не получает.
	ok, err := runCommand("echo hi", t.TempDir(), "")
	if err != nil {
		t.Fatalf("runCommand: %v", err)
	}
	if ok["hint"] != "" {
		t.Fatalf("успешной команде подсказка не нужна, got %q", ok["hint"])
	}
}

// TestTruncateRunOutputUnit — чистая функция обрезки: короткий вывод без
// изменений, длинный обрезается с маркером, граница ровно на лимите.
func TestTruncateRunOutputUnit(t *testing.T) {
	short := strings.Repeat("a", 100)
	if out, trunc := truncateRunOutput(short, 200); trunc || out != short {
		t.Errorf("короткий вывод не должен обрезаться: trunc=%v", trunc)
	}
	exact := strings.Repeat("b", 200)
	if out, trunc := truncateRunOutput(exact, 200); trunc || out != exact {
		t.Errorf("вывод ровно в лимит не должен обрезаться: trunc=%v", trunc)
	}
	long := strings.Repeat("c", 500)
	out, trunc := truncateRunOutput(long, 200)
	if !trunc {
		t.Fatal("длинный вывод должен обрезаться")
	}
	if !strings.HasPrefix(out, strings.Repeat("c", 200)) {
		t.Errorf("обрезанный вывод должен начинаться с первых %d символов", 200)
	}
	if !strings.Contains(out, "вывод обрезан") || !strings.Contains(out, "500") {
		t.Errorf("маркер обрезки должен называть лимит и полный объём: %q", out[200:])
	}
}

// TestRunCommandOutputTruncated — Ф-3: длинный вывод обрезается, маркер и флаг
// truncated на месте, статус success и код выхода не ломаются. Лимит
// уменьшен через env — тест не генерирует мегабайты.
func TestRunCommandOutputTruncated(t *testing.T) {
	t.Setenv("CODEGEN_RUN_MAX_OUTPUT", "100")
	t.Setenv("CODEGEN_RUN_TIMEOUT", "10s")
	workdir := t.TempDir()
	res, err := runCommandSandbox("noisy-check", workdir, sandboxConfig{
		Mode: SandboxModeLocal,
		LocalCommand: func(command, wd string) (*exec.Cmd, error) {
			return exec.CommandContext(context.Background(), "sh", "-c",
				"yes tralala | head -200"), nil
		},
	})
	if err != nil {
		t.Fatalf("runCommandSandbox: %v", err)
	}
	if res["status"] != "success" {
		t.Fatalf("ожидался success, got %q (%v)", res["status"], res["message"])
	}
	if res["stdout_truncated"] != "true" {
		t.Fatalf("ожидался флаг stdout_truncated, got %q", res["stdout_truncated"])
	}
	if !strings.Contains(res["stdout"], "вывод обрезан") {
		t.Fatalf("ожидался маркер обрезки в stdout: %q", res["stdout"][:80])
	}
	if strings.Contains(res["stdout"], "tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala tralala") {
		t.Error("вывод не обрезан: виден хвост сверх лимита")
	}
}

// TestRunCommandShortOutputUntouched — короткий вывод не обрезается и флага
// truncated не получает.
func TestRunCommandShortOutputUntouched(t *testing.T) {
	res, err := runCommand("echo hi", t.TempDir(), "")
	if err != nil {
		t.Fatalf("runCommand: %v", err)
	}
	if res["stdout_truncated"] != "" {
		t.Errorf("короткий вывод не должен иметь флаг обрезки: %q", res["stdout_truncated"])
	}
	if !strings.Contains(res["stdout"], "hi") {
		t.Errorf("stdout = %q", res["stdout"])
	}
}

// TestRunCommandTruncationKeepsErrorGates — Ф-3: обрезка не меняет гейты:
// упавшая команда остаётся error с exit_error, обрезается только текст.
func TestRunCommandTruncationKeepsErrorGates(t *testing.T) {
	t.Setenv("CODEGEN_RUN_MAX_OUTPUT", "100")
	t.Setenv("CODEGEN_RUN_TIMEOUT", "10s")
	workdir := t.TempDir()
	res, err := runCommandSandbox("failing-noisy", workdir, sandboxConfig{
		Mode: SandboxModeLocal,
		LocalCommand: func(command, wd string) (*exec.Cmd, error) {
			return exec.CommandContext(context.Background(), "sh", "-c",
				"yes tralala | head -200; exit 1"), nil
		},
	})
	if err != nil {
		t.Fatalf("runCommandSandbox: %v", err)
	}
	if res["status"] != "error" {
		t.Fatalf("ожидался error, got %q", res["status"])
	}
	if res["exit_error"] == "" {
		t.Fatal("exit_error должен остаться после обрезки")
	}
	if res["stdout_truncated"] != "true" {
		t.Fatalf("ожидался флаг stdout_truncated, got %q", res["stdout_truncated"])
	}
}
