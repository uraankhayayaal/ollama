package gitops

// Тесты промежуточных коммитов (Ф-6, PLAN-2026-10-05-todo-kanban-rollback.md,
// этап 1): CommitIfDirty как единица отката, HeadSHA как цель отката и
// InTaskBranch как гард области. Все тесты hermetic — через fakeExecutor
// (dry-run), системный git не требуется.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCommitIfDirtyCommitsWhenDirty(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{resp: map[string]string{
		"/w | git status --porcelain": " M src/a.go\n",
		"/w | git rev-parse HEAD":     "abc123\n",
	}}
	repo := &Repo{Root: "/w", ex: ex}

	sha, ok, err := repo.CommitIfDirty(ctx, "wip: раунд 3")
	if err != nil {
		t.Fatalf("CommitIfDirty: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true (дерево грязное)")
	}
	if sha != "abc123" {
		t.Fatalf("sha = %q, want abc123", sha)
	}
	want := `/w | git status --porcelain
/w | git add -A
/w | git commit -m wip: раунд 3
/w | git rev-parse HEAD`
	if got := strings.Join(ex.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestCommitIfDirtySkipsCleanTree(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{resp: map[string]string{
		"/w | git status --porcelain": "",
	}}
	repo := &Repo{Root: "/w", ex: ex}

	sha, ok, err := repo.CommitIfDirty(ctx, "wip: раунд 4")
	if err != nil {
		t.Fatalf("CommitIfDirty: %v", err)
	}
	if ok {
		t.Fatal("ok = true, want false (дерево чистое)")
	}
	if sha != "" {
		t.Fatalf("sha = %q, want пусто", sha)
	}
	// Ни одного коммита: только проверка статуса.
	if len(ex.calls) != 1 {
		t.Fatalf("вызовы: %v, want только git status", ex.calls)
	}
}

func TestCommitIfDirtyPropagatesStatusError(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{failOn: map[string]error{
		"/w | git status --porcelain": errors.New("нет репозитория"),
	}}
	if _, _, err := (&Repo{Root: "/w", ex: ex}).CommitIfDirty(ctx, "wip"); err == nil {
		t.Fatal("ожидали ошибку git status")
	}
}

func TestCommitIfDirtySubmoduleCommittedFirst(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{resp: map[string]string{
		"/w | git status --porcelain":            " M vendor/lib\n",
		"/w/vendor/lib | git status --porcelain": " M lib.go\n",
		"/w | git rev-parse HEAD":                "def456\n",
	}}
	repo := &Repo{
		Root: "/w", ex: ex,
		Submodules: []Submodule{{Path: "vendor/lib", Root: "/w/vendor/lib"}},
	}

	if _, ok, err := repo.CommitIfDirty(ctx, "wip: раунд 5"); err != nil || !ok {
		t.Fatalf("CommitIfDirty = %v, %v; want ok", ok, err)
	}
	// Порядок: сначала проверка родителя (грязный gitlink сабмодуля), затем
	// сабмодуль фиксируется ДО родителя — remote родителя ссылается на gitlink.
	want := `/w | git status --porcelain
/w/vendor/lib | git status --porcelain
/w/vendor/lib | git add -A
/w/vendor/lib | git commit -m wip: раунд 5
/w | git add -A
/w | git commit -m wip: раунд 5
/w | git rev-parse HEAD`
	if got := strings.Join(ex.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestHeadSHA(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{resp: map[string]string{
		"/w | git rev-parse HEAD": "0123456789abcdef\n",
	}}
	sha, err := (&Repo{Root: "/w", ex: ex}).HeadSHA(ctx)
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if sha != "0123456789abcdef" {
		t.Fatalf("sha = %q, want 0123456789abcdef", sha)
	}
}

func TestInTaskBranch(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		branch string
		want   bool
	}{
		{"ai/task/t1", true},
		{"ai/task/task-1", true},
		{"ai/mytrip", false},
		{"ai/epic/e1", false},
		{"main", false},
		{"", false},
	}
	for _, tc := range cases {
		ex := &fakeExecutor{resp: map[string]string{
			"/w | git rev-parse --abbrev-ref HEAD": tc.branch + "\n",
		}}
		got, err := (&Repo{Root: "/w", ex: ex}).InTaskBranch(ctx)
		if err != nil {
			t.Fatalf("InTaskBranch(%q): %v", tc.branch, err)
		}
		if got != tc.want {
			t.Fatalf("InTaskBranch(%q) = %v, want %v", tc.branch, got, tc.want)
		}
	}
}

func TestInTaskBranchPropagatesError(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{failOn: map[string]error{
		"/w | git rev-parse --abbrev-ref HEAD": errors.New("detached HEAD"),
	}}
	got, err := (&Repo{Root: "/w", ex: ex}).InTaskBranch(ctx)
	if err == nil {
		t.Fatal("ожидали ошибку rev-parse")
	}
	if got {
		t.Fatal("при ошибке ожидали false")
	}
}
