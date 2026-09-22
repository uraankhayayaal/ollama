package server

// Hermetic-тесты авто-шагов git-workflow (Ф-1/Ф-2/Ф-3): серверные хуки доски
// (attachGitHooks) создают ветки эпиков/задач при появлении записей, worktree
// ветки при переводе задачи «в работу», а gitStatus считает «есть коммиты» для
// кнопки MR. Все git-операции выполняет fakeGit — реальный CLI не нужен.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"ai/board"
	"ai/projects"
	"ai/workspace"
)

// TestAutoCreateEpicBranchOnHook — Ф-1: добавление эпика на доску через
// hooked-хранилище автоматически создаёт ветку ai/epic/<id> от git_base и
// проставляет её в side-реестр и в запись эпика.
func TestAutoCreateEpicBranchOnHook(t *testing.T) {
	git := &fakeGit{fails: map[string]string{
		"git rev-parse --verify --quiet refs/heads/ai/epic/epic-1": "ветки нет",
	}}
	srv, _, mr := setupGitflow(t, git)
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")

	ctx := context.Background()
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	srv.attachGitHooks("myrepo", store)

	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Релиз"}, Status: board.StatusNew,
	}); err != nil {
		t.Fatal(err)
	}
	if !git.saw("git branch ai/epic/epic-1 main") {
		t.Fatalf("авто-создание ветки эпика не вызвало git branch, вызовы: %v", git.callsList())
	}
	ref, err := srv.reg.EpicBranch("myrepo", "epic-1")
	if err != nil || ref.Branch != "ai/epic/epic-1" || ref.Base != "main" {
		t.Fatalf("EpicBranch = %+v, %v", ref, err)
	}
	epic, err := store.GetEpic(ctx, "epic-1")
	if err != nil || epic.GitBranch != "ai/epic/epic-1" {
		t.Fatalf("epic.git_branch = %q, %v", epic.GitBranch, err)
	}
}

// TestAutoCreateTaskBranchOnHook — Ф-1: добавление задачи (после того, как ветка
// её эпика уже создана) автоматически создаёт ai/task/<id> от ветки эпика.
func TestAutoCreateTaskBranchOnHook(t *testing.T) {
	git := &fakeGit{fails: map[string]string{
		"git rev-parse --verify --quiet refs/heads/ai/epic/epic-1": "ветки нет",
		"git rev-parse --verify --quiet refs/heads/ai/task/task-1": "ветки нет",
	}}
	srv, _, mr := setupGitflow(t, git)
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")

	ctx := context.Background()
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	srv.attachGitHooks("myrepo", store)

	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Релиз"}, Status: board.StatusNew,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "task-1", Title: "Фича"},
		EpicID:   "epic-1",
	}); err != nil {
		t.Fatal(err)
	}
	if !git.saw("git branch ai/task/task-1 ai/epic/epic-1") {
		t.Fatalf("авто-создание ветки задачи: ожидали git branch от ветки эпика, вызовы: %v", git.callsList())
	}
	tref, err := srv.reg.TaskBranch("myrepo", "task-1")
	if err != nil || tref.Branch != "ai/task/task-1" || tref.Base != "ai/epic/epic-1" {
		t.Fatalf("TaskBranch = %+v, %v", tref, err)
	}
	task, err := store.GetTask(ctx, "task-1")
	if err != nil || task.GitBranch != "ai/task/task-1" {
		t.Fatalf("task.git_branch = %q, %v", task.GitBranch, err)
	}
}

// TestAutoCreateTaskBranchSkipsWithoutEpicBranch — Ф-1: задача без ветки эпика
// ветку не получает (доска и git не мутируют) — «Создать ветку задачи»
// остаётся ручной страховкой.
func TestAutoCreateTaskBranchSkipsWithoutEpicBranch(t *testing.T) {
	git := &fakeGit{}
	srv, _, mr := setupGitflow(t, git)
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")

	ctx := context.Background()
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()

	// Эпик заводим ДО крепления хуков: пары «ветка эпика ↔ ветка задачи» не
	// создаём (сценарий: эпик без ветки + задача). Сам эпик в хранилище нужен,
	// иначе CreateTask упрётся в валидацию EpicID.
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Релиз"}, Status: board.StatusNew,
	}); err != nil {
		t.Fatal(err)
	}
	srv.attachGitHooks("myrepo", store)

	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "task-1", Title: "Фича"},
		EpicID:   "epic-1",
	}); err != nil {
		t.Fatal(err)
	}
	if git.saw("git branch ") {
		t.Fatalf("без ветки эпика ветка задачи не должна создаваться, вызовы: %v", git.callsList())
	}
	if _, err := srv.reg.TaskBranch("myrepo", "task-1"); err == nil {
		t.Fatalf("TaskBranch не должен быть зарегистрирован")
	}
}

