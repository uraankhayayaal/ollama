package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ai/board"
	"ai/forges"
	"ai/gitops"
	"ai/workspace"

	"github.com/alicebob/miniredis/v2"
)

type orderedGitExecutor struct{ events *[]string }

func (e orderedGitExecutor) Exec(ctx context.Context, dir string, argv ...string) (string, error) {
	if len(argv) > 1 && argv[1] == "push" {
		*e.events = append(*e.events, "push:"+dir)
	}
	return (gitops.CLIExecutor{}).Exec(ctx, dir, argv...)
}

type orderedForge struct {
	events *[]string
	url    string
}

func (f orderedForge) GetDiff() (string, error)                 { return "", nil }
func (f orderedForge) PostComment(_ forges.ReviewComment) error { return nil }
func (f orderedForge) PostSummary(string) error                 { return nil }
func (f orderedForge) Approve(string) error                     { return nil }
func (f orderedForge) CreateMergeRequest(_ forges.MergeRequestOptions) (string, error) {
	*f.events = append(*f.events, "mr:"+f.url)
	return f.url, nil
}

// submoduleTaskFixture — готовая связка «родитель с сабмодулем»: клон родителя,
// worktree задачи в нём и worktree задачи сабмодуля (вложенный), с
// зарегистрированными ветками задачи в обоих репозиториях.
type submoduleTaskFixture struct {
	reg       *workspace.Registry
	store     *board.Store
	srv       *Server
	task      *board.Task
	parentWT  string
	childWT   string
	childName string
	childRoot string
	childBase string
	childRepo string
	parentOrg string
	childOrg  string
}

