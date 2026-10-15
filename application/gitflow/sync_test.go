package gitflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ai/domain"
)

// fakeEpicRepo — заглушка EpicRepo: фиксирует вызовы, отдаёт заданные исходы.
type fakeEpicRepo struct {
	root string

	mergeFeatureErr  error
	mergeFeatureRes  *domain.MergeResult
	mergeFeatureCall int

	addWorktreePath string
	addWorktreeErr  error
	wt              *fakeWorktreeOps

	removeCalls []string
}

func (r *fakeEpicRepo) Root() string { return r.root }

func (r *fakeEpicRepo) MergeFeature(context.Context, string, string, domain.MergeFeatureOptions) (*domain.MergeResult, error) {
	r.mergeFeatureCall++
	return r.mergeFeatureRes, r.mergeFeatureErr
}

func (r *fakeEpicRepo) AddWorktree(_ context.Context, path, _ string) (WorktreeOps, error) {
	r.addWorktreePath = path
	if r.addWorktreeErr != nil {
		return nil, r.addWorktreeErr
	}
	return r.wt, nil
}

func (r *fakeEpicRepo) RemoveWorktree(_ context.Context, path string) error {
	r.removeCalls = append(r.removeCalls, path)
	return nil
}

// fakeWorktreeOps — заглушка WorktreeOps.
type fakeWorktreeOps struct {
	unmerged []string
	stageErr error
}

func (w *fakeWorktreeOps) MergeBranch(context.Context, string, string) (*domain.MergeResult, error) {
	return &domain.MergeResult{}, nil
}

func (w *fakeWorktreeOps) UnmergedFiles(context.Context) ([]string, error) { return w.unmerged, nil }

func (w *fakeWorktreeOps) Stage(context.Context, []string) error { return w.stageErr }

func (w *fakeWorktreeOps) CommitAllowEmpty(context.Context, string) error { return nil }

func (w *fakeWorktreeOps) PushTo(context.Context, string) error { return nil }

// fakeEpicMainHost — заглушка EpicMainHost.
type fakeEpicMainHost struct {
	base    string
	pushURL string
	infoOK  bool

	epicRef BranchRef
	refOK   bool

	repo       *fakeEpicRepo
	repoErr    bool
	repoCalled int

	dropCalls   int
	lockedCalls int

	attemptOK    bool
	attemptCalls int
	attemptFiles []string

	trivialResolved []string
	trivialHard     []string
	trivialErr      error

	conflictFiles []string
	conflictSet   int

	notifies []string
	statuses []string
}

func (h *fakeEpicMainHost) ProjectInfo() (string, string, bool) { return h.base, h.pushURL, h.infoOK }

func (h *fakeEpicMainHost) EpicBranch(string) (BranchRef, bool) { return h.epicRef, h.refOK }

func (h *fakeEpicMainHost) Repo(context.Context) (EpicRepo, bool) {
	h.repoCalled++
	if h.repoErr {
		return nil, false
	}
	return h.repo, true
}

func (h *fakeEpicMainHost) DropEpicWorktreeLocked(context.Context, string) { h.dropCalls++ }

func (h *fakeEpicMainHost) Locked(fn func()) {
	h.lockedCalls++
	fn()
}

func (h *fakeEpicMainHost) AttemptLLMResolve(_ context.Context, _ *domain.Epic, _ string, files []string, _ string) bool {
	h.attemptCalls++
	h.attemptFiles = files
	return h.attemptOK
}

func (h *fakeEpicMainHost) SetEpicMergeConflict(_ context.Context, _ string, files []string) {
	h.conflictSet++
	h.conflictFiles = files
}

func (h *fakeEpicMainHost) Notify(msg string) { h.notifies = append(h.notifies, msg) }

func (h *fakeEpicMainHost) Status(msg string) { h.statuses = append(h.statuses, msg) }

func (h *fakeEpicMainHost) TrivialResolve(_ string, _ []string) ([]string, []string, error) {
	return h.trivialResolved, h.trivialHard, h.trivialErr
}

func newEpic() *domain.Epic {
	return &domain.Epic{TaskSpec: domain.TaskSpec{TaskID: "E-1"}}
}

