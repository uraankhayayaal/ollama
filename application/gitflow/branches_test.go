package gitflow

import (
	"context"
	"errors"
	"testing"

	"ai/domain"
)

// fakeBranchRepo — заглушка BranchRepo: помнит, какие ветки «есть», фиксирует
// создание.
type fakeBranchRepo struct {
	exists    map[string]bool
	existsErr error
	createErr error
	created   []string // "branch<-base"
}

func (r *fakeBranchRepo) BranchExists(_ context.Context, branch string) (bool, error) {
	if r.existsErr != nil {
		return false, r.existsErr
	}
	return r.exists[branch], nil
}

func (r *fakeBranchRepo) CreateBranch(_ context.Context, branch, base string) error {
	if r.createErr != nil {
		return r.createErr
	}
	r.created = append(r.created, branch+"<-"+base)
	return nil
}

// fakeBranchRegistry — заглушка side-реестра веток.
type fakeBranchRegistry struct {
	epics map[string]BranchRef
	tasks map[string]BranchRef

	setEpicErr error
	epicErr    error

	deletedEpicBranches []string
	deletedTaskBranches []string
	deletedEpicMRs      []string
	deletedTaskMRs      []string
}

func newFakeBranchRegistry() *fakeBranchRegistry {
	return &fakeBranchRegistry{epics: map[string]BranchRef{}, tasks: map[string]BranchRef{}}
}

func (r *fakeBranchRegistry) SetEpicBranch(epicID string, ref BranchRef) error {
	if r.setEpicErr != nil {
		return r.setEpicErr
	}
	r.epics[epicID] = ref
	return nil
}

func (r *fakeBranchRegistry) EpicBranch(epicID string) (BranchRef, error) {
	if r.epicErr != nil {
		return BranchRef{}, r.epicErr
	}
	ref, ok := r.epics[epicID]
	if !ok {
		return BranchRef{}, errors.New("нет ветки")
	}
	return ref, nil
}

func (r *fakeBranchRegistry) SetTaskBranch(taskID string, ref BranchRef) error {
	r.tasks[taskID] = ref
	return nil
}

func (r *fakeBranchRegistry) DeleteEpicBranch(epicID string) error {
	r.deletedEpicBranches = append(r.deletedEpicBranches, epicID)
	return nil
}

func (r *fakeBranchRegistry) DeleteTaskBranch(taskID string) error {
	r.deletedTaskBranches = append(r.deletedTaskBranches, taskID)
	return nil
}

func (r *fakeBranchRegistry) DeleteEpicMR(epicID string) error {
	r.deletedEpicMRs = append(r.deletedEpicMRs, epicID)
	return nil
}

func (r *fakeBranchRegistry) DeleteTaskMR(taskID string) error {
	r.deletedTaskMRs = append(r.deletedTaskMRs, taskID)
	return nil
}

// fakeBranchStore — заглушка Store (SaveEpic/SaveTask).
type fakeBranchStore struct {
	savedEpics []string
	savedTasks []string
	saveErr    error
}

func (s *fakeBranchStore) SaveEpic(_ context.Context, epic *domain.Epic) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.savedEpics = append(s.savedEpics, epic.TaskID)
	return nil
}

func (s *fakeBranchStore) SaveTask(_ context.Context, task *domain.Task) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.savedTasks = append(s.savedTasks, task.TaskID)
	return nil
}

func newBrancher(repo *fakeBranchRepo, reg *fakeBranchRegistry, store *fakeBranchStore) (Brancher, *int) {
	changed := new(int)
	return Brancher{
		Project:  "p",
		Base:     "main",
		Repo:     repo,
		Registry: reg,
		Store:    store,
		Changed:  func() { *changed++ },
	}, changed
}

func epic(id string) *domain.Epic {
	return &domain.Epic{TaskSpec: domain.TaskSpec{TaskID: id}}
}

func task(id, epicID string) *domain.Task {
	return &domain.Task{TaskSpec: domain.TaskSpec{TaskID: id}, EpicID: epicID}
}

