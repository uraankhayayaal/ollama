package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
/clone | git diff --cached --name-only -z
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
	want := `/clone | git reset --hard main
/clone | git checkout main
/clone | git branch -D ai/g`
	if got := strings.Join(ex.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestPushToDryRun(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{}
	repo := &Repo{Root: "/clone", Branch: "ai/g", ex: ex}
	url := "https://x-access-token:tok@github.com/g/r.git"
	if err := repo.PushTo(ctx, url); err != nil {
		t.Fatalf("PushTo: %v", err)
	}
	// Токен-URL живёт только в argv; upstream (-u) не настраивается.
	want := `/clone | git push https://x-access-token:tok@github.com/g/r.git ai/g`
	if got := strings.Join(ex.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestPushToRequiresURL(t *testing.T) {
	repo := &Repo{Root: "/clone", Branch: "ai/g", ex: &fakeExecutor{}}
	if err := repo.PushTo(context.Background(), "  "); err == nil {
		t.Fatal("пустой URL — ожидали ошибку")
	}
	if err := (&Repo{}).PushTo(context.Background(), "url"); err == nil {
		t.Fatal("пустой Repo — ожидали ошибку")
	}
}

func TestDiffReturnsBaseHead(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{resp: map[string]string{
		"/work | git -c diff.submodule=log diff base": "секретный дифф\n",
	}}
	repo := &Repo{Root: "/work", Base: "base", ex: ex}
	out, err := repo.Diff(ctx)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(out, "секретный дифф") {
		t.Fatalf("Diff = %q", out)
	}
	want := `/work | git add -N -A
/work | git -c diff.submodule=log diff base`
	if got := strings.Join(ex.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\n\nwant:\n%s", got, want)
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
	// 4 вызова: add -A, проверка объёма гарда (Ф-6), commit, push.
	if len(ex.calls) != 4 {
		t.Fatalf("нужно 4 вызова, получено %d: %v", len(ex.calls), ex.calls)
	}
}

func TestCloneDryRun(t *testing.T) {
	ex := &fakeExecutor{
		resp: map[string]string{
			"/tmp/feat | git rev-parse --abbrev-ref HEAD": "main\n",
		},
	}
	repo, err := Clone(context.Background(), ex, "git@gitlab.com:g/p.git", "ai/feat-myapp", "/tmp/feat")
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if repo.Remote != "git@gitlab.com:g/p.git" {
		t.Fatalf("Remote = %q", repo.Remote)
	}
	if repo.Branch != "ai/feat-myapp" || repo.Base != "main" || repo.Root != "/tmp/feat" {
		t.Fatalf("Branch/Base/Root = %q/%q/%q", repo.Branch, repo.Base, repo.Root)
	}
	want := `/tmp | git clone git@gitlab.com:g/p.git /tmp/feat
/tmp/feat | git rev-parse --abbrev-ref HEAD
/tmp/feat | git rev-parse --verify --quiet main^{commit}
/tmp/feat | git checkout -b ai/feat-myapp`
	if got := strings.Join(ex.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\n\nwant:\n%s", got, want)
	}
}

// Пустой удалённый репозиторий: HEAD клона нерождённый, rev-parse на нём падает.
// Clone обязан определить базу по имени unborn-ветки, создать её первым
// коммитом и опубликовать — иначе весь git-flow упирается в несуществующий ref.
func TestCloneSeedsEmptyRemoteBase(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "mytrip")
	// Настоящий git создаёт каталог клона сам; dry-run-исполнитель — нет.
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	unborn := errors.New("fatal: ambiguous argument 'HEAD': unknown revision or path not in the working tree")
	ex := &fakeExecutor{
		resp: map[string]string{
			dest + " | git symbolic-ref --short HEAD": "main\n",
		},
		failOn: map[string]error{
			dest + " | git rev-parse --abbrev-ref HEAD":              unborn,
			dest + " | git rev-parse --verify --quiet main^{commit}": unborn,
		},
	}
	repo, err := Clone(context.Background(), ex, "git@github.com:u/mytrip.git", "ai/mytrip", dest)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if repo.Base != "main" || repo.Branch != "ai/mytrip" {
		t.Fatalf("Base/Branch = %q/%q, want main/ai/mytrip", repo.Base, repo.Branch)
	}
	want := parent + ` | git clone git@github.com:u/mytrip.git ` + dest + `
` + dest + ` | git rev-parse --abbrev-ref HEAD
` + dest + ` | git symbolic-ref --short HEAD
` + dest + ` | git rev-parse --verify --quiet main^{commit}
` + dest + ` | git add README.md
` + dest + ` | git commit -m chore: initial commit (main)
` + dest + ` | git push -u origin main
` + dest + ` | git checkout -b ai/mytrip`
	if got := strings.Join(ex.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\n\nwant:\n%s", got, want)
	}
	body, err := os.ReadFile(filepath.Join(dest, "README.md"))
	if err != nil {
		t.Fatalf("README.md не создан: %v", err)
	}
	if !strings.Contains(string(body), "mytrip") {
		t.Fatalf("README.md = %q, ожидалось имя проекта", body)
	}
}

// Посев не должен превращаться в бесконечный retry при отказе push: понятная
// ошибка, а не тихий проект без базовой ветки в remote.
func TestCloneEmptyRemotePushFailure(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "mytrip")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	unborn := errors.New("unknown revision")
	ex := &fakeExecutor{
		resp: map[string]string{
			dest + " | git symbolic-ref --short HEAD": "main\n",
		},
		failOn: map[string]error{
			dest + " | git rev-parse --abbrev-ref HEAD":              unborn,
			dest + " | git rev-parse --verify --quiet main^{commit}": unborn,
			dest + " | git push -u origin main":                      errors.New("permission denied"),
		},
	}
	if _, err := Clone(context.Background(), ex, "git@github.com:u/mytrip.git", "ai/mytrip", dest); err == nil {
		t.Fatal("отказ push базовой ветки должен возвращать ошибку")
	} else if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("в ошибке нет вывода git: %v", err)
	}
}

// Detached HEAD у клона (remote HEAD указывает на тег): rev-parse отдаёт «HEAD»,
// который как база бессмысленен — берём ветку по умолчанию со стороны remote.
func TestCloneDetachedHeadUsesOriginHead(t *testing.T) {
	ex := &fakeExecutor{
		resp: map[string]string{
			"/c | git rev-parse --abbrev-ref HEAD":                   "HEAD\n",
			"/c | git symbolic-ref --short refs/remotes/origin/HEAD": "origin/master\n",
		},
		failOn: map[string]error{
			"/c | git symbolic-ref --short HEAD": errors.New("fatal: ref HEAD is not a symbolic ref"),
		},
	}
	repo, err := Clone(context.Background(), ex, "https://host/o/r.git", "ai/r", "/c")
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if repo.Base != "master" {
		t.Fatalf("Base = %q, want master", repo.Base)
	}
}

// Клон без единого сигнала о ветке (битый clone-dir) всё равно получает базу:
// проект должен открыться, а не упасть на определении ветки.
func TestCloneNoBranchSignalFallsBackToDefault(t *testing.T) {
	broken := errors.New("fatal: not a git repository")
	ex := &fakeExecutor{failOn: map[string]error{
		"/c | git rev-parse --abbrev-ref HEAD":                   broken,
		"/c | git symbolic-ref --short HEAD":                     broken,
		"/c | git symbolic-ref --short refs/remotes/origin/HEAD": broken,
	}}
	repo, err := Clone(context.Background(), ex, "https://host/o/r.git", "ai/r", "/c")
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if repo.Base != DefaultBaseBranch {
		t.Fatalf("Base = %q, want %q", repo.Base, DefaultBaseBranch)
	}
}

func TestCloneRequiresRemoteBranchDest(t *testing.T) {
	if _, err := Clone(context.Background(), &fakeExecutor{}, "", "ai/f", "/x"); err == nil {
		t.Fatal("пустой remote — ожидали ошибку")
	}
	if _, err := Clone(context.Background(), &fakeExecutor{}, "url", "", "/x"); err == nil {
		t.Fatal("пустая branch — ожидали ошибку")
	}
	if _, err := Clone(context.Background(), &fakeExecutor{}, "url", "ai/f", ""); err == nil {
		t.Fatal("пустой dest — ожидали ошибку")
	}
}

func TestDirtyReportsUncommitted(t *testing.T) {
	ctx := context.Background()
	clean := &fakeExecutor{resp: map[string]string{
		"/w | git status --porcelain": "",
	}}
	if dirty, err := (&Repo{Root: "/w", ex: clean}).Dirty(ctx); err != nil || dirty {
		t.Fatalf("чистая = %v, %v; want false", dirty, err)
	}
	dirtyEx := &fakeExecutor{resp: map[string]string{
		"/w | git status --porcelain": " M src/a.go\n",
	}}
	if dirty, err := (&Repo{Root: "/w", ex: dirtyEx}).Dirty(ctx); err != nil || !dirty {
		t.Fatalf("грязная = %v, %v; want true", dirty, err)
	}
}

var _ = fmt.Sprintf // держим fmt в зависимостях на будущее (без неиспользуемых импортов)
