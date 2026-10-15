package gitflow

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"ai/domain"
)

// --- заглушки ---------------------------------------------------------------

type fakeConflictRepo struct {
	mergeBase    string
	mergeBaseErr error
	mergeTree    []string
	mergeTreeErr error
	root         string
	wt           *fakeConflictWorktree
	addErr       error
	addCalls     []string
	removed      []string
	removeErr    error
	mergeRes     *domain.MergeResult
	mergeErr     error
	mergeCalls   []mergeCall
}

type mergeCall struct{ base, source string }

func (r *fakeConflictRepo) MergeFeature(_ context.Context, base, source string, _ domain.MergeFeatureOptions) (*domain.MergeResult, error) {
	r.mergeCalls = append(r.mergeCalls, mergeCall{base, source})
	if r.mergeErr != nil {
		return nil, r.mergeErr
	}
	if r.mergeRes != nil {
		return r.mergeRes, nil
	}
	return &domain.MergeResult{Message: "merged"}, nil
}

func (r *fakeConflictRepo) MergeBase(context.Context, string, string) (string, error) {
	return r.mergeBase, r.mergeBaseErr
}

func (r *fakeConflictRepo) MergeTree(context.Context, string, string, string) ([]string, error) {
	return r.mergeTree, r.mergeTreeErr
}

func (r *fakeConflictRepo) Root() string { return r.root }

func (r *fakeConflictRepo) AddWorktree(_ context.Context, path, branch string) (ConflictWorktree, error) {
	r.addCalls = append(r.addCalls, path+"@"+branch)
	if r.addErr != nil {
		return nil, r.addErr
	}
	return r.wt, nil
}

func (r *fakeConflictRepo) RemoveWorktree(_ context.Context, path string) error {
	r.removed = append(r.removed, path)
	return r.removeErr
}

type fakeConflictWorktree struct {
	mergeBranchErr error
	unmerged       []string
	unmergedErr    error
	staged         [][]string
	stageErr       error
	dirty          bool
	dirtyErr       error
	commitErr      error
	allowEmptyErr  error
	commits        []string
	allowEmpty     []string
}

func (w *fakeConflictWorktree) MergeBranch(context.Context, string, string) (*domain.MergeResult, error) {
	return &domain.MergeResult{}, w.mergeBranchErr
}

func (w *fakeConflictWorktree) UnmergedFiles(context.Context) ([]string, error) {
	return w.unmerged, w.unmergedErr
}

func (w *fakeConflictWorktree) Stage(_ context.Context, paths []string) error {
	w.staged = append(w.staged, paths)
	return w.stageErr
}

func (w *fakeConflictWorktree) Dirty(context.Context) (bool, error) {
	return w.dirty, w.dirtyErr
}

func (w *fakeConflictWorktree) Commit(_ context.Context, msg string) error {
	w.commits = append(w.commits, msg)
	return w.commitErr
}

func (w *fakeConflictWorktree) CommitAllowEmpty(_ context.Context, msg string) error {
	w.allowEmpty = append(w.allowEmpty, msg)
	return w.allowEmptyErr
}

type fakeConflictHost struct {
	inf        MergeProject
	projectErr error

	repo    *fakeConflictRepo
	repoErr error

	status    domain.Status
	found     bool
	statusErr error

	epicRef    BranchRef
	epicRefErr error

	state    *ResolveState
	stateErr error
	setState error
	cleared  int

	worktree    ConflictWorktree
	worktreeErr error

	dropped []string

	trivialResolved []string
	trivialHard     []string
	trivialErr      error
	trivialCalls    []string

	markers    []string
	markersErr error

	report *domain.Report

	conflictFiles []string
}

