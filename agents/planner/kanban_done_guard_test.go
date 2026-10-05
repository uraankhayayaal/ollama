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
