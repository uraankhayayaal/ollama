package logs

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	logpkg "ai/domain/log"
)

func mustDigest(t *testing.T, file, content string) logpkg.LogDigest {
	t.Helper()
	return logpkg.BuildLogDigest(file, content)
}

func TestBuildAnalysisFiltersAndDigest(t *testing.T) {
	dir := t.TempDir()
	own := "proj.log"
	writeFile(t, filepath.Join(dir, own),
		"INFO ok\nERROR upstream timeout id=1\nERROR upstream timeout id=2\nWARN slow\n")

	a, err := BuildAnalysis(dir, dir, own, Req{File: own, Level: "ERROR", Query: "upstream"}, 3)
	if err != nil {
		t.Fatalf("BuildAnalysis: %v", err)
	}
	if len(a.Files) != 1 || a.Files[0] != own {
		t.Fatalf("files = %v", a.Files)
	}
	if !strings.Contains(a.Note, "upstream") || !strings.Contains(a.Note, "ERROR") {
		t.Fatalf("note = %q", a.Note)
	}
	if len(a.Digest.Findings) != 1 {
		t.Fatalf("ожидалась одна склеенная находка: %+v", a.Digest.Findings)
	}
	if a.Digest.Findings[0].Count != 2 {
		t.Fatalf("частота = %d, want 2", a.Digest.Findings[0].Count)
	}
	if a.FilesLabel() != own {
		t.Fatalf("FilesLabel = %q", a.FilesLabel())
	}
}

func TestBuildAnalysisMissingFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "proj.log"), "INFO x\n")
	if _, err := BuildAnalysis(dir, dir, "proj.log", Req{File: "nope.log"}, 3); err == nil {
		t.Fatal("отсутствующий файл должен дать ошибку")
	}
}

func TestPickFiles(t *testing.T) {
	files := []Entry{
		{Name: "own.log", Modified: "2026-01-01 00:00:00"},
		{Name: "a.log", Modified: "2026-01-02 00:00:00"},
		{Name: "b.log", Modified: "2026-01-03 00:00:00"},
	}
	if got := PickFiles(files, "a.log", "own.log", 3); len(got) != 1 || got[0].Name != "a.log" {
		t.Fatalf("по имени: %+v", got)
	}
	if got := PickFiles(files, "nope.log", "own.log", 3); got != nil {
		t.Fatalf("несуществующий: %+v", got)
	}
	got := PickFiles(files, "", "own.log", 3)
	if len(got) != 3 || got[0].Name != "own.log" {
		t.Fatalf("all: файл проекта должен быть первым: %+v", got)
	}
	if got := PickFiles(files, "all", "own.log", 2); len(got) != 2 {
		t.Fatalf("cap max=2: %+v", got)
	}
}

func TestFilesLabel(t *testing.T) {
	if FilesLabel(nil) != "(файлы не выбраны)" {
		t.Fatalf("empty: %q", FilesLabel(nil))
	}
	if FilesLabel([]string{"a.log"}) != "a.log" {
		t.Fatalf("one: %q", FilesLabel([]string{"a.log"}))
	}
	if FilesLabel([]string{"a.log", "b.log"}) != "a.log, b.log" {
		t.Fatalf("many: %q", FilesLabel([]string{"a.log", "b.log"}))
	}
}

func TestDigestPromptContract(t *testing.T) {
	a := Analysis{
		Files:  []string{"proj.log"},
		Digest: mustDigest(t, "proj.log", "ERROR boom id=1\nERROR boom id=2\n"),
	}
	p := DigestPrompt(a)
	if !strings.Contains(p, "РАЗБОР ЛОГА ПРОЕКТА") || !strings.Contains(p, "ЦИТАТА-доказательство") {
		t.Fatalf("промпт без контракта: %q", p)
	}
	empty := DigestPrompt(Analysis{Files: []string{"proj.log"}})
	if !strings.Contains(empty, "Находок нет") {
		t.Fatalf("пустой дайджест: %q", empty)
	}
}

func TestCooldown(t *testing.T) {
	c := NewCooldown(50 * time.Millisecond)
	if c.Mark("k") {
		t.Fatal("первый разбор не должен подавляться")
	}
	if !c.Mark("k") {
		t.Fatal("повтор в пределах окна должен подавляться")
	}
	if c.Mark("other") {
		t.Fatal("другой ключ не подавляется")
	}
	time.Sleep(60 * time.Millisecond)
	if c.Mark("k") {
		t.Fatal("после окна разбор не должен подавляться")
	}
}

func TestDirs(t *testing.T) {
	root := t.TempDir()
	if got := Dirs(root, "/global"); len(got) != 1 || got[0] != "/global" {
		t.Fatalf("без logs/: %+v", got)
	}
	writeFile(t, filepath.Join(root, "logs", "app.log"), "x\n")
	got := Dirs(root, "/global")
	if len(got) != 2 || got[1] != filepath.Join(root, "logs") {
		t.Fatalf("с logs/: %+v", got)
	}
	if got := Dirs("", "/global"); len(got) != 1 {
		t.Fatalf("пустой root: %+v", got)
	}
}
