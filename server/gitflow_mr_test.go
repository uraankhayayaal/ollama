package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai/board"
	"ai/forges"
	"ai/gitops"
	"ai/workspace"

	"github.com/alicebob/miniredis/v2"
)

// stubMRForge — стаб форджа с фиче-ветками и MRStatusProvider: сочетает
// stubForge (CreateMergeRequest) и поиск MR по ветке без сети.
type stubMRForge struct {
	url  string // возвращаемый CreateMergeRequest
	opts forges.MergeRequestOptions
	find map[string]forges.MergeRequestInfo // "[source|target]" → MR
}

func (s *stubMRForge) GetDiff() (string, error)               { return "", nil }
func (s *stubMRForge) PostComment(forges.ReviewComment) error { return nil }
func (s *stubMRForge) PostSummary(string) error               { return nil }
func (s *stubMRForge) Approve(string) error                   { return nil }
func (s *stubMRForge) CreateMergeRequest(o forges.MergeRequestOptions) (string, error) {
	s.opts = o
	return s.url, nil
}

func (s *stubMRForge) FindMergeRequest(source, target string) (*forges.MergeRequestInfo, error) {
	if info, ok := s.find[source+"|"+target]; ok {
		return &info, nil
	}
	return nil, forges.ErrNoMergeRequest
}

// setupMRBoards создаёт git-проект myrepo + эпик/задачу на доске и ветки
// эпика/задачи в side-реестре. forgeFactory выполняет стаб.
func setupMRBoards(t *testing.T, mr *miniredis.Miniredis, srv *Server) {
	t.Helper()
	ctx := context.Background()
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Релиз", Description: "Легенда релиза"},
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
	if err := srv.reg.SetEpicBranch("myrepo", "epic-1", workspace.BranchRef{Branch: "ai/epic/e1", Base: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := srv.reg.SetTaskBranch("myrepo", "task-1", workspace.BranchRef{Branch: "ai/task/t1", Base: "ai/epic/e1"}); err != nil {
		t.Fatal(err)
	}
}

// TestCreateEpicMRPushesAndRegisters — «Создать MR» эпика: ветка ai/epic/e1
// пушится в remote, MR → main создан через фордж, ссылка в side-реестре.
func TestCreateEpicMRPushesAndRegisters(t *testing.T) {
	git := &fakeGit{}
	stub := &stubMRForge{url: "https://gitlab.com/g/myrepo/-/merge_requests/11"}
	srv, handler, mr := newTestServerGit(t, git, func(remote, token string) (forges.Forge, error) {
		if remote != "git@gitlab.com:g/myrepo.git" {
			t.Fatalf("remote = %q", remote)
		}
		return stub, nil
	})
	setupMRBoards(t, mr, srv)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/mr", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("создание MR эпика: %d, body: %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["mr_url"] != stub.url || out["source"] != "ai/epic/e1" || out["target"] != "main" {
		t.Fatalf("ответ = %+v", out)
	}
	if !git.saw("git push -u origin ai/epic/e1") {
		t.Fatalf("ожидали push ветки эпика, вызовы: %v", git.callsList())
	}
	if stub.opts.SourceBranch != "ai/epic/e1" || stub.opts.TargetBranch != "main" {
		t.Fatalf("opts = %+v", stub.opts)
	}
	if !strings.Contains(stub.opts.Title, "Релиз") {
		t.Fatalf("title = %q, want с названием эпика", stub.opts.Title)
	}

	mf, err := srv.reg.EpicMR("myrepo", "epic-1")
	if err != nil || mf.URL != stub.url || mf.State != "open" || mf.Target != "main" {
		t.Fatalf("EpicMR = %+v, %v", mf, err)
	}

	// Идемпотентность: повторный вызов возвращает ту же ссылку без нового push.
	before := len(git.callsList())
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/mr", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("повторный MR эпика: %d, body: %s", rec.Code, rec.Body.String())
	}
	if len(git.callsList()) != before {
		t.Fatalf("повторный вызов не должен пушить, вызовы: %v", git.callsList())
	}
}

// TestAutoTaskMRCBDoneBeforeMerge — авто-MR задачи на done создаётся ДО мёрджа
// в релиз эпика: даже если сам мёрдж падает (merge-base завершается ошибкой),
// MR обязан уже существовать в side-реестре. Регрессия: раньше авто-MR шёл
// после mergeTaskBranch, и fork отклонял его 422 «No commits between …».
func TestAutoTaskMRCBDoneBeforeMerge(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "tok")
	git := &fakeGit{fails: map[string]string{
		"git merge-base": "ошибка merge-base",
	}}
	stub := &stubMRForge{url: "https://github.com/o/r/pull/401"}
	srv, _, _ := newTestServerGit(t, git, func(remote, token string) (forges.Forge, error) {
		if remote != "https://github.com/o/r.git" || token != "tok" {
			t.Fatalf("forge(remote=%q, token=%q)", remote, token)
		}
		return stub, nil
	})
	ctx := context.Background()
	registerGit(t, srv, "myrepo", "https://github.com/o/r.git", "ai/myrepo", "main")
	// Ветки эпика/задачи в side-реестре (как авто-создание Ф-1).
	if err := srv.reg.SetEpicBranch("myrepo", "epic-1", workspace.BranchRef{Branch: "ai/epic/e1", Base: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := srv.reg.SetTaskBranch("myrepo", "task-1", workspace.BranchRef{Branch: "ai/task/t1", Base: "ai/epic/e1"}); err != nil {
		t.Fatal(err)
	}

	// done-автошаг: мёрдж в релиз упадёт, но авто-MR должен быть создан ДО него.
	srv.autoCommitAndMergeTask(ctx, "myrepo", &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "task-1", Title: "Фича"},
		EpicID:   "epic-1",
	}, nil)

	// MR целится в ветку эпика и зарегистрирован ДО попытки мёрджа:
	// merge-base упал → mergeTaskBranch вернул ошибку, но MR жив.
	if stub.opts.SourceBranch != "ai/task/t1" || stub.opts.TargetBranch != "ai/epic/e1" {
		t.Fatalf("авто-MR opts = %+v", stub.opts)
	}
	mrf, err := srv.reg.TaskMR("myrepo", "task-1")
	if err != nil {
		t.Fatalf("авто-MR не создан до мёрджа: %v", err)
	}
	if mrf.URL != stub.url || mrf.Source != "ai/task/t1" || mrf.Target != "ai/epic/e1" {
		t.Fatalf("TaskMR = %+v", mrf)
	}
	// Порядок вызовов: push ветки задачи (авто-MR) идёт раньше merge-base (мёрдж).
	calls := git.callsList()
	pushIdx, mergeIdx := -1, -1
	for i, c := range calls {
		if strings.Contains(c, "push") && strings.Contains(c, "ai/task/t1") && pushIdx < 0 {
			pushIdx = i
		}
		if strings.Contains(c, "merge-base") && mergeIdx < 0 {
			mergeIdx = i
		}
	}
	if pushIdx < 0 || mergeIdx < 0 || pushIdx > mergeIdx {
		t.Fatalf("авто-MR (push %d) должен идти до мёрджа (merge-base %d): %v", pushIdx, mergeIdx, calls)
	}
}

