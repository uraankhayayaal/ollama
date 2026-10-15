package gitflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai/domain"
	"ai/domain/services"
)

// fakeDeveloper — заглушка gitflow.Developer: запоминает промпты и отдаёт
// сценарий ответа LLM. Агента не строит (nil), SetOutputDir не вызывается.
type fakeDeveloper struct {
	prompts   []string
	project   string
	role      string
	responses []*domain.AgentResponse
	errs      []error
	calls     int
}

func (f *fakeDeveloper) SpecialistForRole(project, role, prompt string) services.Agent {
	f.project, f.role = project, role
	f.prompts = append(f.prompts, prompt)
	return nil
}

func (f *fakeDeveloper) Generate(_ context.Context, _ services.Agent) (*domain.AgentResponse, error) {
	i := f.calls
	f.calls++
	var resp *domain.AgentResponse
	if i < len(f.responses) {
		resp = f.responses[i]
	} else {
		resp = &domain.AgentResponse{}
	}
	var err error
	if i < len(f.errs) {
		err = f.errs[i]
	}
	return resp, err
}

// fakeAcceptor — заглушка приёмки: выдаёт заранее заданную серию отчётов,
// затем approve.
type fakeAcceptor struct {
	reports []*domain.Report
	dirs    []string
	i       int
}

func (f *fakeAcceptor) Accept(dir string) *domain.Report {
	f.dirs = append(f.dirs, dir)
	if f.i < len(f.reports) {
		r := f.reports[f.i]
		f.i++
		return r
	}
	return &domain.Report{Verdict: domain.VerdictApprove}
}

// fakeStore — заглушка доски: фиксирует снятие конфликтных полей.
type fakeStore struct {
	epics []*domain.Epic
	tasks []*domain.Task
}

func (f *fakeStore) SaveEpic(_ context.Context, e *domain.Epic) error {
	f.epics = append(f.epics, e)
	return nil
}

func (f *fakeStore) SaveTask(_ context.Context, t *domain.Task) error {
	f.tasks = append(f.tasks, t)
	return nil
}

// fixture — сборка резолвера с записью промптов и сценариев.
type fixture struct {
	r     *Resolver
	dev   *fakeDeveloper
	acc   *fakeAcceptor
	store *fakeStore
	epic  *domain.Epic
	task  *domain.Task
}

// markersSeq возвращает детектор маркеров, отдающий элементы последовательности
// (последний повторяется).
func markersSeq(seq ...[]string) func(string, []string) ([]string, error) {
	i := 0
	return func(string, []string) ([]string, error) {
		var out []string
		if i < len(seq) {
			out = seq[i]
			i++
		} else if len(seq) > 0 {
			out = seq[len(seq)-1]
		}
		return out, nil
	}
}

func newFixture(t *testing.T, hard []string, markers func(string, []string) ([]string, error), reports ...*domain.Report) *fixture {
	t.Helper()
	epic := &domain.Epic{TaskSpec: domain.TaskSpec{TaskID: "E1", Title: "Эпик"}}
	epic.MergeConflictFiles = append([]string(nil), hard...)
	task := &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "T1", Title: "Задача"}}
	task.MergeConflictFiles = append([]string(nil), hard...)

	dev := &fakeDeveloper{}
	acc := &fakeAcceptor{reports: reports}
	store := &fakeStore{}
	r := NewResolver(epic, []*domain.Task{task}, hard, t.TempDir(), Deps{
		Store:     store,
		Developer: dev,
		Acceptor:  acc,
		Markers:   markers,
	})
	return &fixture{r: r, dev: dev, acc: acc, store: store, epic: epic, task: task}
}

// Пустой список конфликтных файлов — разрешать нечего.
func TestResolveWithLLMEmptyHard(t *testing.T) {
	f := newFixture(t, nil, markersSeq(nil))
	res, err := f.r.ResolveWithLLM(context.Background(), "app", "backend")
	if err != nil || res != nil {
		t.Fatalf("ожидалось (nil, nil), получено (%v, %v)", res, err)
	}
	if f.dev.calls != 0 {
		t.Fatalf("агента не должны были звать")
	}
}

