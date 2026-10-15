// Тесты индексации RAG по содержимому ветки (server/ragref.go): сбор файлов из
// git-объекта, индекс main после релиза эпика и синхрон рабочей копии агентов.
package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apprag "ai/application/rag"
	"ai/infrastructure/git"
	"ai/infrastructure/rag"
	"ai/infrastructure/workspace"
)

// TestReleaseEpicRefreshesMainIndex — регрессия «main не попадает в RAG»:
// после «Залить в main» индекс главной ветки пересобирается по её содержимому.
// Без этого агент следующего эпика искал бы по коду до релиза.
func TestReleaseEpicRefreshesMainIndex(t *testing.T) {
	orig := buildProjectIndexer
	defer func() { buildProjectIndexer = orig }()

	// Рабочая копия помечена грязной: фоновый merge main в ai/<имя> тогда
	// пропускается (правки агентов), и тест изолированно проверяет индекс main.
	git := mockReleaseGit("")
	git.starts["git status --porcelain"] = " M server/handler.go\n"
	srv, handler, mr := setupGitflow(t, git)
	seedReleaseBoard(t, mr, srv)

	idx := &fakeProjectIndexer{}
	buildProjectIndexer = func() (projectIndexerCloser, error) { return idx, nil }
	stubRefItems(t, apprag.RefSnapshot{
		CommitSHA: "newmain0000000000000000000000000000000",
		Items:     []rag.IndexItem{{Path: "main.go", Scope: "root", Content: "package main"}},
	}, nil)

	// Сессия создаётся тем же путём, что и в REST-обработчике релиза.
	sess, _, err := srv.getOrCreate("myrepo")
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/release", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("«Залить в main»: %d, body: %s", rec.Code, rec.Body.String())
	}

	waitChatStatusContains(t, sess, "RAG-индекс проекта обновлён", 3*time.Second)
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if len(idx.calls) != 1 {
		t.Fatalf("IndexProject вызван %d раз(а), want 1 (индекс main после релиза)", len(idx.calls))
	}
	call := idx.calls[0]
	if call.opts.Branch != "main" {
		t.Fatalf("индексирована ветка %q, want main", call.opts.Branch)
	}
	if call.opts.CommitSHA != "newmain0000000000000000000000000000000" {
		t.Fatalf("коммит индексации %q — ожидался коммит ветки", call.opts.CommitSHA)
	}
	if call.project != "myrepo" {
		t.Fatalf("проект индексации %q, want myrepo", call.project)
	}
}

// TestSyncAgentBranchWithMain — рабочая копия агентов (ai/<имя>) подтягивается
// к main; при незакоммиченных правках merge не выполняется (правки теряться не
// должны).
func TestSyncAgentBranchWithMain(t *testing.T) {
	dirtyGit := &fakeGit{starts: map[string]string{
		"git rev-parse --abbrev-ref HEAD": "ai/myrepo\n",
		"git status --porcelain":          " M server/handler.go\n",
	}}
	srv, _, _ := newTestServerGit(t, dirtyGit, nil)
	inf := workspace.Info{Name: "myrepo", GitBranch: "ai/myrepo", GitBase: "main", GitRemote: "git@g:r.git"}
	repo := git.RepoFromState(dirtyGit, "/repo", inf.GitRemote, inf.GitBranch, inf.GitBase)

	if srv.syncAgentBranchWithMain(context.Background(), "myrepo", inf, repo) {
		t.Fatal("при грязном дереве merge выполняться не должен")
	}
	if dirtyGit.saw("git merge") {
		t.Fatalf("merge выполнен при грязном дереве: %v", dirtyGit.callsList())
	}

	cleanGit := &fakeGit{starts: map[string]string{
		"git rev-parse --abbrev-ref HEAD": "ai/myrepo\n",
		"git status --porcelain":          "",
	}}
	srv2, _, _ := newTestServerGit(t, cleanGit, nil)
	repo2 := git.RepoFromState(cleanGit, "/repo", inf.GitRemote, inf.GitBranch, inf.GitBase)
	if !srv2.syncAgentBranchWithMain(context.Background(), "myrepo", inf, repo2) {
		t.Fatalf("чистое дерево: ожидали merge main, вызовы: %v", cleanGit.callsList())
	}
	if !cleanGit.saw("git merge --no-ff -m ai/myrepo: синхрон рабочей копии с main main") {
		t.Fatalf("ожидали merge main в ai/myrepo, вызовы: %v", cleanGit.callsList())
	}

	// main == ветка проекта (или пустые значения) — синхрон не нужен.
	same := inf
	same.GitBase = "ai/myrepo"
	if srv2.syncAgentBranchWithMain(context.Background(), "myrepo", same, repo2) {
		t.Fatal("при main == ветке проекта merge выполняться не должен")
	}
}

// TestSyncAgentBranchConflictAborts — конфликтный merge откатывается: рабочая
// копия агентов не должна остаться с конфликтными маркерами (иначе следующий
// агент откроет мусор, а индекс — мусорные точки).
func TestSyncAgentBranchConflictAborts(t *testing.T) {
	fakeExec := &fakeGit{
		starts: map[string]string{
			"git rev-parse --abbrev-ref HEAD": "ai/myrepo\n",
			"git status --porcelain":          "",
		},
		fails: map[string]string{
			"git merge": "CONFLICT (content): Merge conflict in server/handler.go",
		},
	}
	srv, _, _ := newTestServerGit(t, fakeExec, nil)
	inf := workspace.Info{Name: "myrepo", GitBranch: "ai/myrepo", GitBase: "main", GitRemote: "git@g:r.git"}
	repo := git.RepoFromState(fakeExec, "/repo", inf.GitRemote, inf.GitBranch, inf.GitBase)

	if srv.syncAgentBranchWithMain(context.Background(), "myrepo", inf, repo) {
		t.Fatal("при конфликте sync должен сообщить об отказе")
	}
	if !fakeExec.saw("git merge --abort") {
		t.Fatalf("конфликтный merge не откатан: %v", fakeExec.callsList())
	}
}