// Не git-проект / нет базы / нет ветки эпика / нет репозитория — синхрон выходит
// без побочных эффектов.
func TestEpicMainSyncerSkipsBadState(t *testing.T) {
	ctx := context.Background()

	cases := map[string]func(*fakeEpicMainHost){
		"не git":          func(h *fakeEpicMainHost) { h.infoOK = false },
		"нет базы":        func(h *fakeEpicMainHost) { h.infoOK = true; h.base = "  " },
		"нет ветки":       func(h *fakeEpicMainHost) { h.infoOK = true; h.base = "main"; h.refOK = false },
		"нет репозитория": func(h *fakeEpicMainHost) { h.infoOK = true; h.base = "main"; h.refOK = true; h.repoErr = true },
	}
	for name, setup := range cases {
		h := &fakeEpicMainHost{}
		setup(h)
		(&EpicMainSyncer{Host: h}).SyncEpicMain(ctx, "p", newEpic())
		if h.dropCalls != 0 || len(h.notifies) != 0 {
			t.Errorf("%s: синхрон должен выйти рано: %+v", name, h)
		}
	}
}

// Быстрый путь: MergeFeature успешен → конфликт снят, сообщение на доску.
func TestEpicMainSyncerFastPath(t *testing.T) {
	ctx := context.Background()
	h := &fakeEpicMainHost{
		base:    "main",
		infoOK:  true,
		epicRef: BranchRef{Branch: "ai/epic/E-1"},
		refOK:   true,
		repo: &fakeEpicRepo{
			root:            "/tmp/projects/myrepo",
			mergeFeatureRes: &domain.MergeResult{AlreadyMerged: true},
		},
	}
	(&EpicMainSyncer{Host: h}).SyncEpicMain(ctx, "p", newEpic())

	if h.repo.mergeFeatureCall != 1 {
		t.Fatalf("MergeFeature должен быть вызван: %d", h.repo.mergeFeatureCall)
	}
	if h.repo.addWorktreePath != "" {
		t.Errorf("worktree не нужен на быстром пути: %q", h.repo.addWorktreePath)
	}
	if h.conflictSet != 1 || h.conflictFiles != nil {
		t.Errorf("конфликт должен быть снят: %+v", h)
	}
	if len(h.notifies) != 1 || !strings.Contains(h.notifies[0], "авто-синхрон эпика с main") {
		t.Errorf("нет сообщения на доску: %v", h.notifies)
	}
	if h.lockedCalls != 1 {
		t.Errorf("SyncEpicMain должен брать mergeLock: %d", h.lockedCalls)
	}
}

// Ошибка MergeFeature без конфликта (не context.Canceled) — лог, выход.
func TestEpicMainSyncerMergeError(t *testing.T) {
	ctx := context.Background()
	h := &fakeEpicMainHost{
		base:    "main",
		infoOK:  true,
		epicRef: BranchRef{Branch: "ai/epic/E-1"},
		refOK:   true,
		repo: &fakeEpicRepo{
			root:            "/tmp/projects/myrepo",
			mergeFeatureErr: errors.New("git push: exit status 1"),
		},
	}
	(&EpicMainSyncer{Host: h}).SyncEpicMain(ctx, "p", newEpic())

	if h.repo.addWorktreePath != "" || len(h.notifies) != 0 {
		t.Errorf("без конфликта авто-резолв не запускается: %+v", h)
	}
}

// Конфликт, тривиальные блоки авто-разрешены: worktree создаётся, merge-коммит,
// push, конфликт снят.
func TestEpicMainSyncerTrivialResolve(t *testing.T) {
	ctx := context.Background()
	wt := &fakeWorktreeOps{unmerged: []string{"a.go", "b.go"}}
	h := &fakeEpicMainHost{
		base:    "main",
		pushURL: "https://x-access-token:tok@github.com/g/r.git",
		infoOK:  true,
		epicRef: BranchRef{Branch: "ai/epic/E-1"},
		refOK:   true,
		repo: &fakeEpicRepo{
			root:            "/tmp/projects/myrepo",
			mergeFeatureErr: &domain.MergeConflictError{Feature: "main", Files: []string{"a.go", "b.go"}},
			wt:              wt,
		},
		trivialResolved: []string{"a.go", "b.go"},
	}
	(&EpicMainSyncer{Host: h}).SyncEpicMain(ctx, "p", newEpic())

	if h.repo.addWorktreePath == "" {
		t.Fatalf("worktree конфликта должен быть создан: %+v", h.repo)
	}
	if !strings.Contains(h.repo.addWorktreePath, ".conflict-p-E-1") {
		t.Errorf("неверный путь worktree: %q", h.repo.addWorktreePath)
	}
	if h.attemptCalls != 0 {
		t.Errorf("LLM не нужен для тривиальных конфликтов: %d", h.attemptCalls)
	}
	if h.conflictSet != 1 || h.conflictFiles != nil {
		t.Errorf("конфликт должен быть снят: %+v", h)
	}
	if len(h.notifies) != 1 || !strings.Contains(h.notifies[0], "конфликты разрешены") {
		t.Errorf("нет сообщения о разрешении: %v", h.notifies)
	}
}

