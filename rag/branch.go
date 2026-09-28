// Определение текущей ветки и коммита Git для версионированного RAG-индекса
// (см. PLAN-2026-09-27-done-branch-aware-rag.md, Р-6).
//
// Агент-разработчик работает в worktree своей ветки (ai/task/<id>), поэтому
// ветка его кода — это `git rev-parse --abbrev-ref HEAD` в рабочем каталоге.
// Проекты без Git (не gitflow) — MainBranch: индекс работает как раньше, а
// выдача не различает ветку. Определение ветки кэшируется вызывающей стороной
// (tools.detectBranch): в рамках шага агента ветка не меняется.

package rag

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// MainBranch — ветка по умолчанию: проекты без Git и «чистая» линия проекта
// (ветки эпиков/задач отводятся от неё, см. server/gitflow.go).
const MainBranch = "main"

// gitTimeout — предел одного git-вызова: определение ветки не должно
// подвесить шаг агента на сети/блокировке индекса.
const gitTimeout = 5 * time.Second

// gitRunner — исполнятель git-команд определения ветки/коммита. Заменяется в
// тестах (hermetic: без git-бинаря и репозитория).
var gitRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// DetectBranch возвращает имя текущей ветки git в каталоге dir
// (`git rev-parse --abbrev-ref HEAD`). Ошибка — каталог не git-репозиторий
// либо git недоступен: вызывающий решает сам (обычно MainBranch).
func DetectBranch(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errNotGitDir
	}
	out, err := gitRunner(context.Background(), dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(out)
	if branch == "" {
		return "", errNotGitDir
	}
	// Отсоединённый HEAD: git отдаёт "HEAD" — такой код ветки не знает.
	if branch == "HEAD" {
		return "", errNotGitDir
	}
	return branch, nil
}

// DetectCommit возвращает полный SHA HEAD в каталоге dir
// (`git rev-parse HEAD`), он же версия индексации. Ошибка — как у DetectBranch.
func DetectCommit(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errNotGitDir
	}
	out, err := gitRunner(context.Background(), dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return "", errNotGitDir
	}
	return sha, nil
}

// DetectIndexOptions собирает параметры индексации по состоянию рабочего
// каталога: ветка и коммит git, а вне git — MainBranch без коммита (точки
// заменяют друг друга без версионирования). Функция не падает: не-git и
// недоступный git — обычный случай для консольных проектов.
func DetectIndexOptions(dir string) IndexOptions {
	branch, err := DetectBranch(dir)
	if err != nil {
		return IndexOptions{Branch: MainBranch}
	}
	commit, err := DetectCommit(dir)
	if err != nil {
		return IndexOptions{Branch: branch}
	}
	return IndexOptions{Branch: branch, CommitSHA: commit}
}

// errNotGitDir — каталог не является git-репозиторием (или HEAD отсоединён).
var errNotGitDir = errors.New("каталог не является git-репозиторием")
