// Тесты use-case открытия git-проекта (application/gitflow/open.go): правила
// имени/адреса, авточистка полу-клона и порядок клона+регистрации.
package gitflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai/infrastructure/git"
	"ai/workspace"
)

// fakeExec — hermetic-исполнитель git: префиксные ответы/ошибки и запись команд.
type fakeExec struct {
	calls  []string
	starts map[string]string
	fails  map[string]string
}

func (f *fakeExec) Exec(_ context.Context, dir string, argv ...string) (string, error) {
	cmd := strings.Join(argv, " ")
	f.calls = append(f.calls, cmd)
	for prefix, msg := range f.fails {
		if strings.HasPrefix(cmd, prefix) {
			return "", errors.New(msg)
		}
	}
	for prefix, out := range f.starts {
		if strings.HasPrefix(cmd, prefix) {
			return out, nil
		}
	}
	return "", nil
}

// fakeGitOpener — адаптер GitOpener поверх hermetic-исполнителя (как
// serverGitOpener): клон/коммит делегируются реальным git-функциям над fakeExec.
type fakeGitOpener struct{ ex *fakeExec }

func (o fakeGitOpener) Exec(ctx context.Context, dir string, argv ...string) (string, error) {
	return o.ex.Exec(ctx, dir, argv...)
}

func (o fakeGitOpener) Clone(ctx context.Context, url, branch, dest string) (ClonedRepo, error) {
	repo, err := git.Clone(ctx, o.ex, url, branch, dest)
	if err != nil {
		return ClonedRepo{}, err
	}
	out := ClonedRepo{Root: repo.Root, Remote: repo.Remote, Branch: repo.Branch, Base: repo.Base}
	for _, sub := range repo.Submodules {
		out.Submodules = append(out.Submodules, ClonedSubmodule{
			Path: sub.Path, Root: sub.Root, Remote: sub.Remote,
			Branch: sub.Branch, Base: sub.Base, DefaultBranch: sub.DefaultBranch,
		})
	}
	return out, nil
}

func (o fakeGitOpener) HasCommit(ctx context.Context, dir, ref string) bool {
	return git.HasCommit(ctx, o.ex, dir, ref)
}

