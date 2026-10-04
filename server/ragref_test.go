// Тесты индексации RAG по содержимому ветки (server/ragref.go): сбор файлов из
// git-объекта, индекс main после релиза эпика и синхрон рабочей копии агентов.
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ai/gitops"
	"ai/rag"
	"ai/workspace"
)

// refGit — hermetic-исполнитель git для сбора ветки: ls-tree отдаёт готовый
// вывод, show/rev-parse — заранее заданные значения по точному аргументу.
type refGit struct {
	mu      sync.Mutex
	tree    string
	diff    string            // вывод `git diff --name-only -z base...ref`
	commits map[string]string // ref → sha
	sizes   map[string]int64  // "ref:path" → размер blob-а
	shows   map[string]string // "ref:path" → содержимое
	failLS  string
}

func (g *refGit) Exec(_ context.Context, _ string, argv ...string) (string, error) {
	cmd := strings.Join(argv, " ")
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case strings.HasPrefix(cmd, "git ls-tree"):
		if g.failLS != "" {
			return "", errors.New(g.failLS)
		}
		return g.tree, nil
	case strings.HasPrefix(cmd, "git diff"):
		return g.diff, nil
	case strings.HasPrefix(cmd, "git cat-file"):
		size, ok := g.sizes[argv[len(argv)-1]]
		if !ok {
			return "", fmt.Errorf("нет такого пути в ветке: %s", argv[len(argv)-1])
		}
		return strconv.FormatInt(size, 10), nil
	case strings.HasPrefix(cmd, "git rev-parse"):
		return g.commits[argv[len(argv)-1]], nil
	case strings.HasPrefix(cmd, "git show"):
		out, ok := g.shows[argv[len(argv)-1]]
		if !ok {
			return "", fmt.Errorf("нет такого пути в ветке: %s", argv[len(argv)-1])
		}
		return out, nil
	}
	return "", nil
}

// lsTreeEntry собирает строку `git ls-tree --long` для blob-а.
func lsTreeEntry(mode, sha string, size int, path string) string {
	return fmt.Sprintf("%s blob %s %7d\t%s\x00", mode, sha, size, path)
}

func TestCollectRefItemsParsesAndFilters(t *testing.T) {
	binary := "PK\x03\x04\x00bin"
	tree := strings.Join([]string{
		lsTreeEntry("100644", "a1", 12, "main.go"),
		lsTreeEntry("100644", "a2", 20, "server/handler.go"),
		// Скрытый файл и служебные каталоги — как и при обходе рабочей копии.
		lsTreeEntry("100644", "a3", 5, ".env"),
		lsTreeEntry("100644", "a4", 9, ".github/workflows/ci.yml"),
		lsTreeEntry("100644", "a5", 9, "node_modules/pkg/index.js"),
		lsTreeEntry("100644", "a6", 9, "web/dist/bundle.js"),
		// Симлинк и поддерево не индексируем.
		lsTreeEntry("120000", "a7", 4, "link.txt"),
		"040000 tree a8       -\tdir\x00",
		// Пустой и крупный файл — заранее отсекаются по размеру из ls-tree.
		lsTreeEntry("100644", "a9", 0, "empty.txt"),
		lsTreeEntry("100644", "b0", maxRefFileBytes+1, "vendor/big.js"),
	}, "")

	git := &refGit{
		tree: tree,
		commits: map[string]string{
			"main": "c0ffee1234567890abcdef1234567890abcdef12\n",
		},
		shows: map[string]string{
			"main:main.go":             "package main\n",
			"main:server/handler.go":   "package server\n",
			"main:.env":                "TOKEN=1\n",
			"main:.github/workflows/c": "",
			"main:link.txt":            "main.go",
		},
	}

	snap, err := collectRefItems(context.Background(), git, "/repo", "main")
	if err != nil {
		t.Fatalf("collectRefItems: %v", err)
	}
	if snap.Branch != "main" {
		t.Fatalf("ветка снапшота: %q, want main", snap.Branch)
	}
	if snap.CommitSHA != "c0ffee1234567890abcdef1234567890abcdef12" {
		t.Fatalf("коммит снапшота: %q", snap.CommitSHA)
	}
	if len(snap.Items) != 2 {
		t.Fatalf("файлов в снапшоте %d (%+v), want 2: main.go и server/handler.go", len(snap.Items), snap.Items)
	}
	if snap.Items[0].Path != "main.go" || snap.Items[0].Scope != "root" {
		t.Fatalf("первый файл: %+v", snap.Items[0])
	}
	if snap.Items[1].Path != "server/handler.go" || snap.Items[1].Scope != "server" {
		t.Fatalf("второй файл: %+v", snap.Items[1])
	}
	for _, it := range snap.Items {
		if strings.ContainsRune(it.Content, 0) {
			t.Fatalf("бинарное содержимое попало в индекс: %s", it.Path)
		}
	}
	_ = binary // бинарный отсев проверяется через isBinary-предикат отдельно
}

