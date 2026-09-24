package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"ai/projects"
)

func TestDefaultPathEnvOverride(t *testing.T) {
	t.Setenv("AI_WORKSPACES", "/some/where/ws.json")
	if got := DefaultPath(); got != "/some/where/ws.json" {
		t.Fatalf("DefaultPath() = %q, want override", got)
	}
	t.Setenv("AI_WORKSPACES", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got := DefaultPath(); got == "" || filepath.Dir(got) != home {
		t.Fatalf("DefaultPath() = %q, want под домашним каталогом", got)
	}
}

func TestOpenMissingFileIsEmpty(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "nope", "ws.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(r.List()) != 0 {
		t.Fatal("ожидали пустой реестр")
	}
}

func TestAddTempForcesProjectDir(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	name := "ws-temp-probe"
	in, err := r.Add(AddParams{Name: name, Kind: KindTemp})
	if err != nil {
		t.Fatalf("Add temp: %v", err)
	}
	want := projects.ProjectDir(name)
	if in.Root != want {
		t.Fatalf("Root = %q, want %q", in.Root, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("temp-каталог не создан: %v", err)
	}
	got, err := r.Root(name)
	if err != nil || got != want {
		t.Fatalf("Root(%q) = %q, %v", name, got, err)
	}
}

func TestAddDirRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ws.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	in, err := r.Add(AddParams{Name: "mydir", Kind: KindDir, Root: dir, Confirm: true})
	if err != nil {
		t.Fatalf("Add dir: %v", err)
	}
	abs, _ := filepath.Abs(dir)
	if in.Root != filepath.Clean(abs) {
		t.Fatalf("Root = %q, want %q", in.Root, filepath.Clean(abs))
	}
	if in.Kind != KindDir {
		t.Fatalf("Kind = %q", in.Kind)
	}

	// List + Get.
	list := r.List()
	if len(list) != 1 || list[0].Name != "mydir" {
		t.Fatalf("List = %+v", list)
	}
	if _, err := r.Get("mydir"); err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Персистентность: новый реестр на том же файле видит запись.
	r2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r2.Get("mydir"); err != nil {
		t.Fatalf("Get из переоткрытого реестра: %v", err)
	}

	// Remove + персистентность удаления.
	if err := r.Remove("mydir"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	r3, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r3.Get("mydir"); err == nil {
		t.Fatal("запись не удалена из файла")
	}
}

func TestAddDuplicateName(t *testing.T) {
	r, _ := Open(filepath.Join(t.TempDir(), "ws.json"))
	_ = mustDir(t, r, "dup", true)
	if _, err := r.Add(AddParams{Name: "dup", Kind: KindDir, Root: t.TempDir(), Confirm: true}); err == nil {
		t.Fatal("ожидали ErrExists")
	}
}

func TestAddNestedGitSubmoduleOnlyWithRegisteredParent(t *testing.T) {
	r, _ := Open(filepath.Join(t.TempDir(), "ws.json"))
	parentRoot := t.TempDir()
	childRoot := filepath.Join(parentRoot, "packages", "auth")
	if err := os.MkdirAll(childRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Add(AddParams{Name: "app", Kind: KindGit, Root: parentRoot, GitRemote: "https://example.test/app.git"}); err != nil {
		t.Fatal(err)
	}
	child, err := r.Add(AddParams{Name: "app--packages--auth", Kind: KindGit, Root: childRoot,
		GitRemote: "https://example.test/auth.git", GitTarget: "main", Parent: "app"})
	if err != nil {
		t.Fatalf("register child: %v", err)
	}
	if child.Parent != "app" {
		t.Fatalf("Parent=%q", child.Parent)
	}
	if err := r.SetProjectMR(child.Name, MRRef{URL: "https://example.test/mr/1", Source: "feature", Target: "main", State: "open"}); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(r.Path())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.Get(child.Name)
	if err != nil || loaded.Parent != "app" || loaded.GitTarget != "main" {
		t.Fatalf("loaded child=%+v err=%v", loaded, err)
	}
	if mr, err := reopened.ProjectMR(child.Name); err != nil || mr.URL == "" {
		t.Fatalf("ProjectMR=%+v err=%v", mr, err)
	}
	other := filepath.Join(parentRoot, "packages", "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Add(AddParams{Name: "orphan", Kind: KindGit, Root: other, GitRemote: "https://example.test/o.git"}); !errors.Is(err, ErrNested) {
		t.Fatalf("unparented nested repo error=%v", err)
	}
}

func TestAddValidation(t *testing.T) {
	r, _ := Open(filepath.Join(t.TempDir(), "ws.json"))

	if _, err := r.Add(AddParams{Name: "", Kind: KindDir}); err == nil {
		t.Fatal("пустое имя должно отклоняться")
	}
	for _, bad := range []string{"a/b", `a\b`, "..", "."} {
		if _, err := r.Add(AddParams{Name: bad, Kind: KindDir}); err == nil {
			t.Fatalf("имя %q должно отклоняться", bad)
		}
	}
	if _, err := r.Add(AddParams{Name: "nofile", Kind: KindDir, Root: "/нет-такого", Confirm: true}); !errors.Is(err, ErrRootMissing) {
		t.Fatalf("несуществующий каталог: err = %v, want ErrRootMissing", err)
	}
	f := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Add(AddParams{Name: "isfile", Kind: KindDir, Root: f, Confirm: true}); !errors.Is(err, ErrRootNotDir) {
		t.Fatalf("файл вместо каталога: err = %v, want ErrRootNotDir", err)
	}
}

func TestForbiddenRoots(t *testing.T) {
	r, _ := Open(filepath.Join(t.TempDir(), "ws.json"))

	cases := map[string]string{
		"корень ФС":     "/",
		"корень модуля": projects.ModuleRoot(),
		"корень temp":   projects.ProjectDir(""),
	}
	for label, root := range cases {
		if _, err := r.Add(AddParams{Name: "forbidden", Kind: KindDir, Root: root, Confirm: true}); err != ErrForbidden {
			t.Fatalf("%s: err = %v, want ErrForbidden", label, err)
		}
	}

	home, _ := os.UserHomeDir()
	if _, err := r.Add(AddParams{Name: "home", Kind: KindDir, Root: home, Confirm: true}); err != ErrForbidden {
		t.Fatalf("домашний каталог: err = %v, want ErrForbidden", err)
	}
}

func TestNeedConfirm(t *testing.T) {
	r, _ := Open(filepath.Join(t.TempDir(), "ws.json"))
	dir := t.TempDir()
	if _, err := r.Add(AddParams{Name: "noconfirm", Kind: KindDir, Root: dir}); err != ErrNeedConfirm {
		t.Fatalf("err = %v, want ErrNeedConfirm", err)
	}
}

func TestGitRequiresRemote(t *testing.T) {
	r, _ := Open(filepath.Join(t.TempDir(), "ws.json"))
	dir := t.TempDir()
	if _, err := r.Add(AddParams{Name: "nogit", Kind: KindGit, Root: dir}); err != ErrGitRemote {
		t.Fatalf("err = %v, want ErrGitRemote", err)
	}
	dir2 := t.TempDir()
	if _, err := r.Add(AddParams{Name: "git", Kind: KindGit, Root: dir2, GitRemote: "git@x:y/z.git"}); err != nil {
		t.Fatalf("валидный git-проект: %v", err)
	}
}

func TestNesting(t *testing.T) {
	r, _ := Open(filepath.Join(t.TempDir(), "ws.json"))
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(elsewhere, "second"), 0o755); err != nil {
		t.Fatal(err)
	}

	// parent регистрируем первым — child вложен → ErrNested.
	if _, err := r.Add(AddParams{Name: "parent", Kind: KindDir, Root: parent, Confirm: true}); err != nil {
		t.Fatalf("Add parent: %v", err)
	}
	if _, err := r.Add(AddParams{Name: "child", Kind: KindDir, Root: child, Confirm: true}); !errors.Is(err, ErrNested) {
		t.Fatalf("вложенный каталог: err = %v, want ErrNested", err)
	}

	// Отдельный реестр: child регистрируем первым — parent содержит его → ErrContains.
	r2, _ := Open(filepath.Join(t.TempDir(), "ws2.json"))
	if _, err := r2.Add(AddParams{Name: "child", Kind: KindDir, Root: child, Confirm: true}); err != nil {
		t.Fatalf("Add child: %v", err)
	}
	if _, err := r2.Add(AddParams{Name: "parent", Kind: KindDir, Root: parent, Confirm: true}); !errors.Is(err, ErrContains) {
		t.Fatalf("родитель содержит зарегистрированный: err = %v, want ErrContains", err)
	}
}

func TestFindByRoot(t *testing.T) {
	r, _ := Open(filepath.Join(t.TempDir(), "ws.json"))
	dir := t.TempDir()
	if _, ok := r.FindByRoot(dir); ok {
		t.Fatal("незарегистрированный путь найден")
	}
	if _, err := r.Add(AddParams{Name: "other", Kind: KindTemp}); err != nil {
		t.Fatalf("Add temp other: %v", err)
	}
	if in, ok := r.FindByRoot(projects.ProjectDir("other")); !ok || in.Name != "other" {
		t.Fatalf("FindByRoot(temp other) = %+v, %v", in, ok)
	}
}

func mustDir(t *testing.T, r *Registry, name string, confirm bool) Info {
	t.Helper()
	dir := t.TempDir()
	in, err := r.Add(AddParams{Name: name, Kind: KindDir, Root: dir, Confirm: confirm})
	if err != nil {
		t.Fatalf("Add %s: %v", name, err)
	}
	return in
}
