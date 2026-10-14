package gitflow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai/infrastructure/git"
)

// Раскладка worktree: соседи основного клона, id санитизируется.
func TestWorktreePaths(t *testing.T) {
	root := filepath.Join("/tmp", "projects", "myrepo")
	if got, want := TaskWorktreePath(root, "app", "T-01/x"), filepath.Join("/tmp/projects", ".wt-task-app-T-01_x"); got != want {
		t.Errorf("TaskWorktreePath = %q, want %q", got, want)
	}
	if got, want := EpicWorktreePath(root, "app", "E.1"), filepath.Join("/tmp/projects", ".wt-epic-app-E.1"); got != want {
		t.Errorf("EpicWorktreePath = %q, want %q", got, want)
	}
}

func TestChildTaskKey(t *testing.T) {
	if got := ChildTaskKey("app", "T-1"); got != "app::T-1" {
		t.Fatalf("ChildTaskKey = %q", got)
	}
}

// TaskGitignore — заголовок + все паттерны, каждый на своей строке.
func TestTaskGitignore(t *testing.T) {
	full := TaskGitignore()
	if !strings.HasPrefix(full, GitignoreHeader+"\n") {
		t.Fatalf("нет заголовка: %q", full)
	}
	for _, p := range GitignorePatterns {
		if !strings.Contains(full, p+"\n") {
			t.Errorf("нет паттерна %q", p)
		}
	}
	if !strings.HasSuffix(full, "\n") {
		t.Errorf("нет завершающего перевода строки")
	}
}

// Нет файла → создаётся полный .gitignore.
func TestEnsureTaskGitignoreCreates(t *testing.T) {
	dir := t.TempDir()
	EnsureTaskGitignore("app", "t1", dir)

	body := readFile(t, filepath.Join(dir, ".gitignore"))
	for _, want := range []string{"node_modules/", "node-v*/", "*.tsbuildinfo", GitignoreHeader} {
		if !strings.Contains(body, want) {
			t.Errorf("в созданном .gitignore нет %q:\n%s", want, body)
		}
	}
}

// Существующий файл проекта сохраняется, недостающие паттерны дописываются.
func TestEnsureTaskGitignoreAppends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(path, []byte("frontend/node_modules/\npgdata/"), 0o644); err != nil {
		t.Fatal(err)
	}

	EnsureTaskGitignore("app", "t1", dir)

	body := readFile(t, path)
	if !strings.HasPrefix(body, "frontend/node_modules/\npgdata/") {
		t.Errorf("проектный .gitignore потерян:\n%s", body)
	}
	for _, want := range []string{"node_modules/", "node-v*/", GitignoreHeader} {
		if !strings.Contains(body, want) {
			t.Errorf("не дописан %q:\n%s", want, body)
		}
	}
}

// Идемпотентность: повторный вызов не меняет файл.
func TestEnsureTaskGitignoreIdempotent(t *testing.T) {
	dir := t.TempDir()
	EnsureTaskGitignore("app", "t1", dir)
	first := readFile(t, filepath.Join(dir, ".gitignore"))

	EnsureTaskGitignore("app", "t1", dir)
	second := readFile(t, filepath.Join(dir, ".gitignore"))

	if first != second {
		t.Fatalf("повторный вызов изменил файл:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// Пустой корень — no-op (не создаём .gitignore в текущем каталоге).
func TestEnsureTaskGitignoreEmptyRoot(t *testing.T) {
	EnsureTaskGitignore("app", "t1", "  ")
}

// Ошибка git-шага для доски: подсказка по scope workflow, одна строка,
// секреты скрыты, длинный текст усечён.
func TestGitflowErrBoardPolicy(t *testing.T) {
	wrapped := fmt.Errorf("подготовка базы MR: %w",
		fmt.Errorf("gitops: git push %s: %w", "ai/epic/e1",
			fmt.Errorf("exit status 1: %s: %w", " ! [remote rejected] … without `workflow` scope", git.ErrWorkflowScope)))
	msg := GitflowErrBoard("авто-MR задачи T-01 не создан", wrapped)
	if !strings.Contains(msg, "авто-MR задачи T-01 не создан") || !strings.Contains(msg, "GITHUB_TOKEN") {
		t.Fatalf("нет подсказки про scope workflow: %q", msg)
	}

	if got := GitflowErrBoard("префикс", errors.New("первая\nвторая")); got != "префикс: первая" {
		t.Fatalf("многострочная ошибка не схлопнута: %q", got)
	}

	long := errors.New(strings.Repeat("я", 300))
	if r := []rune(GitflowErrBoard("префикс", long)); len(r) > 220 {
		t.Fatalf("сообщение не усечено: %d рун", len(r))
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
