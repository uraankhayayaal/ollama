package gitops

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// CLIExecutor — реальный исполнитель git-команд через git CLI. Используется
// сервером Web UI (Ф-2-3): единственная реализация Executor вне тестов.
// В тестах подставляются fake-исполнители (собесредственный dry-run).
type CLIExecutor struct{}

// ErrWorkflowScope — GitHub отклонил push: у токена форджа (GITHUB_TOKEN)
// нет scope `workflow`, а ветка меняет `.github/workflows/*`. Перехватывается
// вызывающим слоем (авто-MR/авто-мёрдж), чтобы показать на доске понятную
// инструкцию вместо сырого вывода git.
var ErrWorkflowScope = errors.New("у токена форджа нет scope `workflow`: GitHub отклоняет push веток с правками .github/workflows/* — добавь scope `workflow` к GITHUB_TOKEN")

// tokenInURL маскирует userinfo credentialed URL push
// (https://x-access-token:<токен>@host/…) — пароль userinfo не должен
// попадать в логи проекта и ответы REST.
var tokenInURL = regexp.MustCompile(`(://[^:/@\s]+):[^@\s]+@`)

// knownTokens маскирует известные префиксы токенов GitHub/GitLab, если они
// попали в текст не в URL (например, в stderr git или сообщении API-клиента).
var knownTokens = regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{16,}|glpat-[A-Za-z0-9_-]{16,}`)

// RedactSecrets скрывает секреты в тексте git-команд и вывода git: пароль в
// userinfo credentialed URL и известные префиксы токенов GitHub/GitLab.
// Вызывается в CLIExecutor, чтобы токен push не оседал в логах и ошибках.
func RedactSecrets(s string) string {
	s = tokenInURL.ReplaceAllString(s, "$1:***@")
	return knownTokens.ReplaceAllString(s, "***")
}

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