func (h *fakeConflictHost) Project(string) (MergeProject, error) { return h.inf, h.projectErr }
func (h *fakeConflictHost) Repo(context.Context, string) (ConflictRepo, error) {
	return h.repo, h.repoErr
}
func (h *fakeConflictHost) EpicStatus(context.Context, string, string) (domain.Status, bool, error) {
	return h.status, h.found, h.statusErr
}
func (h *fakeConflictHost) EpicBranch(string, string) (BranchRef, error) {
	return h.epicRef, h.epicRefErr
}
func (h *fakeConflictHost) ResolveState(string) (*ResolveState, bool) {
	return h.state, h.state != nil
}
func (h *fakeConflictHost) SetResolveState(string, *ResolveState) error {
	if h.setState == nil {
		h.state = nil
	}
	return h.setState
}
func (h *fakeConflictHost) ClearResolveState(string) error {
	h.cleared++
	h.state = nil
	return h.stateErr
}
func (h *fakeConflictHost) Worktree(string, string, string) (ConflictWorktree, error) {
	return h.worktree, h.worktreeErr
}
func (h *fakeConflictHost) Locked(_ string, fn func()) { fn() }
func (h *fakeConflictHost) DropEpicWorktreeLocked(_ context.Context, _, epicID string) {
	h.dropped = append(h.dropped, epicID)
}
func (h *fakeConflictHost) TrivialResolve(dir string, files []string) ([]string, []string, error) {
	h.trivialCalls = append(h.trivialCalls, dir)
	return h.trivialResolved, h.trivialHard, h.trivialErr
}
func (h *fakeConflictHost) Markers(string, []string) ([]string, error) {
	return h.markers, h.markersErr
}
func (h *fakeConflictHost) Accept(string) *domain.Report {
	if h.report == nil {
		return &domain.Report{Verdict: domain.VerdictApprove}
	}
	return h.report
}
func (h *fakeConflictHost) SetEpicMergeConflict(_ context.Context, _, _ string, files []string) {
	h.conflictFiles = files
}

func baseHost() *fakeConflictHost {
	return &fakeConflictHost{
		inf:     MergeProject{IsGit: true, Kind: "git", Base: "main", PushURL: "url"},
		repo:    &fakeConflictRepo{mergeBase: "B", root: "/tmp/repo"},
		status:  domain.StatusDone,
		found:   true,
		epicRef: BranchRef{Branch: "ai/epic/e1", Base: "main"},
	}
}

// --- StartRebase ------------------------------------------------------------

func TestConflictRebaseNoConflicts(t *testing.T) {
	h := baseHost()
	out, err := ConflictResolver{Host: h}.StartRebase(context.Background(), "myrepo", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "ok" {
		t.Fatalf("status=%q, want ok", out.Status)
	}
	if h.repo.addCalls != nil {
		t.Fatalf("worktree не должен создаваться: %v", h.repo.addCalls)
	}
	if h.conflictFiles != nil {
		t.Fatalf("конфликт не должен помечаться: %v", h.conflictFiles)
	}
}

func TestConflictRebaseResolving(t *testing.T) {
	h := baseHost()
	h.repo.mergeTree = []string{"c.txt", "d.txt"}
	h.repo.wt = &fakeConflictWorktree{unmerged: []string{"c.txt", "d.txt", "ok.txt"}}
	h.trivialResolved = []string{"ok.txt"}
	h.trivialHard = []string{"c.txt", "d.txt"}
	out, err := ConflictResolver{Host: h}.StartRebase(context.Background(), "myrepo", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "resolving" || out.Repeat {
		t.Fatalf("out=%+v", out)
	}
	if len(out.Files) != 2 || len(out.Resolved) != 1 {
		t.Fatalf("files/resolved = %v/%v", out.Files, out.Resolved)
	}
	if out.Worktree == "" || out.Branch != "ai/epic/e1" {
		t.Fatalf("branch/worktree = %q/%q", out.Branch, out.Worktree)
	}
	if len(h.dropped) != 1 || h.cleared != 0 {
		t.Fatalf("dropped=%v cleared=%d", h.dropped, h.cleared)
	}
	if len(h.conflictFiles) != 2 {
		t.Fatalf("conflictFiles=%v", h.conflictFiles)
	}
}

func TestConflictRebaseRepeat(t *testing.T) {
	h := baseHost()
	h.state = &ResolveState{EpicID: "e1", Branch: "ai/epic/e1", Worktree: "/tmp/wt", Files: []string{"c.txt"}}
	out, err := ConflictResolver{Host: h}.StartRebase(context.Background(), "myrepo", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "resolving" || !out.Repeat {
		t.Fatalf("out=%+v", out)
	}
	if len(out.Files) != 1 || out.Files[0] != "c.txt" {
		t.Fatalf("files=%v", out.Files)
	}
}

func TestConflictRebaseBusyOtherEpic(t *testing.T) {
	h := baseHost()
	h.state = &ResolveState{EpicID: "other"}
	out, err := ConflictResolver{Host: h}.StartRebase(context.Background(), "myrepo", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "busy" || out.EpicID != "other" {
		t.Fatalf("out=%+v", out)
	}
}

func TestConflictRebaseStateErrors(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*fakeConflictHost)
		want error
	}{
		{"project", func(h *fakeConflictHost) { h.projectErr = errors.New("x") }, ErrProjectNotFound},
		{"not-git", func(h *fakeConflictHost) { h.inf.IsGit = false }, ErrNotGitProject},
		{"no-base", func(h *fakeConflictHost) { h.inf.Base = "" }, ErrNoBaseBranch},
		{"repo", func(h *fakeConflictHost) { h.repoErr = errors.New("x") }, ErrRepoUnavailable},
		{"board", func(h *fakeConflictHost) { h.statusErr = errors.New("x") }, ErrBoardUnavailable},
		{"no-epic", func(h *fakeConflictHost) { h.found = false }, ErrEpicNotFound},
		{"not-done", func(h *fakeConflictHost) { h.status = domain.StatusNew }, ErrEpicNotDone},
		{"no-branch", func(h *fakeConflictHost) { h.epicRefErr = errors.New("x") }, ErrEpicBranchMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := baseHost()
			tc.mut(h)
			_, err := ConflictResolver{Host: h}.StartRebase(context.Background(), "myrepo", "e1")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v, want %v", err, tc.want)
			}
		})
	}
}

