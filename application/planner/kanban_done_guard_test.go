package planner

// Ф-6 (инцидент FEL-04): fallback оркестратора «в работе → выполнена» больше не
// закрывает задачу, которую специалист фактически не сделал. Отказ серверного
// гарда «phantom done» не рушит раунд: задача остаётся в работе, получает
// требование сделать работу и уходит на эскалацию/«помощь человека» (детектор
// петли).

import (
	"ai/domain/services"
	"ai/infrastructure/board"
	"ai/runner"
	"context"
	"fmt"
	"strings"
	"testing"

	"ai/shared/projects"
	"os"

	"github.com/alicebob/miniredis/v2"
)

// idleProvider ведёт себя как loopedProvider, но специалист просто
// «отвечает, ничего не делая»: вызовов инструментов нет, статус задачи он не
// меняет — ровно сценарий FEL-04, где fallback закрывал пустую задачу.
type idleProvider struct {
	loopedProvider
}

func (p *idleProvider) Generate(ctx context.Context, agent services.Agent) (*runner.AgentResponse, error) {
	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}
	if strings.HasPrefix(umsg, "Ты — специалист") {
		// Разработчик ничего не делает (сценарий FEL-04).
		return &runner.AgentResponse{Content: "посмотрел задачу, делать нечего"}, nil
	}
	return p.loopedProvider.Generate(ctx, agent)
}

// guardProvider — провайдер для тестов гарда (phantom-done). Специалист
// пытается перевести задачу в done через Store напрямую (обходит FSM-пропуск
// executeBoardToolCalls), чтобы TaskDoneGuard реально сработал.
type guardProvider struct {
	idleProvider
	store *board.Store
}

func (p *guardProvider) Generate(ctx context.Context, agent services.Agent) (*runner.AgentResponse, error) {
	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}
	if strings.HasPrefix(umsg, "Ты — специалист") {
		// Находим задачу в работе, переходим в development, затем пытаемся done
		// через SetTaskStatus — чтобы гард сработал.
		tasks, err := p.store.ListTasks(ctx)
		if err != nil {
			return nil, err
		}
		for _, task := range tasks {
			if task.Status != board.StatusInfra {
				continue
			}
			_ = p.store.SetTaskStatus(ctx, task.TaskID, board.StatusTestCases)
			_ = p.store.SetTaskStatus(ctx, task.TaskID, board.StatusDevelopment)
			// Пытаемся перевести в done — гард может отклонить.
			if err := p.store.SetTaskStatus(ctx, task.TaskID, board.StatusDone); err != nil {
				// Гард отклонил — статус остался development, оркестратор
				// увидит "не done" и запустит эскалацию.
				return &runner.AgentResponse{Content: "задача не может стать выполненной: " + err.Error()}, nil
			}
			// Гард пропустил — задача done.
			return &runner.AgentResponse{Content: "задача выполнена"}, nil
		}
		return &runner.AgentResponse{Content: "задача выполнена"}, nil
	}
	return p.idleProvider.Generate(ctx, agent)
}

// newIdleKanbanRunner — доска с гардом «phantom done», который отказывает в
// переходе в done (как сервер для задачи без коммитов и правок).
func newIdleKanbanRunner(t *testing.T) (*KanbanRunner, *board.Store) {
	t.Helper()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-done"})
	store.TaskDoneGuard = func(_ context.Context, task *board.Task, _ board.Status) error {
		return fmt.Errorf("задача %s не может стать выполненной: в ветке нет ни одного коммита", task.TaskID)
	}
	projectDir := projects.ProjectDir("kanban-done")
	t.Cleanup(func() { _ = os.RemoveAll(projectDir) })
	return NewKanbanRunner(&idleProvider{}, board.NewDomainStore(store)), store
}

// newGuardKanbanRunner — доска с гардом «phantom done» и guardProvider,
// который пытается перевести задачу в done через SetTaskStatus (гард сработает).
func newGuardKanbanRunner(t *testing.T) (*KanbanRunner, *board.Store) {
	t.Helper()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-done"})
	store.TaskDoneGuard = func(_ context.Context, task *board.Task, _ board.Status) error {
		return fmt.Errorf("задача %s не может стать выполненной: в ветке нет ни одного коммита", task.TaskID)
	}
	projectDir := projects.ProjectDir("kanban-done")
	t.Cleanup(func() { _ = os.RemoveAll(projectDir) })
	return NewKanbanRunner(&guardProvider{store: store}, board.NewDomainStore(store)), store
}

