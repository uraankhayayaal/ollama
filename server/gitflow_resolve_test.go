package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai/board"
	"ai/gitops"
	"ai/projects"
	"ai/workspace"
)

// conflictMergeOutput — вывод merge-tree для конфликтующего файла f.txt.
const conflictMergeOutput = `changed in both
  base   100644 c0d0fb45c382919737f8d0c20aaf57cf89b74af8 f.txt
  our    100644 ffdfb012e4aec2a6d3c21bb473f19822c5737852 f.txt
  their  100644 542655f4d59aeaeb83b65316f9bc779f6ffb11f9 f.txt
@@ -1 +1,5 @@
+<<<<<<< .our
+=======
+>>>>>>> .their
`

// mockRebaseGit возвращает fakeGit для конфликтного rebase (Ф-4): merge-tree
// даёт f.txt, а настоящий merge в conflict worktree падает (маркеры остаются).
func mockRebaseGit() *fakeGit {
	f := &fakeGit{starts: map[string]string{
		"git merge-base main ai/epic/e1":                                          "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111\n",
		"git merge-tree aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111 main ai/epic/e1": conflictMergeOutput,
		"git ls-files -u": "100644 1 1\tf.txt\n100644 2 2\tf.txt\n100644 3 3\tf.txt\n",
	}}
	f.fails = map[string]string{
		"git merge --no-ff": "merge failed: conflicts",
	}
	return f
}

// conflictWorktreePath — путь постоянного конфликтного worktree в hermetic-
// тестах (сосед клона проекта).
func conflictWorktreePath() string {
	return filepath.Join(filepath.Dir(projects.ProjectDir("myrepo")), ".conflict-myrepo-epic-1")
}

// TestRebaseEpicConflictDryRun — POST .../rebase при конфликтах: worktree
// создан, тривиальные блоки авто-резолвлены (нет — файлов нет на диске),
// сложные уходят модели, процесс регистрируется в реестре.
func TestRebaseEpicConflictDryRun(t *testing.T) {
	git := mockRebaseGit()
	srv, handler, mr := setupGitflow(t, git)
	seedReleaseBoard(t, mr, srv)
	wtPath := conflictWorktreePath()
	t.Cleanup(func() { _ = os.RemoveAll(wtPath) })

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/rebase", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST rebase: %d, body: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Status   string   `json:"status"`
		Files    []string `json:"files"`
		Resolved []string `json:"resolved"`
		Branch   string   `json:"branch"`
		Worktree string   `json:"worktree"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("JSON = %s", rec.Body.String())
	}
	if out.Status != "resolving" || out.Branch != "ai/epic/e1" {
		t.Fatalf("ответ = %+v", out)
	}
	if strings.Join(out.Files, ",") != "f.txt" || len(out.Resolved) != 0 {
		t.Fatalf("files/resolved = %v/%v, want [f.txt]/[]", out.Files, out.Resolved)
	}
	if out.Worktree != wtPath {
		t.Fatalf("worktree = %q, want %q", out.Worktree, wtPath)
	}
	if !git.saw("git worktree add " + wtPath + " ai/epic/e1") {
		t.Fatalf("ожидали создание конфликтного worktree, вызовы: %v", git.callsList())
	}
	if !git.saw("git merge --no-ff") {
		t.Fatalf("ожидали merge main в worktree (конфликтный), вызовы: %v", git.callsList())
	}

	rs, err := srv.reg.EpicResolve("myrepo")
	if err != nil || rs.EpicID != "epic-1" || rs.Branch != "ai/epic/e1" || rs.Worktree != wtPath {
		t.Fatalf("EpicResolve = %+v, %v", rs, err)
	}
	if strings.Join(rs.Files, ",") != "f.txt" {
		t.Fatalf("rs.Files = %v, want [f.txt]", rs.Files)
	}
}

// TestRebaseEpicNoConflicts — merge-tree чистый: rebase 200 с подсказкой идти
// на /release, worktree не создаётся, процесс не регистрируется.
func TestRebaseEpicNoConflicts(t *testing.T) {
	git := &fakeGit{starts: map[string]string{
		"git merge-base main ai/epic/e1":                                          "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111\n",
		"git merge-tree aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111 main ai/epic/e1": "",
	}}
	srv, handler, mr := setupGitflow(t, git)
	seedReleaseBoard(t, mr, srv)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/rebase", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST rebase: %d, body: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Status string `json:"status"`
		Files  []any  `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "ok" || len(out.Files) != 0 {
		t.Fatalf("ответ = %+v", out)
	}
	if git.saw("git worktree add ") {
		t.Fatalf("без конфликтов worktree не создаётся, вызовы: %v", git.callsList())
	}
	if _, err := srv.reg.EpicResolve("myrepo"); err == nil {
		t.Fatal("процесс резолва не должен быть зарегистрирован")
	}
}

