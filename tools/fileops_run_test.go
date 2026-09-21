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
	out, err := runCommand("definitely-not-installed-xyz build", t.TempDir())
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

	// Успешная команда подсказки не получает.
	ok, err := runCommand("echo hi", t.TempDir())
	if err != nil {
		t.Fatalf("runCommand: %v", err)
	}
	if ok["hint"] != "" {
		t.Fatalf("успешной команде подсказка не нужна, got %q", ok["hint"])
	}
}
