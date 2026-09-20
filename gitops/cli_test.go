package gitops

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// run выполняет команду и возвращает вывод; env — дополнительные переменные.
func run(t *testing.T, dir, env string, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if env != "" {
		cmd.Env = append(os.Environ(), env)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

// TestGitCLICloneBranchDiffCommitEndToEnd прогоняет реальный git CLI:
// локальный origin → Clone (ветка по умолчанию + фича-ветка) → изменение →
// Diff/Dirty → Commit → RejectBranch. Пропускается, если git недоступен.
func TestGitCLICloneBranchDiffCommitEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	ctx := context.Background()
	base := t.TempDir()

	origin := filepath.Join(base, "origin")
	if out, err := run(t, "", "", "git", "init", "-b", "main", origin); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	setGitUser(t, origin)
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("# origin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, origin, "", "git", "add", "-A"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := run(t, origin, "", "git", "commit", "-m", "init"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	dest := filepath.Join(base, "feat")
	repo, err := Clone(ctx, CLIExecutor{}, origin, "ai/feat", dest)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if repo.Base != "main" || repo.Branch != "ai/feat" {
		t.Fatalf("Base/Branch = %q/%q", repo.Base, repo.Branch)
	}
	if repo.Remote != origin {
		t.Fatalf("Remote = %q, want %q", repo.Remote, origin)
	}

	// Изменение в фича-ветке → дифф и «грязное» состояние.
	if err := os.WriteFile(filepath.Join(dest, "feature.go"), []byte("package feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff, err := repo.Diff(ctx)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "feature.go") {
		t.Fatalf("дифф не содержит нового файла:\n%s", diff)
	}
	dirty, err := repo.Dirty(ctx)
	if err != nil {
		t.Fatalf("Dirty: %v", err)
	}
	if !dirty {
		t.Fatal("после правки состояние должно быть грязным")
	}

	// Коммит → состояние чистое.
	setGitUser(t, dest)
	if err := repo.Commit(ctx, "фича"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	dirty, err = repo.Dirty(ctx)
	if err != nil {
		t.Fatalf("Dirty: %v", err)
	}
	if dirty {
		t.Fatal("после коммита состояние должно быть чистым")
	}

	// Отклонение: возврат на базу и удаление фича-ветки.
	if err := repo.RejectBranch(ctx); err != nil {
		t.Fatalf("RejectBranch: %v", err)
	}
	if _, err := run(t, dest, "", "git", "rev-parse", "--verify", "refs/heads/ai/feat"); err == nil {
		t.Fatal("фича-ветка должна быть удалена после reject")
	}
	if out, _ := run(t, dest, "", "git", "rev-parse", "--abbrev-ref", "HEAD"); strings.TrimSpace(out) != "main" {
		t.Fatalf("после reject должна быть база main, а HEAD = %q", out)
	}
}

// setGitUser задаёт user.name/email для коммитов (клоны не наследуют конфиг).
func setGitUser(t *testing.T, dir string) {
	t.Helper()
	if _, err := run(t, dir, "", "git", "config", "user.email", "t@example.com"); err != nil {
		t.Fatalf("config user.email: %v", err)
	}
	if _, err := run(t, dir, "", "git", "config", "user.name", "Test"); err != nil {
		t.Fatalf("config user.name: %v", err)
	}
}

// TestGitCLIMergeTreeConflictDetectionEndToEnd прогоняет реальный git CLI для
// merge-примитивов Ф-1: создание веток эпика/задачи, детекция конфликтов через
// git merge-tree (старый формат, git < 2.38) и MergeBranch (-no-ff). Пропускается,
// если git недоступен.
func TestGitCLIMergeTreeConflictDetectionEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	ctx := context.Background()
	base := t.TempDir()

	origin := filepath.Join(base, "origin")
	if out, err := run(t, "", "", "git", "init", "-b", "main", origin); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	setGitUser(t, origin)
	if err := os.WriteFile(filepath.Join(origin, "f.txt"), []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, origin, "", "git", "add", "-A"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := run(t, origin, "", "git", "commit", "-m", "init"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Ветка conflict_a: правит первую строку. Ветка conflict_b: правит ту же
	// строку иначе. Ветка clean_add: только добавляет новый файл (без конфликта).
	for _, b := range []struct{ name, content string }{
		{"conflict_a", "line1-A\nline2\n"},
		{"conflict_b", "line1-B\nline2\n"},
	} {
		if _, err := run(t, origin, "", "git", "checkout", "-b", b.name, "main"); err != nil {
			t.Fatalf("checkout -b %s: %v", b.name, err)
		}
		if err := os.WriteFile(filepath.Join(origin, "f.txt"), []byte(b.content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := run(t, origin, "", "git", "commit", "-am", b.name); err != nil {
			t.Fatalf("commit %s: %v", b.name, err)
		}
	}
	if _, err := run(t, origin, "", "git", "checkout", "-b", "clean_add", "main"); err != nil {
		t.Fatalf("checkout -b clean_add: %v", err)
	}
	if err := os.WriteFile(filepath.Join(origin, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, origin, "", "git", "add", "-A"); err != nil {
		t.Fatalf("add clean_add: %v", err)
	}
	if _, err := run(t, origin, "", "git", "commit", "-m", "clean_add"); err != nil {
		t.Fatalf("commit clean_add: %v", err)
	}

	// Возвращаем origin на главную ветку: клон должен стартовать с main,
	// чтобы в нём была локальная ветка main (база веток эпика/задач).
	if _, err := run(t, origin, "", "git", "checkout", "-q", "main"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}

	dest := filepath.Join(base, "feat")
	repo, err := Clone(ctx, CLIExecutor{}, origin, "ai/feat", dest)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}

	// Создание ветки эпика и задачи без переключения (Ф-1): текущий HEAD (ai/feat)
	// не меняется.
	if err := repo.CreateBranch(ctx, "ai/epic/e1", "main"); err != nil {
		t.Fatalf("CreateBranch эпика: %v", err)
	}
	if err := repo.CreateBranch(ctx, "ai/task/t1", "ai/epic/e1"); err != nil {
		t.Fatalf("CreateBranch задачи: %v", err)
	}
	if ok, err := repo.BranchExists(ctx, "ai/epic/e1"); err != nil || !ok {
		t.Fatalf("BranchExists epic = %v, %v", ok, err)
	}
	if ok, err := repo.BranchExists(ctx, "нет-такой"); err != nil || ok {
		t.Fatalf("BranchExists(нет) = %v, %v; want false", ok, err)
	}

	// Конфликт: conflict_a и conflict_b правят одну строку f.txt.
	conflicts, err := repo.MergeTree(ctx, "main", "origin/conflict_a", "origin/conflict_b")
	if err != nil {
		t.Fatalf("MergeTree конфликт: %v", err)
	}
	if len(conflicts) != 1 || conflicts[0] != "f.txt" {
		t.Fatalf("MergeTree conflict = %v, want [f.txt]", conflicts)
	}

	// Чистое слияние: clean_add и conflict_a не пересекаются.
	clean, err := repo.MergeTree(ctx, "main", "origin/clean_add", "origin/conflict_a")
	if err != nil {
		t.Fatalf("MergeTree clean: %v", err)
	}
	if len(clean) != 0 {
		t.Fatalf("MergeTree clean = %v, want []", clean)
	}

	// ConflictingFiles: merge-base + merge-tree против текущего HEAD.
	files, err := repo.ConflictingFiles(ctx, "ai/epic/e1")
	if err != nil {
		t.Fatalf("ConflictingFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("ConflictingFiles = %v, want [] (HEAD=base main)", files)
	}

	// MergeBranch --no-ff в реальном git: подключаем task-ветку поверх эпика.
	// Для конфликтующей ветки создаём локальную задачу от conflict_a, чтобы
	// у ветки были коммиты относительно main-базы эпика.
	if _, err := run(t, dest, "", "git", "branch", "ai/task/w1", "ai/epic/e1"); err != nil {
		t.Fatalf("branch w1: %v", err)
	}
	if _, err := run(t, dest, "", "git", "checkout", "-q", "ai/task/w1"); err != nil {
		t.Fatalf("checkout w1: %v", err)
	}
	setGitUser(t, dest)
	if err := os.WriteFile(filepath.Join(dest, "f.txt"), []byte("line1-A\nline2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, dest, "", "git", "commit", "-am", "задача w1"); err != nil {
		t.Fatalf("commit w1: %v", err)
	}
	if _, err := run(t, dest, "", "git", "checkout", "-q", "ai/epic/e1"); err != nil {
		t.Fatalf("checkout epic: %v", err)
	}
	if _, err := repo.MergeBranch(ctx, "ai/task/w1", "вливание задачи w1"); err != nil {
		t.Fatalf("MergeBranch: %v", err)
	}
	if out, err := run(t, dest, "", "git", "log", "-1", "--format=%s"); err != nil || strings.TrimSpace(out) != "вливание задачи w1" {
		t.Fatalf("лог последнего коммита = %q, %v", out, err)
	}
	if out, _ := run(t, dest, "", "git", "rev-parse", "--abbrev-ref", "HEAD"); strings.TrimSpace(out) != "ai/epic/e1" {
		t.Fatalf("HEAD после merge = %q, want ai/epic/e1", out)
	}
}

// TestGitCLIMergeFeatureEndToEnd прогоняет MergeFeature на реальном git:
// ветка задачи → релизная ветка эпика через временный worktree (рабочая копия
// клона и HEAD не трогаются), push в локальный origin, идемпотентность и
// конфликт. Пропускается, если git недоступен.
func TestGitCLIMergeFeatureEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	ctx := context.Background()
	base := t.TempDir()

	origin := filepath.Join(base, "origin")
	if out, err := run(t, "", "", "git", "init", "-b", "main", origin); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	setGitUser(t, origin)
	if err := os.WriteFile(filepath.Join(origin, "f.txt"), []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, origin, "", "git", "add", "-A"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := run(t, origin, "", "git", "commit", "-m", "init"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	dest := filepath.Join(base, "feat")
	repo, err := Clone(ctx, CLIExecutor{}, origin, "ai/feat", dest)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	setGitUser(t, dest)

	// Ветки эпика и задач (без переключения HEAD, Ф-1).
	if err := repo.CreateBranch(ctx, "ai/epic/e1", "main"); err != nil {
		t.Fatalf("CreateBranch эпика: %v", err)
	}
	if err := repo.CreateBranch(ctx, "ai/task/t1", "ai/epic/e1"); err != nil {
		t.Fatalf("CreateBranch задачи: %v", err)
	}
	if err := repo.CreateBranch(ctx, "ai/task/t2", "ai/epic/e1"); err != nil {
		t.Fatalf("CreateBranch задачи t2: %v", err)
	}

	// Коммиты в ветках задач (обе правят одну строку f.txt — конфликт t2/t1).
	for _, tc := range []struct{ branch, line string }{
		{"ai/task/t1", "line1-t1"},
		{"ai/task/t2", "line1-t2"},
	} {
		if _, err := run(t, dest, "", "git", "checkout", "-q", tc.branch); err != nil {
			t.Fatalf("checkout %s: %v", tc.branch, err)
		}
		if err := os.WriteFile(filepath.Join(dest, "f.txt"), []byte(tc.line+"\nline2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := run(t, dest, "", "git", "commit", "-am", "задача "+tc.branch); err != nil {
			t.Fatalf("commit %s: %v", tc.branch, err)
		}
	}
	// Возвращаем рабочий HEAD на фича-ветку клона: MergeFeature не должен
	// переключать его.
	if _, err := run(t, dest, "", "git", "checkout", "-q", "ai/feat"); err != nil {
		t.Fatalf("checkout ai/feat: %v", err)
	}

	// Мёрдж t1 → ai/epic/e1 с push в локальный origin.
	res, err := repo.MergeFeature(ctx, "ai/epic/e1", "ai/task/t1", MergeFeatureOptions{
		Message: "задача t1 влита в релиз",
		PushURL: origin,
	})
	if err != nil {
		t.Fatalf("MergeFeature t1: %v", err)
	}
	if res.AlreadyMerged {
		t.Fatal("t1 должна влиться, а не считаться уже слитой")
	}
	// Рабочая копия клона не тронута: HEAD остался на ai/feat, временные
	// worktree-каталоги не висят.
	if out, _ := run(t, dest, "", "git", "rev-parse", "--abbrev-ref", "HEAD"); strings.TrimSpace(out) != "ai/feat" {
		t.Fatalf("HEAD клона = %q, want ai/feat", out)
	}
	if out, _ := run(t, dest, "", "git", "worktree", "list"); strings.Contains(out, ".wt-") {
		t.Fatalf("остались временные worktree:\n%s", out)
	}
	// Релизная ветка продвинута merge-коммитом и запушена в origin.
	if out, err := run(t, dest, "", "git", "log", "-1", "--format=%s", "ai/epic/e1"); err != nil || !strings.Contains(out, "задача t1") {
		t.Fatalf("лог ai/epic/e1 = %q, %v", out, err)
	}
	if _, err := run(t, origin, "", "git", "rev-parse", "--verify", "refs/heads/ai/epic/e1"); err != nil {
		t.Fatalf("ai/epic/e1 не запушена в origin: %v", err)
	}

	// Идемпотентность: повторный мёрдж t1 → AlreadyMerged, новых коммитов нет.
	before, _ := run(t, dest, "", "git", "rev-parse", "ai/epic/e1")
	res, err = repo.MergeFeature(ctx, "ai/epic/e1", "ai/task/t1", MergeFeatureOptions{
		Message: "задача t1 влита в релиз",
		PushURL: origin,
	})
	if err != nil {
		t.Fatalf("MergeFeature (повтор) t1: %v", err)
	}
	if !res.AlreadyMerged {
		t.Fatal("повторный мёрдж должен вернуть AlreadyMerged")
	}
	after, _ := run(t, dest, "", "git", "rev-parse", "ai/epic/e1")
	if before != after {
		t.Fatal("идемпотентность: повторный merge изменил релизную ветку")
	}

	// Конфликт: t2 правит ту же строку f.txt, что и t1.
	_, err = repo.MergeFeature(ctx, "ai/epic/e1", "ai/task/t2", MergeFeatureOptions{
		Message: "задача t2 влита в релиз",
		PushURL: origin,
	})
	var ce *MergeConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("ожидали MergeConflictError для t2, получили %v", err)
	}
	if len(ce.Files) != 1 || ce.Files[0] != "f.txt" {
		t.Fatalf("конфликтующие пути = %v, want [f.txt]", ce.Files)
	}
	// Конфликт не тронул ветку: релиз остался на merge-коммите t1.
	post, _ := run(t, dest, "", "git", "rev-parse", "ai/epic/e1")
	if post != after {
		t.Fatal("конфликт не должен менять релизную ветку")
	}
}
