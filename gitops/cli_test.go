package gitops

import (
	"context"
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