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

func TestTaskWorktreeCommitsNestedSubmoduleIntoProjectBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	ctx := context.Background()
	base := t.TempDir()
	childOrigin := filepath.Join(base, "child-origin")
	initSubmoduleFixtureRepo(t, childOrigin)
	parentOrigin := filepath.Join(base, "parent-origin")
	initSubmoduleFixtureRepo(t, parentOrigin)
	gitFixtureRun(t, parentOrigin, "-c", "protocol.file.allow=always", "submodule", "add", childOrigin, "packages/auth")
	gitFixtureRun(t, parentOrigin, "add", "-A")
	gitFixtureRun(t, parentOrigin, "commit", "-m", "add submodule")

	cloneRoot := filepath.Join(base, "clone")
	parentRepo, err := gitops.Clone(ctx, gitops.CLIExecutor{}, parentOrigin, "ai/app", cloneRoot)
	if err != nil {
		t.Fatal(err)
	}
	setGitUserReal(t, cloneRoot)
	for _, sub := range parentRepo.Submodules {
		setGitUserReal(t, sub.Root)
	}
	if err := parentRepo.CreateBranch(ctx, "ai/epic/e1", parentRepo.Base); err != nil {
		t.Fatal(err)
	}
	if err := parentRepo.CreateBranch(ctx, "ai/task/t1", "ai/epic/e1"); err != nil {
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

	srv := &Server{reg: reg, gitExec: gitops.CLIExecutor{}, mergeLocks: map[string]*sync.Mutex{}}
	task := &board.Task{TaskSpec: board.TaskSpec{TaskID: "t1"}}
	srv.taskWorktree(ctx, "app", task, nil)
	ref, err := reg.TaskBranch("app", "t1")
	if err != nil || ref.Worktree == "" {
		t.Fatalf("task worktree ref=%+v err=%v", ref, err)
	}
	childRef, err := reg.TaskBranch(childName, childTaskKey("app", "t1"))
	if err != nil {
		t.Fatalf("submodule task branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(childRef.Worktree, "task.go"), []byte("package auth\n// task change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv.commitTaskWorktree(ctx, "app", task, ref.Worktree)
	parentGitlink := gitFixtureRun(t, ref.Worktree, "rev-parse", "HEAD:packages/auth")
	childHead := gitFixtureRun(t, sub.Root, "rev-parse", "HEAD")
	if parentGitlink != childHead {
		t.Fatalf("parent gitlink=%s child HEAD=%s", parentGitlink, childHead)
	}
	if !strings.Contains(gitFixtureRun(t, sub.Root, "show", "--stat", "--oneline", "HEAD"), "task.go") {
		t.Fatal("submodule task commit missing")
	}
	srv.removeTaskWorktree("app", "t1", ref.Worktree)
	if _, err := os.Stat(ref.Worktree); !os.IsNotExist(err) {
		t.Fatalf("parent task worktree remains: %v", err)
	}
	if _, err := reg.TaskBranch(childName, childTaskKey("app", "t1")); err == nil {
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
