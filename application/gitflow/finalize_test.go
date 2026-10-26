package gitflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ai/domain"
)

// fakeFinalizerHost — заглушка TaskFinalizerHost: фиксирует вызовы, отдаёт
// заранее заданные исходы.
type fakeFinalizerHost struct {
	proj   Project
	ref    BranchRef
	hasRef bool

	commitCalls int

	mrURL     string
	mrCreated bool
	mrErr     error

	merged   bool
	mergedOK bool

	mergeOutcome MergeOutcome
	mergeErr     error

	resolveOK    bool
	resolveCalls int
	resolveFiles []string

	saved    []*domain.Task
	removed  []string
	emits    []string
	statuses []string
}

func (h *fakeFinalizerHost) Project() Project { return h.proj }

func (h *fakeFinalizerHost) TaskBranch(string) (BranchRef, bool) { return h.ref, h.hasRef }

func (h *fakeFinalizerHost) CommitWorktree(context.Context, *domain.Task, string) { h.commitCalls++ }

func (h *fakeFinalizerHost) CreateTaskMR(context.Context, *domain.Task) (string, bool, error) {
	return h.mrURL, h.mrCreated, h.mrErr
}

func (h *fakeFinalizerHost) BranchAlreadyMerged(context.Context, string, string) (bool, bool) {
	return h.merged, h.mergedOK
}

func (h *fakeFinalizerHost) MergeTaskBranch(context.Context, *domain.Task) (MergeOutcome, error) {
	return h.mergeOutcome, h.mergeErr
}

func (h *fakeFinalizerHost) ResolveTaskConflict(_ context.Context, _ *domain.Task, files []string) bool {
	h.resolveCalls++
	h.resolveFiles = files
	return h.resolveOK
}

func (h *fakeFinalizerHost) SaveTask(_ context.Context, task *domain.Task) error {
	h.saved = append(h.saved, task)
	return nil
}

func (h *fakeFinalizerHost) RemoveTaskWorktree(taskID, worktree string) {
	h.removed = append(h.removed, taskID+"@"+worktree)
}

func (h *fakeFinalizerHost) EmitBoard(msg string) { h.emits = append(h.emits, msg) }

func (h *fakeFinalizerHost) AppendStatus(msg string) { h.statuses = append(h.statuses, msg) }

func newTask(taskID, epicID string) *domain.Task {
	return &domain.Task{TaskSpec: domain.TaskSpec{TaskID: taskID}, EpicID: epicID}
}

// Незаданный проект/без ветки — авто-шаги ничего не делают.
func TestTaskFinalizerSkipsNonGitAndNoBranch(t *testing.T) {
	ctx := context.Background()
	task := newTask("T-1", "E-1")

	notGit := &fakeFinalizerHost{proj: Project{IsGit: false}, hasRef: true}
	(&TaskFinalizer{Host: notGit}).Finalize(ctx, "p", task)
	if notGit.commitCalls != 0 || len(notGit.saved) != 0 {
		t.Fatalf("не-git проект должен пропускаться: %+v", notGit)
	}

	noBranch := &fakeFinalizerHost{proj: Project{IsGit: true}, hasRef: false}
	(&TaskFinalizer{Host: noBranch}).Finalize(ctx, "p", task)
	if noBranch.commitCalls != 0 || len(noBranch.emits) != 0 {
		t.Fatalf("без ветки авто-шаги не идут: %+v", noBranch)
	}
}

// Полный цикл: коммит → авто-MR → мёрдж → снятие worktree; признак конфликта
// снимается.
func TestTaskFinalizerHappyPath(t *testing.T) {
	ctx := context.Background()
	task := newTask("T-1", "E-1")
	task.MergeConflictFiles = []string{"a.go"}
	h := &fakeFinalizerHost{
		proj:         Project{IsGit: true, HasRemote: true},
		ref:          BranchRef{Branch: "ai/task/T-1", Worktree: "/wt"},
		hasRef:       true,
		mrURL:        "https://forge/mr/1",
		mrCreated:    true,
		mergeOutcome: MergeOutcome{AlreadyMerged: true},
	}
	(&TaskFinalizer{Host: h}).Finalize(ctx, "p", task)

	if h.commitCalls != 1 {
		t.Errorf("ожидался авто-коммит, got %d", h.commitCalls)
	}
	if task.MergeConflictFiles != nil {
		t.Errorf("успешный мёрдж должен снять конфликт, got %v", task.MergeConflictFiles)
	}
	if len(h.saved) != 1 {
		t.Errorf("ожидалась запись задачи на доску, got %d", len(h.saved))
	}
	if len(h.removed) != 1 || h.removed[0] != "T-1@/wt" {
		t.Errorf("worktree должен быть снят, got %v", h.removed)
	}
	if len(h.emits) != 1 || !strings.Contains(h.emits[0], "готовность задачи завершена") {
		t.Errorf("нет финального сообщения на доску: %v", h.emits)
	}
}