func TestConflictRebaseForecastError(t *testing.T) {
	h := baseHost()
	h.repo.mergeTreeErr = errors.New("boom")
	_, err := ConflictResolver{Host: h}.StartRebase(context.Background(), "myrepo", "e1")
	if !errors.Is(err, ErrMergeFailed) {
		t.Fatalf("err=%v, want ErrMergeFailed", err)
	}
}

func TestConflictRebaseSetStateFailureCleansUp(t *testing.T) {
	h := baseHost()
	h.repo.mergeTree = []string{"c.txt"}
	h.repo.wt = &fakeConflictWorktree{unmerged: []string{"c.txt"}}
	h.trivialHard = []string{"c.txt"}
	h.setState = errors.New("disk full")
	_, err := ConflictResolver{Host: h}.StartRebase(context.Background(), "myrepo", "e1")
	if !errors.Is(err, ErrResolveStateSave) {
		t.Fatalf("err=%v, want ErrResolveStateSave", err)
	}
	if len(h.repo.removed) != 1 {
		t.Fatalf("worktree должен быть снят при сбое: %v", h.repo.removed)
	}
}

// --- FinishResolve ----------------------------------------------------------

func resolveHost(t *testing.T) (*fakeConflictHost, string) {
	t.Helper()
	wtDir := t.TempDir()
	wt := &fakeConflictWorktree{dirty: true}
	h := baseHost()
	h.state = &ResolveState{EpicID: "e1", Branch: "ai/epic/e1", Worktree: wtDir, Files: []string{"c.txt"}}
	h.worktree = wt
	h.repo.wt = wt
	return h, wtDir
}

