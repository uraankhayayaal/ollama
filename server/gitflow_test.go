package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ai/board"
	"ai/gitops"
	"ai/workspace"

	"github.com/alicebob/miniredis/v2"
)

// setupGitflow создаёт git-проект + эпик/задачу на доске для тестов gitflow.
func setupGitflow(t *testing.T, git *fakeGit) (*Server, http.Handler, *miniredis.Miniredis) {
	t.Helper()
	return newTestServerGit(t, git, nil)
}

func TestCreateEpicBranchCreatesAndRegisters(t *testing.T) {
	git := &fakeGit{fails: map[string]string{
		"git rev-parse --verify --quiet refs/heads/ai/epic/epic-1": "ветки нет",
	}}
	srv, handler, mr := setupGitflow(t, git)

	ctx := context.Background()
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Релиз"}, Status: board.StatusNew,
	}); err != nil {
		t.Fatal(err)
	}
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects/myrepo/epics/epic-1/branch",
		strings.NewReader(`{}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("создание ветки эпика: %d, body: %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["branch"] != "ai/epic/epic-1" || out["base"] != "main" {
		t.Fatalf("ответ = %+v, want branch=ai/epic/epic-1 base=main", out)
	}
	if !git.saw("git branch ai/epic/epic-1 main") {
		t.Fatalf("ожидали git branch, вызовы: %v", git.callsList())
	}

	// Side-реестр workspace.
	ref, err := srv.reg.EpicBranch("myrepo", "epic-1")
	if err != nil || ref.Branch != "ai/epic/epic-1" || ref.Base != "main" {
		t.Fatalf("EpicBranch = %+v, %v", ref, err)
	}
	// Доска: git_branch проставлен.
	epic, err := store.GetEpic(ctx, "epic-1")
	if err != nil || epic.GitBranch != "ai/epic/epic-1" {
		t.Fatalf("epic.git_branch = %q, %v", epic.GitBranch, err)
	}
}

func TestCreateEpicBranchIdempotent(t *testing.T) {
	// Ветка уже существует в клоне (rev-parse успешен) — повторный вызов не
	// создаёт её заново и возвращает ту же.
	git := &fakeGit{}
	srv, handler, mr := setupGitflow(t, git)
	ctx := context.Background()
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Релиз"},
	}); err != nil {
		t.Fatal(err)
	}
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/branch", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("создание ветки эпика: %d, body: %s", rec.Code, rec.Body.String())
	}
	if git.saw("git branch ") {
		t.Fatalf("существующая ветка не должна создаваться повторно, вызовы: %v", git.callsList())
	}
}

func TestCreateTaskBranchFromEpicBranch(t *testing.T) {
	git := &fakeGit{fails: map[string]string{
		"git rev-parse --verify --quiet refs/heads/ai/epic/epic-1": "ветки нет",
		"git rev-parse --verify --quiet refs/heads/ai/task/task-1": "ветки нет",
	}}
	srv, handler, mr := setupGitflow(t, git)
	ctx := context.Background()
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Релиз"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "task-1", Title: "Фича"},
		EpicID:   "epic-1",
	}); err != nil {
		t.Fatal(err)
	}
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")

	// Сначала ветка эпика.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/branch", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("ветка эпика: %d, body: %s", rec.Code, rec.Body.String())
	}

	// Ветка задачи от ветки эпика.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/tasks/task-1/branch", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("ветка задачи: %d, body: %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["branch"] != "ai/task/task-1" || out["base"] != "ai/epic/epic-1" {
		t.Fatalf("ответ = %+v, want task-1 от ветки эпика", out)
	}
	if !git.saw("git branch ai/task/task-1 ai/epic/epic-1") {
		t.Fatalf("ожидали git branch задачи от ветки эпика, вызовы: %v", git.callsList())
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

func TestCreateTaskBranchRequiresEpicBranch(t *testing.T) {
	git := &fakeGit{}
	srv, handler, mr := setupGitflow(t, git)
	ctx := context.Background()
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Релиз"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "task-1", Title: "Фича"},
		EpicID:   "epic-1",
	}); err != nil {
		t.Fatal(err)
	}
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")

	// Ветки эпика нет — задача не может получить ветку.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/tasks/task-1/branch", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ветка задачи без ветки эпика: %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestCreateBranchNonGitRejected(t *testing.T) {
	srv, handler, _ := setupGitflow(t, &fakeGit{})
	dir := t.TempDir()
	if _, err := srv.reg.Add(workspace.AddParams{Name: "plain", Kind: workspace.KindDir, Root: dir, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/plain/epics/epic-1/branch", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("не-git проект: %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestCreateEpicBranchUnknownEpic404(t *testing.T) {
	srv, handler, _ := setupGitflow(t, &fakeGit{})
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/nope/branch", strings.NewReader(`{}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("несуществующий эпик: %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestDeleteEpicRemovesBranches(t *testing.T) {
	git := &fakeGit{fails: map[string]string{
		"git rev-parse --verify --quiet refs/heads/ai/epic/epic-1": "ветки нет",
		"git rev-parse --verify --quiet refs/heads/ai/task/task-1": "ветки нет",
	}}
	srv, handler, mr := setupGitflow(t, git)
	ctx := context.Background()
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Релиз"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "task-1", Title: "Фича"},
		EpicID:   "epic-1",
	}); err != nil {
		t.Fatal(err)
	}
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")

	for _, path := range []string{
		"/api/projects/myrepo/epics/epic-1/branch",
		"/api/projects/myrepo/tasks/task-1/branch",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(`{}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("создание ветки %s: %d, body: %s", path, rec.Code, rec.Body.String())
		}
	}
	if _, err := srv.reg.EpicBranch("myrepo", "epic-1"); err != nil {
		t.Fatalf("ветка эпика должна быть в реестре: %v", err)
	}

	// Удаляем эпик (задачи в статусе new — удаляются каскадом).
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"DELETE", "/api/projects/myrepo/epics/epic-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE эпик: %d, body: %s", rec.Code, rec.Body.String())
	}
	if _, err := srv.reg.EpicBranch("myrepo", "epic-1"); err == nil {
		t.Fatal("ветка эпика должна быть снята из реестра после удаления эпика")
	}
	if _, err := srv.reg.TaskBranch("myrepo", "task-1"); err == nil {
		t.Fatal("ветка задачи должна быть снята из реестра после удаления эпика")
	}
}

func TestCreateBranchSanitizesEpicID(t *testing.T) {
	git := &fakeGit{fails: map[string]string{
		"git rev-parse --verify --quiet refs/heads/ai/epic/Задача_1": "ветки нет",
	}}
	srv, handler, mr := setupGitflow(t, git)
	ctx := context.Background()
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "Задача 1", Title: "С пробелом"},
	}); err != nil {
		t.Fatal(err)
	}
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/"+url.PathEscape("Задача 1")+"/branch", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("ветка эпика с пробелом в ID: %d, body: %s", rec.Code, rec.Body.String())
	}
	if !git.saw("git branch ai/epic/Задача_1 main") {
		t.Fatalf("ожидали санитизованную ветку ai/epic/Задача_1, вызовы: %v", git.callsList())
	}
}

// mockMergeGit возвращает fakeGit с ответами для happy-path MergeFeature:
// merge-base и rev-parse дают разные SHA (задача НЕ слита), merge-tree пустой
// (чисто). Конфликт включается параметром conflicts.
func mockMergeGit(conflicts string) *fakeGit {
	f := &fakeGit{starts: map[string]string{
		"git merge-base ai/epic/e1 ai/task/t1": "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111\n",
		"git rev-parse ai/task/t1":             "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222\n",
	}}
	if conflicts != "" {
		f.starts["git merge-tree aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111 ai/epic/e1 ai/task/t1"] = conflicts
	}
	return f
}

// TestMergeTaskHappyDryRun — REST-мёрдж задачи в релизную ветку: работают
// merge-base → rev-parse → merge-tree → worktree → merge → push → remove.
func TestMergeTaskHappyDryRun(t *testing.T) {
	git := mockMergeGit("")
	srv, handler, mr := setupGitflow(t, git)
	seedGitflowBoard(t, mr, srv)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/tasks/task-1/merge", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("мёрдж задачи: %d, body: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "ok" || out["branch"] != "ai/epic/e1" || out["already_merged"] != false {
		t.Fatalf("ответ = %+v", out)
	}
	if !git.saw("git worktree add ") || !git.saw("git merge --no-ff -m задача task-1: влитие в релиз эпика epic-1 ai/task/t1") {
		t.Fatalf("ожидали worktree-мёрдж, вызовы: %v", git.callsList())
	}
	if !git.saw("git push git@gitlab.com:g/myrepo.git ai/epic/e1") {
		t.Fatalf("ожидали push релизной ветки, вызовы: %v", git.callsList())
	}
	if !git.saw("git worktree remove --force ") {
		t.Fatalf("ожидали снятие worktree, вызовы: %v", git.callsList())
	}
}

// TestMergeTaskConflict409 — merge-tree выявил конфликт: ответ 409 со списком
// путей, рабочие git-команды (worktree/merge) не вызывались, ветки не тронуты.
func TestMergeTaskConflict409(t *testing.T) {
	const conflicts = `changed in both
  base   100644 c0d0fb45c382919737f8d0c20aaf57cf89b74af8 f.txt
  our    100644 ffdfb012e4aec2a6d3c21bb473f19822c5737852 f.txt
  their  100644 542655f4d59aeaeb83b65316f9bc779f6ffb11f9 f.txt
@@ -1 +1,5 @@
+<<<<<<< .our
+=======
+>>>>>>> .their
`
	git := mockMergeGit(conflicts)
	srv, handler, mr := setupGitflow(t, git)
	seedGitflowBoard(t, mr, srv)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/tasks/task-1/merge", strings.NewReader(`{}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("мёрдж с конфликтом: %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "conflicts" {
		t.Fatalf("ответ = %+v", out)
	}
	files, _ := out["files"].([]any)
	if len(files) != 1 || files[0] != "f.txt" {
		t.Fatalf("files = %v, want [f.txt]", out["files"])
	}
	if git.saw("git worktree add ") || git.saw("git merge --no-ff") {
		t.Fatalf("конфликт не должен доходить до worktree-мёрджа, вызовы: %v", git.callsList())
	}
}

// TestMergeTaskAlreadyMerged — задача уже влита: ответ ok с already_merged=true,
// git-мутации (worktree/merge/push) не вызывались.
func TestMergeTaskAlreadyMerged(t *testing.T) {
	git := &fakeGit{starts: map[string]string{
		"git merge-base ai/epic/e1 ai/task/t1": "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222\n",
		"git rev-parse ai/task/t1":             "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222\n",
	}}
	srv, handler, mr := setupGitflow(t, git)
	seedGitflowBoard(t, mr, srv)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/tasks/task-1/merge", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("мёрдж уже слитой задачи: %d, body: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out["already_merged"] != true {
		t.Fatalf("already_merged должен быть true: %+v", out)
	}
	if git.saw("git worktree add ") || git.saw("git merge --no-ff") || git.saw("git push ") {
		t.Fatalf("уже слитая задача не должна мутировать git, вызовы: %v", git.callsList())
	}
}

// TestMergeTaskRequiresBranches — без ветки задачи в side-реестре — 400.
func TestMergeTaskRequiresBranches(t *testing.T) {
	git := mockMergeGit("")
	srv, handler, mr := setupGitflow(t, git)
	seedGitflowBoard(t, mr, srv)
	srv.reg.DeleteTaskBranch("myrepo", "task-1")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/tasks/task-1/merge", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("мёрдж без ветки задачи: %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestTaskDoneAutoMergeHook — перевод задачи в done через REST вызывает
// авто-мёрдж её ветки в релизную (маппинг «done → merge»).
func TestTaskDoneAutoMergeHook(t *testing.T) {
	git := mockMergeGit("")
	srv, handler, mr := setupGitflow(t, git)
	seedGitflowBoard(t, mr, srv)

	// ready → in_progress → done (конечный автомат запрещает new→done).
	for _, st := range []string{"in_progress", "done"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(
			"PUT", "/api/projects/myrepo/tasks/task-1",
			strings.NewReader(`{"status":"`+st+`"}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("перевод в %s: %d, body: %s", st, rec.Code, rec.Body.String())
		}
	}
	if !git.saw("git worktree add ") || !git.saw("git merge --no-ff -m задача task-1: влитие в релиз эпика epic-1 ai/task/t1") {
		t.Fatalf("done → merge не вызвал worktree-мёрдж, вызовы: %v", git.callsList())
	}
}

// TestTaskDoneAutoMergeConflict — done при конфликте: статус всё равно
// проставляется, релизная ветка не трогается (авто-мёрдж сообщает о
// конфликте в лог, не ломая переход), worktree задачи снимается после
// авто-шага (Ф-3/Ф-4: конфликты уходят интерактивному флоу rebase/резолва).
func TestTaskDoneAutoMergeConflict(t *testing.T) {
	const conflicts = `changed in both
  base   100644 c0d0fb45c382919737f8d0c20aaf57cf89b74af8 f.txt
  our    100644 ffdfb012e4aec2a6d3c21bb473f19822c5737852 f.txt
  their  100644 542655f4d59aeaeb83b65316f9bc779f6ffb11f9 f.txt
@@ -1 +1,5 @@
+<<<<<<< .our
+=======
+>>>>>>> .their
`
	git := mockMergeGit(conflicts)
	srv, handler, mr := setupGitflow(t, git)
	seedGitflowBoard(t, mr, srv)

	// ready → in_progress → done; конфликт при done значения не имеет.
	for _, st := range []string{"in_progress", "done"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(
			"PUT", "/api/projects/myrepo/tasks/task-1",
			strings.NewReader(`{"status":"`+st+`"}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("перевод в %s не должен ломаться: %d, body: %s", st, rec.Code, rec.Body.String())
		}
	}
	ctx := context.Background()
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	task, err := store.GetTask(ctx, "task-1")
	if err != nil || task.Status != board.StatusDone {
		t.Fatalf("task после done = %+v, %v", task, err)
	}
	// Ф-3: in_progress создаёт worktree задачи (работу специалиста позже
	// авто-коммитят); после done он снимается.
	if !git.saw("git worktree add ") {
		t.Fatalf("in_progress не создал worktree задачи (Ф-3), вызовы: %v", git.callsList())
	}
	if !git.saw("git worktree remove --force ") {
		t.Fatalf("после done worktree задачи не снят, вызовы: %v", git.callsList())
	}
	// Конфликт НЕ должен сливать ветки: ни worktree-мёрджа, ни force-мерджа.
	if git.saw("git merge --no-ff ") || git.saw("git merge -X ") {
		t.Fatalf("при конфликте релизная ветка не должна трогаться, вызовы: %v", git.callsList())
	}
}

// seedGitflowBoard создаёт на доске эпик/задачу, ветки эпика/задачи в
// side-реестре и регистрирует git-проект.
func seedGitflowBoard(t *testing.T, mr *miniredis.Miniredis, srv *Server) {
	t.Helper()
	ctx := context.Background()
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Релиз"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "task-1", Title: "Фича"},
		EpicID:   "epic-1",
		Status:   board.StatusReady,
	}); err != nil {
		t.Fatal(err)
	}
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")
	if err := srv.reg.SetEpicBranch("myrepo", "epic-1", workspace.BranchRef{Branch: "ai/epic/e1", Base: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := srv.reg.SetTaskBranch("myrepo", "task-1", workspace.BranchRef{Branch: "ai/task/t1", Base: "ai/epic/e1"}); err != nil {
		t.Fatal(err)
	}
}

// seedReleaseBoard создаёт для тестов «Залить в main» done-эпик с релизной
// веткой в side-реестре и регистрирует git-проект (база = main).
func seedReleaseBoard(t *testing.T, mr *miniredis.Miniredis, srv *Server) {
	t.Helper()
	ctx := context.Background()
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Релиз"},
		Status:   board.StatusDone, // done — условие кнопки «Залить в main»
	}); err != nil {
		t.Fatal(err)
	}
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")
	if err := srv.reg.SetEpicBranch("myrepo", "epic-1", workspace.BranchRef{Branch: "ai/epic/e1", Base: "main"}); err != nil {
		t.Fatal(err)
	}
}

// mockReleaseGit возвращает fakeGit с ответами для happy-path «Залить в main»:
// merge-base(main, релиз) и tip релиза разные (релиз НЕ слит в main),
// merge-tree чистый. Конфликт включается параметром conflicts.
func mockReleaseGit(conflicts string) *fakeGit {
	f := &fakeGit{starts: map[string]string{
		"git merge-base main ai/epic/e1": "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111\n",
		"git rev-parse ai/epic/e1":       "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222\n",
	}}
	if conflicts != "" {
		f.starts["git merge-tree aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111 main ai/epic/e1"] = conflicts
	}
	return f
}

// TestReleaseEpicHappyDryRun — REST «Залить в main» без конфликтов: работают
// merge-base → rev-parse → merge-tree → worktree(main) → merge релиза →
// push main → remove. Возвращается status=ok, ветка = main.
func TestReleaseEpicHappyDryRun(t *testing.T) {
	git := mockReleaseGit("")
	srv, handler, mr := setupGitflow(t, git)
	seedReleaseBoard(t, mr, srv)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/release", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("«Залить в main»: %d, body: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "ok" || out["branch"] != "main" || out["source"] != "ai/epic/e1" || out["already_merged"] != false {
		t.Fatalf("ответ = %+v", out)
	}
	if !git.saw("git worktree add ") ||
		!git.saw("git merge --no-ff -m эпик epic-1: релиз в main из ai/epic/e1 ai/epic/e1") {
		t.Fatalf("ожидали worktree-мёрдж релиза в main, вызовы: %v", git.callsList())
	}
	if !git.saw("git push git@gitlab.com:g/myrepo.git main") {
		t.Fatalf("ожидали push main, вызовы: %v", git.callsList())
	}
	if !git.saw("git worktree remove --force ") {
		t.Fatalf("ожидали снятие worktree, вызовы: %v", git.callsList())
	}
}

// TestReleaseEpicConflict409 — конфликт main ↔ релизная ветка: ответ 409 со
// списком путей, рабочие git-команды (worktree/merge/push) не вызывались.
func TestReleaseEpicConflict409(t *testing.T) {
	const conflicts = `changed in both
  base   100644 c0d0fb45c382919737f8d0c20aaf57cf89b74af8 f.txt
  our    100644 ffdfb012e4aec2a6d3c21bb473f19822c5737852 f.txt
  their  100644 542655f4d59aeaeb83b65316f9bc779f6ffb11f9 f.txt
@@ -1 +1,5 @@
+<<<<<<< .our
+=======
+>>>>>>> .their
`
	git := mockReleaseGit(conflicts)
	srv, handler, mr := setupGitflow(t, git)
	seedReleaseBoard(t, mr, srv)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/release", strings.NewReader(`{}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("релиз с конфликтом: %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "conflicts" {
		t.Fatalf("ответ = %+v", out)
	}
	files, _ := out["files"].([]any)
	if len(files) != 1 || files[0] != "f.txt" {
		t.Fatalf("files = %v, want [f.txt]", out["files"])
	}
	if git.saw("git worktree add ") || git.saw("git merge --no-ff") || git.saw("git push ") {
		t.Fatalf("конфликт не должен доходить до worktree-мёрджа, вызовы: %v", git.callsList())
	}
}

// TestReleaseEpicAlreadyMerged — релизная ветка уже в main: ответ ok с
// already_merged=true, git-мутации (worktree/merge/push) не вызывались.
func TestReleaseEpicAlreadyMerged(t *testing.T) {
	git := &fakeGit{starts: map[string]string{
		"git merge-base main ai/epic/e1": "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222\n",
		"git rev-parse ai/epic/e1":       "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222\n",
	}}
	srv, handler, mr := setupGitflow(t, git)
	seedReleaseBoard(t, mr, srv)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/release", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("релиз уже слитой ветки: %d, body: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out["already_merged"] != true {
		t.Fatalf("already_merged должен быть true: %+v", out)
	}
	if git.saw("git worktree add ") || git.saw("git merge --no-ff") || git.saw("git push ") {
		t.Fatalf("уже слитая ветка не должна мутировать git, вызовы: %v", git.callsList())
	}
}

// TestReleaseEpicRequiresDone — «Залить в main» доступен только done-эпику:
// не-done эпик → 400, git не мутируется.
func TestReleaseEpicRequiresDone(t *testing.T) {
	git := mockReleaseGit("")
	srv, handler, mr := setupGitflow(t, git)
	// seedGitflowBoard оставляет эпик в статусе new — кнопка не должна работать.
	seedGitflowBoard(t, mr, srv)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/release", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("релиз не-done эпика: %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if git.saw("git worktree add ") || git.saw("git merge --no-ff") || git.saw("git push ") {
		t.Fatalf("не-done эпик не должен мутировать git, вызовы: %v", git.callsList())
	}
}

// TestReleaseEpicRequiresBranch — без релизной ветки эпика в side-реестре → 400.
func TestReleaseEpicRequiresBranch(t *testing.T) {
	git := mockReleaseGit("")
	srv, handler, mr := setupGitflow(t, git)
	seedReleaseBoard(t, mr, srv)
	if err := srv.reg.DeleteEpicBranch("myrepo", "epic-1"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/release", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("релиз без ветки эпика: %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestReleaseEpicNonGitRejected — не-git проект: ответ 400.
func TestReleaseEpicNonGitRejected(t *testing.T) {
	srv, handler, _ := setupGitflow(t, &fakeGit{})
	dir := t.TempDir()
	if _, err := srv.reg.Add(workspace.AddParams{Name: "plain", Kind: workspace.KindDir, Root: dir, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/plain/epics/epic-1/release", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("не-git проект: %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestReleaseEpicUnknownEpic404 — несуществующий эпик: ответ 404.
func TestReleaseEpicUnknownEpic404(t *testing.T) {
	srv, handler, _ := setupGitflow(t, &fakeGit{})
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/nope/release", strings.NewReader(`{}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("несуществующий эпик: %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestReleaseEpicEndToEndRealGit — E2E «Залить в main» на реальном git: клон
// локального origin → ветки эпика/задачи через REST → коммит в ветке задачи →
// мёрдж задачи в релиз → «Залить в main». Проверяем: main продвинут
// merge-коммитом и запушен в origin, HEAD агента не тронут.
func TestReleaseEpicEndToEndRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	ctx := context.Background()
	base := t.TempDir()

	origin := filepath.Join(base, "origin")
	if out, err := exec.Command("git", "init", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	setGitUserReal(t, origin)
	// «Залить в main» пушит именно main — в тестовом origin она checked-out
	// (не-bare), по умолчанию git запрещает такой push (receive.denyCurrentBranch).
	if _, err := exec.Command("git", "-C", origin, "config", "receive.denyCurrentBranch", "ignore").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	setGitUserReal(t, origin)
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("# origin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", origin, "add", "-A").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", origin, "commit", "-m", "init").CombinedOutput(); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(base, "myrepo")
	repo, err := gitops.Clone(ctx, gitops.CLIExecutor{}, origin, "ai/myrepo", dest)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}

	srv, handler, mr := newTestServerGit(t, gitops.CLIExecutor{}, nil)
	if _, err := srv.reg.Add(workspace.AddParams{
		Name: "myrepo", Kind: workspace.KindGit, Root: repo.Root,
		GitRemote: repo.Remote, GitBranch: repo.Branch, GitBase: repo.Base,
	}); err != nil {
		t.Fatal(err)
	}

	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Релиз"},
		Status:   board.StatusDone, // done — «Залить в main» доступен
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "task-1", Title: "Фича"},
		EpicID:   "epic-1",
	}); err != nil {
		t.Fatal(err)
	}

	// Ветки эпика и задачи (реальный git).
	for _, path := range []string{
		"/api/projects/myrepo/epics/epic-1/branch",
		"/api/projects/myrepo/tasks/task-1/branch",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(`{}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("POST %s: %d, body: %s", path, rec.Code, rec.Body.String())
		}
	}

	// Задача «нарабатывает» коммит в своей ветке; HEAD возвращаем на ai/myrepo.
	setGitUserReal(t, dest)
	if _, err := exec.Command("git", "-C", dest, "checkout", "-q", "ai/task/task-1").CombinedOutput(); err != nil {
		t.Fatalf("checkout ai/task/task-1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dest, "add", "-A").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dest, "commit", "-m", "работа по задаче task-1").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dest, "checkout", "-q", "ai/myrepo").CombinedOutput(); err != nil {
		t.Fatalf("checkout ai/myrepo: %v", err)
	}

	// Мёрдж задачи в релизную ветку эпика → в origin попадает ai/epic/epic-1.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/tasks/task-1/merge", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST merge: %d, body: %s", rec.Code, rec.Body.String())
	}

	// «Залить в main»: релизная ветка → main, push в origin.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/release", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST release: %d, body: %s", rec.Code, rec.Body.String())
	}
	var rout map[string]any
	json.Unmarshal(rec.Body.Bytes(), &rout)
	if rout["status"] != "ok" || rout["branch"] != "main" || rout["already_merged"] != false {
		t.Fatalf("ответ релиза = %+v", rout)
	}

	// main продвинут merge-коммитом релиза и запушен в origin.
	llog, _ := exec.Command("git", "-C", dest, "log", "-1", "--format=%s", "main").CombinedOutput()
	if !strings.Contains(string(llog), "релиз в main") {
		t.Fatalf("лог main = %q, want merge-коммит релиза", llog)
	}
	destMain, _ := exec.Command("git", "-C", dest, "rev-parse", "main").CombinedOutput()
	origMain, err := exec.Command("git", "-C", origin, "rev-parse", "refs/heads/main").CombinedOutput()
	if err != nil {
		t.Fatalf("main не запушена в origin: %v", err)
	}
	if string(strings.TrimSpace(string(destMain))) != string(strings.TrimSpace(string(origMain))) {
		t.Fatalf("origin/main (%s) != локальный main (%s)", origMain, destMain)
	}
	// Рабочий HEAD агента не тронут, временные worktree сняты.
	head, _ := gitHead(dest)
	if head != "ai/myrepo" {
		t.Fatalf("HEAD клона после релиза = %q, want ai/myrepo", head)
	}
	if wl, _ := exec.Command("git", "-C", dest, "worktree", "list").CombinedOutput(); strings.Contains(string(wl), ".wt-") {
		t.Fatalf("остались временные worktree:\n%s", wl)
	}
}

// TestGitflowRestEndToEndRealGit — ручной E2E из верификации Ф-1: открыть
// git-проект (клон в temp/<имя>), создать ветки эпика и задачи через REST и
// убедиться в `git branch` клона. Реальный git CLI, без сети (локальный origin).
func TestGitflowRestEndToEndRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	ctx := context.Background()
	base := t.TempDir()

	origin := filepath.Join(base, "origin")
	if out, err := exec.Command("git", "init", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	setGitUserReal(t, origin)
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("# origin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", origin, "add", "-A").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", origin, "commit", "-m", "init").CombinedOutput(); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(base, "myrepo")
	repo, err := gitops.Clone(ctx, gitops.CLIExecutor{}, origin, "ai/myrepo", dest)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}

	srv, handler, mr := newTestServerGit(t, gitops.CLIExecutor{}, nil)
	srv.reg.Add(workspace.AddParams{
		Name: "myrepo", Kind: workspace.KindGit, Root: repo.Root,
		GitRemote: repo.Remote, GitBranch: repo.Branch, GitBase: repo.Base,
	})

	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Релиз"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "task-1", Title: "Фича"},
		EpicID:   "epic-1",
	}); err != nil {
		t.Fatal(err)
	}

	// Создание веток эпика и задачи через REST (реальный git CLI).
	for _, path := range []string{
		"/api/projects/myrepo/epics/epic-1/branch",
		"/api/projects/myrepo/tasks/task-1/branch",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(`{}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("POST %s: %d, body: %s", path, rec.Code, rec.Body.String())
		}
	}

	// В клоне появились ветки; текущая ветка агента (ai/myrepo) не тронута.
	out, _ := gitBranchList(dest)
	for _, want := range []string{"ai/epic/epic-1", "ai/task/task-1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("клон не содержит ветку %s:\n%s", want, out)
		}
	}
	head, _ := gitHead(dest)
	if head != "ai/myrepo" {
		t.Fatalf("HEAD клона = %q, want ai/myrepo (рабочая ветка не переключается)", head)
	}

	// Ф-2: задача «нарабатывает» коммиты в своей ветке (агент мог работать с
	// веткой через checkout; возвращаем HEAD на ai/myrepo — мёрдж не должен
	// его трогать), затем REST-мёрдж вливает ветку и пушит релиз в origin.
	setGitUserReal(t, dest)
	if _, err := exec.Command("git", "-C", dest, "checkout", "-q", "ai/task/task-1").CombinedOutput(); err != nil {
		t.Fatalf("checkout ai/task/task-1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dest, "add", "-A").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dest, "commit", "-m", "работа по задаче task-1").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dest, "checkout", "-q", "ai/myrepo").CombinedOutput(); err != nil {
		t.Fatalf("checkout ai/myrepo: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/tasks/task-1/merge", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST merge: %d, body: %s", rec.Code, rec.Body.String())
	}
	var mout map[string]any
	json.Unmarshal(rec.Body.Bytes(), &mout)
	if mout["status"] != "ok" || mout["already_merged"] != false {
		t.Fatalf("ответ мёрджа = %+v", mout)
	}

	// Релизная ветка эпика продвинута merge-коммитом, запушена в origin;
	// рабочий HEAD агента остался на ai/myrepo.
	llog, _ := exec.Command("git", "-C", dest, "log", "-1", "--format=%s", "ai/epic/epic-1").CombinedOutput()
	if !strings.Contains(string(llog), "задача task-1") {
		t.Fatalf("лог ai/epic/epic-1 = %q, want merge-коммит задачи", llog)
	}
	if _, err := exec.Command("git", "-C", origin, "rev-parse", "--verify", "refs/heads/ai/epic/epic-1").CombinedOutput(); err != nil {
		t.Fatalf("ai/epic/epic-1 не запушена в origin: %v", err)
	}
	head, _ = gitHead(dest)
	if head != "ai/myrepo" {
		t.Fatalf("HEAD клона после мёрджа = %q, want ai/myrepo", head)
	}
	if wl, _ := exec.Command("git", "-C", dest, "worktree", "list").CombinedOutput(); strings.Contains(string(wl), ".wt-") {
		t.Fatalf("остались временные worktree:\n%s", wl)
	}
}

func setGitUserReal(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.Command("git", "-C", dir, "config", "user.email", "t@example.com").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dir, "config", "user.name", "Test").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
}

func gitBranchList(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "branch", "--list").CombinedOutput()
	return string(out), err
}

func gitHead(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
