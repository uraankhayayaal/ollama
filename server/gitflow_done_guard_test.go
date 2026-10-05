package server

// Тесты гарда «phantom done» (Ф-6, инцидент mytrip/FEL-04): задача без
// результата не должна получать терминальный статус done. Настоящий git:
// доказательство работы — свои коммиты ветки задачи либо незакоммиченные
// правки в её worktree.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"os/exec"
	"sync"

	"ai/board"
	"ai/gitops"
	"ai/workspace"
	"github.com/alicebob/miniredis/v2"
)

// doneGuardFixture — минимальный git-проект для гарда: bare-origin, клон,
// ветки эпика и задачи, зарегистрированные в реестре, и worktree задачи.
// Без сабмодулей: сценарий гарда — «задача не сделала ничего».
type doneGuardFixture struct {
	reg      *workspace.Registry
	srv      *Server
	store    *board.Store
	task     *board.Task
	wt       string // worktree задачи
	origin   string
	root     string
	taskRef  string
	epicRef  string
	projectN string
}

func newDoneGuardFixture(t *testing.T) *doneGuardFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	ctx := context.Background()
	base := t.TempDir()
	origin := filepath.Join(base, "origin.git")
	gitFixtureRun(t, base, "init", "--bare", "-b", "main", origin)
	src := filepath.Join(base, "src")
	gitFixtureRun(t, base, "clone", origin, src)
	gitFixtureRun(t, src, "config", "user.email", "t@example.com")
	gitFixtureRun(t, src, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitFixtureRun(t, src, "add", "-A")
	gitFixtureRun(t, src, "commit", "-m", "init")
	gitFixtureRun(t, src, "push", "-u", "origin", "main")

	root := filepath.Join(base, "clone")
	if _, err := gitops.Clone(ctx, gitops.CLIExecutor{}, origin, "ai/app", root); err != nil {
		t.Fatal(err)
	}
	setGitUserReal(t, root)
	for _, ref := range []string{"ai/epic/e1", "ai/task/t1"} {
		gitFixtureRun(t, root, "branch", ref, "main")
	}

	reg, err := workspace.Open(filepath.Join(base, "workspaces.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Add(workspace.AddParams{Name: "app", Kind: workspace.KindGit, Root: root,
		GitRemote: origin, GitBranch: "ai/app", GitBase: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetEpicBranch("app", "e1", workspace.BranchRef{Branch: "ai/epic/e1", Base: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetTaskBranch("app", "t1", workspace.BranchRef{Branch: "ai/task/t1", Base: "ai/epic/e1"}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{reg: reg, gitExec: gitops.CLIExecutor{}, mergeLocks: map[string]*sync.Mutex{}, sessions: map[string]*Session{}}
	mr := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "app"})
	if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{TaskID: "e1", Title: "эпик"}}); err != nil {
		t.Fatal(err)
	}
	task := &board.Task{TaskSpec: board.TaskSpec{TaskID: "t1", Title: "задача", SequenceOrder: 1}, EpicID: "e1", Status: board.StatusInProgress}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	srv.attachGitHooks("app", store)
	// worktree задачи создаём вручную (как taskWorktree), чтобы состояние
	// дерева в тесте полностью контролировалось.
	wt := srv.taskWorktreePath(root, "app", "t1")
	gitFixtureRun(t, root, "worktree", "add", "-B", "ai/task/t1", wt, "ai/task/t1")
	ref, err := reg.TaskBranch("app", "t1")
	if err != nil {
		t.Fatal(err)
	}
	ref.Worktree = wt
	if err := reg.SetTaskBranch("app", "t1", ref); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".gitignore"), []byte(taskGitignore()), 0o644); err != nil {
		t.Fatal(err)
	}
	return &doneGuardFixture{reg: reg, srv: srv, store: store, task: task, wt: wt, origin: origin, root: root, taskRef: "ai/task/t1", epicRef: "ai/epic/e1", projectN: "app"}
}

// TestGuardTaskDoneBlocksEmptyTask — сценарий FEL-04: ветка задачи совпадает с
// базой эпика (ни одного своего коммита), worktree чистый (кроме служебного
// .gitignore от оркестратора) → done запрещён.
func TestGuardTaskDoneBlocksEmptyTask(t *testing.T) {
	f := newDoneGuardFixture(t)
	ctx := context.Background()
	if out := gitFixtureRun(t, f.wt, "status", "--porcelain"); !strings.Contains(out, ".gitignore") {
		t.Fatalf("ожидался только служебный .gitignore, получено: %q", out)
	}
	err := f.srv.guardTaskDone(ctx, "app", f.task)
	if err == nil {
		t.Fatal("задача без коммитов и правок не должна закрываться в done")
	}
	msg := err.Error()
	for _, want := range []string{"t1", "ai/task/t1", "ai/epic/e1", "KANBAN_DONE_REQUIRES_COMMIT=0"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("в ошибке нет %q: %s", want, msg)
		}
	}
	// Через магазин переход тоже не проходит, и статус остаётся прежним.
	if err := f.store.SetTaskStatus(ctx, "t1", board.StatusDone); err == nil {
		t.Fatal("SetTaskStatus в done должен вернуть ошибку гарда")
	}
	got, err := f.store.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != board.StatusInProgress {
		t.Fatalf("статус после отказа = %q, ожидался in_progress", got.Status)
	}
}

