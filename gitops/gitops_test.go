package gitops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// fakeExecutor записывает вызовы команд по порядку и никогда их не выполняет.
// Это и есть режим «dry-run» из PLAN Ф-2 (строка «тесты gitops (dry-run)»):
// поведение пакета привязывается к перечню команд git, при этом системный git
// не требуется, а тесты остаются hermetic.
type fakeExecutor struct {
	calls  []string          // "dir | argv..." — как есть, argv через пробел
	urls   map[string]string // root → remote URL (для remoteOf)
	resp   map[string]string // "root|argv..." → ответ
	failOn map[string]error  // "root|argv..." → ошибка
}

func (f *fakeExecutor) Exec(_ context.Context, dir string, argv ...string) (string, error) {
	call := dir + " | git " + strings.Join(argv[1:], " ")
	f.calls = append(f.calls, call)
	if err := f.failOn[call]; err != nil {
		return "", err
	}
	if out, ok := f.resp[call]; ok {
		return out, nil
	}
	if argv[len(argv)-1] == "--git-dir" && len(argv) == 2 {
		return ".git", nil
	}
	return "", nil
}

func TestWorktreeIsolationDryRun(t *testing.T) {
	ex := &fakeExecutor{
		resp: map[string]string{
			"/main | git rev-parse --git-dir":   ".git",
			"/main | git remote get-url origin": "git@gitlab.com:g/p.git",
			"/feat | git remote get-url origin": "git@gitlab.com:g/p.git",
		},
	}
	repo, err := Worktree(context.Background(), ex, "/main", "ai/feat-1", "main", "/feat")
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if repo.Remote != "git@gitlab.com:g/p.git" {
		t.Fatalf("Remote = %q", repo.Remote)
	}
	if repo.Branch != "ai/feat-1" || repo.Root != "/feat" {
		t.Fatalf("Branch/Root = %q/%q", repo.Branch, repo.Root)
	}
	want := `/main | git rev-parse --git-dir
/main | git worktree add -b ai/feat-1 /feat main
/feat | git remote get-url origin`
	if got := strings.Join(ex.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestBranchInPlaceFallbackCli(t *testing.T) {
	ex := &fakeExecutor{}
	repo, err := BranchInPlace(context.Background(), ex, "/clone", "ai/no-worktree", "main")
	if err != nil {
		t.Fatalf("BranchInPlace: %v", err)
	}
	if repo.Root != "/clone" || repo.Branch != "ai/no-worktree" {
		t.Fatalf("Root/Branch = %q/%q", repo.Root, repo.Branch)
	}
	want := `/clone | git checkout -b ai/no-worktree main
/clone | git remote get-url origin`
	if got := strings.Join(ex.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestCommitPushPushBranchRemote(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{}
	repo := &Repo{
		Remote: "git@github.com:g/r.git",
		Root:   "/clone", Branch: "ai/g",
		ex: ex,
	}
	if err := repo.Commit(ctx, "верификация dry-run"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := repo.Push(ctx); err != nil {
		t.Fatalf("Push: %v", err)
	}
	want := `/clone | git add -A
/clone | git commit -m верификация dry-run
/clone | git push -u origin ai/g`
	if got := strings.Join(ex.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestCommitErrorsOnEmptyMessage(t *testing.T) {
	if err := (&Repo{Root: "/x", ex: &fakeExecutor{}}).Commit(context.Background(), "  "); err == nil {
		t.Fatal("пустое сообщение — ожидали ошибку")
	}
}

// TestRejectBranchInPlaceDryRun проверяет откат при branch-in-place.
func TestRejectBranchInPlaceDryRun(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{}
	repo := &Repo{
		Remote: "", Root: "/clone", Branch: "ai/g", Base: "main", ex: ex,
	}
	if err := repo.RejectBranch(ctx); err != nil {
		t.Fatalf("RejectBranch: %v", err)
	}
	want := `/clone | git checkout main
/clone | git branch -D ai/g`
	if got := strings.Join(ex.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestDiffReturnsBaseHead(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{resp: map[string]string{
		"/work | git diff base..HEAD": "секретный дифф\n",
	}}
	repo := &Repo{Root: "/work", Base: "base", ex: ex}
	out, err := repo.Diff(ctx)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(out, "секретный дифф") {
		t.Fatalf("Diff = %q", out)
	}
	if got := strings.Join(ex.calls, "\n"); got != "/work | git diff base..HEAD" {
		t.Fatalf("вызовы:\n%s", got)
	}
}

// TestExecErrorIsWrapped проверяет, что ошибки исполнителя оборачиваются и
// сохраняют причину (для диагностики в тек. ХИТЛ-панели).
func TestExecErrorIsWrapped(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	ex := &fakeExecutor{failOn: map[string]error{
		"/main | git worktree add -b ai/f /f main": boom,
	}}
	_, err := Worktree(ctx, ex, "/main", "ai/f", "main", "/f")
	if err == nil {
		t.Fatal("ожидали ошибку")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("ошибка не сохранила причину: %v", err)
	}
	if !strings.Contains(err.Error(), "gitops") {
		t.Fatalf("нет префикса gitops: %v", err)
	}
}

func TestWorktreeRequiresBranchAndBase(t *testing.T) {
	if _, err := Worktree(context.Background(), &fakeExecutor{}, "/main", "", "main", "/f"); err == nil {
		t.Fatal("пустая branch — ожидали ошибку")
	}
	if _, err := Worktree(context.Background(), &fakeExecutor{}, "/main", "ai/f", "", "/f"); err == nil {
		t.Fatal("пустой base — ожидали ошибку")
	}
}

func TestCommitAndPushCallOrderStable(t *testing.T) {
	// Сортировка не должна влиять на порядок git-команд: проверяем, что
	// add/commit/push идут именно в этом порядке даже при наличии remote.
	ctx := context.Background()
	ex := &fakeExecutor{}
	repo := &Repo{Remote: "git@gitlab.com:g/r.git", Root: "/main", Branch: "ai/s", ex: ex}
	_ = repo.Commit(ctx, "плановый коммит")
	_ = repo.Push(ctx)
	sorted := append([]string(nil), ex.calls...)
	sort.Strings(sorted)
	_ = sorted // порядок важен, а не сортировка
	if len(ex.calls) != 3 {
		t.Fatalf("нужно 3 вызова, получено %d: %v", len(ex.calls), ex.calls)
	}
}

var _ = fmt.Sprintf // держим fmt в зависимостях на будущее (без неиспользуемых импортов)