// TestRebaseEpicIdempotentResume — повторный rebase при активном процессе
// возвращает его состояние и не трогает worktree/реестр.
func TestRebaseEpicIdempotentResume(t *testing.T) {
	git := mockRebaseGit()
	srv, handler, mr := setupGitflow(t, git)
	seedReleaseBoard(t, mr, srv)
	wtPath := conflictWorktreePath()
	t.Cleanup(func() { _ = os.RemoveAll(wtPath) })
	if err := srv.reg.SetEpicResolve("myrepo", &workspace.EpicResolve{
		EpicID: "epic-1", Branch: "ai/epic/e1", Worktree: wtPath,
		Files: []string{"f.txt"}, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	before := len(git.callsList())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/rebase", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST rebase (resume): %d, body: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "resolving" {
		t.Fatalf("ответ = %+v", out)
	}
	if after := len(git.callsList()); after != before {
		t.Fatalf("resume не должен вызывать git: было %d, стало %d (%v)",
			before, after, git.callsList())
	}
}

// TestRebaseEpicOtherResolveBusy — другой эпик уже в резолве → 409 busy.
func TestRebaseEpicOtherResolveBusy(t *testing.T) {
	git := mockRebaseGit()
	srv, handler, mr := setupGitflow(t, git)
	seedReleaseBoard(t, mr, srv)
	if err := srv.reg.SetEpicResolve("myrepo", &workspace.EpicResolve{
		EpicID: "epic-2", Branch: "ai/epic/e2", Worktree: "/tmp/wt2",
		Files: []string{"x.txt"}, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/rebase", strings.NewReader(`{}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("чужой резолв: %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "busy" {
		t.Fatalf("ответ = %+v", out)
	}
}

// resolveAcceptanceEnv отключает реальные шаги приёмки: build/run — фиктивные
// `true`, деп/формат/анализ/ЛСП — off. Используется в hermetic-тестах resolve.
func resolveAcceptanceEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ACCEPT_BUILD_CMD", "true")
	t.Setenv("ACCEPT_RUN_CMD", "true")
	t.Setenv("ACCEPT_INSTALL_DEPS", "0")
	t.Setenv("ACCEPT_FORMAT", "0")
	t.Setenv("ACCEPT_ANALYZE", "0")
	t.Setenv("ACCEPT_LSP", "0")
}

// mockResolveGit возвращает fakeGit для happy-path resolve (Ф-4): конфликтный
// worktree чистый (f.txt резолвлен модели, застейджен), финальный
// MergeFeature main ← ai/epic/e1 без конфликтов.
func mockResolveGit() *fakeGit {
	return &fakeGit{starts: map[string]string{
		"git merge-base main ai/epic/e1": "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111\n",
		"git rev-parse ai/epic/e1":       "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222\n",
		"git merge-tree aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111 main ai/epic/e1": "",
		"git ls-files -u":        "",
		"git status --porcelain": " M f.txt\n",
		"git add --":             "",
		"git add -A":             "",
		"git commit -m":          "",
		"git push git@gitlab.com:g/myrepo.git main": "",
	}}
}

// TestResolveEpicHappyDryRun — POST .../resolve при резолвленном файле:
// приёмка проходит, резолв коммитится, main солится и пушится, процесс и
// worktree сняты.
func TestResolveEpicHappyDryRun(t *testing.T) {
	resolveAcceptanceEnv(t)
	git := mockResolveGit()
	srv, handler, mr := setupGitflow(t, git)
	seedReleaseBoard(t, mr, srv)

	wtPath := conflictWorktreePath()
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(wtPath) })
	// «Модель» разрешила конфликт: маркеров в f.txt нет.
	if err := os.WriteFile(filepath.Join(wtPath, "f.txt"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := srv.reg.SetEpicResolve("myrepo", &workspace.EpicResolve{
		EpicID: "epic-1", Branch: "ai/epic/e1", Worktree: wtPath,
		Files: []string{"f.txt"}, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/resolve", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST resolve: %d, body: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Status  string `json:"status"`
		Branch  string `json:"branch"`
		Source  string `json:"source"`
		Already bool   `json:"already_merged"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("JSON = %s", rec.Body.String())
	}
	if out.Status != "ok" || out.Branch != "main" || out.Source != "ai/epic/e1" || out.Already {
		t.Fatalf("ответ = %+v", out)
	}
	if !git.saw("git commit -m эпик epic-1: резолв конфликтов main ↔ ai/epic/e1") {
		t.Fatalf("ожидали коммит резолва, вызовы: %v", git.callsList())
	}
	if !git.saw("git push git@gitlab.com:g/myrepo.git main") {
		t.Fatalf("ожидали push main, вызовы: %v", git.callsList())
	}
	if !git.saw("git worktree remove --force " + wtPath) {
		t.Fatalf("ожидали снятие конфликтного worktree, вызовы: %v", git.callsList())
	}
	if _, err := srv.reg.EpicResolve("myrepo"); err == nil {
		t.Fatal("процесс резолва должен быть снят после завершения")
	}
	if _, err := os.Stat(wtPath); err == nil {
		t.Fatal("конфликтный worktree должен быть удалён")
	}
	if _, err := os.Stat(filepath.Join(wtPath, "f.txt")); err == nil {
		t.Fatal("f.txt не должно остаться на диске")
	}
}

// TestResolveEpicCleanFinalize — резолв вернул дерево на HEAD-содержимое
// (worktree чистый): merge всё равно завершается commit --allow-empty, чтобы
// релизная ветка получила main предком.
func TestResolveEpicCleanFinalize(t *testing.T) {
	resolveAcceptanceEnv(t)
	git := &fakeGit{starts: map[string]string{
		"git merge-base main ai/epic/e1": "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111\n",
		"git rev-parse ai/epic/e1":       "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222\n",
		"git merge-tree aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111 main ai/epic/e1": "",
		"git ls-files -u":                           "",
		"git status --porcelain":                    "",
		"git add --":                                "",
		"git commit --allow-empty":                  "",
		"git merge --no-ff":                         "",
		"git push git@gitlab.com:g/myrepo.git main": "",
	}}
	srv, handler, mr := setupGitflow(t, git)
	seedReleaseBoard(t, mr, srv)

	wtPath := conflictWorktreePath()
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(wtPath) })
	if err := os.WriteFile(filepath.Join(wtPath, "f.txt"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := srv.reg.SetEpicResolve("myrepo", &workspace.EpicResolve{
		EpicID: "epic-1", Branch: "ai/epic/e1", Worktree: wtPath,
		Files: []string{"f.txt"}, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/resolve", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST resolve: %d, body: %s", rec.Code, rec.Body.String())
	}
	if !git.saw("git commit --allow-empty") {
		t.Fatalf("ожидали commit --allow-empty для чистого резолва, вызовы: %v", git.callsList())
	}
	if git.saw("git commit -m эпик epic-1: резолв конфликтов") {
		t.Fatal("обычный git commit не должен вызываться для чистого резолва")
	}
}

// TestResolveEpicNoResolve409 — без активного процесса резолва → 409 no_resolve.
func TestResolveEpicNoResolve409(t *testing.T) {
	git := mockResolveGit()
	srv, handler, mr := setupGitflow(t, git)
	seedReleaseBoard(t, mr, srv)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/resolve", strings.NewReader(`{}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("resolve без процесса: %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "no_resolve" {
		t.Fatalf("ответ = %+v", out)
	}
}

// TestResolveEpicStillConflicts409 — в worktree остались маркеры: 409
// still_conflicts, приёмка/merge не запускаются, процесс сохранён.
func TestResolveEpicStillConflicts409(t *testing.T) {
	resolveAcceptanceEnv(t)
	git := &fakeGit{starts: map[string]string{
		"git ls-files -u": "100644 2 2\tf.txt\n100644 3 3\tf.txt\n",
	}}
	srv, handler, mr := setupGitflow(t, git)
	seedReleaseBoard(t, mr, srv)

	wtPath := conflictWorktreePath()
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(wtPath) })
	// Конфликт НЕ разрешён на диске (маркеры git) — reject до приёмки.
	if err := os.WriteFile(filepath.Join(wtPath, "f.txt"),
		[]byte("a\n<<<<<<< HEAD\nA\n=======\nB\n>>>>>>> ai/epic/e1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := srv.reg.SetEpicResolve("myrepo", &workspace.EpicResolve{
		EpicID: "epic-1", Branch: "ai/epic/e1", Worktree: wtPath,
		Files: []string{"f.txt"}, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/resolve", strings.NewReader(`{}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("resolve с маркерами: %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Status string   `json:"status"`
		Files  []string `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "still_conflicts" || strings.Join(out.Files, ",") != "f.txt" {
		t.Fatalf("ответ = %+v", out)
	}
	if git.saw("git push ") || git.saw("git commit -m") {
		t.Fatalf("при нерешённых конфликтах не должно быть commit/push, вызовы: %v", git.callsList())
	}
	if _, err := srv.reg.EpicResolve("myrepo"); err != nil {
		t.Fatal("процесс резолва должен сохраниться при still_conflicts")
	}
}

// TestResolveEpicAcceptanceFailed409 — приёмка не проходит: 409
// acceptance_failed с отчётом, merge не выполнен, процесс и worktree на месте.
func TestResolveEpicAcceptanceFailed409(t *testing.T) {
	resolveAcceptanceEnv(t)
	t.Setenv("ACCEPT_BUILD_CMD", "exit 1") // сборка падает → reject
	git := &fakeGit{starts: map[string]string{
		"git ls-files -u": "",
	}}
	srv, handler, mr := setupGitflow(t, git)
	seedReleaseBoard(t, mr, srv)

	wtPath := conflictWorktreePath()
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(wtPath) })
	if err := os.WriteFile(filepath.Join(wtPath, "f.txt"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := srv.reg.SetEpicResolve("myrepo", &workspace.EpicResolve{
		EpicID: "epic-1", Branch: "ai/epic/e1", Worktree: wtPath,
		Files: []string{"f.txt"}, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/resolve", strings.NewReader(`{}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("resolve с упавшей приёмкой: %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "acceptance_failed" || out["verdict"] != "reject" {
		t.Fatalf("ответ = %+v", out)
	}
	if git.saw("git commit -m") || git.saw("git push ") {
		t.Fatalf("при упавшей приёмке нет merge/commit/push, вызовы: %v", git.callsList())
	}
	if _, err := srv.reg.EpicResolve("myrepo"); err != nil {
		t.Fatal("процесс резолва должен сохраниться при acceptance_failed")
	}
}

// TestResolveStatusEndpoint — GET .../resolve отдаёт none без процесса и
// resolving при активном.
func TestResolveStatusEndpoint(t *testing.T) {
	git := mockResolveGit()
	srv, handler, mr := setupGitflow(t, git)
	seedReleaseBoard(t, mr, srv)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"GET", "/api/projects/myrepo/epics/epic-1/resolve", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET resolve: %d, body: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "none" {
		t.Fatalf("статус без процесса = %+v", out)
	}

	wtPath := conflictWorktreePath()
	t.Cleanup(func() { _ = os.RemoveAll(wtPath) })
	if err := srv.reg.SetEpicResolve("myrepo", &workspace.EpicResolve{
		EpicID: "epic-1", Branch: "ai/epic/e1", Worktree: wtPath,
		Files: []string{"f.txt"}, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"GET", "/api/projects/myrepo/epics/epic-1/resolve", nil))
	var resolving map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resolving); err != nil {
		t.Fatal(err)
	}
	if resolving["status"] != "resolving" || resolving["epic_id"] != "epic-1" {
		t.Fatalf("статус с процессом = %+v", resolving)
	}
}

// TestRebaseEpicRequiresDone — rebase доступен только done-эпику (как release).
func TestRebaseEpicRequiresDone(t *testing.T) {
	git := mockRebaseGit()
	srv, handler, mr := setupGitflow(t, git)
	seedGitflowBoard(t, mr, srv) // эпик в статусе new

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/rebase", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("rebase не-done эпика: %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if git.saw("git worktree add ") {
		t.Fatalf("не-done эпик не должен мутировать git, вызовы: %v", git.callsList())
	}
}

// TestRebaseEpicNonGitRejected — не-git проект: 400.
func TestRebaseEpicNonGitRejected(t *testing.T) {
	srv, handler, _ := setupGitflow(t, &fakeGit{})
	dir := t.TempDir()
	if _, err := srv.reg.Add(workspace.AddParams{Name: "plain", Kind: workspace.KindDir, Root: dir, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/plain/epics/epic-1/rebase", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("не-git проект: %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestEpicRebaseResolveEndToEndRealGit — E2E Ф-4 на реальном git: конфликт
// main ↔ релизной ветки → rebase (постоянный worktree с маркерами) → ручной
// резолв модели → resolve (приёмка + merge + push main). Проверяем: main
// продвинут и запушен в origin, конфликтный worktree снят, процесс закрыт,
// HEAD агента не тронут.
// setupRealGitEpicConflict готовит реальный git-проект (origin + клон) с
// эпиком в статусе done, у которого релизная ветка (main.go = v1) и main
// (main.go = v2) конфликтуют, и возвращает сервер, сессию чат-ассистента,
// клон и origin. Общая основа E2E Ф-4: REST-путь (release → rebase →
// resolve) и путь ассистента (мосты ResolveGitConflicts/EpicResolve).
func setupRealGitEpicConflict(t *testing.T) (*Server, http.Handler, *Session, string, string) {
	t.Helper()
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
	mainFile := filepath.Join(origin, "main.go")
	if err := os.WriteFile(filepath.Join(origin, "go.mod"), []byte("module myrepo\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mainFile, []byte("package main\n\nvar version = \"v0\"\n\nfunc main() {}\n"), 0o644); err != nil {
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
	setGitUserReal(t, dest)

	srv, handler, mr := newTestServerGit(t, gitops.CLIExecutor{}, nil)
	if _, err := srv.reg.Add(workspace.AddParams{
		Name: "myrepo", Kind: workspace.KindGit, Root: repo.Root,
		GitRemote: repo.Remote, GitBranch: repo.Branch, GitBase: repo.Base,
	}); err != nil {
		t.Fatal(err)
	}

	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	_ = mr
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Релиз"},
		Status:   board.StatusDone,
	}); err != nil {
		t.Fatal(err)
	}

	// Ветка эпика (реальный git), затем нарабатываем «v1» и пушим в origin.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/branch", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST branch: %d, body: %s", rec.Code, rec.Body.String())
	}
	if _, err := exec.Command("git", "-C", dest, "checkout", "-q", "ai/epic/epic-1").CombinedOutput(); err != nil {
		t.Fatalf("checkout ai/epic/epic-1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, "main.go"), []byte("package main\n\nvar version = \"v1\"\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dest, "add", "-A").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dest, "commit", "-m", "эпик: версия v1").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dest, "push", "-q", "origin", "ai/epic/epic-1").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dest, "checkout", "-q", "ai/myrepo").CombinedOutput(); err != nil {
		t.Fatalf("checkout ai/myrepo: %v", err)
	}

	// origin/main «v2» (без сети — напрямую в origin) + подтягиваем ref в клоне.
	if err := os.WriteFile(mainFile, []byte("package main\n\nvar version = \"v2\"\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", origin, "add", "-A").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", origin, "commit", "-m", "origin: версия v2").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dest, "fetch", "-q", "origin").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dest, "branch", "-f", "main", "origin/main").CombinedOutput(); err != nil {
		t.Fatal(err)
	}

	sess, _, err := srv.getOrCreate("myrepo")
	if err != nil {
		t.Fatal(err)
	}
	return srv, srv.routes(), sess, dest, origin
}

func TestEpicRebaseResolveEndToEndRealGit(t *testing.T) {
	srv, handler, _, dest, origin := setupRealGitEpicConflict(t)
	// «Залить в main» → конфликт (409), вход Ф-4.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/release", strings.NewReader(`{}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("release при конфликте: %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}

	// rebase: постоянный конфликтный worktree с маркерами.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/rebase", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST rebase: %d, body: %s", rec.Code, rec.Body.String())
	}
	var rout struct {
		Status string   `json:"status"`
		Files  []string `json:"files"`
		Branch string   `json:"branch"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rout); err != nil {
		t.Fatalf("JSON = %s", rec.Body.String())
	}
	if rout.Status != "resolving" || strings.Join(rout.Files, ",") != "main.go" {
		t.Fatalf("ответ rebase = %+v", rout)
	}

	wtPath := filepath.Join(filepath.Dir(dest), ".conflict-myrepo-epic-1")
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("конфликтный worktree не создан: %v", err)
	}
	wtFile := filepath.Join(wtPath, "main.go")
	data, err := os.ReadFile(wtFile)
	if err != nil {
		t.Fatalf("worktree main.go: %v", err)
	}
	if !strings.Contains(string(data), "<<<<<<< HEAD") || !strings.Contains(string(data), "v1") {
		t.Fatalf("в worktree нет маркеров конфликта:\n%s", data)
	}

	// «Модель» правит файл: берём v1 (версия эпика), маркеры убираем.
	rs, err := srv.reg.EpicResolve("myrepo")
	if err != nil || len(rs.Files) != 1 || rs.Files[0] != "main.go" {
		t.Fatalf("EpicResolve = %+v, %v", rs, err)
	}
	if err := os.WriteFile(wtFile, []byte("package main\n\nvar version = \"v1\"\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// resolve: приёмка (реальные go build/go run в worktree с go.mod), коммит
	// резолва, merge main ← релиз, push, cleanup.
	t.Setenv("ACCEPT_INSTALL_DEPS", "0") // без сети; build/run/format/vet — авто
	t.Setenv("ACCEPT_LSP", "0")          // gopls в headless-среде нестабилен
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		"POST", "/api/projects/myrepo/epics/epic-1/resolve", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST resolve: %d, body: %s", rec.Code, rec.Body.String())
	}
	var xout map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &xout); err != nil {
		t.Fatal(err)
	}
	if xout["status"] != "ok" || xout["branch"] != "main" {
		t.Fatalf("ответ resolve = %+v", xout)
	}

	// Утверждения.
	destMain, _ := exec.Command("git", "-C", dest, "rev-parse", "main").CombinedOutput()
	origMain, err := exec.Command("git", "-C", origin, "rev-parse", "refs/heads/main").CombinedOutput()
	if err != nil {
		t.Fatalf("main не запушена в origin: %v", err)
	}
	if string(strings.TrimSpace(string(destMain))) != string(strings.TrimSpace(string(origMain))) {
		t.Fatalf("origin/main != локальный main: %s vs %s", origMain, destMain)
	}
	mergedVersion, _ := exec.Command("git", "-C", dest, "show", "main:main.go").CombinedOutput()
	if !strings.Contains(string(mergedVersion), `"v1"`) {
		t.Fatalf("main не содержит резолвленную версию v1:\n%s", mergedVersion)
	}
	if _, err := os.Stat(wtPath); err == nil {
		t.Fatal("конфликтный worktree должен быть удалён после resolve")
	}
	if _, err := srv.reg.EpicResolve("myrepo"); err == nil {
		t.Fatal("процесс резолва должен быть снят после resolve")
	}
	head, _ := gitHead(dest)
	if head != "ai/myrepo" {
		t.Fatalf("HEAD клона = %q, want ai/myrepo (рабочая ветка агента не трогается)", head)
	}
	if wl, _ := exec.Command("git", "-C", dest, "worktree", "list").CombinedOutput(); strings.Contains(string(wl), ".conflict-") {
		t.Fatalf("остались конфликтные worktree:\n%s", wl)
	}
}
