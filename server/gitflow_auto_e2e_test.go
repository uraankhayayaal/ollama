package server

// Реальный git-протокол (E2E) для авто-шагов Ф-4: синхрон релизной ветки эпика
// с main через syncEpicMainOnce с авто-резолвом тривиальных конфликтов.
// Сценарий: main и релизная ветка меняют ОДНУ строку, расходятся только в
// пробелах — merge даёт маркеры, TrivialResolve снимает их (наша версия),
// merge-коммит продвигает релизную ветку, main становится анцестором.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ai/board"
	"ai/gitops"
	"ai/workspace"
)

func TestAutoSyncEpicWithMainTrivialResolve(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	ctx := context.Background()

	// Локальный origin с базовым коммитом.
	base := t.TempDir()
	origin := filepath.Join(base, "origin")
	if out, err := exec.Command("git", "init", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	setGitUserReal(t, origin)
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("# repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origin, "f.txt"), []byte("line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", origin, "add", "-A").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", origin, "commit", "-m", "base").CombinedOutput(); err != nil {
		t.Fatalf("commit base: %v", err)
	}

	// Клон в проектную копию (как handleOpenGitProject), без remote на сервере:
	// push в sync-тесте не требуется (remote пуст).
	dest := filepath.Join(base, "myrepo")
	repo, err := gitops.Clone(ctx, gitops.CLIExecutor{}, origin, "ai/myrepo", dest)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	setGitUserReal(t, dest)

	srv, _, _ := newTestServerGit(t, gitops.CLIExecutor{}, nil)
	if _, err := srv.reg.Add(workspace.AddParams{
		Name: "myrepo", Kind: workspace.KindGit, Root: repo.Root,
		GitRemote: origin, GitBranch: repo.Branch, GitBase: repo.Base,
	}); err != nil {
		t.Fatal(err)
	}

	// Релизная ветка эпика от main.
	if err := repo.CreateBranch(ctx, "ai/epic/e1", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	// «Наша» версия на релизной ветке: пробелы в конце строки.
	if out, err := exec.Command("git", "-C", dest, "checkout", "-q", "ai/epic/e1").CombinedOutput(); err != nil {
		t.Fatalf("checkout e1: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dest, "f.txt"), []byte("line   \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dest, "add", "-A").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dest, "commit", "-m", "эпик правит строку").CombinedOutput(); err != nil {
		t.Fatalf("commit эпика: %v", err)
	}
	// «Их» версия на main: та же строка, другое пробельное оформление.
	if out, err := exec.Command("git", "-C", dest, "checkout", "-q", "main").CombinedOutput(); err != nil {
		t.Fatalf("checkout main: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dest, "f.txt"), []byte("line\t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dest, "add", "-A").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dest, "commit", "-m", "main правит строку").CombinedOutput(); err != nil {
		t.Fatalf("commit main: %v", err)
	}
	if out, err := exec.Command("git", "-C", dest, "checkout", "-q", "ai/myrepo").CombinedOutput(); err != nil {
		t.Fatalf("checkout ai/myrepo: %v\n%s", err, out)
	}

	// Реестр ветки эпика.
	if err := srv.reg.SetEpicBranch("myrepo", "epic-1", workspace.BranchRef{Branch: "ai/epic/e1", Base: "main"}); err != nil {
		t.Fatal(err)
	}

	// До синхрона main НЕ анцестор релизной ветки.
	if ok, _ := repo.MergedInto(ctx, "ai/epic/e1", "main"); ok {
		t.Fatal("precondition: main ещё не анцестор ai/epic/e1")
	}

	// Авто-синхрон (что делает EpicDoneHook, без горутины — детерминированно).
	srv.syncEpicMainOnce(ctx, "myrepo", &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Релиз"},
	})

	// main влит в релизную ветку.
	ok, err := repo.MergedInto(ctx, "ai/epic/e1", "main")
	if err != nil || !ok {
		t.Fatalf("после синхрона main должен быть анцестором e1: ok=%v err=%v", ok, err)
	}
	// Маркеров нет, осталась наша (пробельная) версия строки.
	data, rerr := exec.Command("git", "-C", dest, "show", "ai/epic/e1:f.txt").CombinedOutput()
	if rerr != nil {
		t.Fatalf("git show e1:f.txt: %v", rerr)
	}
	if strings.Contains(string(data), "<<<<<<<") || strings.Contains(string(data), "=======") {
		t.Fatalf("маркеры конфликта остались: %q", data)
	}
	if string(data) != "line   \n" {
		t.Fatalf("файл должен сохранить нашу версию, а не %q", data)
	}
	// Осиротевшие worktree убраны.
	for _, wt := range []string{".conflict-myrepo-epic-1", ".wt-main"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(dest), wt)); err == nil {
			t.Fatalf("осиротевший worktree %s не снят", wt)
		}
	}
}

