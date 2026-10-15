// Тесты политики жизненного цикла worktree (application/gitflow/lifecycle.go):
// пропуски (не-git, нет ветки, уже создан), создание worktree задачи/эпика,
// синхронизация эпика и RAG-хуки, снятие worktree эпика. Git подменяется
// hermetic-исполнителем (fakeExec из open_test.go), реестр/host — заглушкой.
package gitflow

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"ai/infrastructure/git"
)

// fakeWTHost — заглушка gitflow.WorktreeHost.
type fakeWTHost struct {
	info    WorktreeProject
	infoErr error

	taskRef BranchRef
	taskErr error
	epicRef BranchRef
	epicErr error

	repo  *git.Repo
	ex    *fakeExec
	child map[string]string // subPath → childName

	locked   int
	ragIndex []string
	ragTask  []string
	syncs    int
	invalid  []string
	baseSHA  []string
	emitted  []string

	setTask []BranchRef
	setEpic []BranchRef
}

func (h *fakeWTHost) Info(string) (WorktreeProject, error) { return h.info, h.infoErr }
func (h *fakeWTHost) Repo(context.Context, string) (WorktreeRepo, error) {
	if h.repo == nil {
		return nil, errors.New("нет репозитория")
	}
	return fakeWTRepo{h.repo}, nil
}
func (h *fakeWTHost) EnsureSubmodules(ctx context.Context, dir string) ([]Submodule, error) {
	subs, err := git.EnsureSubmodules(ctx, h.ex, dir)
	if err != nil {
		return nil, err
	}
	out := make([]Submodule, 0, len(subs))
	for _, sub := range subs {
		out = append(out, Submodule{Path: sub.Path, Root: sub.Root})
	}
	return out, nil
}
func (h *fakeWTHost) SubmoduleBase(ctx context.Context, dir string) (string, error) {
	return h.ex.Exec(ctx, dir, "git", "rev-parse", "HEAD")
}
func (h *fakeWTHost) DeinitSubmodule(ctx context.Context, dir, path string) error {
	_, err := h.ex.Exec(ctx, dir, "git", "submodule", "deinit", "-f", "--", path)
	return err
}
func (h *fakeWTHost) SubmoduleWorktree(ctx context.Context, dir, branch, base, dest string) error {
	_, err := git.Worktree(ctx, h.ex, dir, branch, base, dest)
	return err
}
func (h *fakeWTHost) CommitWorktree(ctx context.Context, dir, msg string) error {
	repo := git.RepoFromState(h.ex, dir, "", "", "")
	_, _, err := repo.CommitIfDirty(ctx, msg)
	return err
}

// fakeWTRepo — адаптер WorktreeRepo поверх *git.Repo (как serverWorktreeRepo).
type fakeWTRepo struct{ r *git.Repo }

