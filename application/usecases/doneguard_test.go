package usecases

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ai/domain"
)

// fakeDoneGuard — заглушка DoneGuardSource: фиксируем вызовы и подсовываем
// нужное состояние (ветка/база/коммиты/правки) без настоящего git.
type fakeDoneGuard struct {
	gitProject bool
	branch     string
	hasBranch  bool
	base       string

	commits    int
	commitsErr error

	dirty    []string
	dirtyErr error

	warns []string
}

func (f *fakeDoneGuard) IsGitProject(string) bool { return f.gitProject }
func (f *fakeDoneGuard) TaskBranch(string, string) (string, bool) {
	return f.branch, f.hasBranch
}
func (f *fakeDoneGuard) EpicBase(string, string) string { return f.base }
func (f *fakeDoneGuard) CommitsAhead(context.Context, string, string, string) (int, error) {
	return f.commits, f.commitsErr
}
func (f *fakeDoneGuard) DirtyPaths(context.Context, string, string) ([]string, error) {
	return f.dirty, f.dirtyErr
}
func (f *fakeDoneGuard) Warn(_ string, format string, args ...any) {
	f.warns = append(f.warns, fmt.Sprintf(format, args...))
}

// Тест main: пустая задача git-проекта (0 коммитов, только служебный
// .gitignore) закрываться в done не должна.
func TestGuardTaskDoneBlocksEmptyTask(t *testing.T) {
	f := &fakeDoneGuard{gitProject: true, hasBranch: true, branch: "ai/task/t1", base: "ai/epic/e1", dirty: []string{".gitignore"}}
	err := GuardTaskDone(context.Background(), f, true, "app", &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "t1"}, EpicID: "e1"})
	if err == nil {
		t.Fatal("пустая задача не должна закрываться в done")
	}
	for _, want := range []string{"t1", "ai/task/t1", "ai/epic/e1", "KANBAN_DONE_REQUIRES_COMMIT=0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в ошибке нет %q: %s", want, err.Error())
		}
	}
}

// Свой коммит ветки относительно базы эпика — работа была.
func TestGuardTaskDoneAllowsCommit(t *testing.T) {
	f := &fakeDoneGuard{gitProject: true, hasBranch: true, branch: "ai/task/t1", base: "ai/epic/e1", commits: 1}
	if err := GuardTaskDone(context.Background(), f, true, "app", &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "t1"}}); err != nil {
		t.Fatalf("задача со своим коммитом должна закрываться: %v", err)
	}
}

// Незакоммиченные правки (не служебный .gitignore) тоже считаются работой.
func TestGuardTaskDoneAllowsDirtyWorktree(t *testing.T) {
	f := &fakeDoneGuard{gitProject: true, hasBranch: true, branch: "ai/task/t1", base: "ai/epic/e1", dirty: []string{".gitignore", "feature.go"}}
	if err := GuardTaskDone(context.Background(), f, true, "app", &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "t1"}}); err != nil {
		t.Fatalf("несохранённые правки должны считаться работой: %v", err)
	}
}

// Только служебный .gitignore доказательством не является.
func TestGuardTaskDoneIgnoresOrchestratorGitignore(t *testing.T) {
	f := &fakeDoneGuard{gitProject: true, hasBranch: true, branch: "ai/task/t1", base: "ai/epic/e1", dirty: []string{".gitignore"}}
	if err := GuardTaskDone(context.Background(), f, true, "app", &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "t1"}}); err == nil {
		t.Fatal("служебный .gitignore не должен открывать дорогу в done")
	}
}

// Гард можно выключить (KANBAN_DONE_REQUIRES_COMMIT=0).
func TestGuardTaskDoneDisabled(t *testing.T) {
	f := &fakeDoneGuard{gitProject: true, hasBranch: true, branch: "ai/task/t1", base: "ai/epic/e1", dirty: []string{".gitignore"}}
	if err := GuardTaskDone(context.Background(), f, false, "app", &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "t1"}}); err != nil {
		t.Fatalf("выключенный гард не должен блокировать: %v", err)
	}
}

// Пропуски: nil-задача, задача без id, не-git проект, задача без ветки.
func TestGuardTaskDoneSkips(t *testing.T) {
	base := &fakeDoneGuard{gitProject: true, hasBranch: true, branch: "ai/task/t1", base: "ai/epic/e1"}
	cases := []struct {
		name   string
		src    *fakeDoneGuard
		task   *domain.Task
		enable bool
	}{
		{"nil задача", base, nil, true},
		{"без id", base, &domain.Task{}, true},
		{"не-git проект", &fakeDoneGuard{gitProject: false, hasBranch: true, branch: "b"}, &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "t1"}}, true},
		{"нет ветки задачи", &fakeDoneGuard{gitProject: true, hasBranch: false}, &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "t1"}}, true},
		{"выключен + без id", base, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := GuardTaskDone(context.Background(), c.src, c.enable, "app", c.task); err != nil {
				t.Fatalf("ожидался пропуск, получено: %v", err)
			}
		})
	}
}

// Ошибка git при подсчёте коммитов — «не доказано»: переход разрешён, но с
// предупреждением в лог (блокировать человека из-за сбоя git нельзя).
func TestGuardTaskDoneCommitErrorAllowsWithWarn(t *testing.T) {
	f := &fakeDoneGuard{gitProject: true, hasBranch: true, branch: "ai/task/t1", base: "ai/epic/e1", commitsErr: errors.New("boom")}
	if err := GuardTaskDone(context.Background(), f, true, "app", &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "t1"}}); err != nil {
		t.Fatalf("сбой git не должен блокировать переход: %v", err)
	}
	if len(f.warns) != 1 {
		t.Fatalf("ожидалось одно предупреждение, получено %d", len(f.warns))
	}
}

// Ошибка чтения правок worktree не блокирует проверку коммитов.
func TestGuardTaskDoneDirtyErrorFallsThroughToCommits(t *testing.T) {
	f := &fakeDoneGuard{gitProject: true, hasBranch: true, branch: "ai/task/t1", base: "ai/epic/e1", dirtyErr: errors.New("boom"), commits: 2}
	if err := GuardTaskDone(context.Background(), f, true, "app", &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "t1"}}); err != nil {
		t.Fatalf("ошибка правок не должна мешать: %v", err)
	}
}

func TestIsOrchestratorArtifact(t *testing.T) {
	cases := map[string]bool{
		"":                     true,
		"   ":                  true,
		".gitignore":           true,
		"sub/dir/.gitignore":   true,
		"feature.go":           false,
		"dir/notes.txt":        false,
		".gitignore.bak":       false,
		"nested/.gitignored/x": false,
	}
	for path, want := range cases {
		if got := IsOrchestratorArtifact(path); got != want {
			t.Errorf("IsOrchestratorArtifact(%q) = %v, want %v", path, got, want)
		}
	}
}