// TestCreateTaskMRTargetsEpicBranch — MR задачи целится в ветку эпика.
func TestCreateTaskMRTargetsEpicBranch(t *testing.T) {
	git := &fakeGit{}
	stub := &stubMRForge{url: "https://gitlab.com/g/myrepo/-/merge_requests/12"}
	srv, handler, mr := newTestServerGit(t, git, func(remote, token string) (forges.Forge, error) {
		return stub, nil
	})
	setupMRBoards(t, mr, srv)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/tasks/task-1/mr", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("создание MR задачи: %d, body: %s", rec.Code, rec.Body.String())
	}
	if !git.saw("git push -u origin ai/task/t1") {
		t.Fatalf("ожидали push ветки задачи, вызовы: %v", git.callsList())
	}
	if stub.opts.SourceBranch != "ai/task/t1" || stub.opts.TargetBranch != "ai/epic/e1" {
		t.Fatalf("opts = %+v", stub.opts)
	}
	if _, err := srv.reg.TaskMR("myrepo", "task-1"); err != nil {
		t.Fatalf("TaskMR не зарегистрирован: %v", err)
	}
}

// TestCreateTaskMRPushesMissingBaseBranch — база MR задачи (ветка эпика) ещё
// не запушена: GitHub/GitLab отклонили бы MR (422 base invalid), поэтому
// «Создать MR» сначала пушит базовую ветку эпика, затем ветку задачи.
func TestCreateTaskMRPushesMissingBaseBranch(t *testing.T) {
	git := &fakeGit{} // ls-remote по умолчанию пуст → ветки эпика в remote нет
	stub := &stubMRForge{url: "https://github.com/o/r/pull/13"}
	srv, handler, mr := newTestServerGit(t, git, func(remote, token string) (forges.Forge, error) {
		return stub, nil
	})
	setupMRBoards(t, mr, srv)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/tasks/task-1/mr", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("создание MR задачи: %d, body: %s", rec.Code, rec.Body.String())
	}
	if !git.saw("ls-remote --heads origin ai/epic/e1") {
		t.Fatalf("ожидали проверку базовой ветки в remote, вызовы: %v", git.callsList())
	}
	if !git.saw("git push -u origin ai/epic/e1") {
		t.Fatalf("ожидали push отсутствующей базовой ветки эпика, вызовы: %v", git.callsList())
	}
	// База пушится до ветки задачи (иначе MR создастся раньше base).
	calls := git.callsList()
	baseIdx, taskIdx := -1, -1
	for i, c := range calls {
		if strings.Contains(c, "push -u origin ai/epic/e1") && baseIdx < 0 {
			baseIdx = i
		}
		if strings.Contains(c, "push -u origin ai/task/t1") && taskIdx < 0 {
			taskIdx = i
		}
	}
	if baseIdx < 0 || taskIdx < 0 || baseIdx > taskIdx {
		t.Fatalf("база должна пушиться раньше ветки задачи: base=%d task=%d (%v)", baseIdx, taskIdx, calls)
	}
}