// Ветка задачи уже влита в релиз — мёрдж не запускается, worktree снимается.
func TestTaskFinalizerAlreadyMerged(t *testing.T) {
	ctx := context.Background()
	task := newTask("T-1", "E-1")
	h := &fakeFinalizerHost{
		proj:     Project{IsGit: true},
		ref:      BranchRef{Branch: "ai/task/T-1", Worktree: "/wt"},
		hasRef:   true,
		merged:   true,
		mergedOK: true,
	}
	(&TaskFinalizer{Host: h}).Finalize(ctx, "p", task)

	if h.mergeErr != nil || len(h.saved) != 0 {
		t.Errorf("мёрдж не должен выполняться: %+v", h)
	}
	if len(h.removed) != 1 {
		t.Errorf("worktree должен быть снят: %v", h.removed)
	}
	if len(h.emits) != 1 || !strings.Contains(h.emits[0], "без коммитов") {
		t.Errorf("нет сообщения «без коммитов»: %v", h.emits)
	}
}

// Ошибка авто-MR видна на доске, но не мешает мёрджу.
func TestTaskFinalizerMRErrorContinuesToMerge(t *testing.T) {
	ctx := context.Background()
	task := newTask("T-1", "E-1")
	h := &fakeFinalizerHost{
		proj:     Project{IsGit: true, HasRemote: true},
		ref:      BranchRef{Branch: "ai/task/T-1"},
		hasRef:   true,
		mrErr:    errors.New("push: нет scope `workflow`"),
		mergedOK: false,
	}
	(&TaskFinalizer{Host: h}).Finalize(ctx, "p", task)

	if len(h.emits) == 0 || !strings.Contains(h.emits[0], "авто-MR задачи T-1 не создан") {
		t.Errorf("ошибка MR должна попасть на доску: %v", h.emits)
	}
	if len(h.emits) < 2 || !strings.Contains(h.emits[len(h.emits)-1], "готовность задачи завершена") {
		t.Errorf("после ошибки MR мёрдж должен идти дальше: %v", h.emits)
	}
}

// Конфликт, разрешённый LLM: worktree снимается, конфликт не остаётся.
func TestTaskFinalizerConflictResolvedByLLM(t *testing.T) {
	ctx := context.Background()
	task := newTask("T-1", "E-1")
	h := &fakeFinalizerHost{
		proj:         Project{IsGit: true},
		ref:          BranchRef{Branch: "ai/task/T-1", Worktree: "/wt"},
		hasRef:       true,
		mergeOutcome: MergeOutcome{ConflictFiles: []string{"x.go"}},
		mergeErr:     ErrMergeConflict,
		resolveOK:    true,
	}
	(&TaskFinalizer{Host: h}).Finalize(ctx, "p", task)

	if h.resolveCalls != 1 || len(h.resolveFiles) != 1 || h.resolveFiles[0] != "x.go" {
		t.Errorf("LLM-резолв должен получить файлы конфликта: %+v", h)
	}
	if task.MergeConflictFiles != nil || task.Status == domain.StatusDevelopment {
		t.Errorf("разрешённый конфликт не должен откатывать задачу: %+v", task)
	}
	if len(h.removed) != 1 {
		t.Errorf("worktree должен быть снят: %v", h.removed)
	}
	if len(h.emits) == 0 || !strings.Contains(h.emits[len(h.emits)-1], "разрешён LLM") {
		t.Errorf("нет сообщения о LLM-разрешении: %v", h.emits)
	}
}