// newSubmoduleTaskFixture собирает фикстуру и СРАЗУ создаёт worktree задачи
// (taskWorktree): дальше тест работает с готовыми деревьями.
func newSubmoduleTaskFixture(t *testing.T) *submoduleTaskFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	base := t.TempDir()
	childOrigin := filepath.Join(base, "child-origin")
	initSubmoduleFixtureRepo(t, childOrigin)
	parentOrigin := filepath.Join(base, "parent-origin")
	initSubmoduleFixtureRepo(t, parentOrigin)
	gitFixtureRun(t, parentOrigin, "-c", "protocol.file.allow=always", "submodule", "add", childOrigin, "packages/auth")
	gitFixtureRun(t, parentOrigin, "add", "-A")
	gitFixtureRun(t, parentOrigin, "commit", "-m", "add submodule")

	cloneRoot := filepath.Join(base, "clone")
	parentRepo, err := gitops.Clone(context.Background(), gitops.CLIExecutor{}, parentOrigin, "ai/app", cloneRoot)
	if err != nil {
		t.Fatal(err)
	}
	setGitUserReal(t, cloneRoot)
	for _, sub := range parentRepo.Submodules {
		setGitUserReal(t, sub.Root)
	}
	if err := parentRepo.CreateBranch(context.Background(), "ai/epic/e1", parentRepo.Base); err != nil {
		t.Fatal(err)
	}
	if err := parentRepo.CreateBranch(context.Background(), "ai/task/t1", "ai/epic/e1"); err != nil {
		t.Fatal(err)
	}

	reg, err := workspace.Open(filepath.Join(base, "workspaces.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Add(workspace.AddParams{Name: "app", Kind: workspace.KindGit, Root: cloneRoot, GitRemote: parentRepo.Remote, GitBranch: parentRepo.Branch, GitBase: parentRepo.Base}); err != nil {
		t.Fatal(err)
	}
	if len(parentRepo.Submodules) != 1 {
		t.Fatalf("submodules=%+v", parentRepo.Submodules)
	}
	sub := parentRepo.Submodules[0]
	childName := submoduleProjectName("app", sub.Path)
	if _, err := reg.Add(workspace.AddParams{Name: childName, Kind: workspace.KindGit, Root: sub.Root, GitRemote: sub.Remote,
		GitBranch: sub.Branch, GitBase: sub.Base, GitTarget: sub.DefaultBranch, Parent: "app"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetEpicBranch("app", "e1", workspace.BranchRef{Branch: "ai/epic/e1", Base: parentRepo.Base}); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetTaskBranch("app", "t1", workspace.BranchRef{Branch: "ai/task/t1", Base: "ai/epic/e1"}); err != nil {
		t.Fatal(err)
	}

	srv := &Server{reg: reg, gitExec: gitops.CLIExecutor{}, mergeLocks: map[string]*sync.Mutex{}, sessions: map[string]*Session{}}
	task := &board.Task{TaskSpec: board.TaskSpec{TaskID: "t1"}}
	mr := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "app"})
	if err := store.CreateEpic(context.Background(), &board.Epic{TaskSpec: board.TaskSpec{TaskID: "e1", Title: "epic"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(context.Background(), &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "t1", Title: "задача", SequenceOrder: 1},
		EpicID:   "e1",
		Status:   board.StatusInProgress,
	}); err != nil {
		t.Fatal(err)
	}
	srv.taskWorktree(context.Background(), "app", task, store)
	ref, err := reg.TaskBranch("app", "t1")
	if err != nil || ref.Worktree == "" {
		t.Fatalf("task worktree ref=%+v err=%v", ref, err)
	}
	childRef, err := reg.TaskBranch(childName, childTaskKey("app", "t1"))
	if err != nil || childRef.Worktree == "" {
		t.Fatalf("submodule task branch: ref=%+v err=%v", childRef, err)
	}
	return &submoduleTaskFixture{
		reg: reg, srv: srv, task: task, store: store,
		parentWT: ref.Worktree, childWT: childRef.Worktree,
		childName: childName, childRoot: sub.Root,
		childBase: sub.Branch, childRepo: sub.Remote,
		parentOrg: parentOrigin, childOrg: childOrigin,
	}
}

// TestGitStatusExposesTaskWorktreeAndSubmodules — этап 5.3: снимок доски
// должен говорить, что задачу ещё можно откатить (worktree жив) и какие
// вложенные сабмодули вернутся к состоянию откатываемого коммита. Иначе
// кнопка отката обещает действие, которого нет (worktree удалён).
func TestGitStatusExposesTaskWorktreeAndSubmodules(t *testing.T) {
	f := newSubmoduleTaskFixture(t)
	ctx := context.Background()
	epics, err := f.store.ListEpics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := f.store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}

	view := f.srv.gitStatus(ctx, "app", epics, tasks)
	if view == nil {
		t.Fatal("gitStatus вернул nil для git-проекта")
	}
	link, ok := view.Tasks["t1"]
	if !ok {
		t.Fatalf("в снимке нет ветки задачи: %+v", view.Tasks)
	}
	if !link.Worktree {
		t.Fatalf("worktree задачи есть на диске (%s), но снимок этого не говорит", f.parentWT)
	}
	if len(link.Submodules) != 1 || link.Submodules[0] != "packages/auth" {
		t.Fatalf("submodules=%v, ожидался [packages/auth]", link.Submodules)
	}

	// После удаления worktree (задача выполнена) откат недоступен — кнопка на
	// доске прячется вместо того, чтобы упасть с 404 по клику.
	f.srv.removeTaskWorktree("app", "t1", f.parentWT)
	view = f.srv.gitStatus(ctx, "app", epics, tasks)
	if view.Tasks["t1"].Worktree {
		t.Fatalf("worktree удалён, но снимок всё ещё предлагает откат: %+v", view.Tasks["t1"])
	}
	if len(view.Tasks["t1"].Submodules) != 0 {
		t.Fatalf("submodules=%v после удаления worktree", view.Tasks["t1"].Submodules)
	}
}

