package devopslead

import (
	"os"
	"path/filepath"
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
	for _, want := range []string{"List", "ReadFiles"} {
		if !names[want] {
			t.Errorf("агент не включает инструмент %q", want)
		}
	}
	for _, disallowed := range []string{"WriteFiles", "DeleteFiles", "AppendFile", "Run"} {
		if names[disallowed] {
			t.Errorf("агент не должен включать инструмент %q (лид не пишет код и не запускает команды)", disallowed)
		}
	}
}

func TestLeadCannotWriteThroughTools(t *testing.T) {
	d := newTestLead(t)
	if _, err := d.CallFunction("WriteFiles", map[string]any{
		"files": []map[string]string{{"filename": "x.yaml", "content": "services: {}\n"}},
	}); err == nil {
		t.Fatal("лид не должен уметь писать файлы через инструменты")
	}
	if _, err := d.CallFunction("Run", map[string]any{"command": "docker compose config"}); err == nil {
		t.Fatal("лид не должен уметь запускать консольные команды через инструменты")
	}
}

func TestLeadConstrainScopePreventsWriteOutsideScope(t *testing.T) {
	d := newTestLead(t)
	d.SetScope([]string{"k8s/"})
	if err := d.Write("k8s/deploy.yaml", "apiVersion: v1\n"); err != nil {
		t.Fatalf("запись внутри области работы должна быть разрешена: %v", err)
	}
	if err := d.Write("compose.yaml", "services: {}\n"); err == nil {
		t.Fatal("запись вне области работы должна быть отклонена")
	}
}