// TestCreateTaskMRSkipsExistingBaseBranch — базовая ветка уже есть в remote
// (ls-remote возвращает ref): повторный push базы не выполняется.
func TestCreateTaskMRSkipsExistingBaseBranch(t *testing.T) {
	git := &fakeGit{starts: map[string]string{
		"git ls-remote": "abc123\trefs/heads/ai/epic/e1\n",
	}}
	stub := &stubMRForge{url: "https://github.com/o/r/pull/14"}
	srv, handler, mr := newTestServerGit(t, git, func(remote, token string) (forges.Forge, error) {
		return stub, nil
	})
	setupMRBoards(t, mr, srv)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/tasks/task-1/mr", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("создание MR задачи: %d, body: %s", rec.Code, rec.Body.String())
	}
	if git.saw("push -u origin ai/epic/e1") {
		t.Fatalf("не ждали push существующей базовой ветки, вызовы: %v", git.callsList())
	}
	if !git.saw("push -u origin ai/task/t1") {
		t.Fatalf("ожидали push ветки задачи, вызовы: %v", git.callsList())
	}
}

// TestCreateMRWithoutBranch400 — без ветки эпика/задачи в side-реестре — 400.
func TestCreateMRWithoutBranch400(t *testing.T) {
	srv, handler, mr := newTestServerGit(t, &fakeGit{}, func(remote, token string) (forges.Forge, error) {
		return &stubMRForge{url: "x"}, nil
	})
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
		"POST", "/api/projects/myrepo/epics/epic-1/mr", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("MR без ветки эпика: %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestCreateMRNonGitRejected — не-git проект: 400.
func TestCreateMRNonGitRejected(t *testing.T) {
	srv, handler, _ := newTestServerGit(t, &fakeGit{}, nil)
	dir := t.TempDir()
	if _, err := srv.reg.Add(workspace.AddParams{Name: "plain", Kind: workspace.KindDir, Root: dir, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/plain/epics/epic-1/mr", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("MR не-git проекта: %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestCreateEpicMRUnknownEpic404 — несуществующий эпик: 404.
func TestCreateEpicMRUnknownEpic404(t *testing.T) {
	srv, handler, _ := newTestServerGit(t, &fakeGit{}, nil)
	registerGit(t, srv, "myrepo", "git@gitlab.com:g/myrepo.git", "ai/myrepo", "main")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/nope/mr", strings.NewReader(`{}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("несуществующий эпик: %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestBoardSnapshotIncludesGitStatus — снимок доски (GET /api/projects/{id})
// проставляет git-статус: ветки + ссылки на MR эпика и задачи.
func TestBoardSnapshotIncludesGitStatus(t *testing.T) {
	git := &fakeGit{}
	stub := &stubMRForge{url: "https://github.com/o/r/pulls/7"}
	srv, handler, mr := newTestServerGit(t, git, func(remote, token string) (forges.Forge, error) {
		return stub, nil
	})
	setupMRBoards(t, mr, srv)
	if err := srv.reg.SetEpicMR("myrepo", "epic-1", workspace.MRRef{
		URL: stub.url, Source: "ai/epic/e1", Target: "main", State: "open",
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/projects/myrepo", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET доски: %d, body: %s", rec.Code, rec.Body.String())
	}
	var snap boardSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Git == nil {
		t.Fatal("git-статус не проставлен в снимке доски")
	}
	if snap.Git.Base != "main" {
		t.Fatalf("git.base = %q, want main", snap.Git.Base)
	}
	elink, ok := snap.Git.Epics["epic-1"]
	if !ok || elink.Branch != "ai/epic/e1" || elink.Target != "main" {
		t.Fatalf("git.epics[epic-1] = %+v, ok=%v", elink, ok)
	}
	if elink.MRURL != stub.url || elink.MRState != "open" {
		t.Fatalf("git.epics[epic-1].mr = %+v", elink)
	}
	if elink.BranchURL == "" {
		t.Fatal("ожидали web-ссылку на ветку эпика")
	}
	if !strings.Contains(elink.BranchURL, "gitlab.com") {
		t.Fatalf("branch_url = %q (SSH-remote → gitlab web)", elink.BranchURL)
	}
	tlink, ok := snap.Git.Tasks["task-1"]
	if !ok || tlink.Branch != "ai/task/t1" || tlink.Target != "ai/epic/e1" {
		t.Fatalf("git.tasks[task-1] = %+v, ok=%v", tlink, ok)
	}
}

// TestReconcileMRsFindsForgeCreatedMR — сверка находит MR, созданный вне UI
// (stub его знает, реестр — пуст), и пишет его в side-реестр.
func TestReconcileMRsFindsForgeCreatedMR(t *testing.T) {
	git := &fakeGit{}
	stub := &stubMRForge{find: map[string]forges.MergeRequestInfo{
		"ai/task/t1|ai/epic/e1": {URL: "https://github.com/o/r/pulls/42", State: "open"},
	}}
	srv, _, mr := newTestServerGit(t, git, func(remote, token string) (forges.Forge, error) {
		return stub, nil
	})
	setupMRBoards(t, mr, srv)

	changed, err := srv.reconcileMRs(context.Background(), "myrepo")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("ожидали изменение реестра при найденном MR")
	}
	tmr, err := srv.reg.TaskMR("myrepo", "task-1")
	if err != nil || tmr.URL != "https://github.com/o/r/pulls/42" || tmr.State != "open" {
		t.Fatalf("TaskMR = %+v, %v", tmr, err)
	}
	// Эпик без MR в фордже — реестр не меняется.
	if _, err := srv.reg.EpicMR("myrepo", "epic-1"); err == nil {
		t.Fatal("эпик без MR не должен попасть в реестр")
	}

	// Повторная сверка: без изменений → changed=false.
	changed, err = srv.reconcileMRs(context.Background(), "myrepo")
	if err != nil || changed {
		t.Fatalf("повторная сверка: changed=%v, err=%v", changed, err)
	}
}

// TestReconcileMRsSkipsNonMRForge — фордж без MRStatusProvider (только Forge):
// сверка ничего не делает и не падает.
func TestReconcileMRsSkipsNonMRForge(t *testing.T) {
	srv, _, mr := newTestServerGit(t, &fakeGit{}, func(remote, token string) (forges.Forge, error) {
		return &stubForge{url: "x"}, nil
	})
	setupMRBoards(t, mr, srv)

	changed, err := srv.reconcileMRs(context.Background(), "myrepo")
	if err != nil || changed {
		t.Fatalf("сверка без MR-status: changed=%v, err=%v", changed, err)
	}
}

// TestBranchWebURL формат ссылок на ветку для github/gitlab (https и ssh).
func TestBranchWebURL(t *testing.T) {
	cases := []struct {
		remote, branch, want string
	}{
		{"git@github.com:o/r.git", "ai/epic/e1", "https://github.com/o/r/tree/ai/epic/e1"},
		{"git@gitlab.com:g/r.git", "ai/epic/e1", "https://gitlab.com/g/r/-/tree/ai/epic/e1"},
		{"https://gitlab.example.org/group/proj.git", "ai/task/t-1", "https://gitlab.example.org/group/proj/-/tree/ai/task/t-1"},
		{"https://github.com/o/r.git", "main", "https://github.com/o/r/tree/main"},
	}
	for _, c := range cases {
		got := branchWebURL(c.remote, c.branch)
		if got != c.want {
			t.Errorf("branchWebURL(%q, %q) = %q, want %q", c.remote, c.branch, got, c.want)
		}
	}
	if got := branchWebURL("", "main"); got != "" {
		t.Errorf("пустой remote: %q, want ''", got)
	}
}

// TestEpicBranchKickBoardPublishesWhenIdle — ветка создаётся кнопкой в UI вне
// оркестрации (сессия есть, running=false). boardFlusher в этот момент не
// крутится, поэтому kickBoard обязан опубликовать снимок доски сразу: иначе
// «Создать ветку эпика» в модалке не превратится в ссылку на ветку (Ф-5).
func TestEpicBranchKickBoardPublishesWhenIdle(t *testing.T) {
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

	sess, _, err := srv.getOrCreate("myrepo")
	if err != nil {
		t.Fatal(err)
	}
	sess.mu.Lock()
	sess.running = false
	sess.mu.Unlock()

	srvConn, cliConn := net.Pipe()
	defer cliConn.Close()
	ws := &WsConn{conn: srvConn, br: bufio.NewReader(srvConn), closed: make(chan struct{})}
	t.Cleanup(func() { _ = ws.Close() })
	srv.hub.Subscribe("myrepo", ws)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/branch", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("ветка эпика: %d, body: %s", rec.Code, rec.Body.String())
	}

	// Читаем кадры, пока не увидим board-событие с git-веткой эпика.
	gv, ok := readBoardGit(t, cliConn, 5*time.Second)
	if !ok || gv.Git == nil || gv.Git.Epics["epic-1"].Branch != "ai/epic/epic-1" {
		t.Fatalf("board-событие с веткой ai/epic/epic-1 не пришло; последний git: %+v", gv.Git)
	}
}

// readBoardGit читает кадры WS до первого board-события (или таймаута) и
// возвращает его payload.git. ok=false — события не было. Поддерживает
// extended-length кадры (как ws.go readFrame).
func readBoardGit(t *testing.T, conn net.Conn, timeout time.Duration) (struct {
	Git *gitView `json:"git"`
}, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		var ev struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		hdr := make([]byte, 2)
		if _, err := io.ReadFull(conn, hdr); err != nil {
			continue
		}
		length := int64(hdr[1] & 0x7f)
		switch length {
		case 126:
			ext := make([]byte, 2)
			if _, err := io.ReadFull(conn, ext); err != nil {
				continue
			}
			length = int64(ext[0])<<8 | int64(ext[1])
		case 127:
			ext := make([]byte, 8)
			if _, err := io.ReadFull(conn, ext); err != nil {
				continue
			}
			length = 0
			for _, b := range ext {
				length = length<<8 | int64(b)
			}
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(conn, body); err != nil {
			continue
		}
		if err := json.Unmarshal(body, &ev); err != nil {
			continue
		}
		if ev.Type != "board" {
			continue
		}
		var snap struct {
			Git *gitView `json:"git"`
		}
		if err := json.Unmarshal(ev.Payload, &snap); err != nil {
			continue
		}
		return snap, true
	}
	return struct {
		Git *gitView `json:"git"`
	}{}, false
}

// TestCreateEpicMREndToEndRealGit — E2E «Создать MR» эпика на реальном git:
// клон локального origin → ветка эпика через REST → MR-кнопка пушит ветку
// в origin и регистрирует ссылку.
func TestCreateEpicMREndToEndRealGit(t *testing.T) {
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
	if _, err := exec.Command("git", "-C", origin, "config", "receive.denyCurrentBranch", "ignore").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
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

	stub := &stubMRForge{url: "https://github.com/o/r/pulls/7"}
	srv, handler, mr := newTestServerGit(t, gitops.CLIExecutor{}, func(remote, token string) (forges.Forge, error) {
		return stub, nil
	})
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
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/branch", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("ветка эпика: %d, body: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/mr", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("MR эпика: %d, body: %s", rec.Code, rec.Body.String())
	}
	if _, err := exec.Command("git", "-C", origin, "rev-parse", "--verify", "refs/heads/ai/epic/epic-1").CombinedOutput(); err != nil {
		t.Fatalf("ai/epic/epic-1 не запушена в origin: %v", err)
	}
	if stub.opts.SourceBranch != "ai/epic/epic-1" || stub.opts.TargetBranch != "main" {
		t.Fatalf("opts = %+v", stub.opts)
	}
	if mf, err := srv.reg.EpicMR("myrepo", "epic-1"); err != nil || mf.URL != stub.url {
		t.Fatalf("EpicMR = %+v, %v", mf, err)
	}
}
