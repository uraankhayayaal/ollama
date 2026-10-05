package developer

// Тест гарда области промежуточных коммитов (Ф-6,
// PLAN-2026-10-05-todo-kanban-rollback.md, этап 1.4): агент, работающий в
// общем клоне проекта (ветка ai/<проект>, как в режиме `plan`), не должен
// создавать коммиты — общий клон делят все шаги плана.
//
// Прогоняет реальный git CLI (как gitops/cli_test.go): гард проверяет ветку,
// а не имя каталога, поэтому подменить его нельзя.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"ai/tools"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func gitConfigUser(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "config", "user.email", "t@example.com")
	gitRun(t, dir, "config", "user.name", "Test")
}

// TestCommitRoundTouchedSkipsSharedClone проверяет, что в общем клоне
// (ветка ai/<проект>) промежуточный коммит не создаётся: гард InTaskBranch
// отсекает его до обращения к git.
func TestCommitRoundTouchedSkipsSharedClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "ai/mytrip")
	gitConfigUser(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "init")

	// Грязное дерево: агент что-то поменял в раунде.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := &base{FileOps: &tools.FileOps{OutputDir: dir}}
	sha, committed, err := d.CommitRoundTouched([]string{"main.go"}, 1)
	if err != nil {
		t.Fatalf("CommitRoundTouched: %v", err)
	}
	if committed || sha != "" {
		t.Fatalf("в общем клоне коммит не должен создаваться: sha=%q committed=%v", sha, committed)
	}
	// И действительно ни одного коммита не появилось.
	if out := gitRun(t, dir, "rev-list", "--count", "HEAD"); out != "1\n" {
		t.Fatalf("число коммитов = %q, want 1", out)
	}
}

// TestCommitRoundTouchedCommitsInTaskWorktree — зеркальный случай: в ветке
// задачи (ai/task/<id>) правки раунда фиксируются, а SHA возвращается как
// цель будущего ручного отката.
func TestCommitRoundTouchedCommitsInTaskWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "ai/task/t1")
	gitConfigUser(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "init")

	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := &base{FileOps: &tools.FileOps{OutputDir: dir}}
	sha, committed, err := d.CommitRoundTouched([]string{"main.go"}, 2)
	if err != nil {
		t.Fatalf("CommitRoundTouched: %v", err)
	}
	if !committed {
		t.Fatal("в ветке задачи коммит должен создаваться")
	}
	if len(sha) != 40 {
		t.Fatalf("sha = %q, want 40 символов", sha)
	}
	// Дерево после коммита чистое — повторный вызов не создаёт пустой коммит.
	sha2, committed2, err := d.CommitRoundTouched([]string{"main.go"}, 3)
	if err != nil {
		t.Fatalf("повторный CommitRoundTouched: %v", err)
	}
	if committed2 || sha2 != "" {
		t.Fatalf("чистое дерево: sha=%q committed=%v, want пусто/false", sha2, committed2)
	}
	if out := gitRun(t, dir, "rev-list", "--count", "HEAD"); out != "2\n" {
		t.Fatalf("число коммитов = %q, want 2", out)
	}
}

// TestCommitRoundTouchedWithoutOutputDir — пустой OutputDir (агент без рабочего
// каталога) не должен падать и не должен обращаться к git.
func TestCommitRoundTouchedWithoutOutputDir(t *testing.T) {
	d := &base{FileOps: &tools.FileOps{}}
	sha, committed, err := d.CommitRoundTouched(nil, 1)
	if err != nil {
		t.Fatalf("CommitRoundTouched: %v", err)
	}
	if committed || sha != "" {
		t.Fatalf("без OutputDir: sha=%q committed=%v, want пусто/false", sha, committed)
	}
}
