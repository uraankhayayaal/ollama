package server

// E2E-тест авторезолвинга конфликтов мёрджа задачи в релиз эпика через LLM (Ф-9).
// Использует реальный git-протокол: origin → клон → ветки эпика/задачи →
// конфликт при мёрдже → LLM разрешает → приёмка → вливание.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ai/board"
	"ai/gitops"
	"ai/runner"
	"ai/tools"
	"ai/workspace"
)

func TestTaskLLMResolveE2E(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	ctx := context.Background()

	// 1. Origin с базовым коммитом.
	base := t.TempDir()
	origin := filepath.Join(base, "origin")
	if out, err := exec.Command("git", "init", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	setGitUserReal(t, origin)
	if err := os.WriteFile(filepath.Join(origin, "go.mod"), []byte("module test\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origin, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origin, "f.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", origin, "add", "-A").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", origin, "commit", "-m", "base").CombinedOutput(); err != nil {
		t.Fatalf("commit base: %v", err)
	}

	// 2. Клон проекта.
	dest := filepath.Join(base, "myrepo")
	repo, err := gitops.Clone(ctx, gitops.CLIExecutor{}, origin, "ai/myrepo", dest)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	setGitUserReal(t, dest)

	srv, _, mr := newTestServerGit(t, gitops.CLIExecutor{}, nil)
	if _, err := srv.reg.Add(workspace.AddParams{
		Name: "myrepo", Kind: workspace.KindGit, Root: repo.Root,
		GitRemote: origin, GitBranch: repo.Branch, GitBase: repo.Base,
	}); err != nil {
		t.Fatal(err)
	}

	// 3. Ветки эпика и задачи.
	if err := repo.CreateBranch(ctx, "ai/epic/e1", "main"); err != nil {
		t.Fatalf("CreateBranch эпика: %v", err)
	}
	if err := repo.CreateBranch(ctx, "ai/task/t1", "ai/epic/e1"); err != nil {
		t.Fatalf("CreateBranch задачи: %v", err)
	}
	if err := srv.reg.SetEpicBranch("myrepo", "epic-1", workspace.BranchRef{Branch: "ai/epic/e1", Base: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := srv.reg.SetTaskBranch("myrepo", "task-1", workspace.BranchRef{Branch: "ai/task/t1", Base: "ai/epic/e1"}); err != nil {
		t.Fatal(err)
	}

	// 4. Конфликт: эпик и задача меняют одну строку по-разному.
	if out, err := exec.Command("git", "-C", dest, "checkout", "-q", "ai/epic/e1").CombinedOutput(); err != nil {
		t.Fatalf("checkout e1: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dest, "f.txt"), []byte("epic version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dest, "add", "-A").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dest, "commit", "-m", "эпик правит").CombinedOutput(); err != nil {
		t.Fatalf("commit эпика: %v", err)
	}

	if out, err := exec.Command("git", "-C", dest, "checkout", "-q", "ai/task/t1").CombinedOutput(); err != nil {
		t.Fatalf("checkout t1: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dest, "f.txt"), []byte("task version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dest, "add", "-A").CombinedOutput(); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", dest, "commit", "-m", "задача правит").CombinedOutput(); err != nil {
		t.Fatalf("commit задачи: %v", err)
	}
	if out, err := exec.Command("git", "-C", dest, "checkout", "-q", "ai/myrepo").CombinedOutput(); err != nil {
		t.Fatalf("checkout ai/myrepo: %v\n%s", err, out)
	}

	// 5. Mock LLM: эмулирует разработчика — WriteFiles tool call с результатом.
	resolvedContent := "merged: epic + task\n"
	writeFilesArgs := fmt.Sprintf(`{"files":[{"filename":"f.txt","content":%q}]}`, resolvedContent)
	mockLLM := &scriptedGenerateProvider{chat: &scriptedChatProvider{replies: []*runner.ModelReply{
		{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: writeFilesArgs}}, FinishReason: "tool_calls"},
		{Content: "готово", FinishReason: "stop"},
	}}}
	srv.prov = providerResolve{prov: mockLLM, done: true}

	// 6. Доска: эпик + задача (AssignedRole: backend → BackendDeveloper).
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "myrepo"})
	defer store.Close()
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Релиз"}, Status: board.StatusNew,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "task-1", Title: "Фича", AssignedRole: "Backend Developer"},
		EpicID:   "epic-1", Status: board.StatusDone,
	}); err != nil {
		t.Fatal(err)
	}

	// 7. Авто-мёрдж задачи с LLM-авторезолвингом.
	srv.autoCommitAndMergeTask(ctx, "myrepo", &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "task-1", Title: "Фича"},
		EpicID:   "epic-1",
	}, store)

	// 8. Проверки.
	task, err := store.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(task.MergeConflictFiles) != 0 {
		t.Fatalf("конфликт не снят с задачи: %v", task.MergeConflictFiles)
	}

	if ok, _ := repo.MergedInto(ctx, "ai/epic/e1", "ai/task/t1"); !ok {
		t.Fatal("ai/task/t1 не влита в ai/epic/e1 после LLM-резолвина")
	}

	data, err := exec.Command("git", "-C", dest, "show", "ai/epic/e1:f.txt").CombinedOutput()
	if err != nil {
		t.Fatalf("git show: %v", err)
	}
	if strings.Contains(string(data), "<<<<<<<") || strings.Contains(string(data), "=======") {
		t.Fatalf("маркеры конфликта остались: %q", data)
	}
	if string(data) != resolvedContent {
		t.Fatalf("f.txt = %q, want %q", data, resolvedContent)
	}

	// Worktree резолвина снят.
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), ".resolve-myrepo-task-1")); err == nil {
		t.Fatal("worktree резолвина не снят")
	}
}