// Создание ветки эпика: ref создаётся от базы, пишется в реестр, git_branch
// проставляется на доске, Changed зовётся.
func TestBrancherCreateEpicBranch(t *testing.T) {
	repo := &fakeBranchRepo{exists: map[string]bool{}}
	reg := newFakeBranchRegistry()
	store := &fakeBranchStore{}
	b, changed := newBrancher(repo, reg, store)

	e := epic("epic-1")
	ref, err := b.CreateEpicBranch(context.Background(), e)
	if err != nil {
		t.Fatalf("CreateEpicBranch: %v", err)
	}
	if ref.Branch != "ai/epic/epic-1" || ref.Base != "main" {
		t.Fatalf("ref = %+v", ref)
	}
	if len(repo.created) != 1 || repo.created[0] != "ai/epic/epic-1<-main" {
		t.Fatalf("created = %v", repo.created)
	}
	if reg.epics["epic-1"].Branch != "ai/epic/epic-1" {
		t.Fatalf("реестр = %+v", reg.epics)
	}
	if e.GitBranch != "ai/epic/epic-1" {
		t.Fatalf("git_branch = %q", e.GitBranch)
	}
	if len(store.savedEpics) != 1 || *changed != 1 {
		t.Fatalf("saved=%v changed=%d", store.savedEpics, *changed)
	}
}

// Идемпотентность: ветка уже есть — git branch не зовётся повторно, но запись
// в реестр и доску происходит.
func TestBrancherCreateEpicBranchIdempotent(t *testing.T) {
	repo := &fakeBranchRepo{exists: map[string]bool{"ai/epic/epic-1": true}}
	reg := newFakeBranchRegistry()
	store := &fakeBranchStore{}
	b, _ := newBrancher(repo, reg, store)

	if _, err := b.CreateEpicBranch(context.Background(), epic("epic-1")); err != nil {
		t.Fatalf("CreateEpicBranch: %v", err)
	}
	if len(repo.created) != 0 {
		t.Fatalf("существующая ветка не должна создаваться повторно: %v", repo.created)
	}
	if reg.epics["epic-1"].Base != "main" {
		t.Fatalf("реестр = %+v", reg.epics)
	}
}

// База не задана — ошибка, ничего не создаётся.
func TestBrancherCreateEpicBranchNoBase(t *testing.T) {
	b, _ := newBrancher(&fakeBranchRepo{exists: map[string]bool{}}, newFakeBranchRegistry(), &fakeBranchStore{})
	b.Base = ""
	if _, err := b.CreateEpicBranch(context.Background(), epic("epic-1")); err == nil {
		t.Fatal("ожидали ошибку при пустой базе")
	}
}

// Сбой git при создании — ErrBranchCreate.
func TestBrancherCreateEpicBranchGitError(t *testing.T) {
	repo := &fakeBranchRepo{exists: map[string]bool{}, createErr: errors.New("boom")}
	b, _ := newBrancher(repo, newFakeBranchRegistry(), &fakeBranchStore{})
	_, err := b.CreateEpicBranch(context.Background(), epic("epic-1"))
	if !errors.Is(err, ErrBranchCreate) {
		t.Fatalf("err = %v, want ErrBranchCreate", err)
	}
}

// Ветка задачи создаётся от релизной ветки эпика из реестра.
func TestBrancherCreateTaskBranch(t *testing.T) {
	repo := &fakeBranchRepo{exists: map[string]bool{}}
	reg := newFakeBranchRegistry()
	reg.epics["epic-1"] = BranchRef{Branch: "ai/epic/epic-1", Base: "main"}
	store := &fakeBranchStore{}
	b, _ := newBrancher(repo, reg, store)

	tr := task("task-1", "epic-1")
	ref, err := b.CreateTaskBranch(context.Background(), tr)
	if err != nil {
		t.Fatalf("CreateTaskBranch: %v", err)
	}
	if ref.Branch != "ai/task/task-1" || ref.Base != "ai/epic/epic-1" {
		t.Fatalf("ref = %+v", ref)
	}
	if repo.created[0] != "ai/task/task-1<-ai/epic/epic-1" {
		t.Fatalf("created = %v", repo.created)
	}
	if tr.GitBranch != "ai/task/task-1" {
		t.Fatalf("git_branch = %q", tr.GitBranch)
	}
}