// TestTaskInProgressCreatesWorktree — Ф-3: перевод задачи «в работу» через
// REST создаёт постоянный worktree её ветки и пишет путь в side-реестр.
func TestTaskInProgressCreatesWorktree(t *testing.T) {
	git := mockMergeGit("")
	srv, handler, mr := setupGitflow(t, git)
	seedGitflowBoard(t, mr, srv)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"PUT", "/api/projects/myrepo/tasks/task-1", strings.NewReader(`{"status":"in_progress"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("перевод в in_progress: %d, body: %s", rec.Code, rec.Body.String())
	}

	ref, err := srv.reg.TaskBranch("myrepo", "task-1")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(projects.ProjectDir("myrepo")), ".wt-task-myrepo-task-1")
	if ref.Worktree != want {
		t.Fatalf("Worktree = %q, want %q", ref.Worktree, want)
	}
	if !git.saw("git worktree add "+want) {
		t.Fatalf("in_progress не создал worktree деревья, вызовы: %v", git.callsList())
	}
}

// TestGitStatusHasCommits — Ф-2: gitStatus считает has_commits локально через
// git rev-list --count: 0 → false (коммитов ещё нет), ненулевое → true, ошибка
// git → nil (поле отсутствует, кнопка MR остаётся).
func TestGitStatusHasCommits(t *testing.T) {
	ctx := context.Background()

	// Новое, ещё «пустое» дерево: rev-list = 0.
	git := &fakeGit{starts: map[string]string{"git rev-list --count": "0\n"}}
	srv, _, _ := setupGitflow(t, git)
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")
	if err := srv.reg.SetEpicBranch("myrepo", "epic-1", workspace.BranchRef{Branch: "ai/epic/e1", Base: "main"}); err != nil {
		t.Fatal(err)
	}
	v := srv.gitStatus(ctx, "myrepo",
		[]*board.Epic{{TaskSpec: board.TaskSpec{TaskID: "epic-1"}}}, nil)
	if v == nil {
		t.Fatal("gitStatus вернул nil для git-проекта")
	}
	lv := v.Epics["epic-1"]
	if lv.HasCommits == nil || *lv.HasCommits {
		t.Fatalf("has_commits пустого дерева = %v, want false", lv.HasCommits)
	}

	// Ветка с коммитами: count = 5.
	git2 := &fakeGit{starts: map[string]string{"git rev-list --count": "5\n"}}
	srv2, _, _ := setupGitflow(t, git2)
	registerGit(t, srv2, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")
	if err := srv2.reg.SetEpicBranch("myrepo", "epic-1", workspace.BranchRef{Branch: "ai/epic/e1", Base: "main"}); err != nil {
		t.Fatal(err)
	}
	v2 := srv2.gitStatus(ctx, "myrepo", []*board.Epic{{TaskSpec: board.TaskSpec{TaskID: "epic-1"}}}, nil)
	if lv2 := v2.Epics["epic-1"]; lv2.HasCommits == nil || !*lv2.HasCommits {
		t.Fatalf("has_commits непустого дерева = %v, want true", lv2.HasCommits)
	}

	// Ошибка git (ветка/база не резолвятся) — nil, не ломает статус.
	git3 := &fakeGit{fails: map[string]string{"git rev-list --count": "нет ветки"}}
	srv3, _, _ := setupGitflow(t, git3)
	registerGit(t, srv3, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")
	if err := srv3.reg.SetEpicBranch("myrepo", "epic-1", workspace.BranchRef{Branch: "ai/epic/e1", Base: "main"}); err != nil {
		t.Fatal(err)
	}
	v3 := srv3.gitStatus(ctx, "myrepo", []*board.Epic{{TaskSpec: board.TaskSpec{TaskID: "epic-1"}}}, nil)
	if lv3 := v3.Epics["epic-1"]; lv3.HasCommits != nil {
		t.Fatalf("has_commits при ошибке git = %v, want nil", lv3.HasCommits)
	}
}