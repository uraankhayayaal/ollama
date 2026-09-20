package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ai/forges"
	"ai/gitops"
	"ai/projects"
	"ai/workspace"

	"github.com/alicebob/miniredis/v2"
)

// fakeGit — исполнитель git для hermetic-тестов: записывает вызовы и может
// отвечать заданными значениями (префиксное совпадение по команде git);
// реальный git CLI не требуется.
type fakeGit struct {
	mu     sync.Mutex
	calls  []string          // "dir | git ..."
	starts map[string]string // префикс команды → вывод
	fails  map[string]string // префикс команды → текст ошибки
}

func (f *fakeGit) Exec(_ context.Context, dir string, argv ...string) (string, error) {
	call := dir + " | git " + strings.Join(argv[1:], " ")
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	cmd := "git " + strings.Join(argv[1:], " ")
	for prefix, errMsg := range f.fails {
		if strings.HasPrefix(cmd, prefix) {
			return "", errors.New(errMsg)
		}
	}
	for prefix, out := range f.starts {
		if strings.HasPrefix(cmd, prefix) {
			return out, nil
		}
	}
	return "", nil
}

func (f *fakeGit) saw(arg string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.Contains(c, arg) {
			return true
		}
	}
	return false
}

func (f *fakeGit) callsList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// stubForge — стаб провайдера форджа: захватывает параметры MR/PR и
// возвращает предзаданную ссылку. Реализует forges.Forge без сети.
type stubForge struct {
	opts forges.MergeRequestOptions
	url  string
}

func (s *stubForge) GetDiff() (string, error)               { return "", nil }
func (s *stubForge) PostComment(forges.ReviewComment) error { return nil }
func (s *stubForge) PostSummary(string) error               { return nil }
func (s *stubForge) Approve(string) error                   { return nil }
func (s *stubForge) CreateMergeRequest(o forges.MergeRequestOptions) (string, error) {
	s.opts = o
	return s.url, nil
}

// newTestServerGit создаёт сервер с fake-исполнителем git и стабом форджа.
func newTestServerGit(t *testing.T, git gitops.Executor, forge func(remote, token string) (forges.Forge, error)) (*Server, http.Handler, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	t.Setenv("BOARD_REDIS_ADDR", mr.Addr())
	wsPath := filepath.Join(t.TempDir(), "workspaces.json")
	srv, err := NewServer(Config{WorkspacesPath: wsPath, GitExec: git, ForgeFactory: forge})
	if err != nil {
		t.Fatal(err)
	}
	return srv, srv.routes(), mr
}