func (f *fakeExec) saw(sub string) bool {
	for _, c := range f.calls {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

// fakeOpenRegistry — реестр проектов без Redis.
type fakeOpenRegistry struct {
	infos  map[string]workspace.Info
	added  []workspace.AddParams
	addErr error
}

func (r *fakeOpenRegistry) Get(name string) (workspace.Info, error) {
	if inf, ok := r.infos[name]; ok {
		return inf, nil
	}
	return workspace.Info{}, errors.New("нет проекта " + name)
}

func (r *fakeOpenRegistry) Add(p workspace.AddParams) (workspace.Info, error) {
	if r.addErr != nil {
		return workspace.Info{}, r.addErr
	}
	r.added = append(r.added, p)
	return workspace.Info{Name: p.Name, Kind: p.Kind, Root: p.Root, GitRemote: p.GitRemote, GitBranch: p.GitBranch, GitBase: p.GitBase}, nil
}

func TestGitProjectName(t *testing.T) {
	cases := map[string]string{
		"git@gitlab.com:group/proj.git":          "proj",
		"git@gitlab.com:g1/g2/proj.git":          "proj",
		"https://github.com/owner/repo.git":      "repo",
		"https://gitlab.com/a/b/c/name":          "name",
		"https://host/inner/deep/repo.git?x=1#y": "repo",
		"":                                       "",
	}
	for in, want := range cases {
		if got := GitProjectName(in); got != want {
			t.Fatalf("GitProjectName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSubmoduleProjectName(t *testing.T) {
	if got := SubmoduleProjectName("app", "packages/auth"); got != "app--packages~sauth" {
		t.Fatalf("SubmoduleProjectName = %q", got)
	}
	if SubmoduleProjectName("app", "packages~sauth") == SubmoduleProjectName("app", "packages/auth") {
		t.Fatal("экранирование ~ и / не различает пути")
	}
}

func TestSameGitRemote(t *testing.T) {
	if !SameGitRemote("git@x:y/z.git", "git@x:y/z") {
		t.Fatal("разные формы одного адреса должны совпадать")
	}
	if SameGitRemote("git@x:y/z", "https://x/y/z") {
		t.Fatal("разные схемы — разные адреса")
	}
}

func TestOpenBadURL(t *testing.T) {
	o := &ProjectOpener{Host: fakeGitOpener{&fakeExec{}}, Registry: &fakeOpenRegistry{}, ProjectDir: func(string) string { return "" }}
	if _, err := o.Open(context.Background(), "git@host:"); !errors.Is(err, ErrGitURLName) {
		t.Fatalf("err = %v, want ErrGitURLName", err)
	}
}

func TestOpenExistingIsIdempotent(t *testing.T) {
	reg := &fakeOpenRegistry{infos: map[string]workspace.Info{
		"repo": {Name: "repo", Kind: workspace.KindGit, GitRemote: "git@x:y/repo.git"},
	}}
	ex := &fakeExec{}
	o := &ProjectOpener{Host: fakeGitOpener{ex}, Registry: reg, ProjectDir: func(string) string { return t.TempDir() }}
	got, err := o.Open(context.Background(), "git@x:y/repo.git")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got.Name != "repo" || got.Info.GitRemote != "git@x:y/repo.git" {
		t.Fatalf("got %+v", got)
	}
	if ex.saw("clone") {
		t.Fatalf("существующий проект не клонируется: %v", ex.calls)
	}
}

func TestOpenForeignDirRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("user data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ex := &fakeExec{
		starts: map[string]string{
			"git rev-parse --git-dir":   ".git\n",
			"git remote get-url origin": "git@x:y/other.git\n",
			"git status --porcelain":    "",
		},
	}
	reg := &fakeOpenRegistry{}
	o := &ProjectOpener{Host: fakeGitOpener{ex}, Registry: reg, ProjectDir: func(string) string { return dir }}
	_, err := o.Open(context.Background(), "git@x:y/repo.git")
	if !errors.Is(err, ErrDirNotEmpty) {
		t.Fatalf("err = %v, want ErrDirNotEmpty", err)
	}
	if ex.saw("clone") {
		t.Fatalf("чужой каталог клонировать нельзя: %v", ex.calls)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "keep.txt")); statErr != nil {
		t.Fatalf("содержимое каталога уничтожено: %v", statErr)
	}
}

func TestOpenDiscardsHalfClone(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ex := &fakeExec{
		starts: map[string]string{
			"git rev-parse --git-dir":             ".git\n",
			"git remote get-url origin":           "git@x:y/repo.git\n",
			"git status --porcelain":              "",
			"git rev-parse --abbrev-ref HEAD":     "main\n",
			"git rev-parse --verify --quiet main": "abc123\n",
		},
		fails: map[string]string{
			"git rev-parse --verify --quiet HEAD": "fatal: Needed a single revision",
		},
	}
	t.Setenv("GITOPS_SUBMODULES", "0")
	reg := &fakeOpenRegistry{}
	o := &ProjectOpener{Host: fakeGitOpener{ex}, Registry: reg, ProjectDir: func(string) string { return dir }}
	got, err := o.Open(context.Background(), "git@x:y/repo.git")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !ex.saw("clone") {
		t.Fatalf("после авточистки должен быть клон: %v", ex.calls)
	}
	if len(reg.added) != 1 || got.Info.GitBase != "main" {
		t.Fatalf("регистрация: added=%+v info=%+v", reg.added, got.Info)
	}
}

func TestOpenCloneFailureRemovesDest(t *testing.T) {
	dir := t.TempDir()
	ex := &fakeExec{fails: map[string]string{"git clone": "fatal: could not read from remote"}}
	t.Setenv("GITOPS_SUBMODULES", "0")
	reg := &fakeOpenRegistry{}
	o := &ProjectOpener{Host: fakeGitOpener{ex}, Registry: reg, ProjectDir: func(string) string { return dir }}
	_, err := o.Open(context.Background(), "git@x:y/repo.git")
	if !errors.Is(err, ErrCloneFailed) {
		t.Fatalf("err = %v, want ErrCloneFailed", err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("полу-клон не убран: %v", statErr)
	}
	if len(reg.added) != 0 {
		t.Fatalf("проект не должен регистрироваться: %+v", reg.added)
	}
}

func TestOpenRegisterFailure(t *testing.T) {
	dir := t.TempDir()
	ex := &fakeExec{starts: map[string]string{
		"git rev-parse --abbrev-ref HEAD": "main\n",
		"git rev-parse --verify":          "abc123\n",
	}}
	t.Setenv("GITOPS_SUBMODULES", "0")
	reg := &fakeOpenRegistry{addErr: errors.New("редis недоступен")}
	o := &ProjectOpener{Host: fakeGitOpener{ex}, Registry: reg, ProjectDir: func(string) string { return dir }}
	_, err := o.Open(context.Background(), "git@x:y/repo.git")
	if !errors.Is(err, ErrRegisterFailed) {
		t.Fatalf("err = %v, want ErrRegisterFailed", err)
	}
}
