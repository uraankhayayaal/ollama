package gitflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ai/domain"
)

// fakeMergeRepo — заглушка MergeRepo.
type fakeMergeRepo struct {
	res    *domain.MergeResult
	err    error
	calls  int
	base   string
	source string
	opts   domain.MergeFeatureOptions
}

func (r *fakeMergeRepo) MergeFeature(_ context.Context, base, source string, opts domain.MergeFeatureOptions) (*domain.MergeResult, error) {
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
		repo:    &fakeMergeRepo{res: &domain.MergeResult{Message: "merged"}},
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
	conflict := &domain.MergeConflictError{Files: []string{"a.go"}}
	h.repo.err = conflict

	_, err := TaskMerger{Host: h}.MergeTaskBranch(context.Background(), "p", mergeTask())
	var ce *domain.MergeConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *domain.MergeConflictError", err)
	}
	if h.invalidate != 0 {
		t.Fatalf("конфликт не должен инвалидировать диффы: %d", h.invalidate)
	}
}

// fakeReleaseHost — заглушка ReleaseHost (расширяет fakeMergeHost).
type fakeReleaseHost struct {
	*fakeMergeHost

	status    domain.Status
	found     bool
	statusErr error

	mainChanged       int
	mainChangedMain   string
	mainChangedMerged bool
}

func newFakeReleaseHost() *fakeReleaseHost {
	return &fakeReleaseHost{
		fakeMergeHost: okMergeHost(),
		status:        domain.StatusDone,
		found:         true,
	}
}

func (h *fakeReleaseHost) EpicStatus(context.Context, string, string) (domain.Status, bool, error) {
	return h.status, h.found, h.statusErr
}

func (h *fakeReleaseHost) MainChanged(_ string, main string, alreadyMerged bool) {
	h.mainChanged++
	h.mainChangedMain = main
	h.mainChangedMerged = alreadyMerged
}

// Релиз эпика: MergeFeature(main ← ветка эпика) под локом, инвалидация диффов и
// хук «main изменился» на успехе.
func TestReleaserReleaseEpicSuccess(t *testing.T) {
	h := newFakeReleaseHost()
	h.epicRef = BranchRef{Branch: "ai/epic/E-1"}

	res, source, main, err := Releaser{Host: h}.ReleaseEpic(context.Background(), "p", "E-1")
	if err != nil {
		t.Fatalf("ReleaseEpic: %v", err)
	}
	if res == nil || source != "ai/epic/E-1" || main != "main" {
		t.Fatalf("res=%+v source=%q main=%q", res, source, main)
	}
	if h.locked != 1 {
		t.Fatalf("Locked = %d", h.locked)
	}
	if h.repo.base != "main" || h.repo.source != "ai/epic/E-1" {
		t.Fatalf("merge base/source = %q/%q", h.repo.base, h.repo.source)
	}
	if h.invalidate != 1 || h.mainChanged != 1 || h.mainChangedMerged {
		t.Fatalf("invalidate=%d mainChanged=%d already=%v", h.invalidate, h.mainChanged, h.mainChangedMerged)
	}
}

// AlreadyMerged прокидывается в MainChanged.
func TestReleaserReleaseEpicAlreadyMerged(t *testing.T) {
	h := newFakeReleaseHost()
	h.repo.res = &domain.MergeResult{AlreadyMerged: true}
	if _, _, _, err := (Releaser{Host: h}).ReleaseEpic(context.Background(), "p", "E-1"); err != nil {
		t.Fatalf("ReleaseEpic: %v", err)
	}
	if h.mainChanged != 1 || !h.mainChangedMerged {
		t.Fatalf("mainChanged=%d already=%v", h.mainChanged, h.mainChangedMerged)
	}
}

