// Тесты учёта расхода токенов по задачам и эпикам в Kanban-раннере (Ф-2/Ф-3
// PLAN-2026-09-19-done-epic-task-token.md): атрибуция раундов по scope,
// фиксация факта при выполнении и идемпотентный проход по доске.
package planner

import (
	"context"
	"os"
	"testing"

	"ai/agents"
	"ai/board"
	"ai/projects"
	"ai/runevents"
	"ai/runner"
	"ai/tokens"
	"github.com/alicebob/miniredis/v2"
)

// scopeProvider — провайдер, который в каждом раунде эмитит расход токенов.
// Репортёра в контексте может не быть (консольный режим): тогда расход просто
// не атрибутируется, и вызов не падает.
type scopeProvider struct{ t *testing.T }

func (p *scopeProvider) Generate(ctx context.Context, _ agents.Agent) (*runner.AgentResponse, error) {
	if rep := runevents.ReporterFromContext(ctx); rep != nil {
		rep.OnTokens(100, 20, 0)
	}
	return &runner.AgentResponse{}, nil
}

// newTokenRunner собирает раннер с доской и счётчиком токенов на одном
// miniredis.
func newTokenRunner(t *testing.T, project string) (*KanbanRunner, *board.Store, *tokens.Store) {
	t.Helper()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: project})
	tok := tokens.NewStoreNoCheck(tokens.StoreConfig{Addr: srv.Addr(), Project: project})
	t.Cleanup(func() { _ = os.RemoveAll(projects.ProjectDir(project)) })

	kr := NewKanbanRunner(&scopeProvider{t: t}, store)
	kr.SetTokens(tok)
	return kr, store, tok
}

// TestGenerateAttributesScopeToUnit проверяет, что scope единицы работы,
// заданный обёрткой generate, доходит до события расхода токенов (Ф-2).
func TestGenerateAttributesScopeToUnit(t *testing.T) {
	ctx := context.Background()
	kr, store, _ := newTokenRunner(t, "kanban-tok-scope")
	if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{TaskID: "ARCH-01"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "T-1"}, EpicID: "ARCH-01", Status: board.StatusInProgress,
	}); err != nil {
		t.Fatal(err)
	}

	// Репортёра в контексте нет (консольный режим) — атрибуция безопасна.
	if _, err := kr.generate(ctx, tokens.ScopeTask("T-1"), nil); err != nil {
		t.Fatalf("generate без репортёра: %v", err)
	}
	if runevents.ReporterFromContext(ctx) != nil {
		t.Fatalf("контекст Generate без репортёра не должен его получать")
	}

	// С репортёром в контексте scope доходит до события.
	var gotScope string
	var gotType runevents.EventType
	ctx = runevents.WithReporter(ctx, runevents.NewRouter(func(ev runevents.Event) {
		if ev.Type != runevents.TypeTokenCount {
			return
		}
		gotType, gotScope = ev.Type, ev.Scope
	}))
	if _, err := kr.generate(ctx, tokens.ScopeTask("T-1"), nil); err != nil {
		t.Fatal(err)
	}
	if gotType != runevents.TypeTokenCount || gotScope != tokens.ScopeTask("T-1") {
		t.Fatalf("событие %q со scope %q, ожидалось tokens/%s", gotType, gotScope, tokens.ScopeTask("T-1"))
	}
}

