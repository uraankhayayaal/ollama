package devops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestDevops создаёт DevOps-агента во временной директории.
func newTestDevops(t *testing.T) *Devops {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	return newDevops(dir, "", Config{})
}

func TestResolvePathRejectsTraversal(t *testing.T) {
	d := newTestDevops(t)
	for _, bad := range []string{"../evil.yaml", "a/../../etc/passwd", "/etc/passwd"} {
		if _, err := d.ResolvePath(bad); err == nil {
			t.Errorf("ResolvePath(%q) должен вернуть ошибку выхода за пределы OutputDir", bad)
		}
	}
}

func TestWriteWritesIntoOutputDir(t *testing.T) {
	d := newTestDevops(t)
	if err := d.Write("compose.yaml", "services: {}\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := filepath.Join(d.OutputDir, "compose.yaml")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("файл %q не создан: %v", want, err)
	}
}

func TestDevopsToolsAreSelected(t *testing.T) {
	d := newTestDevops(t)
	got := d.GetTools()
	names := map[string]bool{}
	for _, td := range got {
		names[td.Name] = true
	}
	for _, want := range []string{"WriteFiles", "ReadFiles", "DeleteFiles", "AppendFile", "List", "Run"} {
		if !names[want] {
			t.Errorf("агент не включает инструмент %q", want)
		}
	}
}

func TestConstrainScopePreventsWriteOutsideScope(t *testing.T) {
	d := newTestDevops(t)
	d.SetScope([]string{"k8s/"})
	if err := d.Write("k8s/deploy.yaml", "apiVersion: v1\n"); err != nil {
		t.Fatalf("запись внутри области работы должна быть разрешена: %v", err)
	}
	if err := d.Write("compose.yaml", "services: {}\n"); err == nil {
		t.Fatal("запись вне области работы должна быть отклонена")
	}
}

// TestDevopsPromptMentionsMakefile — Ф-4 PLAN-2026-09-24-todo-makefile.md:
// инфра-блок DevOps живёт в корневом Makefile: цели up/down/logs/ps,
// зеркальные infra.<цель> (docker compose run) и самозавершающийся e2e.
func TestDevopsPromptMentionsMakefile(t *testing.T) {
	d := newTestDevops(t)
	p := d.GetSystemMessages(nil)[0].Message
	for _, want := range []string{
		"Makefile", "infra.<цель>", "docker compose run -it --rm",
		"up", "down", "logs", "ps", "e2e",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт DevOps-инженера не содержит %q:\n%s", want, p)
		}
	}
}