// TestTaskWorktreeRecordsBaseSHA — точка отката «снести всё» (Ф-6, этап 2)
// снимается при создании worktree задачи и не перетирается при повторном
// вызове хука (иначе после отката кода «база» уехала бы вперёд).
func TestTaskWorktreeRecordsBaseSHA(t *testing.T) {
	f := newSubmoduleTaskFixture(t)
	base, err := f.store.GetTask(context.Background(), "t1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	head := gitFixtureRun(t, f.parentWT, "rev-parse", "HEAD")
	if base.Checkpoint == nil || base.Checkpoint.BaseSHA != head {
		t.Fatalf("base_sha=%+v, HEAD worktree=%s", base.Checkpoint, head)
	}
	// Ветка задачи уехала вперёд — «база» остаётся прежней.
	if err := os.WriteFile(filepath.Join(f.parentWT, "a.go"), []byte("package app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitFixtureRun(t, f.parentWT, "add", "-A")
	gitFixtureRun(t, f.parentWT, "commit", "-m", "round 1")
	if got := gitFixtureRun(t, f.parentWT, "rev-parse", "HEAD"); got == head {
		t.Fatal("тест не сдвинул ветку задачи")
	}
	f.srv.taskWorktree(context.Background(), "app", f.task, f.store)
	after, _ := f.store.GetTask(context.Background(), "t1")
	if after.Checkpoint.BaseSHA != head {
		t.Fatalf("base_sha перетёрт: %s, ожидалось %s", after.Checkpoint.BaseSHA, head)
	}
}

func TestTaskWorktreeBootstrapsGitignore(t *testing.T) {
	f := newSubmoduleTaskFixture(t)
	for _, root := range []string{f.parentWT, f.childWT} {
		data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
		if err != nil {
			t.Fatalf("%s: .gitignore: %v", root, err)
		}
		body := string(data)
		for _, want := range []string{"node_modules/", "__pycache__/", ".venv/"} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s: .gitignore без %q: %q", root, want, body)
			}
		}
		if strings.Contains(body, "vendor/") {
			t.Fatalf("%s: vendor/ в списке не должен быть: %q", root, body)
		}
	}
	// Свой .gitignore репозитория не трогаем: решения проекта важнее списка.
	if err := os.WriteFile(filepath.Join(f.parentWT, ".gitignore"), []byte("custom/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := gitFixtureRun(t, f.parentWT, "rev-parse", "HEAD")
	f.srv.taskWorktree(context.Background(), "app", f.task, nil)
	if body, err := os.ReadFile(filepath.Join(f.parentWT, ".gitignore")); err != nil || string(body) != "custom/\n" {
		t.Fatalf("существующий .gitignore перезаписан: %q err=%v", body, err)
	}
	if after := gitFixtureRun(t, f.parentWT, "rev-parse", "HEAD"); after != before {
		t.Fatalf("повторный taskWorktree пересоздал worktree: %s -> %s", before, after)
	}
}

// TestCommitTaskWorktreePublishesSubmoduleAfterWIPCommit — регрессия Ф-6: с
// промежуточными коммитами worktree сабмодуля на `done` ЧИСТЫЙ. Публикация его
// ветки (merge в базовую + push + обновление gitlink) всё равно обязана
// выполниться, иначе gitlink родителя указал бы на неопубликованный коммит.
func TestCommitTaskWorktreePublishesSubmoduleAfterWIPCommit(t *testing.T) {
	f := newSubmoduleTaskFixture(t)
	ctx := context.Background()
	// Промежуточный коммит раунда в сабмодуле: правка есть, дерево чистое.
	if err := os.WriteFile(filepath.Join(f.childWT, "wip.go"), []byte("package auth\n// wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitFixtureRun(t, f.childWT, "add", "-A")
	gitFixtureRun(t, f.childWT, "commit", "-m", "wip")
	if out := gitFixtureRun(t, f.childWT, "status", "--porcelain"); out != "" {
		t.Fatalf("дерево сабмодуля должно быть чистым, получено: %q", out)
	}
	wipSHA := gitFixtureRun(t, f.childWT, "rev-parse", "HEAD")

	f.srv.commitTaskWorktree(ctx, "app", f.task, f.parentWT)

	// Коммит раунда опубликован в origin сабмодуля.
	published := gitFixtureRun(t, f.childOrg, "rev-parse", f.childBase)
	if !gitFixtureContains(t, f.childOrg, published, wipSHA) {
		t.Fatalf("коммит %s не попал в %s сабмодуля (%s)", wipSHA, f.childBase, published)
	}
	// Gitlink родителя указывает на опубликованный коммит, дерево родителя чистое.
	if link := gitFixtureRun(t, f.parentWT, "rev-parse", "HEAD:packages/auth"); link != published {
		t.Fatalf("gitlink=%s, опубликованный коммит=%s", link, published)
	}
	if out := gitFixtureRun(t, f.parentWT, "status", "--porcelain"); out != "" {
		t.Fatalf("дерево worktree задачи не закоммичено: %q", out)
	}
	// Повторный вызов (следующий прогон/повторный done) — no-op без ошибок.
	f.srv.commitTaskWorktree(ctx, "app", f.task, f.parentWT)
	if link := gitFixtureRun(t, f.parentWT, "rev-parse", "HEAD:packages/auth"); link != published {
		t.Fatalf("повторный вызов сменил gitlink: %s != %s", link, published)
	}
}

// gitFixtureContains сообщает, достижим ли commit из head в репозитории dir.
func gitFixtureContains(t *testing.T, dir, head, commit string) bool {
	t.Helper()
	_, err := exec.Command("git", "-C", dir, "merge-base", "--is-ancestor", commit, head).CombinedOutput()
	return err == nil
}

func TestTaskWorktreeCommitsNestedSubmoduleIntoProjectBranch(t *testing.T) {
	f := newSubmoduleTaskFixture(t)
	if err := os.WriteFile(filepath.Join(f.childWT, "task.go"), []byte("package auth\n// task change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.srv.commitTaskWorktree(context.Background(), "app", f.task, f.parentWT)
	parentGitlink := gitFixtureRun(t, f.parentWT, "rev-parse", "HEAD:packages/auth")
	childHead := gitFixtureRun(t, f.childRoot, "rev-parse", "HEAD")
	if parentGitlink != childHead {
		t.Fatalf("parent gitlink=%s child HEAD=%s", parentGitlink, childHead)
	}
	if !strings.Contains(gitFixtureRun(t, f.childRoot, "show", "--stat", "--oneline", "HEAD"), "task.go") {
		t.Fatal("submodule task commit missing")
	}
	f.srv.removeTaskWorktree("app", "t1", f.parentWT)
	if _, err := os.Stat(f.parentWT); !os.IsNotExist(err) {
		t.Fatalf("parent task worktree remains: %v", err)
	}
	if _, err := f.reg.TaskBranch(f.childName, childTaskKey("app", "t1")); err == nil {
		t.Fatal("submodule task branch registry ref remains")
	}
}

func TestAcceptGitProjectCreatesSubmoduleMRBeforeParent(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	base := t.TempDir()
	childOrigin := filepath.Join(base, "child-origin")
	initSubmoduleFixtureRepo(t, childOrigin)
	parentOrigin := filepath.Join(base, "parent-origin")
	initSubmoduleFixtureRepo(t, parentOrigin)
	gitFixtureRun(t, parentOrigin, "-c", "protocol.file.allow=always", "submodule", "add", childOrigin, "packages/auth")
	gitFixtureRun(t, parentOrigin, "add", "-A")
	gitFixtureRun(t, parentOrigin, "commit", "-m", "add submodule")

	cloneRoot := filepath.Join(base, "clone")
	cloned, err := gitops.Clone(context.Background(), gitops.CLIExecutor{}, parentOrigin, "ai/app", cloneRoot)
	if err != nil {
		t.Fatal(err)
	}
	setGitUserReal(t, cloneRoot)
	for _, sub := range cloned.Submodules {
		setGitUserReal(t, sub.Root)
	}
	sub := cloned.Submodules[0]
	if err := os.WriteFile(filepath.Join(sub.Root, "feature.go"), []byte("package auth\n// changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloneRoot, "app.go"), []byte("package app\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg, err := workspace.Open(filepath.Join(base, "workspaces.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Add(workspace.AddParams{Name: "app", Kind: workspace.KindGit, Root: cloneRoot, GitRemote: cloned.Remote, GitBranch: cloned.Branch, GitBase: cloned.Base}); err != nil {
		t.Fatal(err)
	}
	childName := submoduleProjectName("app", sub.Path)
	if _, err := reg.Add(workspace.AddParams{Name: childName, Kind: workspace.KindGit, Root: sub.Root, GitRemote: sub.Remote,
		GitBranch: sub.Branch, GitBase: sub.Base, GitTarget: sub.DefaultBranch, Parent: "app"}); err != nil {
		t.Fatal(err)
	}
	events := []string{}
	server := &Server{reg: reg, gitExec: orderedGitExecutor{events: &events}, sessions: map[string]*Session{},
		forgeFactory: func(remote, _ string) (forges.Forge, error) {
			return orderedForge{events: &events, url: "https://example.test/mr/" + filepath.Base(remote)}, nil
		}}
	req := httptest.NewRequest("POST", "/api/projects/app/accept", bytes.NewBufferString(`{"title":"Batch change"}`))
	req.SetPathValue("id", "app")
	response := httptest.NewRecorder()
	server.handleAccept(response, req)
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(events) != 4 || !strings.HasPrefix(events[0], "push:"+sub.Root) || !strings.HasPrefix(events[1], "mr:") || !strings.HasPrefix(events[2], "push:"+cloneRoot) || !strings.HasPrefix(events[3], "mr:") {
		t.Fatalf("expected child push/MR before parent push/MR, got %v", events)
	}
	var result struct {
		Repositories map[string]map[string]string `json:"repositories"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 2 || result.Repositories[childName]["url"] == "" || result.Repositories["app"]["url"] == "" {
		t.Fatalf("repositories result=%+v", result.Repositories)
	}
	// Повторный accept после push/MR retry-safe: обновляет те же ветки и
	// возвращает существующие MR, не создавая дубликаты.
	response = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/projects/app/accept", bytes.NewBufferString(`{"title":"Batch change"}`))
	req.SetPathValue("id", "app")
	server.handleAccept(response, req)
	if response.Code != 200 {
		t.Fatalf("retry status=%d body=%s", response.Code, response.Body.String())
	}
	if len(events) != 6 || !strings.HasPrefix(events[4], "push:"+sub.Root) || !strings.HasPrefix(events[5], "push:"+cloneRoot) {
		t.Fatalf("retry duplicated an MR or changed push order: %v", events)
	}
}

func TestSubmoduleProjectNameEscapesPathSeparatorsWithoutCollision(t *testing.T) {
	if submoduleProjectName("app", "packages/auth") != "app--packages~sauth" {
		t.Fatal("slash path encoding changed")
	}
	if submoduleProjectName("app", "packages~sauth") == submoduleProjectName("app", "packages/auth") {
		t.Fatal("escaped paths collided")
	}
}

func TestRejectBranchResetsSubmoduleBeforeParent(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	base := t.TempDir()
	childOrigin := filepath.Join(base, "child-origin")
	initSubmoduleFixtureRepo(t, childOrigin)
	parentOrigin := filepath.Join(base, "parent-origin")
	initSubmoduleFixtureRepo(t, parentOrigin)
	gitFixtureRun(t, parentOrigin, "-c", "protocol.file.allow=always", "submodule", "add", childOrigin, "packages/auth")
	gitFixtureRun(t, parentOrigin, "add", "-A")
	gitFixtureRun(t, parentOrigin, "commit", "-m", "add submodule")
	cloneRoot := filepath.Join(base, "clone")
	cloned, err := gitops.Clone(context.Background(), gitops.CLIExecutor{}, parentOrigin, "ai/app", cloneRoot)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := workspace.Open(filepath.Join(base, "workspaces.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Add(workspace.AddParams{Name: "app", Kind: workspace.KindGit, Root: cloneRoot, GitRemote: cloned.Remote, GitBranch: cloned.Branch, GitBase: cloned.Base}); err != nil {
		t.Fatal(err)
	}
	sub := cloned.Submodules[0]
	childName := submoduleProjectName("app", sub.Path)
	if _, err := reg.Add(workspace.AddParams{Name: childName, Kind: workspace.KindGit, Root: sub.Root, GitRemote: sub.Remote,
		GitBranch: sub.Branch, GitBase: sub.Base, GitTarget: sub.DefaultBranch, Parent: "app"}); err != nil {
		t.Fatal(err)
	}
	server := &Server{reg: reg, gitExec: gitops.CLIExecutor{}, sessions: map[string]*Session{}}
	req := httptest.NewRequest("POST", "/api/projects/app/reject-branch", nil)
	req.SetPathValue("id", "app")
	response := httptest.NewRecorder()
	server.handleRejectBranch(response, req)
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got := gitFixtureRun(t, sub.Root, "rev-parse", sub.Branch); got != sub.Base {
		t.Fatalf("submodule feature branch reset to %s, want %s", got, sub.Base)
	}
	parentHead := gitFixtureRun(t, cloneRoot, "rev-parse", "ai/app")
	baseHead := gitFixtureRun(t, cloneRoot, "rev-parse", "main")
	if parentHead != baseHead {
		t.Fatalf("parent feature branch reset to %s, want %s", parentHead, baseHead)
	}
}

func initSubmoduleFixtureRepo(t *testing.T, path string) {
	t.Helper()
	if out, err := exec.Command("git", "init", "-b", "main", path).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	setGitUserReal(t, path)
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitFixtureRun(t, path, "add", "-A")
	gitFixtureRun(t, path, "commit", "-m", "init")
}

func gitFixtureRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	all := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", all...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
