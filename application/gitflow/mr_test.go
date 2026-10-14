package gitflow

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeGateway — заглушка MRGateway: фиксирует порядок вызовов и позволяет
// подсунуть ошибки/число коммитов.
type fakeGateway struct {
	order     *[]string
	count     int
	countErr  error
	ensureErr error
	pushErr   error
}

func (g *fakeGateway) CountCommits(_ context.Context, base, branch string) (int, error) {
	*g.order = append(*g.order, "count:"+base+".."+branch)
	return g.count, g.countErr
}

func (g *fakeGateway) EnsureRemoteBranch(_ context.Context, base string) error {
	*g.order = append(*g.order, "ensure:"+base)
	return g.ensureErr
}

func (g *fakeGateway) PushBranch(_ context.Context, branch, base string) error {
	*g.order = append(*g.order, "push:"+branch)
	return g.pushErr
}

// fakeForge — заглушка Forge.
type fakeForge struct {
	order *[]string
	url   string
	err   error
	opts  []MROptions
}

func (f *fakeForge) CreateMergeRequest(_ context.Context, o MROptions) (string, error) {
	*f.order = append(*f.order, "create")
	f.opts = append(f.opts, o)
	return f.url, f.err
}

// fakeRegistry — заглушка MRRegistry.
type fakeRegistry struct {
	taskMRs map[string]MR
	epicMRs map[string]MR
	err     error
}

func (r *fakeRegistry) SetTaskMR(id string, mr MR) error {
	if r.err != nil {
		return r.err
	}
	r.taskMRs[id] = mr
	return nil
}

func (r *fakeRegistry) SetEpicMR(id string, mr MR) error {
	if r.err != nil {
		return r.err
	}
	r.epicMRs[id] = mr
	return nil
}

type creatorFixture struct {
	c     MRCreator
	gw    *fakeGateway
	forge *fakeForge
	reg   *fakeRegistry
	order []string
}

func newCreatorFixture(count int) *creatorFixture {
	order := &[]string{}
	gw := &fakeGateway{order: order, count: count}
	forge := &fakeForge{order: order, url: "https://host/mr/1"}
	reg := &fakeRegistry{taskMRs: map[string]MR{}, epicMRs: map[string]MR{}}
	return &creatorFixture{
		c:     MRCreator{Gateway: gw, Forge: forge, Registry: reg},
		gw:    gw,
		forge: forge,
		reg:   reg,
		order: *order,
	}
}

func (f *creatorFixture) fixtureOrder() []string { return *f.gw.order }

// Успешный путь MR задачи: проверка коммитов → база в remote → push → create →
// запись в реестр (ровно в этом порядке).
func TestCreateTaskMRHappyPath(t *testing.T) {
	f := newCreatorFixture(2)
	url, err := f.c.CreateTaskMR(context.Background(), "T1", "Задача T1: t", "desc", "ai/task/T1", "ai/epic/E1")
	if err != nil || url != "https://host/mr/1" {
		t.Fatalf("ожидался URL MR, получено (%q, %v)", url, err)
	}
	want := []string{"count:ai/epic/E1..ai/task/T1", "ensure:ai/epic/E1", "push:ai/task/T1", "create"}
	if got := f.fixtureOrder(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("порядок вызовов: %v, ожидался %v", got, want)
	}
	mr := f.reg.taskMRs["T1"]
	if mr.URL != url || mr.Source != "ai/task/T1" || mr.Target != "ai/epic/E1" || mr.State != "open" {
		t.Fatalf("MR в реестре неверен: %+v", mr)
	}
	if o := f.forge.opts[0]; o.Title != "Задача T1: t" || o.Description != "desc" || o.SourceBranch != "ai/task/T1" || o.TargetBranch != "ai/epic/E1" {
		t.Fatalf("опции MR неверны: %+v", o)
	}
}

