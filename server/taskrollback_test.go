package server

// Тесты ручного отката задачи через REST (Ф-6, этап 3,
// PLAN-2026-10-05-todo-kanban-rollback.md): POST .../tasks/{id}/rollback.
// Прогоняют настоящий git CLI и настоящий HTTP-роутинг (не вызов хендлера
// напрямую) — важно проверить, что маршрут вообще зарегистрирован, а статус
// задачи доезжает до ответа.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai/board"
	"ai/gitops"
	"ai/projects"
	"ai/workspace"
)

// rollbackFixture: реальный git-проект (bare origin + клон в temp/), задача на
// доске, её worktree с двумя раундами коммитов.
type rollbackFixture struct {
	srv     *Server
	handler http.Handler
	store   *board.Store
	work    string
	project string
}

func newRollbackFixture(t *testing.T) *rollbackFixture {
	t.Helper()
	if _, err := os.Stat("/usr/bin/git"); err != nil {
		// git может быть не в /usr/bin — проверяем через запуск.
		ex := gitops.CLIExecutor{}
		if _, err := ex.Exec(t.Context(), "", "git", "--version"); err != nil {
			t.Skip("git недоступен")
		}
	}
	srv, handler, mr := newTestServer(t)

	// Настоящий origin с одним коммитом.
	tmp := t.TempDir()
	origin := filepath.Join(tmp, "origin.git")
	gitFixtureRun(t, tmp, "init", "--bare", "-b", "main", origin)
	seed := filepath.Join(tmp, "seed")
	gitFixtureRun(t, tmp, "clone", origin, seed)
	gitFixtureRun(t, seed, "config", "user.email", "t@example.com")
	gitFixtureRun(t, seed, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(seed, "a.go"), []byte("package app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitFixtureRun(t, seed, "add", "-A")
	gitFixtureRun(t, seed, "commit", "-m", "init")
	gitFixtureRun(t, seed, "push", "-u", "origin", "main")

	const project = "rbapp"
	root := projects.ProjectDir(project)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// worktree задачи лежит рядом с клоном (temp/.wt-task-<проект>-<задача>);
	// без явной уборки он пережил бы тест и сломал следующий ("каталог уже
	// существует" при git worktree add).
	wt := filepath.Join(filepath.Dir(root), ".wt-task-"+project+"-t1")
	t.Cleanup(func() { _ = os.RemoveAll(root); _ = os.RemoveAll(wt) })
	gitFixtureRun(t, tmp, "clone", origin, root)
	gitFixtureRun(t, root, "config", "user.email", "t@example.com")
	gitFixtureRun(t, root, "config", "user.name", "Test")
	gitFixtureRun(t, root, "checkout", "-b", "ai/"+project)
	// Фича-ветка задачи (её создаёт handleCreateTaskBranch; здесь — напрямую,
	// чтобы проверка была про откат, а не про создание ветки).
	gitFixtureRun(t, root, "branch", gitops.TaskBranchPrefix+"t1", "main")
	if _, err := srv.reg.Add(workspace.AddParams{
		Name: project, Kind: workspace.KindGit, Root: root,
		GitRemote: origin, GitBranch: "ai/" + project, GitBase: "main",
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.reg.SetTaskBranch(project, "t1", workspace.BranchRef{
		Branch: gitops.TaskBranchPrefix + "t1", Base: "ai/epic/e1",
	}); err != nil {
		t.Fatal(err)
	}

	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: project})
	ctx := t.Context()
	if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{TaskID: "e1", Title: "epic"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "t1", Title: "задача", SequenceOrder: 1},
		EpicID:   "e1",
		Status:   board.StatusReady,
	}); err != nil {
		t.Fatal(err)
	}
	// Старт задачи: хук создаёт worktree и снимает base_sha.
	f := &rollbackFixture{srv: srv, handler: handler, store: store, project: project}
	f.do(t, "PUT", "/api/projects/"+project+"/tasks/t1", `{"status":"in_progress"}`, http.StatusOK)
	ref, err := srv.reg.TaskBranch(project, "t1")
	if err != nil || strings.TrimSpace(ref.Worktree) == "" {
		t.Fatalf("worktree задачи не создан: %+v err=%v", ref, err)
	}
	f.work = ref.Worktree
	return f
}

