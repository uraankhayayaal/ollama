package devopslead

import (
	"ai/agents"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestLead создаёт DevOps Lead во временной директории.
func newTestLead(t *testing.T) *DevopsLead {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	return newDevopsLead(dir, "", Config{})
}

func TestLeadResolvePathRejectsTraversal(t *testing.T) {
	d := newTestLead(t)
	for _, bad := range []string{"../evil.yaml", "a/../../etc/passwd", "/etc/passwd"} {
		if _, err := d.ResolvePath(bad); err == nil {
			t.Errorf("ResolvePath(%q) должен вернуть ошибку выхода за пределы OutputDir", bad)
		}
	}
}

func TestLeadToolsAreSelected(t *testing.T) {
	d := newTestLead(t)
	got := d.GetTools()
	names := map[string]bool{}
	for _, td := range got {
		names[td.Name] = true
	}
	// 5.1: лид пишет скелетон (WriteFiles/AppendFile) и гоняет проверку/git
	// через Run; удаления файлов у него нет (реализацию не трогает).
	for _, want := range []string{"List", "ReadFiles", "WriteFiles", "AppendFile", "Run"} {
		if !names[want] {
			t.Errorf("агент не включает инструмент %q", want)
		}
	}
	if names["DeleteFiles"] {
		t.Error("агент не должен включать DeleteFiles (лид не удаляет файлы проекта)")
	}
}

func TestLeadCannotWriteThroughTools(t *testing.T) {
	d := newTestLead(t)
	// Запись вне allowlist через инструменты запрещена (гард в инструменте):
	// WriteFiles возвращает статус "error" внутри JSON-результата.
	out, err := d.CallFunction("WriteFiles", map[string]any{
		"files": []map[string]string{{"filename": "x.yaml", "content": "services: {}\n"}},
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка инструмента: %v", err)
	}
	if !strings.Contains(string(out), `"status":"error"`) {
		t.Fatalf("запись вне allowlist должна вернуть статус error, got: %s", out)
	}
	// Файл разрешённой директории пишется штатно (скелетон в ./cicd).
	if _, err := d.CallFunction("WriteFiles", map[string]any{
		"files": []map[string]string{{"filename": "cicd/pipeline.yaml", "content": "stages: []\n"}},
	}); err != nil {
		t.Fatalf("запись скелетона в cicd: %v", err)
	}
	if _, err := os.Stat(filepath.Join(d.OutputDir, "cicd", "pipeline.yaml")); err != nil {
		t.Fatalf("скелетон cicd/pipeline.yaml не создан: %v", err)
	}
}

// TestLeadWriteSkeletonAllowlist (5.1): readme-only снят — лид пишет скелетон
// по allowlist (директории 1-го уровня и корневые файлы), всё остальное
// запись отклоняет (гард в инструменте, а не только в промпте).
func TestLeadWriteSkeletonAllowlist(t *testing.T) {
	d := newTestLead(t)
	for _, ok := range []string{
		"README.md", "readme.md", "AGENTS.md",
		"compose.yaml", "Makefile",
		"cicd/pipeline.yaml",
		"docs/runbook.md",
	} {
		if err := d.Write(ok, "skel\n"); err != nil {
			t.Errorf("запись %q должна быть разрешена allowlist: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"k8s/deploy.yaml",
		"main.go",
		"x.yaml",
	} {
		if err := d.Write(bad, "x\n"); err == nil {
			t.Errorf("запись %q вне allowlist должна быть отклонена", bad)
		}
	}
}

// TestLeadPromptSkeletonRules (5.3): системный промпт лида ведёт скелетон
// (не «не пишешь код»), знает про allowlist и Run и несёт общий Run-фрагмент
// компактного вывода.
func TestLeadPromptSkeletonRules(t *testing.T) {
	d := newTestLead(t)
	p := d.GetSystemMessages(nil)[0].Message
	for _, want := range []string{
		"скелетон", "СКЕЛЕТОН", "allowlist", "Run", "ВЕТКА ЭПИКА",
		agents.RunTokenEconomy,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт девопс-лида не содержит %q", want)
		}
	}
	if strings.Contains(p, "Ты НЕ пишешь код") {
		t.Error("промпт лида не должен запрещать код: роль — скелетон")
	}
}

// TestLeadPromptMentionsMakefile — Ф-2/Ф-4 PLAN-2026-09-24-todo-makefile.md:
// лид DevOps считает корневой Makefile источником команд, требует наличия
// эпика «Makefile проекта» (dependencies) и зеркальных инфра-целей.
func TestLeadPromptMentionsMakefile(t *testing.T) {
	d := newTestLead(t)
	p := d.GetSystemMessages(nil)[0].Message
	for _, want := range []string{
		"Makefile", "make test", "make build",
		"infra.<цель>", "docker compose run -it --rm",
		"e2e", "up → проверки → down",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт девопс-лида не содержит %q:\n%s", want, p)
		}
	}
}