// Успех: маркеров нет, приёмка approve — поля конфликтов сняты с доски.
func TestResolveWithLLMClearsConflicts(t *testing.T) {
	f := newFixture(t, []string{"a.go", "b.go"}, markersSeq(nil))
	res, err := f.r.ResolveWithLLM(context.Background(), "app", "backend")
	if err != nil || res == nil || !res.Cleared {
		t.Fatalf("ожидался успех, получено (%+v, %v)", res, err)
	}
	if f.epic.MergeConflictFiles != nil || f.task.MergeConflictFiles != nil {
		t.Fatalf("конфликтные поля должны быть очищены: epic=%v task=%v", f.epic.MergeConflictFiles, f.task.MergeConflictFiles)
	}
	if len(f.store.epics) != 1 || len(f.store.tasks) != 1 {
		t.Fatalf("ожидалось сохранение эпика и задачи, получено %d/%d", len(f.store.epics), len(f.store.tasks))
	}
	if f.dev.project != "app" || f.dev.role != "backend" {
		t.Fatalf("роль/проект не доехали: %q/%q", f.dev.project, f.dev.role)
	}
	p := f.dev.prompts[0]
	for _, want := range []string{"=== ЭПИК ===", "E1", "a.go", "b.go"} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт не содержит %q:\n%s", want, p)
		}
	}
	if f.acc.dirs[0] != f.r.workdir {
		t.Errorf("приёмку звали не на worktree: %q != %q", f.acc.dirs[0], f.r.workdir)
	}
}

// Провал приёмки первой попытки → второй промпт несёт ошибки верификации.
func TestResolveWithLLMRetriesWithVerifyError(t *testing.T) {
	reject := &domain.Report{Verdict: domain.VerdictReject, Summary: "не собралось", Build: domain.BuildResult{Output: "boom"}}
	f := newFixture(t, []string{"a.go"}, markersSeq(nil), reject)
	res, err := f.r.ResolveWithLLM(context.Background(), "app", "backend")
	if err != nil || res == nil || !res.Cleared {
		t.Fatalf("ожидался успех со второй попытки, получено (%+v, %v)", res, err)
	}
	if f.dev.calls != 2 {
		t.Fatalf("ожидалось 2 прогона, получено %d", f.dev.calls)
	}
	second := f.dev.prompts[1]
	for _, want := range []string{"ПРЕДЫДУЩАЯ ПОПЫТКА НЕ УДАЛАСЬ", "BUILD OUTPUT", "boom"} {
		if !strings.Contains(second, want) {
			t.Errorf("второй промпт не содержит %q:\n%s", want, second)
		}
	}
}

// Маркеры конфликта остались во всех попытках → бюджет исчерпан, не cleared.
func TestResolveWithLLMKeepsConflictMarkers(t *testing.T) {
	f := newFixture(t, []string{"a.go"}, markersSeq([]string{"a.go"}))
	res, err := f.r.ResolveWithLLM(context.Background(), "app", "backend")
	if err != nil || res == nil || res.Cleared {
		t.Fatalf("ожидался провал, получено (%+v, %v)", res, err)
	}
	if !strings.Contains(res.VerifyErr, "остались маркеры конфликта") || !strings.Contains(res.VerifyErr, "a.go") {
		t.Errorf("причина не про маркеры: %q", res.VerifyErr)
	}
	if f.dev.calls != ResolveMaxAttempts {
		t.Fatalf("ожидалось %d попыток, получено %d", ResolveMaxAttempts, f.dev.calls)
	}
	if len(f.store.epics) != 0 || len(f.store.tasks) != 0 {
		t.Fatalf("доску не должны трогать при провале")
	}
}

// Приёмка всегда reject → бюджет исчерпан, в причине — вывод сборки.
func TestResolveWithLLMVerifyExhausts(t *testing.T) {
	reject := &domain.Report{Verdict: domain.VerdictReject, Summary: "нет", Build: domain.BuildResult{Output: "go build failed"}}
	f := newFixture(t, []string{"a.go"}, markersSeq(nil), reject, reject, reject)
	res, err := f.r.ResolveWithLLM(context.Background(), "app", "backend")
	if err != nil || res == nil || res.Cleared {
		t.Fatalf("ожидался провал, получено (%+v, %v)", res, err)
	}
	if !strings.Contains(res.VerifyErr, "acceptor reject") || !strings.Contains(res.VerifyErr, "go build failed") {
		t.Errorf("причина не про приёмку: %q", res.VerifyErr)
	}
}