// Ошибки состояния релиза → доменные sentinel'ы.
func TestReleaserReleaseEpicStateErrors(t *testing.T) {
	ctx := context.Background()

	notFound := newFakeReleaseHost()
	notFound.projectErr = errors.New("нет")
	if _, _, _, err := (Releaser{Host: notFound}).ReleaseEpic(ctx, "p", "E-1"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("проект не найден: %v", err)
	}

	notGit := newFakeReleaseHost()
	notGit.project = MergeProject{IsGit: false, Kind: "local"}
	if _, _, _, err := (Releaser{Host: notGit}).ReleaseEpic(ctx, "p", "E-1"); !errors.Is(err, ErrNotGitProject) {
		t.Fatalf("не git: %v", err)
	}

	noBase := newFakeReleaseHost()
	noBase.project.Base = ""
	if _, _, _, err := (Releaser{Host: noBase}).ReleaseEpic(ctx, "p", "E-1"); !errors.Is(err, ErrNoBaseBranch) {
		t.Fatalf("нет базы: %v", err)
	}

	repoErr := newFakeReleaseHost()
	repoErr.repoErr = errors.New("нет репо")
	if _, _, _, err := (Releaser{Host: repoErr}).ReleaseEpic(ctx, "p", "E-1"); !errors.Is(err, ErrRepoUnavailable) {
		t.Fatalf("нет репозитория: %v", err)
	}

	boardErr := newFakeReleaseHost()
	boardErr.statusErr = errors.New("redis")
	if _, _, _, err := (Releaser{Host: boardErr}).ReleaseEpic(ctx, "p", "E-1"); !errors.Is(err, ErrBoardUnavailable) {
		t.Fatalf("доска недоступна: %v", err)
	}

	noEpic := newFakeReleaseHost()
	noEpic.found = false
	if _, _, _, err := (Releaser{Host: noEpic}).ReleaseEpic(ctx, "p", "E-1"); !errors.Is(err, ErrEpicNotFound) {
		t.Fatalf("эпик не найден: %v", err)
	}

	notDone := newFakeReleaseHost()
	notDone.status = domain.StatusInProgress
	if _, _, _, err := (Releaser{Host: notDone}).ReleaseEpic(ctx, "p", "E-1"); !errors.Is(err, ErrEpicNotDone) {
		t.Fatalf("эпик не done: %v", err)
	}

	noBranch := newFakeReleaseHost()
	noBranch.epicErr = errors.New("нет ветки")
	if _, _, _, err := (Releaser{Host: noBranch}).ReleaseEpic(ctx, "p", "E-1"); !errors.Is(err, ErrEpicBranchMissing) {
		t.Fatalf("нет ветки эпика: %v", err)
	}

	if notGit.repo.calls != 0 || noBase.repo.calls != 0 {
		t.Fatal("git не должен вызываться при ошибке состояния")
	}
}

// Ошибка слияния: конфликт возвращается как *domain.MergeConflictError, прочее —
// ErrMergeFailed; MainChanged не зовётся.
func TestReleaserReleaseEpicMergeErrors(t *testing.T) {
	ctx := context.Background()

	conflict := newFakeReleaseHost()
	conflict.repo.err = &domain.MergeConflictError{Files: []string{"a.go"}}
	if _, _, _, err := (Releaser{Host: conflict}).ReleaseEpic(ctx, "p", "E-1"); err == nil {
		t.Fatal("ожидали ошибку конфликта")
	} else {
		var ce *domain.MergeConflictError
		if !errors.As(err, &ce) {
			t.Fatalf("err = %v, want *domain.MergeConflictError", err)
		}
	}
	if conflict.mainChanged != 0 || conflict.invalidate != 0 {
		t.Fatal("при конфликте не должно быть побочных эффектов")
	}

	failed := newFakeReleaseHost()
	failed.repo.err = errors.New("git сломался")
	if _, _, _, err := (Releaser{Host: failed}).ReleaseEpic(ctx, "p", "E-1"); !errors.Is(err, ErrMergeFailed) {
		t.Fatalf("ошибка слияния: %v", err)
	}
	if failed.mainChanged != 0 {
		t.Fatal("при ошибке слияния MainChanged не зовётся")
	}
}