// Сложный конфликт, разрешённый LLM: worktree снят, конфликт снят, сообщение.
func TestEpicMainSyncerHardConflictLLMResolved(t *testing.T) {
	ctx := context.Background()
	h := &fakeEpicMainHost{
		base:    "main",
		infoOK:  true,
		epicRef: BranchRef{Branch: "ai/epic/E-1"},
		refOK:   true,
		repo: &fakeEpicRepo{
			root:            "/tmp/projects/myrepo",
			mergeFeatureErr: &domain.MergeConflictError{Feature: "main", Files: []string{"hard.go"}},
			wt:              &fakeWorktreeOps{unmerged: []string{"hard.go"}},
		},
		trivialHard: []string{"hard.go"},
		attemptOK:   true,
	}
	(&EpicMainSyncer{Host: h}).SyncEpicMain(ctx, "p", newEpic())

	if h.attemptCalls != 1 || len(h.attemptFiles) != 1 || h.attemptFiles[0] != "hard.go" {
		t.Errorf("LLM должен получить сложные файлы: %+v", h)
	}
	if h.conflictSet != 1 || h.conflictFiles != nil {
		t.Errorf("разрешённый конфликт должен быть снят: %+v", h)
	}
	if len(h.notifies) != 1 || !strings.Contains(h.notifies[0], "разрешены LLM") {
		t.Errorf("нет сообщения о LLM-разрешении: %v", h.notifies)
	}
}

// Сложный конфликт, LLM не справился: признак конфликта записан, статус в чат,
// сообщение на доску.
func TestEpicMainSyncerHardConflictNeedsHuman(t *testing.T) {
	ctx := context.Background()
	h := &fakeEpicMainHost{
		base:    "main",
		infoOK:  true,
		epicRef: BranchRef{Branch: "ai/epic/E-1"},
		refOK:   true,
		repo: &fakeEpicRepo{
			root:            "/tmp/projects/myrepo",
			mergeFeatureErr: &domain.MergeConflictError{Feature: "main", Files: []string{"hard.go"}},
			wt:              &fakeWorktreeOps{unmerged: []string{"hard.go"}},
		},
		trivialHard: []string{"hard.go"},
		attemptOK:   false,
	}
	(&EpicMainSyncer{Host: h}).SyncEpicMain(ctx, "p", newEpic())

	if h.attemptCalls != 1 {
		t.Fatalf("LLM должен быть вызван: %d", h.attemptCalls)
	}
	if h.conflictSet != 1 || len(h.conflictFiles) != 1 || h.conflictFiles[0] != "hard.go" {
		t.Errorf("конфликт должен быть записан на доску: %+v", h)
	}
	if len(h.statuses) != 1 || !strings.Contains(h.statuses[0], "rebase/резолв") {
		t.Errorf("специалист должен получить статус: %v", h.statuses)
	}
	if len(h.notifies) != 1 || !strings.Contains(h.notifies[0], "сложные конфликты") {
		t.Errorf("нет сообщения о сложных конфликтах: %v", h.notifies)
	}
}

// SyncMainForTask не берёт mergeLock (вызывающий уже держит его).
func TestEpicMainSyncerForTaskNoLock(t *testing.T) {
	ctx := context.Background()
	h := &fakeEpicMainHost{
		base:    "main",
		infoOK:  true,
		epicRef: BranchRef{Branch: "ai/epic/E-1"},
		refOK:   true,
		repo: &fakeEpicRepo{
			root:            "/tmp/projects/myrepo",
			mergeFeatureRes: &domain.MergeResult{},
		},
	}
	(&EpicMainSyncer{Host: h}).SyncMainForTask(ctx, "p", "E-1")

	if h.lockedCalls != 0 {
		t.Errorf("SyncMainForTask не должен брать mergeLock: %d", h.lockedCalls)
	}
	if h.repo.mergeFeatureCall != 1 {
		t.Errorf("MergeFeature должен быть вызван: %d", h.repo.mergeFeatureCall)
	}
}
