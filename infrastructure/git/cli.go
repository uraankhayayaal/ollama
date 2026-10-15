package git

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// CLIExecutor — реальный исполнитель git-команд через git CLI. Используется
// сервером Web UI (Ф-2-3): единственная реализация Executor вне тестов.
// В тестах подставляются fake-исполнители (собесредственный dry-run).
type CLIExecutor struct{}

// workflowScopeRefused распознаёт отказ GitHub по отсутствию scope `workflow`
// (реальный текст: «…to create or update workflow `.github/workflows/ci.yml`
// without `workflow` scope»).
func workflowScopeRefused(msg string) bool {
	return strings.Contains(msg, "without `workflow` scope") ||
		strings.Contains(msg, "without 'workflow' scope")
}

// Exec запускает argv (первый элемент — "git") в каталоге dir и возвращает
// stdout. Для диагностики stderr при ошибке включается в текст ошибки:
// команды git пишут объяснение именно в stderr. Секреты (токен в URL push)
// маскируются; отказ GitHub по scope workflow заворачивается в ErrWorkflowScope.
func (CLIExecutor) Exec(ctx context.Context, dir string, argv ...string) (string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), nil
	}
	msg := RedactSecrets(strings.TrimSpace(string(out)))
	command := RedactSecrets(strings.Join(argv, " "))
	switch {
	case msg == "":
		return "", fmt.Errorf("gitops: %s: %w", command, err)
	case workflowScopeRefused(msg):
		return "", fmt.Errorf("gitops: %s: %w: %s: %w", command, err, msg, ErrWorkflowScope)
	default:
		return "", fmt.Errorf("gitops: %s: %w: %s", command, err, msg)
	}
}