// Ошибка LLM пробрасывается наверх.
func TestResolveWithLLMGenerateError(t *testing.T) {
	f := newFixture(t, []string{"a.go"}, markersSeq(nil))
	f.dev.errs = []error{errors.New("no model")}
	res, err := f.r.ResolveWithLLM(context.Background(), "app", "backend")
	if err == nil || !strings.Contains(err.Error(), "developer resolve generate") {
		t.Fatalf("ожидалась ошибка генерации, получено (%+v, %v)", res, err)
	}
}

// Пустой ответ агента — ошибка, а не «успех по умолчанию».
func TestResolveWithLLMEmptyResponse(t *testing.T) {
	f := newFixture(t, []string{"a.go"}, markersSeq(nil))
	f.dev.responses = []*domain.AgentResponse{nil}
	_, err := f.r.ResolveWithLLM(context.Background(), "app", "backend")
	if err == nil || !strings.Contains(err.Error(), "empty developer response") {
		t.Fatalf("ожидалась ошибка пустого ответа, получено %v", err)
	}
}

// flattenOutputDir переносит файлы из OUTPUTDIR/ в корень, не перетирая
// существующие.
func TestResolveFlattenOutputDir(t *testing.T) {
	f := newFixture(t, []string{"a.go"}, markersSeq(nil))
	sub := filepath.Join(f.r.workdir, "OUTPUTDIR")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(sub, "new.go"), "new")
	mustWrite(t, filepath.Join(sub, "keep.go"), "from-sub")
	mustWrite(t, filepath.Join(f.r.workdir, "keep.go"), "from-root")

	f.r.flattenOutputDir()

	if got := mustRead(t, filepath.Join(f.r.workdir, "new.go")); got != "new" {
		t.Errorf("new.go не перенесён: %q", got)
	}
	if got := mustRead(t, filepath.Join(f.r.workdir, "keep.go")); got != "from-root" {
		t.Errorf("существующий файл перетёрт: %q", got)
	}
}

// buildDeveloperPrompt: задачи идут по порядку SequenceOrder, с ролью и
// описанием.
func TestBuildDeveloperPromptOrdersTasks(t *testing.T) {
	epic := &domain.Epic{TaskSpec: domain.TaskSpec{TaskID: "E1", Title: "Эпик", Description: "описание"}}
	epic.Summary = "архитектура"
	t1 := &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "T2", Title: "вторая", SequenceOrder: 2, AssignedRole: "qa", Description: "т2"}}
	t0 := &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "T1", Title: "первая", SequenceOrder: 1}}
	r := NewResolver(epic, []*domain.Task{t1, t0}, []string{"x.go"}, "/tmp/wd", Deps{})
	p := r.buildDeveloperPrompt()

	if i, j := strings.Index(p, "T1"), strings.Index(p, "T2"); i < 0 || j < 0 || i > j {
		t.Fatalf("задачи не отсортированы по SequenceOrder:\n%s", p)
	}
	for _, want := range []string{"архитектура", "описание", "Role: qa", "т2"} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт не содержит %q", want)
		}
	}
}

// clearConflictField снимает поле только у тех задач, чьи конфликты полностью
// разрешены.
func TestClearConflictFieldPartial(t *testing.T) {
	epic := &domain.Epic{TaskSpec: domain.TaskSpec{TaskID: "E1"}}
	epic.MergeConflictFiles = []string{"a.go", "b.go"}
	done := &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "T1"}, MergeConflictFiles: []string{"a.go"}}
	other := &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "T2"}, MergeConflictFiles: []string{"a.go", "b.go"}}
	store := &fakeStore{}
	r := NewResolver(epic, []*domain.Task{done, other}, []string{"a.go"}, "/tmp/wd", Deps{Store: store})

	r.clearConflictField([]string{"a.go"})

	if epic.MergeConflictFiles == nil {
		t.Errorf("эпик: неполный набор не должен сниматься")
	}
	if done.MergeConflictFiles != nil {
		t.Errorf("T1: конфликты разрешены — поле должно быть очищено")
	}
	if len(other.MergeConflictFiles) != 2 {
		t.Errorf("T2: неполный набор не должен сниматься: %v", other.MergeConflictFiles)
	}
	if len(store.epics) != 0 || len(store.tasks) != 1 || store.tasks[0].TaskID != "T1" {
		t.Errorf("сохранить должны только T1: epics=%d tasks=%v", len(store.epics), store.tasks)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
