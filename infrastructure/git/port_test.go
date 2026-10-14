package git

import (
	"context"
	"testing"

	"ai/domain/services"
)

// Порт services.GitOperations используется приложением вместо конкретного
// *Repo: проверяем, что репозиторий отдаётся через интерфейс и работает
// (компиляционная проверка лежит в port.go, здесь — исполняемая).
func TestRepoImplementsGitOperationsPort(t *testing.T) {
	ex := &fakeExecutor{
		resp: map[string]string{
			"/repo | git rev-parse --abbrev-ref HEAD": "ai/task/T-01\n",
			"/repo | git status --porcelain":          "",
		},
	}
	repo := RepoFromState(ex, "/repo", "git@gitlab.com:g/p.git", "ai/task/T-01", "main")

	// Repo передаётся как порт: application-слой видит только интерфейс.
	var gate services.GitOperations = repo

	branch, err := gate.CurrentBranch(context.Background())
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if branch != "ai/task/T-01" {
		t.Fatalf("CurrentBranch = %q, want ai/task/T-01", branch)
	}

	dirty, err := gate.Dirty(context.Background())
	if err != nil {
		t.Fatalf("Dirty: %v", err)
	}
	if dirty {
		t.Fatal("чистое дерево не должно считаться грязным")
	}
}
