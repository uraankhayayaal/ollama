package server

// Тесты worktree ветки эпика (Этап 4.1 плана harness-rework): ленивое создание
// рабочей точки системного архитектора, инвариант «ветка занята — git откажет»,
// автокоммит + снятие worktree перед merge-операциями и degrade для
// не-git-проекта / отсутствующей ветки. Настоящий git CLI.

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

// setupEpicWorktreeRepo — общий каркас: origin с базовым коммитом → клон
// ai/myrepo, реестр проекта и ветки эпика ai/epic/e1 (то, что делают
// handleOpenGitProject и EpicCreatedHook).
func setupEpicWorktreeRepo(t *testing.T) (context.Context, *Server, *gitops.Repo) {
	t.Helper()
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
	if err := repo.CreateBranch(ctx, "ai/epic/e1", repo.Base); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := srv.reg.SetEpicBranch("myrepo", "e1", workspace.BranchRef{
		Branch: "ai/epic/e1", Base: repo.Base,
	}); err != nil {
		t.Fatal(err)
	}
	return ctx, srv, repo
}

// gitOutput — компактный запуск git в каталоге для проверок в тестах.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	argv := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", argv...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestEpicWorktreeLazyCreateAndDrop — 4.1: резолвер создаёт worktree ветки
// эпика, реестр помнит его, повторный вызов идемпотентен; снятие перед
// операцией, которой нужен свободный checkout, фиксирует незакоммиченные
// правки архитектора автокоммитом и освобождает ветку.
func TestEpicWorktreeLazyCreateAndDrop(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	ctx, srv, repo := setupEpicWorktreeRepo(t)
	project := "myrepo"

	wtPath := srv.epicWorktree(ctx, project, "e1")
	if wtPath == "" {
		t.Fatal("epicWorktree: пустой путь — ожидался worktree")
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("каталог worktree: %v", err)
	}
	if got := gitOutput(t, wtPath, "branch", "--show-current"); got != "ai/epic/e1" {
		t.Fatalf("worktree должен стоять на ai/epic/e1, а не на %q", got)
	}
	ref, err := srv.reg.EpicBranch(project, "e1")
	if err != nil || ref.Worktree != wtPath {
		t.Fatalf("реестр: worktree=%q err=%v (ожидался %q)", ref.Worktree, err, wtPath)
	}
	if again := srv.epicWorktree(ctx, project, "e1"); again != wtPath {
		t.Fatalf("повторный вызов: %q != %q", again, wtPath)
	}

	// Правка архитектора без коммита (модель не успела / не стала).
	if err := os.WriteFile(filepath.Join(wtPath, "README.md"),
		[]byte("# repo\n\nструктура проекта\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Инвариант merge-путей: пока worktree жив, чужой checkout ветки ОБЯЗАН
	// падать — именно поэтому merge-операции снимают worktree заранее.
	probe := filepath.Join(filepath.Dir(repo.Root), ".probe-e1")
	if probeWT, err := repo.AddWorktree(ctx, probe, "ai/epic/e1"); err == nil {
		_ = probeWT.RemoveWorktree(ctx, probe)
		t.Fatal("git worktree add на занятую ветку должен был отказать, но прошёл")
	}
	_ = os.RemoveAll(probe)

	lock := srv.mergeLock(project)
	lock.Lock()
	srv.dropEpicWorktreeLocked(ctx, project, "e1")
	lock.Unlock()

	if _, err := os.Stat(wtPath); err == nil {
		t.Fatalf("каталог worktree не снят: %s", wtPath)
	}
	ref, err = srv.reg.EpicBranch(project, "e1")
	if err != nil || ref.Worktree != "" {
		t.Fatalf("реестр после снятия: worktree=%q err=%v", ref.Worktree, err)
	}
	// Правка не потеряна: автокоммит ушёл в ветку эпика.
	data, err := exec.Command("git", "-C", repo.Root, "show", "ai/epic/e1:README.md").CombinedOutput()
	if err != nil {
		t.Fatalf("git show ai/epic/e1:README.md: %v\n%s", err, data)
	}
	if !strings.Contains(string(data), "структура проекта") {
		t.Fatalf("автокоммит не сохранил правку архитектора: %q", data)
	}
	// Ветка снова свободна.
	wt, err := repo.AddWorktree(ctx, probe, "ai/epic/e1")
	if err != nil {
		t.Fatalf("после снятия worktree ветка должна быть свободна: %v", err)
	}
	if err := wt.RemoveWorktree(ctx, probe); err != nil {
		t.Fatalf("снятие контрольного worktree: %v", err)
	}
}

// TestEpicWorktreeOrphanDirReplaced — осиротевший каталог прошлого (прерванного)
// цикла не должен попасть в результат: резолвер снимает его и создаёт worktree.
func TestEpicWorktreeOrphanDirReplaced(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	ctx, srv, repo := setupEpicWorktreeRepo(t)

	wtPath := srv.epicWorktreePath(repo.Root, "myrepo", "e1")
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "garbage.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := srv.epicWorktree(ctx, "myrepo", "e1")
	if got != wtPath {
		t.Fatalf("epicWorktree: %q != %q", got, wtPath)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "garbage.txt")); err == nil {
		t.Fatal("осиротевший каталог не очищен перед созданием worktree")
	}
	if b := gitOutput(t, wtPath, "branch", "--show-current"); b != "ai/epic/e1" {
		t.Fatalf("worktree должен стоять на ai/epic/e1, а не на %q", b)
	}
}

// TestEpicWorktreeDegrades — без git-проекта/ветки/ID резолвер возвращает "",
// а не падает: архитектор получит note и не будет писать структуру.
func TestEpicWorktreeDegrades(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	ctx, srv, _ := setupEpicWorktreeRepo(t)

	cases := []struct {
		name, project, epicID string
	}{
		{"пустой epicID", "myrepo", ""},
		{"нет ветки эпика", "myrepo", "нет-такого"},
		{"неизвестный проект", "unknown", "e1"},
	}
	for _, c := range cases {
		if got := srv.epicWorktree(ctx, c.project, c.epicID); got != "" {
			t.Errorf("%s: ожидался degrade (\"\"), получен %q", c.name, got)
		}
	}
}

// TestMergeTaskBranchDropsEpicWorktree — сквозная проверка точки вызова:
// worktree ветки эпика жив, авто-мёрдж задачи в релиз эпика обязан снять его
// (иначе git откажет в checkout релизной ветки) и провести слияние.
func TestMergeTaskBranchDropsEpicWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	ctx, srv, repo := setupEpicWorktreeRepo(t)
	project := "myrepo"

	// Ветка задачи с одним коммитом (правка README).
	if err := repo.CreateBranch(ctx, "ai/task/t1", repo.Base); err != nil {
		t.Fatalf("CreateBranch task: %v", err)
	}
	if out, err := exec.Command("git", "-C", repo.Root, "checkout", "-q", "ai/task/t1").CombinedOutput(); err != nil {
		t.Fatalf("checkout task: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo.Root, "feature.md"), []byte("фича\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", repo.Root, "add", "-A").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", repo.Root, "commit", "-m", "фича").CombinedOutput(); err != nil {
		t.Fatalf("commit task: %v", err)
	}
	if out, err := exec.Command("git", "-C", repo.Root, "checkout", "-q", repo.Branch).CombinedOutput(); err != nil {
		t.Fatalf("checkout back: %v\n%s", err, out)
	}
	if err := srv.reg.SetTaskBranch(project, "t1", workspace.BranchRef{
		Branch: "ai/task/t1", Base: repo.Base,
	}); err != nil {
		t.Fatal(err)
	}

	// Рабочая точка архитектора жива.
	wtPath := srv.epicWorktree(ctx, project, "e1")
	if wtPath == "" {
		t.Fatal("epicWorktree: пустой путь")
	}

	res, err := srv.mergeTaskBranch(ctx, project, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "t1"},
		EpicID:   "e1",
	})
	if err != nil {
		t.Fatalf("mergeTaskBranch со живым worktree эпика: %v", err)
	}
	if res == nil {
		t.Fatal("mergeTaskBranch: пустой результат")
	}
	// Worktree снят, ветка задачи влита в релиз.
	if _, err := os.Stat(wtPath); err == nil {
		t.Fatalf("worktree эпика не снят перед мёрджем: %s", wtPath)
	}
	ref, err := srv.reg.EpicBranch(project, "e1")
	if err != nil || ref.Worktree != "" {
		t.Fatalf("реестр после мёрджа: worktree=%q err=%v", ref.Worktree, err)
	}
	if ok, err := repo.MergedInto(ctx, "ai/epic/e1", "ai/task/t1"); err != nil || !ok {
		t.Fatalf("ветка задачи должна быть влита в релиз: ok=%v err=%v", ok, err)
	}
}
