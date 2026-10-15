package workspace

import (
	"path/filepath"
	"testing"
)

func TestEpicTaskBranchesRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ws.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := r.Add(AddParams{Name: "mygit", Kind: KindGit, Root: dir,
		GitRemote: "git@x:y/z.git", GitBranch: "ai/mygit", GitBase: "main"}); err != nil {
		t.Fatal(err)
	}

	// Нет веток до создания.
	if _, err := r.EpicBranch("mygit", "epic-1"); err == nil {
		t.Fatal("ожидали ErrEpicBranchMissing")
	}
	if _, err := r.TaskBranch("mygit", "t1"); err == nil {
		t.Fatal("ожидали ErrTaskBranchMissing")
	}

	// Создание веток эпика и задачи.
	epicRef := BranchRef{Branch: "ai/epic/epic-1", Base: "main"}
	if err := r.SetEpicBranch("mygit", "epic-1", epicRef); err != nil {
		t.Fatalf("SetEpicBranch: %v", err)
	}
	taskRef := BranchRef{Branch: "ai/task/t1", Base: "ai/epic/epic-1"}
	if err := r.SetTaskBranch("mygit", "t1", taskRef); err != nil {
		t.Fatalf("SetTaskBranch: %v", err)
	}

	got, err := r.EpicBranch("mygit", "epic-1")
	if err != nil || got != epicRef {
		t.Fatalf("EpicBranch = %+v, %v; want %+v", got, err, epicRef)
	}
	got, err = r.TaskBranch("mygit", "t1")
	if err != nil || got != taskRef {
		t.Fatalf("TaskBranch = %+v, %v; want %+v", got, err, taskRef)
	}

	// Персистентность: переоткрытый реестр видит ветки.
	r2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err = r2.EpicBranch("mygit", "epic-1")
	if err != nil || got != epicRef {
		t.Fatalf("EpicBranch (переоткрытый) = %+v, %v", got, err)
	}
	got, err = r2.TaskBranch("mygit", "t1")
	if err != nil || got != taskRef {
		t.Fatalf("TaskBranch (переоткрытый) = %+v, %v", got, err)
	}

	// Удаление.
	if err := r.DeleteEpicBranch("mygit", "epic-1"); err != nil {
		t.Fatalf("DeleteEpicBranch: %v", err)
	}
	if err := r.DeleteTaskBranch("mygit", "t1"); err != nil {
		t.Fatalf("DeleteTaskBranch: %v", err)
	}
	if _, err := r.EpicBranch("mygit", "epic-1"); err == nil {
		t.Fatal("эпик-ветка не удалена из реестра")
	}
	if _, err := r.TaskBranch("mygit", "t1"); err == nil {
		t.Fatal("задача-ветка не удалена из реестра")
	}
}

func TestBranchesUnknownProjectRejected(t *testing.T) {
	r, _ := Open(filepath.Join(t.TempDir(), "ws.json"))
	if err := r.SetEpicBranch("nope", "e1", BranchRef{Branch: "x"}); err == nil {
		t.Fatal("несуществующий проект — ожидали ошибку")
	}
	if _, err := r.EpicBranch("nope", "e1"); err == nil {
		t.Fatal("несуществующий проект — ожидали ошибку")
	}
}

func TestBranchesEmptyIDsRejected(t *testing.T) {
	r, _ := Open(filepath.Join(t.TempDir(), "ws.json"))
	dir := t.TempDir()
	if _, err := r.Add(AddParams{Name: "g", Kind: KindGit, Root: dir, GitRemote: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetEpicBranch("g", "", BranchRef{Branch: "b"}); err == nil {
		t.Fatal("пустой ID эпика — ожидали ошибку")
	}
	if err := r.SetTaskBranch("g", "", BranchRef{Branch: "b"}); err == nil {
		t.Fatal("пустой ID задачи — ожидали ошибку")
	}
}

func TestTwoEpicsIndependentBranches(t *testing.T) {
	r, _ := Open(filepath.Join(t.TempDir(), "ws.json"))
	dir := t.TempDir()
	if _, err := r.Add(AddParams{Name: "g", Kind: KindGit, Root: dir, GitRemote: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetEpicBranch("g", "e1", BranchRef{Branch: "ai/epic/e1", Base: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetEpicBranch("g", "e2", BranchRef{Branch: "ai/epic/e2", Base: "main"}); err != nil {
		t.Fatal(err)
	}
	e1, _ := r.EpicBranch("g", "e1")
	e2, _ := r.EpicBranch("g", "e2")
	if e1.Branch == e2.Branch || e1.Branch != "ai/epic/e1" || e2.Branch != "ai/epic/e2" {
		t.Fatalf("ветки эпиков должны быть независимы: %+v / %+v", e1, e2)
	}
}
