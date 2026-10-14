package gitflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ai/domain"
	"ai/infrastructure/git"
)

// fakeMergeRepo — заглушка MergeRepo.
type fakeMergeRepo struct {
	res    *git.MergeResult
	err    error
	calls  int
	base   string
	source string
	opts   git.MergeFeatureOptions
}

func (r *fakeMergeRepo) MergeFeature(_ context.Context, base, source string, opts git.MergeFeatureOptions) (*git.MergeResult, error) {
	r.calls++
	r.base, r.source, r.opts = base, source, opts
	return r.res, r.err
}

// fakeMergeHost — заглушка MergeTaskHost.
type fakeMergeHost struct {
	project    MergeProject
	projectErr error

	repo    *fakeMergeRepo
	repoErr error

	epicRef BranchRef
	epicErr error

	taskRef BranchRef
	taskErr error

	locked     int
	dropCalls  []string
	invalidate int
}

func (h *fakeMergeHost) Project(string) (MergeProject, error) { return h.project, h.projectErr }

func (h *fakeMergeHost) Repo(context.Context, string) (MergeRepo, error) {
	if h.repoErr != nil {
		return nil, h.repoErr
	}
	return h.repo, nil
}

func (h *fakeMergeHost) EpicBranch(_, _ string) (BranchRef, error) { return h.epicRef, h.epicErr }

func (h *fakeMergeHost) TaskBranch(_, _ string) (BranchRef, error) { return h.taskRef, h.taskErr }

func (h *fakeMergeHost) Locked(_ string, fn func()) {
	h.locked++
	fn()
}

func (h *fakeMergeHost) DropEpicWorktreeLocked(_ context.Context, _, epicID string) {
	h.dropCalls = append(h.dropCalls, epicID)
}

func (h *fakeMergeHost) InvalidateDiffs(string) { h.invalidate++ }

func okMergeHost() *fakeMergeHost {
	return &fakeMergeHost{
		project: MergeProject{IsGit: true, Kind: "git", Base: "main", PushURL: "https://token@x"},
		repo:    &fakeMergeRepo{res: &git.MergeResult{Message: "merged"}},
		epicRef: BranchRef{Branch: "ai/epic/E-1"},
		taskRef: BranchRef{Branch: "ai/task/T-1"},
	}
}

func mergeTask() *domain.Task {
	return &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "T-1"}, EpicID: "E-1"}
}

// Полный цикл: лок взят, worktree эпика снят, MergeFeature с правильными
// аргументами, инвалидация диффов на успехе.
func TestTaskMergerSuccess(t *testing.T) {
	h := okMergeHost()
	res, err := TaskMerger{Host: h}.MergeTaskBranch(context.Background(), "p", mergeTask())
	if err != nil {
		t.Fatalf("MergeTaskBranch: %v", err)
	}
	if res == nil || res.Message != "merged" {
		t.Fatalf("res = %+v", res)
	}
	if h.locked != 1 {
		t.Fatalf("Locked = %d", h.locked)
	}
	if len(h.dropCalls) != 1 || h.dropCalls[0] != "E-1" {
		t.Fatalf("dropCalls = %v", h.dropCalls)
	}
	repo := h.repo
	if repo.base != "ai/epic/E-1" || repo.source != "ai/task/T-1" {
		t.Fatalf("merge base/source = %q/%q", repo.base, repo.source)
	}
	if repo.opts.PushURL != "https://token@x" {
		t.Fatalf("pushURL = %q", repo.opts.PushURL)
	}
	if h.invalidate != 1 {
		t.Fatalf("invalidate = %d", h.invalidate)
	}
}

// nil-задача / без эпика — ошибка, git не зовётся.
func TestTaskMergerBadTask(t *testing.T) {
	h := okMergeHost()
	m := TaskMerger{Host: h}
	if _, err := m.MergeTaskBranch(context.Background(), "p", nil); err == nil {
		t.Fatal("nil-задача → ошибка")
	}
	if _, err := m.MergeTaskBranch(context.Background(), "p", &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "T-1"}}); err == nil {
		t.Fatal("задача без эпика → ошибка")
	}
	if h.repo.calls != 0 {
		t.Fatalf("git не должен вызываться: %d", h.repo.calls)
	}
}

// Проект не найден / не git / нет репозитория — ошибки, git не зовётся.
func TestTaskMergerBadProject(t *testing.T) {
	ctx := context.Background()

	notFound := okMergeHost()
	notFound.projectErr = errors.New("нет проекта")
	if _, err := (TaskMerger{Host: notFound}).MergeTaskBranch(ctx, "p", mergeTask()); err == nil {
		t.Fatal("проект не найден → ошибка")
	}

	notGit := okMergeHost()
	notGit.project = MergeProject{IsGit: false, Kind: "local"}
	if _, err := (TaskMerger{Host: notGit}).MergeTaskBranch(ctx, "p", mergeTask()); err == nil {
		t.Fatal("не git → ошибка")
	} else if !strings.Contains(err.Error(), "kind=local") {
		t.Fatalf("сообщение: %v", err)
	}

	repoErr := okMergeHost()
	repoErr.repoErr = errors.New("нет репо")
	if _, err := (TaskMerger{Host: repoErr}).MergeTaskBranch(ctx, "p", mergeTask()); err == nil {
		t.Fatal("нет репозитория → ошибка")
	}
	if repoErr.repo.calls != 0 {
		t.Fatalf("git не должен вызываться: %d", repoErr.repo.calls)
	}
}

// Нет ветки эпика/задачи — ошибки до git.
func TestTaskMergerMissingBranches(t *testing.T) {
	ctx := context.Background()

	noEpic := okMergeHost()
	noEpic.epicErr = errors.New("нет ветки")
	if _, err := (TaskMerger{Host: noEpic}).MergeTaskBranch(ctx, "p", mergeTask()); err == nil {
		t.Fatal("нет ветки эпика → ошибка")
	}
	if noEpic.repo.calls != 0 {
		t.Fatalf("git не должен вызываться: %d", noEpic.repo.calls)
	}

	noTask := okMergeHost()
	noTask.taskErr = errors.New("нет ветки")
	if _, err := (TaskMerger{Host: noTask}).MergeTaskBranch(ctx, "p", mergeTask()); err == nil {
		t.Fatal("нет ветки задачи → ошибка")
	}
	if noTask.repo.calls != 0 {
		t.Fatalf("git не должен вызываться: %d", noTask.repo.calls)
	}
}

// Ошибка слияния (конфликт): возвращается, диффы НЕ инвалидируются.
func TestTaskMergerConflictNoInvalidate(t *testing.T) {
	h := okMergeHost()
	conflict := &git.MergeConflictError{Files: []string{"a.go"}}
	h.repo.err = conflict

	_, err := TaskMerger{Host: h}.MergeTaskBranch(context.Background(), "p", mergeTask())
	var ce *git.MergeConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *git.MergeConflictError", err)
	}
	if h.invalidate != 0 {
		t.Fatalf("конфликт не должен инвалидировать диффы: %d", h.invalidate)
	}
}