// Конфликт, который LLM не разрешил: задача → in_progress, worktree НЕ снят,
// конфликт записан, специалист получил статусное сообщение.
func TestTaskFinalizerConflictNeedsHuman(t *testing.T) {
	ctx := context.Background()
	task := newTask("T-1", "E-1")
	task.Status = domain.StatusDone
	h := &fakeFinalizerHost{
		proj:         Project{IsGit: true},
		ref:          BranchRef{Branch: "ai/task/T-1", Worktree: "/wt"},
		hasRef:       true,
		mergeOutcome: MergeOutcome{ConflictFiles: []string{"x.go", "y.go"}},
		mergeErr:     ErrMergeConflict,
		resolveOK:    false,
	}
	(&TaskFinalizer{Host: h}).Finalize(ctx, "p", task)

	if task.Status != domain.StatusDevelopment {
		t.Errorf("задача должна откатиться в in_progress, got %q", task.Status)
	}
	if strings.Join(task.MergeConflictFiles, ",") != "x.go,y.go" {
		t.Errorf("файлы конфликта должны осесть в задаче: %v", task.MergeConflictFiles)
	}
	if len(h.saved) != 1 {
		t.Errorf("откат должен записать задачу: %d", len(h.saved))
	}
	if len(h.removed) != 0 {
		t.Errorf("worktree НЕ снимается при неразрешённом конфликте: %v", h.removed)
	}
	if len(h.statuses) != 1 || !strings.Contains(h.statuses[0], "не прошёл") {
		t.Errorf("специалист должен получить статус: %v", h.statuses)
	}
	if len(h.emits) == 0 || !strings.Contains(h.emits[len(h.emits)-1], "откат в development") {
		t.Errorf("нет сообщения об откате: %v", h.emits)
	}
}

// Не-конфликтная ошибка мёрджа: worktree снимается, ошибка на доске.
func TestTaskFinalizerMergeErrorRemovesWorktree(t *testing.T) {
	ctx := context.Background()
	task := newTask("T-1", "E-1")
	h := &fakeFinalizerHost{
		proj:     Project{IsGit: true},
		ref:      BranchRef{Branch: "ai/task/T-1", Worktree: "/wt"},
		hasRef:   true,
		mergeErr: errors.New("git push: exit status 1"),
	}
	(&TaskFinalizer{Host: h}).Finalize(ctx, "p", task)

	if len(h.removed) != 1 {
		t.Errorf("worktree должен быть снят при ошибке мёрджа: %v", h.removed)
	}
	if len(h.emits) == 0 || !strings.Contains(h.emits[len(h.emits)-1], "завершён с ошибкой") {
		t.Errorf("нет сообщения об ошибке на доске: %v", h.emits)
	}
}

// Пустой worktree: коммит и снятие не зовутся, авто-MR/мёрдж продолжаются.
func TestTaskFinalizerEmptyWorktree(t *testing.T) {
	ctx := context.Background()
	task := newTask("T-1", "E-1")
	h := &fakeFinalizerHost{
		proj:   Project{IsGit: true},
		ref:    BranchRef{Branch: "ai/task/T-1", Worktree: "  "},
		hasRef: true,
	}
	(&TaskFinalizer{Host: h}).Finalize(ctx, "p", task)

	if h.commitCalls != 0 || len(h.removed) != 0 {
		t.Errorf("пустой worktree — no-op для коммита/снятия: %+v", h)
	}
	if len(h.emits) == 0 || !strings.Contains(h.emits[len(h.emits)-1], "готовность задачи завершена") {
		t.Errorf("мёрдж должен пройти: %v", h.emits)
	}
}

// Задача без TaskID игнорируется.
func TestTaskFinalizerIgnoresEmptyTask(t *testing.T) {
	h := &fakeFinalizerHost{proj: Project{IsGit: true}, hasRef: true}
	(&TaskFinalizer{Host: h}).Finalize(context.Background(), "p", nil)
	(&TaskFinalizer{Host: h}).Finalize(context.Background(), "p", &domain.Task{})
	if h.commitCalls != 0 || len(h.emits) != 0 {
		t.Fatalf("пустая задача не должна ничего делать: %+v", h)
	}
}