func TestCollectRefItemsBinaryAndErrors(t *testing.T) {
	git := &refGit{
		tree: strings.Join([]string{
			lsTreeEntry("100644", "a1", 8, "logo.png"),
			lsTreeEntry("100644", "a2", 6, "readme.md"),
		}, ""),
		shows: map[string]string{
			"main:logo.png":  "PK\x03\x04\x00\x01",
			"main:readme.md": "# проект\n",
		},
	}
	snap, err := collectRefItems(context.Background(), git, "/repo", "main")
	if err != nil {
		t.Fatalf("collectRefItems: %v", err)
	}
	if len(snap.Items) != 1 || snap.Items[0].Path != "readme.md" {
		t.Fatalf("бинарный файл не отсеян: %+v", snap.Items)
	}

	failing := &refGit{failLS: "unknown revision"}
	if _, err := collectRefItems(context.Background(), failing, "/repo", "main"); err == nil {
		t.Fatal("ошибка git ls-tree должна пробрасываться (нет молчаливого пустого индекса)")
	}
	if _, err := collectRefItems(context.Background(), nil, "/repo", "main"); err == nil {
		t.Fatal("без git-исполнителя — ошибка")
	}
	if _, err := collectRefItems(context.Background(), git, "", "main"); err == nil {
		t.Fatal("без каталога — ошибка")
	}
}

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
	stubRefItems(t, refSnapshot{
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
	repo := gitops.RepoFromState(dirtyGit, "/repo", inf.GitRemote, inf.GitBranch, inf.GitBase)

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
	repo2 := gitops.RepoFromState(cleanGit, "/repo", inf.GitRemote, inf.GitBranch, inf.GitBase)
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
	git := &fakeGit{
		starts: map[string]string{
			"git rev-parse --abbrev-ref HEAD": "ai/myrepo\n",
			"git status --porcelain":          "",
		},
		fails: map[string]string{
			"git merge": "CONFLICT (content): Merge conflict in server/handler.go",
		},
	}
	srv, _, _ := newTestServerGit(t, git, nil)
	inf := workspace.Info{Name: "myrepo", GitBranch: "ai/myrepo", GitBase: "main", GitRemote: "git@g:r.git"}
	repo := gitops.RepoFromState(git, "/repo", inf.GitRemote, inf.GitBranch, inf.GitBase)

	if srv.syncAgentBranchWithMain(context.Background(), "myrepo", inf, repo) {
		t.Fatal("при конфликте sync должен сообщить об отказе")
	}
	if !git.saw("git merge --abort") {
		t.Fatalf("конфликтный merge не откатан: %v", git.callsList())
	}
}

// TestSyncAgentBranchNoChangeSkips — если merge ничего не меняет (main уже
// внутри ветки проекта), повторная индексация ветки не нужна: она стоит
// эмбеддингов всего проекта.
func TestSyncAgentBranchNoChangeSkips(t *testing.T) {
	git := &fakeGit{starts: map[string]string{
		"git rev-parse --abbrev-ref HEAD": "ai/myrepo\n",
		"git status --porcelain":          "",
		"git rev-parse HEAD":              "aaaa1111\n",
	}}
	srv, _, _ := newTestServerGit(t, git, nil)
	inf := workspace.Info{Name: "myrepo", GitBranch: "ai/myrepo", GitBase: "main", GitRemote: "git@g:r.git"}
	repo := gitops.RepoFromState(git, "/repo", inf.GitRemote, inf.GitBranch, inf.GitBase)

	if srv.syncAgentBranchWithMain(context.Background(), "myrepo", inf, repo) {
		t.Fatal("без сдвига HEAD повторная индексация не нужна")
	}
}

// TestCollectRefDiffOnlyChanged — индексация ветки задачи берёт только
// изменения относительно базы (`git diff base...ref`): остальное покрывает
// индекс main. Удалённые файлы и мусорные пути отсеиваются.
func TestCollectRefDiffOnlyChanged(t *testing.T) {
	git := &refGit{
		diff:    "server/handler.go\x00README.md\x00node_modules/pkg/i.js\x00old/gone.go\x00",
		commits: map[string]string{"ai/task/T-01": "b0b0b0b0\n"},
		sizes:   map[string]int64{"ai/task/T-01:server/handler.go": 20, "ai/task/T-01:README.md": 8, "ai/task/T-01:old/gone.go": 4},
		shows: map[string]string{
			"ai/task/T-01:server/handler.go": "package server\n",
			"ai/task/T-01:README.md":         "# проект\n",
		},
	}
	snap, err := collectRefDiff(context.Background(), git, "/repo", "ai/task/T-01", "main")
	if err != nil {
		t.Fatalf("collectRefDiff: %v", err)
	}
	if snap.Branch != "ai/task/T-01" || snap.CommitSHA != "b0b0b0b0" {
		t.Fatalf("снапшот: %+v", snap)
	}
	got := make([]string, 0, len(snap.Items))
	for _, it := range snap.Items {
		got = append(got, it.Path)
	}
	want := []string{"server/handler.go", "README.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("файлы изменений = %v, want %v", got, want)
	}

	if _, err := collectRefDiff(context.Background(), git, "/repo", "", "main"); err == nil {
		t.Fatal("без ветки — ошибка")
	}
	if _, err := collectRefDiff(context.Background(), git, "/repo", "ai/task/T-01", ""); err == nil {
		t.Fatal("без базовой ветки — ошибка")
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
	stubRefItems(t, refSnapshot{CommitSHA: "c0ffee", Items: []rag.IndexItem{{Path: "main.go", Content: "package main"}}}, nil)

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
	loadRefDiff = func(_ context.Context, _ gitops.Executor, _, ref, base string) (refSnapshot, error) {
		gotRef, gotBase = ref, base
		return refSnapshot{
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