// Нет ветки эпика — ErrEpicBranchMissing.
func TestBrancherCreateTaskBranchNeedsEpicBranch(t *testing.T) {
	repo := &fakeBranchRepo{exists: map[string]bool{}}
	b, _ := newBrancher(repo, newFakeBranchRegistry(), &fakeBranchStore{})
	_, err := b.CreateTaskBranch(context.Background(), task("task-1", "epic-1"))
	if !errors.Is(err, ErrEpicBranchMissing) {
		t.Fatalf("err = %v, want ErrEpicBranchMissing", err)
	}
	if len(repo.created) != 0 {
		t.Fatalf("git не должен вызываться: %v", repo.created)
	}
}

// Авто-ветка эпика: сбой git → false, без паники.
func TestBrancherAutoCreateEpicBranchError(t *testing.T) {
	repo := &fakeBranchRepo{exists: map[string]bool{}, createErr: errors.New("boom")}
	b, _ := newBrancher(repo, newFakeBranchRegistry(), &fakeBranchStore{})
	if b.AutoCreateEpicBranch(context.Background(), epic("epic-1")) {
		t.Fatal("ожидали false при сбое git")
	}
	if b.AutoCreateEpicBranch(context.Background(), nil) {
		t.Fatal("nil-эпик → false")
	}
}

// Авто-ветка задачи без ветки эпика: false (штатный пропуск).
func TestBrancherAutoCreateTaskBranchSkipsWithoutEpic(t *testing.T) {
	repo := &fakeBranchRepo{exists: map[string]bool{}}
	b, _ := newBrancher(repo, newFakeBranchRegistry(), &fakeBranchStore{})
	if b.AutoCreateTaskBranch(context.Background(), task("task-1", "epic-1")) {
		t.Fatal("ожидали false без ветки эпика")
	}
}

// RemoveEpicBranches: снимает ветки эпика и всех задач + MR, зовёт Changed.
func TestBrancherRemoveEpicBranches(t *testing.T) {
	reg := newFakeBranchRegistry()
	reg.epics["epic-1"] = BranchRef{Branch: "ai/epic/epic-1"}
	reg.tasks["task-1"] = BranchRef{Branch: "ai/task/task-1"}
	reg.tasks["task-2"] = BranchRef{Branch: "ai/task/task-2"}
	b, changed := newBrancher(&fakeBranchRepo{exists: map[string]bool{}}, reg, &fakeBranchStore{})

	b.RemoveEpicBranches("epic-1", []string{"task-1", "task-2"})
	if len(reg.deletedEpicBranches) != 1 || reg.deletedEpicBranches[0] != "epic-1" {
		t.Fatalf("epic branches: %v", reg.deletedEpicBranches)
	}
	if len(reg.deletedTaskBranches) != 2 {
		t.Fatalf("task branches: %v", reg.deletedTaskBranches)
	}
	if len(reg.deletedEpicMRs) != 1 || len(reg.deletedTaskMRs) != 2 {
		t.Fatalf("MRs: epic=%v task=%v", reg.deletedEpicMRs, reg.deletedTaskMRs)
	}
	if *changed != 1 {
		t.Fatalf("Changed = %d", *changed)
	}
}

// EnsureBranch: ветка есть — no-op; нет — создаётся; ошибка проверки — ошибка.
func TestBrancherEnsureBranch(t *testing.T) {
	ctx := context.Background()

	repo := &fakeBranchRepo{exists: map[string]bool{"b": true}}
	b, _ := newBrancher(repo, newFakeBranchRegistry(), &fakeBranchStore{})
	if err := b.EnsureBranch(ctx, "b", "main"); err != nil || len(repo.created) != 0 {
		t.Fatalf("существующая ветка: err=%v created=%v", err, repo.created)
	}
	if err := b.EnsureBranch(ctx, "new", "main"); err != nil || len(repo.created) != 1 {
		t.Fatalf("новая ветка: err=%v created=%v", err, repo.created)
	}

	failing := &fakeBranchRepo{existsErr: errors.New("boom")}
	b2, _ := newBrancher(failing, newFakeBranchRegistry(), &fakeBranchStore{})
	if err := b2.EnsureBranch(ctx, "x", "main"); err == nil {
		t.Fatal("ожидали ошибку проверки ветки")
	}
}