// TestPhantomDoneNotClosedByFallback — специалист ничего не сделал и не сменил
// статус: задача НЕ становится done, а получает требование сделать работу.
func TestPhantomDoneNotClosedByFallback(t *testing.T) {
	ctx := context.Background()
	kr, store := newIdleKanbanRunner(t)

	_ = kr.Run(ctx, "kanban-done", "Сделай todo-приложение")

	tasks, terr := store.ListTasks(ctx)
	if terr != nil {
		t.Fatalf("ListTasks: %v", terr)
	}
	if len(tasks) == 0 {
		t.Fatal("задача должна существовать на доске")
	}
	worked := false
	for _, task := range tasks {
		if task.Status == board.StatusDone {
			t.Fatalf("задача %s не должна закрываться в done без работы (инцидент FEL-04)", task.TaskID)
		}
		if task.Status == board.StatusDraft || task.Status == board.StatusInfra {
			continue // не бралась в работу (зависимости не выполнены) — её состояние нас не интересует
		}
		worked = true
		// Требование сделать работу остаётся на доске (инъекция/комментарий эскалации).
		found := false
		for _, inj := range task.Injections {
			if strings.Contains(inj.Content, "внеси правки в проект") {
				found = true
			}
		}
		for _, c := range task.Comments {
			if strings.Contains(c.Body, "внеси правки в проект") || strings.Contains(c.Body, "BoardSetTaskStatus") || strings.Contains(c.Body, "Форсмажор") {
				found = true
			}
		}
		if !found {
			t.Fatalf("в задаче %s нет требования сделать работу: inj=%+v comments=%+v", task.TaskID, task.Injections, task.Comments)
		}
	}
	if !worked {
		t.Fatal("ни одна задача не бралась в работу — тест ничего не проверил")
	}
}

// TestFallbackDoneAllowedWhenGuardPasses — когда гард подтверждает работу
// (в проекте есть коммиты), fallback по-прежнему закрывает задачу: регрессия
// «гард не сломал обычный автономный цикл».
func TestFallbackDoneAllowedWhenGuardPasses(t *testing.T) {
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-done-ok"})
	projectDir := projects.ProjectDir("kanban-done-ok")
	t.Cleanup(func() { _ = os.RemoveAll(projectDir) })
	// Без TaskDoneGuard — гард не установлен, SetTaskStatus(done) пройдёт.
	kr := NewKanbanRunner(&guardProvider{store: store}, board.NewDomainStore(store))

	if err := kr.Run(context.Background(), "kanban-done-ok", "Сделай todo-приложение"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	tasks, err := store.ListTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) == 0 {
		t.Fatal("задача должна существовать")
	}
	last := tasks[len(tasks)-1]
	if last.Status != board.StatusDone && last.Status != board.StatusCancelled {
		t.Fatalf("задача %s: статус %s, ожидался done/cancelled", last.TaskID, last.Status)
	}
}

// TestGuardRejectionEscalatesWithinBudget — инцидент QAL-01/FEL-04: отказ
// гарда «phantom done» съедает бюджет автономии, а не крутится бесконечно.
// По бюджету задача уходит в cancelled с причиной остановки.
func TestGuardRejectionEscalatesWithinBudget(t *testing.T) {
	t.Setenv("KANBAN_MAX_ESCALATIONS", "1")
	ctx := context.Background()
	kr, store := newGuardKanbanRunner(t)

	var audit []string
	kr.SetStatusNotifier(func(msg string) { audit = append(audit, msg) })

	err := kr.Run(ctx, "kanban-done", "Сделай todo-приложение")
	if err == nil {
		t.Fatal("исчерпание бюджета на пустые работы должно останавливать запуск")
	}
	for _, want := range []string{"бюджет автономии исчерпан", "нужен человек"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ошибка должна содержать %q, got %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "нет прогресса") {
		t.Fatalf("причина остановки не должна прятаться под «нет прогресса»: %v", err)
	}

	tasks, terr := store.ListTasks(ctx)
	if terr != nil {
		t.Fatalf("ListTasks: %v", terr)
	}
	stopped := 0
	for _, task := range tasks {
		if task.Status == board.StatusDone {
			t.Fatalf("задача %s не должна закрываться в done без работы (инцидент FEL-04)", task.TaskID)
		}
		if task.Status != board.StatusCancelled {
			continue
		}
		stopped++
		if task.Escalations != 1 {
			t.Fatalf("задача %s: эскалаций %d, ожидался бюджет 1", task.TaskID, task.Escalations)
		}
		// Инъекция последней попытки объясняет модели, что работа не сделана.
		found := false
		for _, inj := range task.Injections {
			if strings.Contains(inj.Content, "внеси правки в проект") {
				found = true
			}
		}
		if !found {
			t.Fatalf("в остановленной по бюджету задаче %s нет инъекции о пустой работе: %+v", task.TaskID, task.Injections)
		}
	}
	if stopped == 0 {
		t.Fatalf("хотя бы одна задача должна уйти в cancelled по бюджету: %+v", tasks)
	}
	joined := strings.Join(audit, "\n")
	if !strings.Contains(joined, "нужен человек") {
		t.Fatalf("в аудите нет сообщения о нужде в человеке: %v", audit)
	}
}