func (w fakeWTRepo) Root() string { return w.r.Root }
func (w fakeWTRepo) WorktreeList(ctx context.Context) ([]WorktreeEntry, error) {
	list, err := w.r.WorktreeList(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]WorktreeEntry, 0, len(list))
	for _, wt := range list {
		out = append(out, WorktreeEntry{Branch: wt.Branch, Path: wt.Path})
	}
	return out, nil
}
func (w fakeWTRepo) RemoveWorktree(ctx context.Context, path string) error {
	return w.r.RemoveWorktree(ctx, path)
}
func (w fakeWTRepo) Rebase(ctx context.Context, branch, onto string) error {
	return w.r.Rebase(ctx, branch, onto)
}
func (w fakeWTRepo) Checkout(ctx context.Context, branch string) error {
	return w.r.Checkout(ctx, branch)
}
func (w fakeWTRepo) AddWorktree(ctx context.Context, path, branch string) (WorktreeRepo, error) {
	wt, err := w.r.AddWorktree(ctx, path, branch)
	if err != nil {
		return nil, err
	}
	return fakeWTRepo{wt}, nil
}
func (h *fakeWTHost) TaskBranch(string, string) (BranchRef, error) {
	return h.taskRef, h.taskErr
}
func (h *fakeWTHost) EpicBranch(string, string) (BranchRef, error) {
	return h.epicRef, h.epicErr
}
func (h *fakeWTHost) SetTaskBranch(_, _ string, ref BranchRef) error {
	h.setTask = append(h.setTask, ref)
	return nil
}
func (h *fakeWTHost) SetEpicBranch(_, _ string, ref BranchRef) error {
	h.setEpic = append(h.setEpic, ref)
	return nil
}
func (h *fakeWTHost) ChildProject(_, subPath string) (string, string, bool) {
	name, ok := h.child[subPath]
	if !ok {
		return "", "", false
	}
	return name, "/tmp/" + name, true
}
func (h *fakeWTHost) Locked(_ string, fn func()) { h.locked++; fn() }
func (h *fakeWTHost) RefreshRagIndexBranch(_, base string) {
	h.ragIndex = append(h.ragIndex, base)
}
func (h *fakeWTHost) RefreshRagTaskBranch(_, branch, base string) {
	h.ragTask = append(h.ragTask, branch+"<-"+base)
}
func (h *fakeWTHost) SyncEpicMainForTask(context.Context, string, string) { h.syncs++ }
func (h *fakeWTHost) InvalidateDiffs(p string)                            { h.invalid = append(h.invalid, p) }
func (h *fakeWTHost) RecordTaskBaseSHA(_ context.Context, _, taskID, wt string) {
	h.baseSHA = append(h.baseSHA, taskID+"@"+wt)
}
func (h *fakeWTHost) EmitBoard(_, msg string) { h.emitted = append(h.emitted, msg) }

func newFakeWTHost(t *testing.T) *fakeWTHost {
	t.Helper()
	root := t.TempDir()
	ex := &fakeExec{starts: map[string]string{}}
	return &fakeWTHost{
		info:    WorktreeProject{IsGit: true, Root: root, GitBranch: "ai/proj", GitBase: "main"},
		ex:      ex,
		taskRef: BranchRef{Branch: "ai/task/T-1", Base: "main"},
		epicRef: BranchRef{Branch: "ai/epic/E-1", Base: "main"},
		repo:    git.RepoFromState(ex, root, "origin", "ai/proj", "main"),
	}
}

func TestTaskWorktreeSkipsNonGit(t *testing.T) {
	h := newFakeWTHost(t)
	h.info.IsGit = false
	(&WorktreeManager{Host: h}).TaskWorktree(context.Background(), "p", WorktreeTask{ID: "T-1"})
	if h.locked != 0 || len(h.setTask) != 0 {
		t.Fatalf("не-git проект не должен трогать worktree: locked=%d set=%v", h.locked, h.setTask)
	}
}

func TestTaskWorktreeSkipsEmptyID(t *testing.T) {
	h := newFakeWTHost(t)
	(&WorktreeManager{Host: h}).TaskWorktree(context.Background(), "p", WorktreeTask{})
	if h.locked != 0 {
		t.Fatalf("пустой ID — пропуск, locked=%d", h.locked)
	}
}

func TestTaskWorktreeSkipsWhenAlreadyCreated(t *testing.T) {
	h := newFakeWTHost(t)
	h.taskRef.Worktree = t.TempDir()
	(&WorktreeManager{Host: h}).TaskWorktree(context.Background(), "p", WorktreeTask{ID: "T-1"})
	if h.locked != 0 || len(h.setTask) != 0 {
		t.Fatalf("готовый worktree — пропуск: locked=%d set=%v", h.locked, h.setTask)
	}
}

