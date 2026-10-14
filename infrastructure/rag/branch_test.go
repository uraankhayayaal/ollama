package rag

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// withGit подменяет git-вызовы на детерминированный fake: отвечает по
// подстроке аргументов команды. Возвращает счётчик вызовов для проверки, что
// определение версии кэшируется вызывающей стороной и не дёргает git на каждый
// поиск (в tools ветка кэшируется в codeSearchTool).
func withGit(t *testing.T, branch, commit string, brErr, cmErr error) *int {
	t.Helper()
	calls := 0
	orig := gitRunner
	gitRunner = func(ctx context.Context, dir string, args ...string) (string, error) {
		calls++
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "--abbrev-ref"):
			if brErr != nil {
				return "", brErr
			}
			return branch + "\n", nil
		case strings.Contains(joined, "rev-parse"):
			if cmErr != nil {
				return "", cmErr
			}
			return commit + "\n", nil
		}
		return "", errors.New("неожиданная git-команда: " + joined)
	}
	t.Cleanup(func() { gitRunner = orig })
	return &calls
}

// DetectBranch возвращает ветку рабочего каталога агента (ai/task/<id>).
func TestDetectBranch(t *testing.T) {
	withGit(t, "ai/task/T-01", "abc123", nil, nil)
	got, err := DetectBranch("/work/task")
	if err != nil {
		t.Fatalf("DetectBranch: %v", err)
	}
	if got != "ai/task/T-01" {
		t.Fatalf("ветка: got %q, want ai/task/T-01", got)
	}
}

// Вне git (или git недоступен) — ошибка, а не пустая строка: вызывающий
// обязан откатиться на MainBranch (проекты без gitflow, Р-6/решение №7).
func TestDetectBranchNotGitRepo(t *testing.T) {
	withGit(t, "", "", errors.New("not a git repository"), nil)
	if _, err := DetectBranch("/work/plain"); err == nil {
		t.Fatal("ожидалась ошибка вне git-репозитория")
	}
	// Пустой каталог — тоже ошибка (git не запускается).
	if _, err := DetectBranch("  "); err == nil {
		t.Fatal("пустой каталог должен давать ошибку")
	}
}

// Отсоединённый HEAD не является именем ветки — код такого состояния не знает.
func TestDetectBranchDetachedHead(t *testing.T) {
	withGit(t, "HEAD", "abc123", nil, nil)
	if _, err := DetectBranch("/work/detached"); err == nil {
		t.Fatal("отсоединённый HEAD должен давать ошибку")
	}
}

// DetectCommit возвращает полный SHA HEAD — он же версия индексации.
func TestDetectCommit(t *testing.T) {
	withGit(t, "main", "9f1c2b3a4d5e6f708192a3b4c5d6e7f8091a2b3c", nil, nil)
	got, err := DetectCommit("/work/main")
	if err != nil {
		t.Fatalf("DetectCommit: %v", err)
	}
	if len(got) != 40 {
		t.Fatalf("ожидался полный SHA (40 симв.), got %q", got)
	}
}

// DetectIndexOptions: git-репозиторий → ветка + коммит; не-git → main без
// коммита; git без коммита → ветка без версии.
func TestDetectIndexOptions(t *testing.T) {
	t.Run("git", func(t *testing.T) {
		withGit(t, "ai/epic/ARCH-01", "deadbeef", nil, nil)
		opts := DetectIndexOptions("/work/epic")
		if opts.Branch != "ai/epic/ARCH-01" || opts.CommitSHA != "deadbeef" {
			t.Fatalf("опции: %+v", opts)
		}
	})
	t.Run("не git", func(t *testing.T) {
		withGit(t, "", "", errors.New("no git"), errors.New("no git"))
		opts := DetectIndexOptions("/work/plain")
		if opts.Branch != MainBranch || opts.CommitSHA != "" {
			t.Fatalf("опции вне git: %+v (ожидались main без коммита)", opts)
		}
	})
	t.Run("без коммита", func(t *testing.T) {
		withGit(t, "ai/task/T-02", "", nil, errors.New("no HEAD"))
		opts := DetectIndexOptions("/work/task")
		if opts.Branch != "ai/task/T-02" || opts.CommitSHA != "" {
			t.Fatalf("опции без коммита: %+v", opts)
		}
	})
}
