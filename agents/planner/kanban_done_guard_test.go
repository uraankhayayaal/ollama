package planner

// Ф-6 (инцидент FEL-04): fallback оркестратора «в работе → выполнена» больше не
// закрывает задачу, которую специалист фактически не сделал. Отказ серверного
// гарда «phantom done» не рушит раунд: задача остаётся в работе, получает
// требование сделать работу и уходит на эскалацию/паузу (детектор петли).

import (
	"ai/agents"
	"ai/board"
	"ai/runner"
	"context"
	"fmt"
	"strings"
	"testing"

	"ai/projects"
	"github.com/alicebob/miniredis/v2"
	"os"
)

// idleProvider ведёт себя как loopedProvider, но специалист просто
// «отвечает, ничего не делая»: вызовов инструментов нет, статус задачи он не
// меняет — ровно сценарий FEL-04, где fallback закрывал пустую задачу.
type idleProvider struct {
	loopedProvider
}

func (p *idleProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}
	if strings.HasPrefix(umsg, "Ты — специалист") {
		return &runner.AgentResponse{Content: "посмотрел задачу, делать нечего"}, nil
	}
	return p.loopedProvider.Generate(ctx, agent)
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
	return NewKanbanRunner(&idleProvider{}, store), store
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
		if task.Status == board.StatusNew {
			continue // не бралась в работу — её состояние нас не интересует
		}
		worked = true
		// Требование сделать работу остаётся на доске: следующая попытка (с
		// сильной моделью после эскалации) читает его вместе с задачей.
		found := false
		for _, inj := range task.Injections {
			if strings.Contains(inj.Content, "внеси правки в проект") {
				found = true
			}
		}
		if !found {
			t.Fatalf("в задаче %s нет требования сделать работу: %+v", task.TaskID, task.Injections)
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
	kr := NewKanbanRunner(&idleProvider{}, store)

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
	if last.Status != board.StatusDone {
		t.Fatalf("задача %s: статус %s, ожидался done (без гарда работа засчитывается)", last.TaskID, last.Status)
	}
}

// TestGuardRejectionEscalatesWithinBudget — инцидент QAL-01/FEL-04: отказ
// гарда «phantom done» съедает бюджет автономии, а не крутится бесконечно.
// По бюджету задача встаёт на паузу, а запуск заканчивается сообщением
// «нужен человек» с последней причиной (а не 100 раундами сжигания токенов).
func TestGuardRejectionEscalatesWithinBudget(t *testing.T) {
	t.Setenv("KANBAN_MAX_ESCALATIONS", "1")
	ctx := context.Background()
	kr, store := newIdleKanbanRunner(t)

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
	paused := 0
	for _, task := range tasks {
		if task.Status == board.StatusDone {
			t.Fatalf("задача %s не должна закрываться в done без работы (инцидент FEL-04)", task.TaskID)
		}
		if task.Status != board.StatusPaused {
			continue
		}
		paused++
		if task.Escalations != 1 {
			t.Fatalf("задача %s: эскалаций %d, ожидался бюджет 1", task.TaskID, task.Escalations)
		}
		// Инъекция последней попытки объясняет модели, что работа не сделана,
		// и подсказывает честный выход через paused.
		found := false
		for _, inj := range task.Injections {
			if strings.Contains(inj.Content, "внеси правки в проект") &&
				strings.Contains(inj.Content, "BoardSetTaskStatus") {
				found = true
			}
		}
		if !found {
			t.Fatalf("в поставленной на паузу задаче %s нет инъекции о пустой работе: %+v", task.TaskID, task.Injections)
		}
	}
	if paused == 0 {
		t.Fatalf("хотя бы одна задача должна встать на паузу по бюджету: %+v", tasks)
	}
	joined := strings.Join(audit, "\n")
	if !strings.Contains(joined, "нужен человек") {
		t.Fatalf("в аудите нет сообщения о нужде в человеке: %v", audit)
	}
}

// pauseProvider — специалист сам ставит свою задачу на паузу с причиной
// (задача невыполнима), как это делает BoardSetTaskStatus(status=paused,
// reason): оркестратор обязан уважать этот выбор, а не затирать его
// fallback-ом в done и не сжигать бюджет эскалаций.
type pauseProvider struct {
	idleProvider
	store *board.Store
}

func (p *pauseProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}
	if !strings.HasPrefix(umsg, "Ты — специалист") {
		return p.idleProvider.Generate(ctx, agent)
	}
	tasks, err := p.store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	for _, task := range tasks {
		if task.Status != board.StatusInProgress {
			continue
		}
		if err := p.store.SetTaskStatus(ctx, task.TaskID, board.StatusPaused); err != nil {
			return nil, err
		}
		if err := p.store.PatchTask(ctx, task.TaskID, func(cur *board.Task) error {
			cur.PauseReason = "нет пакета internal/service: тестировать нечего"
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return &runner.AgentResponse{Content: "задача невыполнима, поставил паузу"}, nil
}

// TestAgentPauseRespectedByFallback — пауза специалиста не проходит через
// fallback «в работу → выполнена» и не тратит бюджет: задача остаётся на
// паузе с причиной, человек получает сообщение, эскалаций нет.
func TestAgentPauseRespectedByFallback(t *testing.T) {
	ctx := context.Background()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-pause"})
	// Гард на месте: если бы паузу затирал fallback, он бы тут же отказал.
	store.TaskDoneGuard = func(_ context.Context, task *board.Task, _ board.Status) error {
		return fmt.Errorf("задача %s не может стать выполненной: worktree пуст", task.TaskID)
	}
	projectDir := projects.ProjectDir("kanban-pause")
	t.Cleanup(func() { _ = os.RemoveAll(projectDir) })
	kr := NewKanbanRunner(&pauseProvider{store: store}, store)

	var audit []string
	kr.SetStatusNotifier(func(msg string) { audit = append(audit, msg) })

	// Остановка «нет прогресса» (на паузе всё, дальше бессмысленно) — норма.
	_ = kr.Run(ctx, "kanban-pause", "Сделай todo-приложение")

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	paused := 0
	for _, task := range tasks {
		if task.Status == board.StatusDone {
			t.Fatalf("задача %s: пауза не должна затираться fallback-ом в done", task.TaskID)
		}
		if task.Status != board.StatusPaused {
			continue
		}
		paused++
		if task.PauseReason != "нет пакета internal/service: тестировать нечего" {
			t.Fatalf("задача %s: причина паузы %q не сохранилась", task.TaskID, task.PauseReason)
		}
		if task.Escalations != 0 {
			t.Fatalf("задача %s: эскалаций %d — осознанная пауза не должна сжигать бюджет", task.TaskID, task.Escalations)
		}
	}
	if paused == 0 {
		t.Fatalf("задача должна остаться на паузе, не ставшей done: %+v", tasks)
	}
	joined := strings.Join(audit, "\n")
	if !strings.Contains(joined, "ждёт решения человека") || !strings.Contains(joined, "нет пакета") {
		t.Fatalf("в аудите нет сообщения о паузе с причиной: %v", audit)
	}
}