// TestFinalizeTaskTokensWritesFact проверяет фиксацию факта задачи: счётчик
// scope'а снимается, итог пишется в сущность, счётчик обнуляется, повторный
// вызов ничего не меняет.
func TestFinalizeTaskTokensWritesFact(t *testing.T) {
	ctx := context.Background()
	kr, store, tok := newTokenRunner(t, "kanban-tok-final")
	if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{TaskID: "ARCH-01"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "T-1"}, EpicID: "ARCH-01", Status: board.StatusInProgress,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tok.AddScoped(ctx, tokens.ScopeTask("T-1"), 500, 100); err != nil {
		t.Fatal(err)
	}

	if err := kr.finalizeTaskTokens(ctx, "T-1"); err != nil {
		t.Fatalf("finalizeTaskTokens: %v", err)
	}
	task, err := store.GetTask(ctx, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.TokensTotal != 600 || task.TokensInput != 500 || task.TokensOutput != 100 {
		t.Fatalf("факт задачи = %+v, ожидалось 500/100/600", task.TokenUsage)
	}
	if in, out, _ := tok.GetScoped(ctx, tokens.ScopeTask("T-1")); in != 0 || out != 0 {
		t.Errorf("счётчик scope не обнулён: %d/%d", in, out)
	}

	// Идемпотентность: второй вызов при пустом счётчике — no-op.
	if err := kr.finalizeTaskTokens(ctx, "T-1"); err != nil {
		t.Fatal(err)
	}
	task, _ = store.GetTask(ctx, "T-1")
	if task.TokensTotal != 600 {
		t.Errorf("повторная фиксация изменила факт: %d", task.TokensTotal)
	}
}

// TestFinalizeEpicTokensSumsTasksLeadAndArchitecture проверяет формулу итога
// эпика: сумма факта задач + расход лида (scope epic:<id>) + доля архитектора
// (scope architecture, когда эпик на доске единственный).
func TestFinalizeEpicTokensSumsTasksLeadAndArchitecture(t *testing.T) {
	ctx := context.Background()
	kr, store, tok := newTokenRunner(t, "kanban-tok-epic")
	if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{TaskID: "ARCH-01"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"T-1", "T-2"} {
		if err := store.CreateTask(ctx, &board.Task{
			TaskSpec: board.TaskSpec{TaskID: id}, EpicID: "ARCH-01", Status: board.StatusDone,
		}); err != nil {
			t.Fatal(err)
		}
		// Факт задач уже записан на доске фиксацией по ходу выполнения.
		if err := store.FinalizeTaskTokens(ctx, id, 300, 100); err != nil {
			t.Fatal(err)
		}
	}
	if err := tok.AddScoped(ctx, tokens.ScopeEpic("ARCH-01"), 50, 10); err != nil { // лид
		t.Fatal(err)
	}
	if err := tok.AddScoped(ctx, tokens.ScopeArchitecture, 200, 40); err != nil { // архитектор
		t.Fatal(err)
	}

	if err := kr.finalizeEpicTokens(ctx, "ARCH-01"); err != nil {
		t.Fatalf("finalizeEpicTokens: %v", err)
	}
	epic, err := store.GetEpic(ctx, "ARCH-01")
	if err != nil {
		t.Fatal(err)
	}
	// 2 задачи по 400 (вход 600) + лид 60 (50) + архитектор 240 (200) = 1100.
	if epic.TokensTotal != 1100 || epic.TokensInput != 850 || epic.TokensOutput != 250 {
		t.Fatalf("итог эпика = %+v, ожидалось in=850 out=250 total=1100", epic.TokenUsage)
	}
	if in, out, _ := tok.GetScoped(ctx, tokens.ScopeArchitecture); in != 0 || out != 0 {
		t.Errorf("счётчик architecture не обнулён: %d/%d", in, out)
	}
	if in, out, _ := tok.GetScoped(ctx, tokens.ScopeEpic("ARCH-01")); in != 0 || out != 0 {
		t.Errorf("счётчик epic:ARCH-01 не обнулён: %d/%d", in, out)
	}
}

// TestFinalizeEpicTokensKeepsArchitectureWhenManyEpics: при нескольких эпиках
// расход архитектора не делится наугад — остаётся в счётчике проекта, в итог
// эпика попадает только задачи + лид.
func TestFinalizeEpicTokensKeepsArchitectureWhenManyEpics(t *testing.T) {
	ctx := context.Background()
	kr, store, tok := newTokenRunner(t, "kanban-tok-multi")
	for _, id := range []string{"ARCH-01", "ARCH-02"} {
		if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{TaskID: id}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "T-1"}, EpicID: "ARCH-01", Status: board.StatusDone,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinalizeTaskTokens(ctx, "T-1", 1000, 200); err != nil {
		t.Fatal(err)
	}
	if err := tok.AddScoped(ctx, tokens.ScopeArchitecture, 5000, 1000); err != nil {
		t.Fatal(err)
	}

	if err := kr.finalizeEpicTokens(ctx, "ARCH-01"); err != nil {
		t.Fatal(err)
	}
	epic, _ := store.GetEpic(ctx, "ARCH-01")
	if epic.TokensTotal != 1200 {
		t.Errorf("итог эпика = %d, ожидалось 1200 (задачи без архитектора)", epic.TokensTotal)
	}
	if in, _, _ := tok.GetScoped(ctx, tokens.ScopeArchitecture); in != 5000 {
		t.Errorf("расход архитектора должен остаться в счётчике (5000), получен %d", in)
	}
}

// TestFinalizeTokensSweepCoversExternalTransitions проверяет проход по доске:
// переход в терминальный статус, сделанный мимо оркестратора (специалист,
// чат-ассистент, человек), всё равно фиксируется фактом.
func TestFinalizeTokensSweepCoversExternalTransitions(t *testing.T) {
	ctx := context.Background()
	kr, store, tok := newTokenRunner(t, "kanban-tok-sweep")
	if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{TaskID: "ARCH-01"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "T-1"}, EpicID: "ARCH-01", Status: board.StatusInProgress,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tok.AddScoped(ctx, tokens.ScopeTask("T-1"), 700, 300); err != nil {
		t.Fatal(err)
	}
	// Переход в cancelled сделал человек в UI, оркестратор не участвовал.
	if err := store.SetTaskStatus(ctx, "T-1", board.StatusCancelled); err != nil {
		t.Fatal(err)
	}

	kr.finalizeTokens(ctx)

	task, _ := store.GetTask(ctx, "T-1")
	if task.TokensTotal != 1000 {
		t.Fatalf("факт отменённой задачи = %d, ожидалось 1000", task.TokensTotal)
	}
	// Повторный проход без новых порций ничего не меняет.
	kr.finalizeTokens(ctx)
	task, _ = store.GetTask(ctx, "T-1")
	if task.TokensTotal != 1000 {
		t.Errorf("повторный проход изменил факт: %d", task.TokensTotal)
	}
}

// TestFinalizeTokensNoopWithoutStore: без счётчика (консольный режим) все
// точки фиксации безопасны.
func TestFinalizeTokensNoopWithoutStore(t *testing.T) {
	ctx := context.Background()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-tok-nil"})
	if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{TaskID: "ARCH-01"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "T-1"}, EpicID: "ARCH-01", Status: board.StatusDone,
	}); err != nil {
		t.Fatal(err)
	}
	kr := &KanbanRunner{store: store} // без SetTokens
	kr.finalizeTokens(ctx)
	if err := kr.finalizeTaskTokens(ctx, "T-1"); err != nil {
		t.Fatalf("finalizeTaskTokens без счётчика: %v", err)
	}
	if err := kr.finalizeEpicTokens(ctx, "ARCH-01"); err != nil {
		t.Fatalf("finalizeEpicTokens без счётчика: %v", err)
	}
}