// TestAutoCreateBranchesRealGit — Ф-1 E2E: создание эпика и задачи через
// hooked-хранилище (это делает чат/инструменты доски) автоматически заводит
// ветки ai/epic/… и ai/task/… в КЛОНЕ реального git.
func TestAutoCreateBranchesRealGit(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("# repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", origin, "add", "-A").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", origin, "commit", "-m", "base").CombinedOutput(); err != nil {
		t.Fatalf("commit base: %v", err)
	}

	dest := filepath.Join(base, "myrepo")
	repo, err := gitops.Clone(ctx, gitops.CLIExecutor{}, origin, "ai/myrepo", dest)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}

	srv, _, mr := newTestServerGit(t, gitops.CLIExecutor{}, nil)
	if _, err := srv.reg.Add(workspace.AddParams{
		Name: "myrepo", Kind: workspace.KindGit, Root: repo.Root,
		GitRemote: origin, GitBranch: repo.Branch, GitBase: repo.Base,
	}); err != nil {
		t.Fatal(err)
	}
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	srv.attachGitHooks("myrepo", store)

	// Эпик → ветка ai/epic/epic-1 от main; задача → ветка от ветки эпика.
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Релиз"}, Status: board.StatusNew,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "task-1", Title: "Фича"},
		EpicID:   "epic-1",
	}); err != nil {
		t.Fatal(err)
	}

	out, _ := gitBranchList(dest)
	for _, want := range []string{"ai/epic/epic-1", "ai/task/task-1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("клон не содержит авто-ветку %s:\n%s", want, out)
		}
	}
	// Текущая ветка агента не тронута.
	if head, _ := gitHead(dest); head != "ai/myrepo" {
		t.Fatalf("HEAD клона = %q, want ai/myrepo", head)
	}
	if ref, _ := srv.reg.TaskBranch("myrepo", "task-1"); ref.Branch != "ai/task/task-1" || ref.Base != "ai/epic/epic-1" {
		t.Fatalf("TaskBranch = %+v", ref)
	}
}

// TestAutoCommitAndMergeTaskRealGit — Ф-3 E2E: специалист оставил изменения в
// worktree ветки задачи → авто-шаг done коммитит их в ai/task/<id>, вливает
// ветку в релиз эпика и снимает worktree. Токен форджа в тесте не задан —
// авто-MR тихо пропускается.
func TestAutoCommitAndMergeTaskRealGit(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("# repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", origin, "add", "-A").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", origin, "commit", "-m", "base").CombinedOutput(); err != nil {
		t.Fatalf("commit base: %v", err)
	}

	dest := filepath.Join(base, "myrepo")
	repo, err := gitops.Clone(ctx, gitops.CLIExecutor{}, origin, "ai/myrepo", dest)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	setGitUserReal(t, dest)

	srv, _, _ := newTestServerGit(t, gitops.CLIExecutor{}, nil)
	if _, err := srv.reg.Add(workspace.AddParams{
		Name: "myrepo", Kind: workspace.KindGit, Root: repo.Root,
		GitRemote: origin, GitBranch: repo.Branch, GitBase: repo.Base,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateBranch(ctx, "ai/epic/e1", "main"); err != nil {
		t.Fatalf("CreateBranch эпика: %v", err)
	}
	if err := repo.CreateBranch(ctx, "ai/task/t1", "ai/epic/e1"); err != nil {
		t.Fatalf("CreateBranch задачи: %v", err)
	}
	if err := srv.reg.SetEpicBranch("myrepo", "epic-1", workspace.BranchRef{Branch: "ai/epic/e1", Base: "main"}); err != nil {
		t.Fatal(err)
	}

	// Worktree ветки задачи (создаётся сервером на in_progress) + правки
	// специалиста, оставшиеся незакоммиченными.
	worktree := filepath.Join(filepath.Dir(dest), ".wt-task-myrepo-task-1")
	if _, err := exec.Command("git", "-C", dest, "worktree", "add", worktree, "ai/task/t1").CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	if err := srv.reg.SetTaskBranch("myrepo", "task-1", workspace.BranchRef{Branch: "ai/task/t1", Base: "ai/epic/e1", Worktree: worktree}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "feature.txt"), []byte("работа специалиста\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// done-автошаг (что делает TaskDoneHook).
	srv.autoCommitAndMergeTask(ctx, "myrepo", &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "task-1", Title: "Фича"},
		EpicID:   "epic-1",
	}, nil)

	// Правки специалиста закоммичены в ветку задачи.
	out, _ := exec.Command("git", "-C", dest, "log", "--oneline", "ai/task/t1").CombinedOutput()
	if !strings.Contains(string(out), "работа специалиста (авто-коммит)") {
		t.Fatalf("авто-коммит не попал в ai/task/t1:\n%s", out)
	}
	// Ветка задачи влита в релиз эпика.
	if ok, _ := repo.MergedInto(ctx, "ai/epic/e1", "ai/task/t1"); !ok {
		t.Fatal("ai/task/t1 не влита в ai/epic/e1 после done")
	}
	if content, _ := exec.Command("git", "-C", dest, "show", "ai/epic/e1:feature.txt").CombinedOutput(); string(content) != "работа специалиста\n" {
		t.Fatalf("feature.txt в релизе = %q", content)
	}
	// Worktree снят, путь в реестре очищен.
	if _, err := os.Stat(worktree); err == nil {
		t.Fatal("worktree задачи не снят после done")
	}
	if ref, _ := srv.reg.TaskBranch("myrepo", "task-1"); ref.Worktree != "" {
		t.Fatalf("Worktree в реестре не очищен: %q", ref.Worktree)
	}
	// Авто-MR не создан: токена форджа нет (GITLAB_TOKEN/GITHUB_TOKEN пусты).
	if _, err := srv.reg.TaskMR("myrepo", "task-1"); err == nil {
		t.Fatal("без токена форджа авто-MR не должен создаваться")
	}
}