// TestGuardTaskDoneAllowsCommit — задача с собственным коммитом закрывается.
func TestGuardTaskDoneAllowsCommit(t *testing.T) {
	f := newDoneGuardFixture(t)
	if err := os.WriteFile(filepath.Join(f.wt, "feature.go"), []byte("package app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitFixtureRun(t, f.wt, "add", "-A")
	gitFixtureRun(t, f.wt, "commit", "-m", "wip: раунд 1")
	if err := f.srv.guardTaskDone(context.Background(), "app", f.task); err != nil {
		t.Fatalf("задача со своим коммитом должна закрываться: %v", err)
	}
}

// TestGuardTaskDoneAllowsDirtyWorktree — незакоммиченные правки тоже считаются
// работой: авто-коммит при done их зафиксирует (агент мог не успеть отдать
// раунд с коммитом).
func TestGuardTaskDoneAllowsDirtyWorktree(t *testing.T) {
	f := newDoneGuardFixture(t)
	if err := os.WriteFile(filepath.Join(f.wt, "feature.go"), []byte("package app // wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.srv.guardTaskDone(context.Background(), "app", f.task); err != nil {
		t.Fatalf("несохранённые правки должны считаться работой: %v", err)
	}
}

// TestGuardTaskDoneIgnoresOrchestratorGitignore — служебный .gitignore,
// который пишет сам оркестратор, доказательством работы не считается
// (иначе гард был бы слепым).
func TestGuardTaskDoneIgnoresOrchestratorGitignore(t *testing.T) {
	f := newDoneGuardFixture(t) // без изменений агента
	if err := f.srv.guardTaskDone(context.Background(), "app", f.task); err == nil {
		t.Fatal("только служебный .gitignore не должен открывать дорогу в done")
	}
}

// TestGuardTaskDoneDisabledByEnv — доска, где задачи закрываются без кода
// (исследования, решения), отключает гард переменной.
func TestGuardTaskDoneDisabledByEnv(t *testing.T) {
	t.Setenv("KANBAN_DONE_REQUIRES_COMMIT", "0")
	f := newDoneGuardFixture(t)
	if err := f.srv.guardTaskDone(context.Background(), "app", f.task); err != nil {
		t.Fatalf("KANBAN_DONE_REQUIRES_COMMIT=0 должен отключать гард: %v", err)
	}
}

// TestGuardTaskDoneIgnoresNonGit — у не-git проекта (kind=dir) гард молчит:
// там нет ни веток, ни worktree.
func TestGuardTaskDoneIgnoresNonGit(t *testing.T) {
	f := newDoneGuardFixture(t)
	if _, err := f.reg.Add(workspace.AddParams{Name: "plain", Kind: workspace.KindDir, Root: t.TempDir(), Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.srv.guardTaskDone(context.Background(), "plain", &board.Task{TaskSpec: board.TaskSpec{TaskID: "t1"}}); err != nil {
		t.Fatalf("для не-git проекта гард не применяется: %v", err)
	}
}

// TestEnsureTaskGitignoreAppendsMissingPatterns — инцидент FEL-05: у mytrip
// .gitignore свой (frontend/node_modules/, pgdata/…), поэтому корневого
// `node_modules/` в нём не было, и `git add -A` на раунде забрал распакованный
// тулчейн (4287 файлов) в коммит задачи. Оркестратор дописывает недостающие
// паттерны, ничего не удаляя и не переставляя.
func TestEnsureTaskGitignoreAppendsMissingPatterns(t *testing.T) {
	dir := t.TempDir()
	own := "# проект\nfrontend/node_modules/\npgdata/\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	ensureTaskGitignore("app", "t1", dir)

	body, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.HasPrefix(got, own) {
		t.Fatalf("свои паттерны должны остаться в начале файла:\n%s", got)
	}
	for _, want := range []string{"node_modules/", "node-v*/", "*.tsbuildinfo", taskGitignoreHeader} {
		if !strings.Contains(got, want) {
			t.Fatalf("в .gitignore нет %q:\n%s", want, got)
		}
	}
	// vendor/ для Go коммитят осознанно — добавлять его в игнор нельзя.
	if strings.Contains(got, "vendor/") {
		t.Fatalf("vendor/ в списке не должен быть:\n%s", got)
	}
	// Повторный вызов ничего не меняет (идемпотентность).
	ensureTaskGitignore("app", "t1", dir)
	again, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != got {
		t.Fatalf("повторный вызов изменил .gitignore:\n%s\n---\n%s", got, again)
	}
}

// TestEnsureTaskGitignoreCreatesFileWhenMissing — проект без .gitignore:
// создаём полный список один раз (как раньше).
func TestEnsureTaskGitignoreCreatesFileWhenMissing(t *testing.T) {
	dir := t.TempDir()
	ensureTaskGitignore("app", "t1", dir)
	body, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf(".gitignore должен быть создан: %v", err)
	}
	if !strings.Contains(string(body), "node_modules/") || !strings.Contains(string(body), taskGitignoreHeader) {
		t.Fatalf("созданный .gitignore беднее ожидаемого:\n%s", body)
	}
}