// tokenEchoProvider — фейковый провайдер kanbanProvider, дополнительно
// эмитящий расход токенов в каждом раунде (как это делает runner.Generate).
type tokenEchoProvider struct{ t *testing.T }

func (p *tokenEchoProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	if rep := runevents.ReporterFromContext(ctx); rep != nil {
		rep.OnTokens(1000, 200, 0)
	}
	return (&kanbanProvider{}).Generate(ctx, agent)
}

func (p *tokenEchoProvider) ChatOnce(ctx context.Context, a agents.Agent, m []runner.Message) (*runner.ModelReply, error) {
	return (&kanbanProvider{}).ChatOnce(ctx, a, m)
}

// TestTokenAccountingEndToEnd прогоняет полный цикл Kanban и проверяет, что
// факт расхода токенов дошёл до задач и эпика: раунды специалистов легли в
// scope задач, лида — в scope эпика, архитектора — в scope architecture, а по
// завершении всё это записано на доску и счётчики обнулены.
func TestTokenAccountingEndToEnd(t *testing.T) {
	const project = "kanban-tok-e2e"
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: project})
	tok := tokens.NewStoreNoCheck(tokens.StoreConfig{Addr: srv.Addr(), Project: project})
	t.Cleanup(func() { _ = os.RemoveAll(projects.ProjectDir(project)) })

	kr := NewKanbanRunner(&tokenEchoProvider{t: t}, store)
	kr.SetTokens(tok)

	// Репортёр сессии: события расхода пишутся в счётчики scope'ов — ровно так,
	// как это делает server.Session.addTokens.
	ctx := runevents.WithReporter(context.Background(), runevents.NewRouter(func(ev runevents.Event) {
		if ev.Type != runevents.TypeTokenCount || ev.Scope == "" {
			return
		}
		if err := tok.AddScoped(context.Background(), ev.Scope, ev.In, ev.Out); err != nil {
			t.Errorf("AddScoped(%s): %v", ev.Scope, err)
		}
	}))

	if err := kr.Run(ctx, project, "Сделай todo-приложение"); err != nil {
		t.Fatalf("Kanban Run: %v", err)
	}

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var doneSum int64
	for _, task := range tasks {
		if task.Status != board.StatusDone {
			t.Fatalf("задача %s не выполнена: статус %s", task.TaskID, task.Status)
		}
		if task.TokensTotal <= 0 {
			t.Errorf("у задачи %s не записан факт расхода", task.TaskID)
		}
		doneSum += task.TokensTotal
	}
	if doneSum == 0 {
		t.Fatal("ни у одной задачи нет факта расхода")
	}

	epics, err := store.ListEpics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, epic := range epics {
		if epic.Status != board.StatusDone {
			t.Fatalf("эпик %s не выполнен: статус %s", epic.TaskID, epic.Status)
		}
		if epic.TokensTotal <= doneSum {
			t.Errorf("итог эпика %s = %d — должен быть больше суммы задач (%d): лид/архитектор не учтены",
				epic.TaskID, epic.TokensTotal, doneSum)
		}
		if extra := epic.TokensTotal - doneSum; extra%1200 != 0 {
			t.Errorf("вклад лида/архитектора = %d — не кратно расходу раунда 1200", extra)
		}
	}

	// Все счётчики scope'ов обнулены: факт уже на доске, двойного счёта нет.
	for _, scope := range []string{
		tokens.ScopeTask("T-01"), tokens.ScopeTask("T-02"),
		tokens.ScopeEpic("ARCH-01"), tokens.ScopeArchitecture, tokens.ScopeBugs,
	} {
		if in, out, err := tok.GetScoped(ctx, scope); err != nil {
			t.Fatal(err)
		} else if in != 0 || out != 0 {
			t.Errorf("счётчик %s не обнулён: %d/%d", scope, in, out)
		}
	}
}