func TestTaskWorktreeHappyPath(t *testing.T) {
	h := newFakeWTHost(t)
	(&WorktreeManager{Host: h}).TaskWorktree(context.Background(), "p", WorktreeTask{ID: "T-1", EpicID: "E-1"})

	if len(h.setTask) != 1 || h.setTask[0].Worktree == "" {
		t.Fatalf("ожидалась запись worktree задачи: %+v", h.setTask)
	}
	if h.syncs != 1 || len(h.ragIndex) != 1 || len(h.ragTask) != 1 {
		t.Fatalf("ожидались sync=%d ragIndex=%v ragTask=%v", h.syncs, h.ragIndex, h.ragTask)
	}
	if len(h.invalid) != 1 || len(h.baseSHA) != 1 || len(h.emitted) != 1 {
		t.Fatalf("ожидались инвалидация/чекпойнт/доска: %v %v %v", h.invalid, h.baseSHA, h.emitted)
	}
	if !strings.HasSuffix(h.setTask[0].Worktree, "T-1") {
		t.Fatalf("путь worktree задачи неверен: %s", h.setTask[0].Worktree)
	}
}

func TestTaskWorktreeNoEpicBranchStillCreates(t *testing.T) {
	h := newFakeWTHost(t)
	h.epicErr = errors.New("нет ветки эпика")
	(&WorktreeManager{Host: h}).TaskWorktree(context.Background(), "p", WorktreeTask{ID: "T-1"})

	if h.syncs != 0 {
		t.Fatalf("без ветки эпика синхронизации нет: %d", h.syncs)
	}
	if len(h.setTask) != 1 {
		t.Fatalf("worktree всё равно создаётся: %+v", h.setTask)
	}
}

func TestTaskWorktreeAddFails(t *testing.T) {
	h := newFakeWTHost(t)
	h.ex.fails = map[string]string{"git worktree add": "boom"}
	(&WorktreeManager{Host: h}).TaskWorktree(context.Background(), "p", WorktreeTask{ID: "T-1"})

	if len(h.setTask) != 0 || len(h.emitted) != 0 {
		t.Fatalf("при сбое worktree add не регистрируем: %v", h.setTask)
	}
}

func TestEpicWorktreeSkipsNonGit(t *testing.T) {
	h := newFakeWTHost(t)
	h.info.IsGit = false
	if got := (&WorktreeManager{Host: h}).EpicWorktree(context.Background(), "p", "E-1"); got != "" {
		t.Fatalf("не-git → \"\": %q", got)
	}
}

func TestEpicWorktreeReturnsExisting(t *testing.T) {
	h := newFakeWTHost(t)
	dir := t.TempDir()
	h.epicRef.Worktree = dir
	if got := (&WorktreeManager{Host: h}).EpicWorktree(context.Background(), "p", "E-1"); got != dir {
		t.Fatalf("ожидался существующий worktree %q, got %q", dir, got)
	}
	if h.locked != 0 {
		t.Fatalf("существующий worktree — без лока: %d", h.locked)
	}
}

func TestEpicWorktreeCreates(t *testing.T) {
	h := newFakeWTHost(t)
	got := (&WorktreeManager{Host: h}).EpicWorktree(context.Background(), "p", "E-1")
	if got == "" || len(h.setEpic) != 1 || h.setEpic[0].Worktree != got {
		t.Fatalf("ожидалось создание worktree эпика: got=%q set=%+v", got, h.setEpic)
	}
	if !strings.HasSuffix(got, "E-1") {
		t.Fatalf("путь worktree эпика неверен: %s", got)
	}
}

func TestDropEpicWorktreeLockedNoWorktree(t *testing.T) {
	h := newFakeWTHost(t)
	(&WorktreeManager{Host: h}).DropEpicWorktreeLocked(context.Background(), "p", "E-1")
	if len(h.setEpic) != 0 {
		t.Fatalf("без worktree записи нет: %+v", h.setEpic)
	}
}

func TestDropEpicWorktreeLockedClears(t *testing.T) {
	h := newFakeWTHost(t)
	dir := t.TempDir()
	h.epicRef.Worktree = dir
	(&WorktreeManager{Host: h}).DropEpicWorktreeLocked(context.Background(), "p", "E-1")

	if len(h.setEpic) != 1 || h.setEpic[0].Worktree != "" {
		t.Fatalf("ожидалась очистка worktree в реестре: %+v", h.setEpic)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("каталог worktree должен быть удалён")
	}
}