// do выполняет HTTP-запрос к роутеру сервера и проверяет код ответа.
func (f *rollbackFixture) do(t *testing.T, method, path, body string, want int) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body == "" {
		rdr = bytes.NewReader(nil)
	} else {
		rdr = bytes.NewReader([]byte(body))
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(method, path, rdr))
	if rec.Code != want {
		t.Fatalf("%s %s: код %d, ожидался %d: %s", method, path, rec.Code, want, rec.Body.String())
	}
	return rec
}

func (f *rollbackFixture) commitRound(t *testing.T, n int) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.work, "a.go"),
		[]byte(fmt.Sprintf("package app\n\n// раунд %d\n", n)), 0o644); err != nil {
		t.Fatal(err)
	}
	gitFixtureRun(t, f.work, "add", "-A")
	gitFixtureRun(t, f.work, "commit", "-m", fmt.Sprintf("round %d", n))
	return gitFixtureRun(t, f.work, "rev-parse", "HEAD")
}

func (f *rollbackFixture) task(t *testing.T) *board.Task {
	t.Helper()
	task, err := f.store.GetTask(t.Context(), "t1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	return task
}

func (f *rollbackFixture) setLastGood(t *testing.T, sha string) {
	t.Helper()
	if err := f.store.PatchTask(t.Context(), "t1", func(task *board.Task) error {
		if task.Checkpoint == nil {
			task.Checkpoint = &board.TaskCheckpoint{}
		}
		task.Checkpoint.LastGoodSHA = sha
		task.LastError = "старое падение"
		task.AgentState = board.AgentStateFixingErrors
		return nil
	}); err != nil {
		t.Fatalf("PatchTask: %v", err)
	}
}

// TestRollbackTaskToLastGood — основной сценарий: откат к последнему
// зелёному раунду возвращает код, снимает ошибку проверки отменённого кода и
// возвращает задачу в очередь на новый прогон.
func TestRollbackTaskToLastGood(t *testing.T) {
	f := newRollbackFixture(t)
	sha1 := f.commitRound(t, 1)
	sha2 := f.commitRound(t, 2)
	f.setLastGood(t, sha1)
	base := f.task(t).Checkpoint.BaseSHA
	if base == "" {
		t.Fatal("base_sha не записан при старте задачи")
	}

	rec := f.do(t, "POST", "/api/projects/"+f.project+"/tasks/t1/rollback",
		`{"to":"last_good"}`, http.StatusOK)
	var resp rollbackResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("ответ: %v (%s)", err, rec.Body.String())
	}
	if !resp.OK || resp.SHA != sha1 || resp.Status != string(board.StatusReady) {
		t.Fatalf("ответ: %+v", resp)
	}
	if got := gitFixtureRun(t, f.work, "rev-parse", "HEAD"); got != sha1 {
		t.Fatalf("HEAD=%s, ожидался %s (раунд 2 = %s)", got, sha1, sha2)
	}
	body, err := os.ReadFile(filepath.Join(f.work, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "раунд 1") || strings.Contains(string(body), "раунд 2") {
		t.Fatalf("файл не откатан: %q", body)
	}
	task := f.task(t)
	if task.Status != board.StatusReady {
		t.Fatalf("статус задачи: %s, ожидался ready", task.Status)
	}
	if task.LastError != "" || task.AgentState != "" {
		t.Fatalf("ошибка/состояние отменённого кода не сняты: %q / %q", task.LastError, task.AgentState)
	}
	if task.Checkpoint.LastSHA != sha1 || task.Checkpoint.LastGoodSHA != sha1 {
		t.Fatalf("чекпойнт: %+v", task.Checkpoint)
	}
}

// TestRollbackTaskToBase — «снести всё»: код возвращается к состоянию на
// момент старта задачи, дальнейшая работа продолжится с него.
func TestRollbackTaskToBase(t *testing.T) {
	f := newRollbackFixture(t)
	base := f.task(t).Checkpoint.BaseSHA
	f.commitRound(t, 1)
	f.commitRound(t, 2)
	f.setLastGood(t, gitFixtureRun(t, f.work, "rev-parse", "HEAD"))

	rec := f.do(t, "POST", "/api/projects/"+f.project+"/tasks/t1/rollback",
		`{"to":"base"}`, http.StatusOK)
	var resp rollbackResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.SHA != base {
		t.Fatalf("sha=%s, ожидалась база %s", resp.SHA, base)
	}
	if got := gitFixtureRun(t, f.work, "rev-parse", "HEAD"); got != base {
		t.Fatalf("HEAD=%s, ожидалась база %s", got, base)
	}
	if body, _ := os.ReadFile(filepath.Join(f.work, "a.go")); strings.Contains(string(body), "раунд") {
		t.Fatalf("правки не снесены: %q", body)
	}
}

// TestRollbackTaskToCommitSHA — произвольный откат к конкретному коммиту
// (ревизия из MR, «вернуть вот этот кусок»).
func TestRollbackTaskToCommitSHA(t *testing.T) {
	f := newRollbackFixture(t)
	sha1 := f.commitRound(t, 1)
	f.commitRound(t, 2)
	rec := f.do(t, "POST", "/api/projects/"+f.project+"/tasks/t1/rollback",
		fmt.Sprintf(`{"to":%q}`, sha1), http.StatusOK)
	var resp rollbackResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.SHA != sha1 {
		t.Fatalf("sha=%s, ожидался %s", resp.SHA, sha1)
	}
	if got := gitFixtureRun(t, f.work, "rev-parse", "HEAD"); got != sha1 {
		t.Fatalf("HEAD=%s", got)
	}
}

// TestRollbackTaskRefusals — отказы: неизвестная точка, неизвестный SHA,
// несуществующая задача, кривое тело, «снести всё» без записанной базы.
// Ни один из них не должен двигать код.
func TestRollbackTaskRefusals(t *testing.T) {
	f := newRollbackFixture(t)
	f.commitRound(t, 1)
	head := gitFixtureRun(t, f.work, "rev-parse", "HEAD")

	cases := []struct {
		name, path, body string
		want             int
	}{
		{"пустая цель", "/api/projects/" + f.project + "/tasks/t1/rollback", `{}`, http.StatusBadRequest},
		{"битое тело", "/api/projects/" + f.project + "/tasks/t1/rollback", `{`, http.StatusBadRequest},
		{"нет last_good", "/api/projects/" + f.project + "/tasks/t1/rollback", `{"to":"last_good"}`, http.StatusBadRequest},
		{"нет задачи", "/api/projects/" + f.project + "/tasks/nope/rollback", `{"to":"base"}`, http.StatusNotFound},
		// Несуществующий коммит — ошибка запроса (400), а не сбой git (502):
		// человек указал цель, которой нет.
		{"неизвестный sha", "/api/projects/" + f.project + "/tasks/t1/rollback",
			`{"to":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}`, http.StatusBadRequest},
		{"пробелы в цели", "/api/projects/" + f.project + "/tasks/t1/rollback", `{"to":"HEAD HEAD"}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := f.do(t, "POST", c.path, c.body, c.want)
			if strings.Contains(rec.Body.String(), "\"ok\":true") {
				t.Fatalf("ответ утверждает успех: %s", rec.Body.String())
			}
		})
	}
	if got := gitFixtureRun(t, f.work, "rev-parse", "HEAD"); got != head {
		t.Fatalf("отказы изменили код: HEAD=%s, ожидался %s", got, head)
	}
	if f.task(t).Status != board.StatusInProgress {
		t.Fatalf("отказы должны были оставить задачу в работе: %s", f.task(t).Status)
	}
}

// TestRollbackTaskWithoutWorktree — у задачи без worktree откатывать нечего:
// понятный отказ вместо падения в git.
func TestRollbackTaskWithoutWorktree(t *testing.T) {
	srv, handler, mr := newTestServer(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "nowt"})
	ctx := t.Context()
	if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{TaskID: "e1", Title: "epic"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "t1", Title: "задача", SequenceOrder: 1},
		EpicID:   "e1", Status: board.StatusInProgress,
	}); err != nil {
		t.Fatal(err)
	}
	f := &rollbackFixture{srv: srv, handler: handler, store: store, project: "nowt"}
	rec := f.do(t, "POST", "/api/projects/nowt/tasks/t1/rollback", `{"to":"base"}`, http.StatusNotFound)
	if !strings.Contains(rec.Body.String(), "worktree") {
		t.Fatalf("ответ без упоминания worktree: %s", rec.Body.String())
	}
}
