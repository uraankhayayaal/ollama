package gitops

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

// Exec запускает argv (первый элемент — "git") в каталоге dir и возвращает
// stdout. Для диагностики stderr при ошибке включается в текст ошибки:
// команды git пишут объяснение именно в stderr.
func (CLIExecutor) Exec(ctx context.Context, dir string, argv ...string) (string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return "", fmt.Errorf("gitops: %s: %w", strings.Join(argv, " "), err)
		}
		return "", fmt.Errorf("gitops: %s: %w: %s", strings.Join(argv, " "), err, msg)
	}
	return string(out), nil
}
