package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSubmodulesConfig(t *testing.T) {
	got := parseSubmodulesConfig("submodule.auth.path=packages/auth\nsubmodule.auth.url=../auth.git\nsubmodule.auth.branch=main\nsubmodule.docs.path=docs\n")
	if len(got) != 1 || got[0].Path != "packages/auth" || got[0].URL != "../auth.git" || got[0].Branch != "main" {
		t.Fatalf("parsed submodules = %+v", got)
	}
}

func TestListSubmodulesRejectsPathsOutsideProject(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitmodules"), []byte("[submodule \"x\"]\npath = ../outside\nurl = https://example.test/x.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ex := &fakeExecutor{resp: map[string]string{root + " | git config -f .gitmodules --list": "submodule.x.path=../outside\nsubmodule.x.url=https://example.test/x.git\n"}}
	if _, err := ListSubmodules(context.Background(), ex, root); err == nil || !strings.Contains(err.Error(), "небезопасный путь") {
		t.Fatalf("expected unsafe path error, got %v", err)
	}
}

func TestGitCLISubmoduleCommitAndPush(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	ctx := context.Background()
	base := t.TempDir()
	childOrigin := filepath.Join(base, "auth-origin")
	initGitRepo(t, childOrigin)
	if err := os.WriteFile(filepath.Join(childOrigin, "auth.go"), []byte("package auth\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, childOrigin, "add", "-A")
	gitRun(t, childOrigin, "commit", "-m", "init auth")

	parentOrigin := filepath.Join(base, "app-origin")
	initGitRepo(t, parentOrigin)
	gitRun(t, parentOrigin, "-c", "protocol.file.allow=always", "submodule", "add", childOrigin, "packages/auth")
	gitRun(t, parentOrigin, "add", "-A")
	gitRun(t, parentOrigin, "commit", "-m", "init app")

	clone := filepath.Join(base, "clone")
	repo, err := Clone(ctx, CLIExecutor{}, parentOrigin, "ai/app", clone)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if len(repo.Submodules) != 1 {
		t.Fatalf("submodules=%+v", repo.Submodules)
	}
	sub := repo.Submodules[0]
	if sub.Branch != "ai/app/submodule/packages_auth" {
		t.Fatalf("branch=%q", sub.Branch)
	}
	if sub.DefaultBranch != "main" {
		t.Fatalf("default branch=%q, want main", sub.DefaultBranch)
	}
	setGitUser(t, clone)
	setGitUser(t, sub.Root)
	if err := os.WriteFile(filepath.Join(sub.Root, "feature.go"), []byte("package auth\n// feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := repo.Commit(ctx, "add auth feature"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := repo.Push(ctx); err != nil {
		t.Fatalf("Push: %v", err)
	}
	out, err := run(t, childOrigin, "", "git", "branch", "--list", sub.Branch)
	if err != nil || strings.TrimSpace(out) != sub.Branch {
		t.Fatalf("submodule branch not pushed: %q, %v", out, err)
	}
	if out, err := run(t, parentOrigin, "", "git", "branch", "--list", "ai/app"); err != nil || strings.TrimSpace(out) != "ai/app" {
		t.Fatalf("parent feature branch missing: %q, %v", out, err)
	}
}

func initGitRepo(t *testing.T, path string) {
	t.Helper()
	if out, err := run(t, "", "", "git", "init", "-b", "main", path); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	setGitUser(t, path)
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("# repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, path, "add", "-A")
	gitRun(t, path, "commit", "-m", "init")
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := run(t, dir, "", "git", args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(out)
}