// TestSyncAgentBranchNoChangeSkips — если merge ничего не меняет (main уже
// внутри ветки проекта), повторная индексация ветки не нужна: она стоит
// эмбеддингов всего проекта.
func TestSyncAgentBranchNoChangeSkips(t *testing.T) {
	fakeExec := &fakeGit{starts: map[string]string{
		"git rev-parse --abbrev-ref HEAD": "ai/myrepo\n",
		"git status --porcelain":          "",
		"git rev-parse HEAD":              "aaaa1111\n",
	}}
	srv, _, _ := newTestServerGit(t, fakeExec, nil)
	inf := workspace.Info{Name: "myrepo", GitBranch: "ai/myrepo", GitBase: "main", GitRemote: "git@g:r.git"}
	repo := git.RepoFromState(fakeExec, "/repo", inf.GitRemote, inf.GitBranch, inf.GitBase)

	if srv.syncAgentBranchWithMain(context.Background(), "myrepo", inf, repo) {
		t.Fatal("без сдвига HEAD повторная индексация не нужна")
	}
}

// TestIndexRefBackgroundSkipsFreshBranch — хук не переиндексирует ветку, если
// активные точки на этом коммите уже есть (main не менялся с прошлого прогона):
// полная индексация стоит эмбеддингов всех файлов.
func TestIndexRefBackgroundSkipsFreshBranch(t *testing.T) {
	orig := buildProjectIndexer
	defer func() { buildProjectIndexer = orig }()

	srv, _, _ := newTestServer(t)
	registerTestDir(t, srv, "proj-rag-fresh")
	sess, _, err := srv.getOrCreate("proj-rag-fresh")
	if err != nil {
		t.Fatal(err)
	}
	idx := &fakeProjectIndexer{indexed: true}
	buildProjectIndexer = func() (projectIndexerCloser, error) { return idx, nil }
	stubRefItems(t, apprag.RefSnapshot{CommitSHA: "c0ffee", Items: []rag.IndexItem{{Path: "main.go", Content: "package main"}}}, nil)

	if err := sess.IndexBackground(context.Background(), "main"); err != nil {
		t.Fatalf("IndexBackground: %v", err)
	}
	m := waitChatStatusContains(t, sess, "RAG-индекс проекта актуален", 3*time.Second)
	if !strings.Contains(m.Content, "main") {
		t.Fatalf("в отчёте нет ветки: %q", m.Content)
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if len(idx.calls) != 0 {
		t.Fatalf("IndexProject вызван при актуальном индексе: %+v", idx.calls)
	}
}

// TestRefreshRagTaskBranchHooks — старт задачи индексирует изменения ветки
// задачи относительно главной ветки (не весь worktree): остальное покрывает
// индекс main.
func TestRefreshRagTaskBranchHooks(t *testing.T) {
	orig := buildProjectIndexer
	defer func() { buildProjectIndexer = orig }()
	origDiff := loadRefDiff
	t.Cleanup(func() { loadRefDiff = origDiff })

	srv, _, _ := newTestServer(t)
	registerTestDir(t, srv, "proj-rag-task")
	sess, _, err := srv.getOrCreate("proj-rag-task")
	if err != nil {
		t.Fatal(err)
	}
	idx := &fakeProjectIndexer{}
	buildProjectIndexer = func() (projectIndexerCloser, error) { return idx, nil }
	var gotRef, gotBase string
	loadRefDiff = func(_ context.Context, _ git.Executor, _, ref, base string) (apprag.RefSnapshot, error) {
		gotRef, gotBase = ref, base
		return apprag.RefSnapshot{
			Branch:    ref,
			CommitSHA: "d0d0d0",
			Items:     []rag.IndexItem{{Path: "server/handler.go", Scope: "server", Content: "package server\n"}},
		}, nil
	}

	srv.refreshRagTaskBranch("proj-rag-task", "ai/task/T-01", "main")
	waitChatStatusContains(t, sess, "RAG-индекс проекта обновлён", 3*time.Second)
	if gotRef != "ai/task/T-01" || gotBase != "main" {
		t.Fatalf("запрошены ref=%q base=%q, want ai/task/T-01/main", gotRef, gotBase)
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if len(idx.calls) != 1 {
		t.Fatalf("IndexProject вызван %d раз(а), want 1", len(idx.calls))
	}
	if c := idx.calls[0]; c.opts.Branch != "ai/task/T-01" || c.opts.CommitSHA != "d0d0d0" || len(c.items) != 1 {
		t.Fatalf("вызов индексации: %+v", c.opts)
	}
}

// TestRefreshRagIndexHooks — хуки не бросают ошибок, если сессии/RAG нет:
// git-flow от них не зависит.
func TestRefreshRagIndexHooks(t *testing.T) {
	srv, _, _ := newTestServerGit(t, &fakeGit{}, nil)
	// Сессии нет — хуки молча выходят, операция доски продолжается.
	srv.refreshRagIndexBranch("myrepo", "main")
	srv.refreshRagIndexDir("myrepo", "", "ветка задачи ai/task/T-01")
}