// humanHelpProvider — специалист сам останавливает свою задачу с причиной
// (задача невыполнима), как это делает BoardSetTaskStatus(status=human_help,
// reason): оркестратор обязан уважать этот выбор, а не затирать его
// fallback-ом в done и не сжигать бюджет эскалаций.
type humanHelpProvider struct {
	idleProvider
	store *board.Store
}

func (p *humanHelpProvider) Generate(ctx context.Context, agent services.Agent) (*runner.AgentResponse, error) {
	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}
	// Обрабатываем только сообщения от специалистов — все остальные
	// делегируем chain (architect, leads).
	if !strings.HasPrefix(umsg, "Ты — специалист") {
		return p.idleProvider.loopedProvider.Generate(ctx, agent)
	}
	tasks, err := p.store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	for _, task := range tasks {
		if task.Status != board.StatusInfra {
			continue
		}
		if err := p.store.SetTaskStatus(ctx, task.TaskID, board.StatusCancelled); err != nil {
			return nil, err
		}
		if err := p.store.PatchTask(ctx, task.TaskID, func(cur *board.Task) error {
			cur.PauseReason = "нет пакета internal/service: тестировать нечего"
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return &runner.AgentResponse{Content: "задача невыполнима, перевёл в human_help"}, nil
}

// TestAgentHumanHelpRespectedByFallback — остановка специалиста не проходит
// через fallback «в работу → выполнена» и не тратит бюджет: задача остаётся
// в human_help с причиной, человек получает сообщение, эскалаций нет.
func TestAgentHumanHelpRespectedByFallback(t *testing.T) {
	ctx := context.Background()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-pause"})
	// Гард на месте: если бы остановку затирал fallback, он бы тут же отказал.
	store.TaskDoneGuard = func(_ context.Context, task *board.Task, _ board.Status) error {
		return fmt.Errorf("задача %s не может стать выполненной: worktree пуст", task.TaskID)
	}
	projectDir := projects.ProjectDir("kanban-pause")
	t.Cleanup(func() { _ = os.RemoveAll(projectDir) })
	kr := NewKanbanRunner(&humanHelpProvider{store: store}, board.NewDomainStore(store))

	var audit []string
	kr.SetStatusNotifier(func(msg string) { audit = append(audit, msg) })

	// Остановка «нет прогресса» (всё в human_help, дальше бессмысленно) — норма.
	_ = kr.Run(ctx, "kanban-pause", "Сделай todo-приложение")

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	stopped := 0
	for _, task := range tasks {
		if task.Status == board.StatusDone {
			t.Fatalf("задача %s: остановка не должна затираться fallback-ом в done", task.TaskID)
		}
		if task.Status != board.StatusCancelled {
			continue
		}
		stopped++
		if task.PauseReason != "нет пакета internal/service: тестировать нечего" {
			t.Fatalf("задача %s: причина остановки %q не сохранилась", task.TaskID, task.PauseReason)
		}
		if task.Escalations != 0 {
			t.Fatalf("задача %s: эскалаций %d — осознанная остановка не должна сжигать бюджет", task.TaskID, task.Escalations)
		}
	}
	if stopped == 0 {
		t.Fatalf("задача должна остаться в human_help, а не стать done: %+v", tasks)
	}
	joined := strings.Join(audit, "\n")
	if !strings.Contains(joined, "нет пакета") {
		t.Fatalf("в аудите нет сообщения об остановке с причиной: %v", audit)
	}
}