// registerGit регистрирует git-проект и создаёт его корень на диске.
func registerGit(t *testing.T, srv *Server, name, remote, branch, base string) {
	t.Helper()
	root := projects.ProjectDir(name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if _, err := srv.reg.Add(workspace.AddParams{
		Name: name, Kind: workspace.KindGit, Root: root,
		GitRemote: remote, GitBranch: branch, GitBase: base,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenGitProjectClonesAndRegisters(t *testing.T) {
	git := &fakeGit{starts: map[string]string{
		"git rev-parse --abbrev-ref HEAD": "main\n",
	}}
	srv, handler, _ := newTestServerGit(t, git, nil)

	// Реальный git CLI создал бы каталог клона; в hermetic-тесте создаём сами.
	dest := projects.ProjectDir("myrepo")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dest) })

	url := "git@gitlab.com:g/myrepo.git"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects",
		bytes.NewBufferString(`{"path_or_git":"`+url+`"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST path_or_git: %d, body: %s", rec.Code, rec.Body.String())
	}

	var meta map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta["project_name"] != "myrepo" {
		t.Fatalf("project_name = %v, want myrepo", meta["project_name"])
	}
	if meta["kind"] != string(workspace.KindGit) {
		t.Fatalf("kind = %v, want %v", meta["kind"], workspace.KindGit)
	}
	if meta["git_branch"] != "ai/myrepo" || meta["git_base"] != "main" {
		t.Fatalf("git_branch/base = %v/%v, want ai/myrepo/main", meta["git_branch"], meta["git_base"])
	}
	if meta["git_remote"] != url {
		t.Fatalf("git_remote = %v", meta["git_remote"])
	}

	inf, err := srv.reg.Get("myrepo")
	if err != nil {
		t.Fatalf("реестр: %v", err)
	}
	if inf.Kind != workspace.KindGit || inf.GitRemote != url {
		t.Fatalf("запись реестра: %+v", inf)
	}
	if !git.saw("git clone") || !git.saw("git checkout -b ai/myrepo") {
		t.Fatalf("ожидали clone + checkout -b, вызовы: %v", git.callsList())
	}

	// Повторное открытие идемпотентно (тот же проект возвращается).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/projects",
		bytes.NewBufferString(`{"path_or_git":"`+url+`"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("повторное открытие: %d", rec.Code)
	}
}

func TestGitProjectNameFromURLs(t *testing.T) {
	cases := map[string]string{
		"git@gitlab.com:group/proj.git":          "proj",
		"git@gitlab.com:g1/g2/proj.git":          "proj",
		"https://github.com/owner/repo.git":      "repo",
		"https://gitlab.com/a/b/c/name":          "name",
		"https://host/inner/deep/repo.git?x=1#y": "repo",
		"":                                       "",
	}
	for in, want := range cases {
		if got := gitProjectName(in); got != want {
			t.Fatalf("gitProjectName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGetDiffGitProject(t *testing.T) {
	const raw = "diff --git a/x b/x\nindex 1..2 100644\n--- a/x\n+++ b/x\n@@ -1 +1,2 @@\n-старая\n+строка\n+nовая\n" +
		"diff --git a/f.go b/f.go\nnew file mode 100644\n--- /dev/null\n+++ b/f.go\n@@ -0,0 +1 @@\n+package f\n"
	git := &fakeGit{starts: map[string]string{
		"git diff main": raw,
	}}
	srv, handler, _ := newTestServerGit(t, git, nil)
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/projects/myrepo/diff", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET diff: %d, body: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Kind   string `json:"kind"`
		Branch string `json:"branch"`
		Base   string `json:"base"`
		Remote string `json:"remote"`
		Files  []struct {
			Path    string `json:"path"`
			Status  string `json:"status"`
			Added   int    `json:"added"`
			Deleted int    `json:"deleted"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Kind != "git" || out.Branch != "ai/myrepo" || out.Base != "main" {
		t.Fatalf("diff meta: %+v", out)
	}
	if len(out.Files) != 2 {
		t.Fatalf("файлов = %+v, want 2", out.Files)
	}
	if out.Files[0].Path != "x" || out.Files[0].Status != "modified" ||
		out.Files[0].Added != 2 || out.Files[0].Deleted != 1 {
		t.Fatalf("файл x: %+v", out.Files[0])
	}
	if out.Files[1].Path != "f.go" || out.Files[1].Status != "added" || out.Files[1].Added != 1 {
		t.Fatalf("файл f.go: %+v", out.Files[1])
	}

	// Ленивая загрузка: патч конкретного файла.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/projects/myrepo/diff?file=f.go", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET diff?file: %d, body: %s", rec.Code, rec.Body.String())
	}
	var fout struct {
		Kind  string `json:"kind"`
		Path  string `json:"path"`
		Patch string `json:"patch"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &fout); err != nil {
		t.Fatal(err)
	}
	if fout.Path != "f.go" || !strings.Contains(fout.Patch, "+package f") ||
		!strings.Contains(fout.Patch, "+++ b/f.go") {
		t.Fatalf("патч файла: %+v", fout)
	}
}

func TestGetDiffGitProjectUnknownFileNotFound(t *testing.T) {
	git := &fakeGit{starts: map[string]string{
		"git diff main": "--- a/x\n+++ b/x\n+строка\n",
	}}
	srv, handler, _ := newTestServerGit(t, git, nil)
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/projects/myrepo/diff?file=missing.go", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("diff?file=missing: %d, want 404", rec.Code)
	}
}

func TestAcceptGitProjectCreatesMR(t *testing.T) {
	git := &fakeGit{starts: map[string]string{
		"git status --porcelain": " M file.go\n",
	}}
	stub := &stubForge{url: "https://gitlab.com/g/myrepo/-/merge_requests/1"}
	srv, handler, _ := newTestServerGit(t, git, func(remote, token string) (forges.Forge, error) {
		if remote != "git@gitlab.com:g/myrepo.git" {
			t.Fatalf("remote для форджа = %q", remote)
		}
		return stub, nil
	})
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")

	reqBody := `{"title":"Готово","description":"Итог","message":"фикс"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects/myrepo/accept",
		bytes.NewBufferString(reqBody))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST accept: %d, body: %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["url"] != stub.url {
		t.Fatalf("url = %q, want %q", out["url"], stub.url)
	}
	if stub.opts.SourceBranch != "ai/myrepo" || stub.opts.TargetBranch != "main" {
		t.Fatalf("opts = %+v", stub.opts)
	}
	if stub.opts.Title != "Готово" || stub.opts.Description != "Итог" {
		t.Fatalf("opts title/desc = %q/%q", stub.opts.Title, stub.opts.Description)
	}
	if !git.saw("git commit") || !git.saw("git push") {
		t.Fatalf("ожидали commit+push, вызовы: %v", git.callsList())
	}
}

func TestAcceptGitProjectWithoutChangesRejected(t *testing.T) {
	git := &fakeGit{starts: map[string]string{
		"git status --porcelain": "",
	}}
	srv, handler, _ := newTestServerGit(t, git, func(remote, token string) (forges.Forge, error) {
		return &stubForge{url: "x"}, nil
	})
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects/myrepo/accept",
		bytes.NewBufferString(`{"title":"Пусто"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("accept без изменений: %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestAcceptNonGitProjectRejected(t *testing.T) {
	srv, handler, _ := newTestServerGit(t, &fakeGit{}, nil)
	dir := t.TempDir()
	if _, err := srv.reg.Add(workspace.AddParams{Name: "plain", Kind: workspace.KindDir, Root: dir, Confirm: true}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects/plain/accept",
		bytes.NewBufferString(`{"title":"x"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("accept не-git: %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestAcceptGitProjectPushesWithToken проверяет https-remote с токеном:
// push идёт на URL с встроенным x-access-token (headless-сервер без
// credential-helper), и форджу передаётся тот же токен.
func TestAcceptGitProjectPushesWithToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "tok")
	git := &fakeGit{starts: map[string]string{
		"git status --porcelain": " M file.go\n",
	}}
	stub := &stubForge{url: "https://github.com/o/r/pulls/7"}
	srv, handler, _ := newTestServerGit(t, git, func(remote, token string) (forges.Forge, error) {
		if remote != "https://github.com/o/r.git" || token != "tok" {
			t.Fatalf("forge(remote=%q, token=%q)", remote, token)
		}
		return stub, nil
	})
	registerGit(t, srv, "myrepo", "https://github.com/o/r.git", "ai/myrepo", "main")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects/myrepo/accept",
		bytes.NewBufferString(`{"title":"Готово"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST accept: %d, body: %s", rec.Code, rec.Body.String())
	}
	if !git.saw("git push https://x-access-token:tok@github.com/o/r.git ai/myrepo") {
		t.Fatalf("ожидали push с токеном, вызовы: %v", git.callsList())
	}
}

// TestAcceptGitProjectSSHRemoteIgnoresToken: SSH-remote токеном не
// авторизуется — остаётся штатный `git push -u origin` (SSH-ключ).
func TestAcceptGitProjectSSHRemoteIgnoresToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "tok")
	git := &fakeGit{starts: map[string]string{
		"git status --porcelain": " M file.go\n",
	}}
	srv, handler, _ := newTestServerGit(t, git, func(remote, token string) (forges.Forge, error) {
		return &stubForge{url: "x"}, nil
	})
	registerGit(t, srv, "myrepo", "git@github.com:o/r.git", "ai/myrepo", "main")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects/myrepo/accept",
		bytes.NewBufferString(`{"title":"Готово"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST accept: %d, body: %s", rec.Code, rec.Body.String())
	}
	if !git.saw("git push -u origin ai/myrepo") {
		t.Fatalf("SSH-remote должны пушить через origin, вызовы: %v", git.callsList())
	}
}

func TestRejectBranchGitProject(t *testing.T) {
	git := &fakeGit{}
	srv, handler, _ := newTestServerGit(t, git, nil)
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects/myrepo/reject-branch",
		bytes.NewBufferString(`{}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reject-branch: %d, body: %s", rec.Code, rec.Body.String())
	}
	if !git.saw("git reset --hard main") || !git.saw("git branch -D ai/myrepo") {
		t.Fatalf("ожидали reset --hard базы + удаление ветки, вызовы: %v", git.callsList())
	}
}

func TestRejectBranchNonGitRejected(t *testing.T) {
	srv, handler, _ := newTestServerGit(t, &fakeGit{}, nil)
	dir := t.TempDir()
	if _, err := srv.reg.Add(workspace.AddParams{Name: "plain", Kind: workspace.KindDir, Root: dir, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects/plain/reject-branch",
		bytes.NewBufferString(`{}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reject не-git: %d, want 400", rec.Code)
	}
}

// TestSnapDiffBaselineAtOpen: у локального проекта «точка отхода» фиксируется
// при открытии (не при первом запросе диффа), поэтому изменения, сделанные
// после открытия, показываются в диффе.
func TestSnapDiffBaselineAtOpen(t *testing.T) {
	_, handler, _ := newTestServerGit(t, &fakeGit{}, nil)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Открытие проекта сразу фиксирует baseline.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects",
		bytes.NewBufferString(`{"path_or_git":"`+dir+`"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST projects: %d, body: %s", rec.Code, rec.Body.String())
	}
	name := dirBase(dir)

	// Вносим изменения после открытия (так делает агент).
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/projects/"+name+"/diff", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET diff: %d, body: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Kind     string
		Added    []string
		Modified []string
		Removed  []string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Kind != "snap" {
		t.Fatalf("kind = %q, want snap", out.Kind)
	}
	if len(out.Added) != 1 || !strings.HasSuffix(out.Added[0], "b.txt") {
		t.Fatalf("added = %v, want [b.txt]", out.Added)
	}
	if len(out.Modified) != 1 || !strings.HasSuffix(out.Modified[0], "a.txt") {
		t.Fatalf("modified = %v, want [a.txt]", out.Modified)
	}
	if len(out.Removed) != 0 {
		t.Fatalf("removed = %v, want none", out.Removed)
	}
}