func TestConflictResolveOK(t *testing.T) {
	h, wtDir := resolveHost(t)
	h.repo.mergeRes = &domain.MergeResult{Message: "merged", AlreadyMerged: false}
	out, err := ConflictResolver{Host: h}.FinishResolve(context.Background(), "myrepo", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "ok" || out.Branch != "main" || out.Source != "ai/epic/e1" {
		t.Fatalf("out=%+v", out)
	}
	wt := h.worktree.(*fakeConflictWorktree)
	if len(wt.commits) != 1 {
		t.Fatalf("ожидался один коммит резолва: %v", wt.commits)
	}
	if len(h.repo.mergeCalls) != 1 || h.repo.mergeCalls[0].base != "main" {
		t.Fatalf("mergeCalls=%v", h.repo.mergeCalls)
	}
	if len(h.repo.removed) != 1 || h.repo.removed[0] != wtDir {
		t.Fatalf("removed=%v", h.repo.removed)
	}
	if h.cleared != 1 || h.state != nil {
		t.Fatalf("процесс не снят: cleared=%d state=%v", h.cleared, h.state)
	}
}

func TestConflictResolveAllowEmptyWhenClean(t *testing.T) {
	h, _ := resolveHost(t)
	h.worktree.(*fakeConflictWorktree).dirty = false
	out, err := ConflictResolver{Host: h}.FinishResolve(context.Background(), "myrepo", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "ok" {
		t.Fatalf("out=%+v", out)
	}
	wt := h.worktree.(*fakeConflictWorktree)
	if len(wt.allowEmpty) != 1 || len(wt.commits) != 0 {
		t.Fatalf("commits=%v allowEmpty=%v", wt.commits, wt.allowEmpty)
	}
}

func TestConflictResolveNoState(t *testing.T) {
	h := baseHost()
	out, err := ConflictResolver{Host: h}.FinishResolve(context.Background(), "myrepo", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "no_resolve" || out.Message != "" {
		t.Fatalf("out=%+v", out)
	}
}

func TestConflictResolveWorktreeMissing(t *testing.T) {
	h := baseHost()
	h.state = &ResolveState{EpicID: "e1", Worktree: filepath.Join(t.TempDir(), "nope")}
	out, err := ConflictResolver{Host: h}.FinishResolve(context.Background(), "myrepo", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "no_resolve" || out.Message == "" {
		t.Fatalf("out=%+v", out)
	}
}

func TestConflictResolveBusyOtherEpic(t *testing.T) {
	h := baseHost()
	h.state = &ResolveState{EpicID: "other", Worktree: t.TempDir()}
	out, err := ConflictResolver{Host: h}.FinishResolve(context.Background(), "myrepo", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "busy" || out.EpicID != "other" {
		t.Fatalf("out=%+v", out)
	}
}

func TestConflictResolveStillConflicts(t *testing.T) {
	h, _ := resolveHost(t)
	h.worktree.(*fakeConflictWorktree).unmerged = []string{"c.txt"}
	out, err := ConflictResolver{Host: h}.FinishResolve(context.Background(), "myrepo", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "still_conflicts" || len(out.Files) != 1 {
		t.Fatalf("out=%+v", out)
	}
	if h.repo.mergeCalls != nil {
		t.Fatal("merge не должен выполняться")
	}
}

func TestConflictResolveAcceptanceFailed(t *testing.T) {
	h, _ := resolveHost(t)
	h.report = &domain.Report{Verdict: domain.VerdictReject, Summary: "build failed"}
	out, err := ConflictResolver{Host: h}.FinishResolve(context.Background(), "myrepo", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "acceptance_failed" || out.Verdict != domain.VerdictReject {
		t.Fatalf("out=%+v", out)
	}
	if h.repo.mergeCalls != nil {
		t.Fatal("merge не должен выполняться")
	}
	if h.cleared != 0 {
		t.Fatal("процесс должен сохраниться при acceptance_failed")
	}
}

func TestConflictResolveFinalConflict(t *testing.T) {
	h, _ := resolveHost(t)
	h.repo.mergeErr = &domain.MergeConflictError{Feature: "ai/epic/e1", Files: []string{"z.txt"}}
	out, err := ConflictResolver{Host: h}.FinishResolve(context.Background(), "myrepo", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "conflicts" || len(out.Files) != 1 {
		t.Fatalf("out=%+v", out)
	}
	if h.cleared != 0 {
		t.Fatal("процесс должен сохраниться при неожиданном конфликте")
	}
}

func TestConflictResolvePlatformErrors(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*fakeConflictHost)
		want error
	}{
		{"project", func(h *fakeConflictHost) { h.projectErr = errors.New("x") }, ErrProjectNotFound},
		{"not-git", func(h *fakeConflictHost) { h.inf.IsGit = false }, ErrNotGitProject},
		{"no-base", func(h *fakeConflictHost) { h.inf.Base = "" }, ErrNoBaseBranch},
		{"repo", func(h *fakeConflictHost) { h.repoErr = errors.New("x") }, ErrRepoUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := baseHost()
			tc.mut(h)
			_, err := ConflictResolver{Host: h}.FinishResolve(context.Background(), "myrepo", "e1")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v, want %v", err, tc.want)
			}
		})
	}
}

func TestConflictResolveMergeFatalError(t *testing.T) {
	h, _ := resolveHost(t)
	h.repo.mergeErr = errors.New("git blew up")
	_, err := ConflictResolver{Host: h}.FinishResolve(context.Background(), "myrepo", "e1")
	if !errors.Is(err, ErrMergeFailed) {
		t.Fatalf("err=%v, want ErrMergeFailed", err)
	}
}
