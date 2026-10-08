package planner

import (
	"ai/board"
	"ai/projects"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// TestKanbanBoardOnlyEmptyGoesStandby — запуск по кнопке (board-only) на пустой
// доске: оркестрация не создаёт новых записей (не вызывает архитектора/лидов,
// провайдер вообще не дёргается), уходит в режим ожидания и ждёт появления
// работы; остановка (контекст) выводит из ожидания.
func TestKanbanBoardOnlyEmptyGoesStandby(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-bo"})
	t.Cleanup(func() { _ = os.RemoveAll(projects.ProjectDir("kanban-bo")) })

	standby := make(chan bool, 8)
	kr := NewKanbanRunner(&kanbanProvider{}, store)
	kr.SetBoardOnly(true)
	kr.SetStandbyNotifier(func(v bool) {
		select {
		case standby <- v:
		default:
		}
	})

	done := make(chan error, 1)
	go func() { done <- kr.Run(ctx, "kanban-bo", "Продолжить") }()

	// Раннер входит в standby (без единого вызова провайдера — работаю только
	// с записями доски, которых нет).
	select {
	case v := <-standby:
		if !v {
			t.Fatalf("первый нотификатор standby = %v, want true", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("раннер не ушёл в standby за 3 с")
	}

	// Отменяем контекст — выход из ожидания, завершение без ошибок доски.
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run на остановке: %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("раннер не завершился после отмены")
	}
}

// TestKanbanBoardOnlyWakeLeavesStandby — runner в standby (пустая доска);
// после появления работы событие Wake выводит его из ожидания сразу, а не по
// 5-секундному тику опроса (Ф-2, PLAN-done-dashboard-events: board_changed → Wake).
func TestKanbanBoardOnlyWakeLeavesStandby(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-bo-wake"})
	t.Cleanup(func() { _ = os.RemoveAll(projects.ProjectDir("kanban-bo-wake")) })

	standby := make(chan bool, 8)
	kr := NewKanbanRunner(&kanbanProvider{}, store)
	kr.SetBoardOnly(true)
	kr.SetStandbyNotifier(func(v bool) {
		select {
		case standby <- v:
		default:
		}
	})

	done := make(chan error, 1)
	go func() { done <- kr.Run(ctx, "kanban-bo-wake", "Продолжить") }()

	select {
	case v := <-standby:
		if !v {
			t.Fatalf("первый нотификатор standby = %v, want true", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("раннер не ушёл в standby за 3 с")
	}

	// На доске появляется работа (эпик, доступный board-only декомпозиции),
	// и сервер будит runner'а событием Wake — не ждём ticker-standing.
	if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{
		TaskID:       "WAKE-01",
		Title:        "Эпик после standby",
		Description:  "Создаётся событием, будит ожидание",
		AssignedRole: "Backend Lead",
	}}); err != nil {
		t.Fatal(err)
	}
	kr.Wake()

	select {
	case v := <-standby:
		if v {
			t.Fatalf("после Wake нотификатор = %v, want false (работа появилась)", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Wake не вывел раннера из standby за 3 с")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("раннер не завершился после отмены")
	}
}

// TestKanbanBoardOnlyDecomposesExistingEpic — запуск по доске (board-only) с
// эпиком без задач (как после создания чатом): эпик берётся в работу — лид
// декомпозирует его на задачи, специалисты их выполняют, а новых эпиков
// архитектор не создаёт (фаза архитектора пропущена).
func TestKanbanBoardOnlyDecomposesExistingEpic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "kanban-bo-epic"})
	t.Cleanup(func() { _ = os.RemoveAll(projects.ProjectDir("kanban-bo-epic")) })

	if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{
		TaskID:       "CALC-01",
		Title:        "Веб-калькулятор",
		Description:  "Калькулятор на React.",
		AssignedRole: "Backend Lead",
	}}); err != nil {
		t.Fatal(err)
	}

	kr := NewKanbanRunner(&kanbanProvider{}, store)
	kr.SetBoardOnly(true)

	done := make(chan error, 1)
	go func() { done <- kr.Run(ctx, "kanban-bo-epic", "Продолжить") }()

	// Эпик без задач должен быть декомпозирован и исполнен: доска решена.
	deadline := time.Now().Add(5 * time.Second)
	for {
		ok, err := store.AllDone(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("доска не решена за 5 с: эпик без задач не взят в работу")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Новых эпиков не создано (архитектор в board-only не работает).
	epics, err := store.ListEpics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(epics) != 1 {
		t.Fatalf("эпиков %d, ожидался 1 (архитектор не должен создавать новые)", len(epics))
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) == 0 {
		t.Fatal("эпик не декомпозирован: задач нет")
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run на остановке: %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("раннер не завершился после отмены")
	}
}
