package devopslead

import (
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
	// Лид документирует план в readme (WriteFiles/AppendFile), но не пишет код
	// (DeleteFiles/Run запрещены).
	for _, want := range []string{"List", "ReadFiles", "WriteFiles", "AppendFile"} {
		if !names[want] {
			t.Errorf("агент не включает инструмент %q", want)
		}
	}
	for _, disallowed := range []string{"DeleteFiles", "Run"} {
		if names[disallowed] {
			t.Errorf("агент не должен включать инструмент %q (лид не пишет код и не запускает команды)", disallowed)
		}
	}
}

func TestLeadCannotWriteThroughTools(t *testing.T) {
	d := newTestLead(t)
	// Запись вне readme через инструменты запрещена (readme-only scope):
	// WriteFiles возвращает статус "error" внутри JSON-результата.
	out, err := d.CallFunction("WriteFiles", map[string]any{
		"files": []map[string]string{{"filename": "x.yaml", "content": "services: {}\n"}},
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка инструмента: %v", err)
	}
	if !strings.Contains(string(out), `"status":"error"`) {
		t.Fatalf("запись не-readme файла должна вернуть статус error, got: %s", out)
	}
	if _, err := d.CallFunction("Run", map[string]any{"command": "docker compose config"}); err == nil {
		t.Fatal("лид не должен уметь запускать консольные команды через инструменты")
	}
}

// TestLeadWriteOnlyReadme: запись лида разрешена ТОЛЬКО в файлы readme* в корне
// проекта (документирование плана работ); код лид писать не должен.
func TestLeadWriteOnlyReadme(t *testing.T) {
	d := newTestLead(t)
	d.SetScope([]string{"k8s/"})
	if err := d.Write("README.md", "# План работ\n"); err != nil {
		t.Fatalf("запись в readme должна быть разрешена: %v", err)
	}
	if err := d.Write("readme.md", "# План работ\n"); err != nil {
		t.Fatalf("запись в readme* без учёта регистра должна быть разрешена: %v", err)
	}
	if err := d.Write("k8s/deploy.yaml", "apiVersion: v1\n"); err == nil {
		t.Fatal("запись вне readme должна быть отклонена (лид ведёт только readme)")
	}
	if err := d.Write("compose.yaml", "services: {}\n"); err == nil {
		t.Fatal("запись вне readme должна быть отклонена")
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
