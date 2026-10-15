package server

// Адаптер gitflow.EpicMainHost (Этап 6, срез F3f): политика синхронизации
// релизной ветки эпика с main живёт в application/gitflow, здесь — только
// проводник к серверным помощникам (git, реестр веток, доска, сессия, шина).

import (
	"context"

	"ai/application/gitflow"
	"ai/domain"
	"ai/infrastructure/chat"
	"ai/infrastructure/git"
	"ai/infrastructure/workspace"
)

// epicMainHost — адаптер gitflow.EpicMainHost поверх серверных помощников.
type epicMainHost struct {
	s       *Server
	project string
}

func (h *epicMainHost) ProjectInfo() (base, pushURL string, ok bool) {
	inf, err := h.s.reg.Get(h.project)
	if err != nil || inf.Kind != workspace.KindGit {
		return "", "", false
	}
	return inf.GitBase, remotePushURL(inf), true
}

func (h *epicMainHost) EpicBranch(epicID string) (gitflow.BranchRef, bool) {
	ref, err := h.s.reg.EpicBranch(h.project, epicID)
	if err != nil {
		return gitflow.BranchRef{}, false
	}
	return gitflow.BranchRef{Branch: ref.Branch, Base: ref.Base, Worktree: ref.Worktree}, true
}

func (h *epicMainHost) Repo(ctx context.Context) (gitflow.EpicRepo, bool) {
	repo, err := h.s.repoOf(ctx, h.project)
	if err != nil {
		return nil, false
	}
	return serverEpicRepo{repo}, true
}

func (h *epicMainHost) DropEpicWorktreeLocked(ctx context.Context, epicID string) {
	h.s.dropEpicWorktreeLocked(ctx, h.project, epicID)
}

func (h *epicMainHost) Locked(fn func()) {
	lock := h.s.mergeLock(h.project)
	lock.Lock()
	defer lock.Unlock()
	fn()
}

func (h *epicMainHost) AttemptLLMResolve(ctx context.Context, epic *domain.Epic, worktreePath string, files []string, role string) bool {
	inf, err := h.s.reg.Get(h.project)
	if err != nil {
		return false
	}
	ref, err := h.s.reg.EpicBranch(h.project, epic.TaskID)
	if err != nil {
		return false
	}
	return h.s.attemptLLMResolve(ctx, h.project, epic, worktreePath, files, inf, ref.Branch, role)
}

func (h *epicMainHost) SetEpicMergeConflict(ctx context.Context, epicID string, files []string) {
	h.s.setEpicMergeConflict(ctx, h.project, epicID, files)
}

func (h *epicMainHost) TrivialResolve(wtPath string, unmerged []string) ([]string, []string, error) {
	return git.TrivialResolve(wtPath, unmerged)
}

func (h *epicMainHost) Notify(msg string) {
	h.s.srvEmitBoard(h.project, msg)
}

func (h *epicMainHost) Status(msg string) {
	if sess := h.s.session(h.project); sess != nil {
		sess.append(chat.RoleStatus, msg, "", "", nil)
	}
}

// serverEpicRepo — адаптер *git.Repo к gitflow.EpicRepo.
type serverEpicRepo struct {
	repo *git.Repo
}

func (r serverEpicRepo) Root() string { return r.repo.Root }

func (r serverEpicRepo) MergeFeature(ctx context.Context, release, feature string, opts git.MergeFeatureOptions) (*git.MergeResult, error) {
	return r.repo.MergeFeature(ctx, release, feature, opts)
}

func (r serverEpicRepo) AddWorktree(ctx context.Context, worktreePath, branch string) (gitflow.WorktreeOps, error) {
	wt, err := r.repo.AddWorktree(ctx, worktreePath, branch)
	if err != nil {
		return nil, err
	}
	return serverWorktreeOps{wt}, nil
}

func (r serverEpicRepo) RemoveWorktree(ctx context.Context, worktreePath string) error {
	return r.repo.RemoveWorktree(ctx, worktreePath)
}

// serverWorktreeOps — адаптер *git.Repo (worktree) к gitflow.WorktreeOps.
type serverWorktreeOps struct {
	wt *git.Repo
}

func (w serverWorktreeOps) MergeBranch(ctx context.Context, branch, message string) (*git.MergeResult, error) {
	return w.wt.MergeBranch(ctx, branch, message)
}

func (w serverWorktreeOps) UnmergedFiles(ctx context.Context) ([]string, error) {
	return w.wt.UnmergedFiles(ctx)
}

func (w serverWorktreeOps) Stage(ctx context.Context, paths []string) error {
	return w.wt.Stage(ctx, paths)
}

func (w serverWorktreeOps) CommitAllowEmpty(ctx context.Context, message string) error {
	return w.wt.CommitAllowEmpty(ctx, message)
}

func (w serverWorktreeOps) PushTo(ctx context.Context, remoteURL string) error {
	return w.wt.PushTo(ctx, remoteURL)
}
