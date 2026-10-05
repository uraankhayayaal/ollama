package planner

import (
	"ai/agents"
	"ai/agents/architect"
	"ai/board"
	"ai/runner"
	"context"
	"strings"
	"testing"

	"ai/projects"
	"github.com/alicebob/miniredis/v2"
	"os"
)

// loopedProvider повторяет поведение kanbanProvider (архитектор публикует
// бэклог, лид декомпозирует эпик), но специалиста заставляет ЗАЦИКЛИТЬСЯ:
// цикл прерван детектором, задача не выполнена.
type loopedProvider struct{}

func (p *loopedProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}
	switch {
	case strings.HasPrefix(umsg, "Декомпозируй эпик"):
		return &runner.AgentResponse{Content: leadDecompositionJSON}, nil
	case strings.HasPrefix(umsg, "Ты — специалист"):
		return &runner.AgentResponse{
			Content:    "пробую снова",
			Looped:     true,
			LoopReason: "вызов WriteFiles повторён 3 раз с одними и теми же аргументами",
			Rounds:     6,
		}, nil
	default:
		if a, ok := agent.(*architect.Architect); ok {
			if _, err := a.CallFunction(architect.SubmitBacklogToolName, architectBacklogArgs); err != nil {
				return nil, err
			}
		}
		return &runner.AgentResponse{Content: ""}, nil
	}
}

func (p *loopedProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

func newLoopedKanbanRunner(t *testing.T) (*KanbanRunner, *board.Store) {
	t.Helper()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-loop"})

	projectDir := projects.ProjectDir("kanban-loop")
	t.Cleanup(func() { _ = os.RemoveAll(projectDir) })

	return NewKanbanRunner(&loopedProvider{}, store), store
}

// Специалист зациклился (Ф-6, этапы 4.4–4.5): работа НЕ откатывается и НЕ
// помечается выполненной (тихий fallback «выполнена» скрыл бы потерю работы) —
// идёт эскалация: сильная модель + инъекция с разбором петли, задача
// возвращается в очередь. Исчерпание бюджета останавливает работу задачи и
// зовёт человека понятным сообщением, а не «нет прогресса».
func TestKanbanTaskLoopEscalatesThenAsksHuman(t *testing.T) {
	t.Setenv("KANBAN_MAX_ESCALATIONS", "2")
	ctx := context.Background()
	kr, store := newLoopedKanbanRunner(t)

	var audit []string
	kr.SetStatusNotifier(func(msg string) { audit = append(audit, msg) })

	err := kr.Run(ctx, "kanban-loop", "Сделай todo-приложение")
	if err == nil {
		t.Fatal("исчерпание бюджета автономии должно останавливать запуск")
	}
	if !strings.Contains(err.Error(), "бюджет автономии исчерпан") || !strings.Contains(err.Error(), "нужен человек") {
		t.Fatalf("ошибка должна называть исчерпание бюджета и нужду в человеке, got %v", err)
	}
	if !strings.Contains(err.Error(), "WriteFiles") {
		t.Fatalf("ошибка должна нести причину петли, got %v", err)
	}
	if strings.Contains(err.Error(), "нет прогресса") {
		t.Fatalf("ошибка не должна прятаться под «нет прогресса»: %v", err)
	}

	tasks, terr := store.ListTasks(ctx)
	if terr != nil {
		t.Fatalf("ListTasks: %v", terr)
	}
	if len(tasks) == 0 {
		t.Fatal("задача должна существовать на доске")
	}
	audited := false
	for _, task := range tasks {
		if task.Status == board.StatusDone {
			t.Fatalf("задача %s не должна считаться выполненной при зацикливании: %+v", task.TaskID, task.Status)
		}
		if task.Escalations == 0 {
			continue
		}
		audited = true
		// Задача остановлена на паузе (не терминальный статус: вернуть в
		// очередь можно), с требованием сильной модели и разбором петли.
		if task.Status != board.StatusPaused {
			t.Fatalf("задача %s: статус %s, ожидалась пауза", task.TaskID, task.Status)
		}
		if task.ModelTier != board.ModelTierLarge {
			t.Fatalf("задача %s: model_tier=%q, ожидался %q", task.TaskID, task.ModelTier, board.ModelTierLarge)
		}
		if task.Escalations != 2 {
			t.Fatalf("задача %s: эскалаций %d, ожидалось 2 (бюджет)", task.TaskID, task.Escalations)
		}
		if len(task.Injections) == 0 {
			t.Fatalf("задача %s: после петли должна остаться инъекция с разбором", task.TaskID)
		}
	}
	if !audited {
		t.Fatalf("ни одна задача не эскалировалась: %+v", tasks)
	}
	if len(audit) == 0 {
		t.Fatal("аудит (SetStatusNotifier) должен получать решения оркестратора")
	}
	joined := strings.Join(audit, "\n")
	if !strings.Contains(joined, "эскалация") {
		t.Fatalf("в аудите нет эскалации: %v", audit)
	}
}

// Зацикливание архитектора: эпики не публикуются, и запуск падает сразу на
// своей фазе, а не идёт дальше по конвейеру с пустой доской.
func TestKanbanArchitectLoopFails(t *testing.T) {
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-arch-loop"})
	projectDir := projects.ProjectDir("kanban-arch-loop")
	t.Cleanup(func() { _ = os.RemoveAll(projectDir) })

	kr := NewKanbanRunner(&architectLoopProvider{}, store)

	err := kr.Run(context.Background(), "kanban-arch-loop", "Сделай сервис")
	if err == nil {
		t.Fatal("зацикливание архитектора должно останавливать запуск")
	}
	if !strings.Contains(err.Error(), "зациклился") {
		t.Fatalf("ошибка должна называть зацикливание, got %v", err)
	}
	if epics, eerr := store.ListEpics(context.Background()); eerr != nil {
		t.Fatalf("ListEpics: %v", eerr)
	} else if len(epics) != 0 {
		t.Fatalf("при зацикливании архитектора эпиков быть не должно, got %d", len(epics))
	}
}

// architectLoopProvider — архитектор зациклился с первого же вызова.
type architectLoopProvider struct{}

func (p *architectLoopProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	return &runner.AgentResponse{
		Looped:     true,
		LoopReason: "последние 5 раундов подряд не дали ни одного успешного действия",
		Rounds:     6,
	}, nil
}

func (p *architectLoopProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}
