package workspace

import (
	"path/filepath"
	"testing"
)

func TestEpicTaskMRsRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ws.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := r.Add(AddParams{Name: "mygit", Kind: KindGit, Root: dir,
		GitRemote: "git@github.com:o/r.git", GitBranch: "ai/mygit", GitBase: "main"}); err != nil {
		t.Fatal(err)
	}

	// Нет MR до создания.
	if _, err := r.EpicMR("mygit", "epic-1"); err == nil {
		t.Fatal("ожидали ErrEpicMRMissing")
	}
	if _, err := r.TaskMR("mygit", "t1"); err == nil {
		t.Fatal("ожидали ErrTaskMRMissing")
	}

	epicMR := MRRef{URL: "https://github.com/o/r/pull/11", Source: "ai/epic/epic-1", Target: "main", State: "open"}
	if err := r.SetEpicMR("mygit", "epic-1", epicMR); err != nil {
		t.Fatalf("SetEpicMR: %v", err)
	}
	taskMR := MRRef{URL: "https://github.com/o/r/pull/12", Source: "ai/task/t1", Target: "ai/epic/epic-1", State: "open"}
	if err := r.SetTaskMR("mygit", "t1", taskMR); err != nil {
		t.Fatalf("SetTaskMR: %v", err)
	}

	got, err := r.EpicMR("mygit", "epic-1")
	if err != nil || got != epicMR {
		t.Fatalf("EpicMR = %+v, %v; want %+v", got, err, epicMR)
	}
	got, err = r.TaskMR("mygit", "t1")
	if err != nil || got != taskMR {
		t.Fatalf("TaskMR = %+v, %v; want %+v", got, err, taskMR)
	}

	// Персистентность: переоткрытый реестр видит MR.
	r2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err = r2.EpicMR("mygit", "epic-1")
	if err != nil || got != epicMR {
		t.Fatalf("EpicMR (переоткрытый) = %+v, %v", got, err)
	}
	got, err = r2.TaskMR("mygit", "t1")
	if err != nil || got != taskMR {
		t.Fatalf("TaskMR (переоткрытый) = %+v, %v", got, err)
	}

	// Удаление.
	if err := r.DeleteEpicMR("mygit", "epic-1"); err != nil {
		t.Fatalf("DeleteEpicMR: %v", err)
	}
	if err := r.DeleteTaskMR("mygit", "t1"); err != nil {
		t.Fatalf("DeleteTaskMR: %v", err)
	}
	if _, err := r.EpicMR("mygit", "epic-1"); err == nil {
		t.Fatal("MR эпика не удалён из реестра")
	}
	if _, err := r.TaskMR("mygit", "t1"); err == nil {
		t.Fatal("MR задачи не удалён из реестра")
	}
}

func TestMRsUnknownProjectRejected(t *testing.T) {
	r, _ := Open(filepath.Join(t.TempDir(), "ws.json"))
	if err := r.SetEpicMR("nope", "e1", MRRef{URL: "x"}); err == nil {
		t.Fatal("несуществующий проект — ожидали ошибку")
	}
	if _, err := r.EpicMR("nope", "e1"); err == nil {
		t.Fatal("несуществующий проект — ожидали ошибку")
	}
}

func TestMRsIndependentPerEpic(t *testing.T) {
	r, _ := Open(filepath.Join(t.TempDir(), "ws.json"))
	dir := t.TempDir()
	if _, err := r.Add(AddParams{Name: "g", Kind: KindGit, Root: dir, GitRemote: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetEpicMR("g", "e1", MRRef{URL: "http://mr/1"}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetEpicMR("g", "e2", MRRef{URL: "http://mr/2"}); err != nil {
		t.Fatal(err)
	}
	m1, _ := r.EpicMR("g", "e1")
	m2, _ := r.EpicMR("g", "e2")
	if m1.URL == m2.URL || m1.URL != "http://mr/1" || m2.URL != "http://mr/2" {
		t.Fatalf("MR эпиков должны быть независимы: %+v / %+v", m1, m2)
	}
}