// Пустая ветка (0 коммитов) — ErrEmptyBranch и НИКАКИХ push/create.
func TestCreateTaskMREmptyBranch(t *testing.T) {
	f := newCreatorFixture(0)
	_, err := f.c.CreateTaskMR(context.Background(), "T1", "t", "", "ai/task/T1", "ai/epic/E1")
	if !errors.Is(err, ErrEmptyBranch) {
		t.Fatalf("ожидался ErrEmptyBranch, получено %v", err)
	}
	for _, o := range f.fixtureOrder() {
		if strings.HasPrefix(o, "push") || o == "create" {
			t.Fatalf("при пустой ветке не должно быть %q", o)
		}
	}
}

// Ошибка git при подсчёте коммитов — не повод блокировать MR.
func TestCreateTaskMRCountErrorProceeds(t *testing.T) {
	f := newCreatorFixture(0)
	f.gw.countErr = errors.New("boom")
	url, err := f.c.CreateTaskMR(context.Background(), "T1", "t", "", "ai/task/T1", "ai/epic/E1")
	if err != nil || url == "" {
		t.Fatalf("ошибка подсчёта не должна блокировать MR: (%q, %v)", url, err)
	}
}

// Сбой подготовки базы MR прерывает цепочку до push.
func TestCreateTaskMREnsureError(t *testing.T) {
	f := newCreatorFixture(1)
	f.gw.ensureErr = errors.New("remote недоступен")
	_, err := f.c.CreateTaskMR(context.Background(), "T1", "t", "", "ai/task/T1", "ai/epic/E1")
	if err == nil || !strings.Contains(err.Error(), "подготовка базы MR") {
		t.Fatalf("ожидалась ошибка подготовки базы, получено %v", err)
	}
	for _, o := range f.fixtureOrder() {
		if o == "create" {
			t.Fatalf("MR не должен создаваться после сбоя базы")
		}
	}
}

// Ошибка форджа не пишет MR в реестр.
func TestCreateTaskMRForgeError(t *testing.T) {
	f := newCreatorFixture(1)
	f.forge.err = errors.New("422")
	_, err := f.c.CreateTaskMR(context.Background(), "T1", "t", "", "ai/task/T1", "ai/epic/E1")
	if err == nil || !strings.Contains(err.Error(), "создание MR задачи") {
		t.Fatalf("ожидалась ошибка форджа, получено %v", err)
	}
	if len(f.reg.taskMRs) != 0 {
		t.Fatalf("реестр не должен обновляться при сбое форджа")
	}
}

// Ошибка записи реестра пробрасывается.
func TestCreateTaskMRRegistryError(t *testing.T) {
	f := newCreatorFixture(1)
	f.reg.err = errors.New("диск")
	_, err := f.c.CreateTaskMR(context.Background(), "T1", "t", "", "ai/task/T1", "ai/epic/E1")
	if err == nil || !strings.Contains(err.Error(), "ошибка записи MR в реестр") {
		t.Fatalf("ожидалась ошибка реестра, получено %v", err)
	}
}

// MR эпика: без ensureRemoteBranch (база main уже в remote), но с push и
// записью SetEpicMR.
func TestCreateEpicMRHappyPath(t *testing.T) {
	f := newCreatorFixture(3)
	url, err := f.c.CreateEpicMR(context.Background(), "E1", "Эпик E1", "эпик", "ai/epic/E1", "main")
	if err != nil || url != "https://host/mr/1" {
		t.Fatalf("ожидался URL MR, получено (%q, %v)", url, err)
	}
	want := []string{"count:main..ai/epic/E1", "push:ai/epic/E1", "create"}
	if got := f.fixtureOrder(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("порядок вызовов: %v, ожидался %v", got, want)
	}
	if mr := f.reg.epicMRs["E1"]; mr.URL != url || mr.Target != "main" || mr.State != "open" {
		t.Fatalf("MR эпика в реестре неверен: %+v", mr)
	}
}

// MR эпика с пустой релизной веткой не создаётся.
func TestCreateEpicMREmptyBranch(t *testing.T) {
	f := newCreatorFixture(0)
	_, err := f.c.CreateEpicMR(context.Background(), "E1", "Эпик E1", "", "ai/epic/E1", "main")
	if !errors.Is(err, ErrEmptyBranch) {
		t.Fatalf("ожидался ErrEmptyBranch, получено %v", err)
	}
	if len(f.reg.epicMRs) != 0 {
		t.Fatalf("реестр не должен обновляться")
	}
}
