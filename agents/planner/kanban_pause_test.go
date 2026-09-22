package planner

import (
	"ai/board"
	"context"
	"testing"
)

// TestKanbanSkipsPausedEpic — приостановленный эпик (Ф-6) «замораживает» свою
// ветку работы: оркестратор не раздаёт его задачи специалистам, не продвигает
// статусы и не считает его работой (board-only ушёл бы в ожидание). Пауза не
// блокирует остальные эпики. Возобновление возвращает эпик в цикл, а задачу —
// в исходный статус.
func TestKanbanSkipsPausedEpic(t *testing.T) {
	ctx := context.Background()
	kr, store := newKanbanRunner(t)
	kr.SetBoardOnly(true)

	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "E-1", Title: "Эпик", AssignedRole: "Backend Lead"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "T-1", Title: "Задача", AssignedRole: "Go Developer", SequenceOrder: 1},
		EpicID:   "E-1",
	}); err != nil {
		t.Fatal(err)
	}
	// Задача доводится до «готова к работе», эпик — до «в работе».
	for _, st := range []board.Status{board.StatusAnalysis, board.StatusReady} {
		if err := store.SetTaskStatus(ctx, "T-1", st); err != nil {
			t.Fatal(err)
		}
	}
	for _, st := range []board.Status{board.StatusAnalysis, board.StatusReady, board.StatusInProgress} {
		if err := store.SetEpicStatus(ctx, "E-1", st); err != nil {
			t.Fatal(err)
		}
	}

	// Пауза эпика: задача каскадом уходит «на паузу».
	if err := store.SetEpicStatus(ctx, "E-1", board.StatusPaused); err != nil {
		t.Fatalf("эпик in_progress -> paused: %v", err)
	}
	if task, err := store.GetTask(ctx, "T-1"); err != nil || task.Status != board.StatusPaused {
		t.Fatalf("задача после паузы эпика = %+v, %v", task, err)
	}

	// Работы на доске нет, цикл ничего не продвигает.
	if ok, err := kr.hasWork(ctx); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("приостановленный эпик не должен считаться работой")
	}
	progress, err := kr.runPhases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if progress {
		t.Fatal("приостановленный эпик не должен давать прогресс цикла")
	}
	if task, err := store.GetTask(ctx, "T-1"); err != nil || task.Status != board.StatusPaused {
		t.Fatalf("задача должна остаться на паузе: %+v, %v", task, err)
	}
	if epic, err := store.GetEpic(ctx, "E-1"); err != nil || epic.Status != board.StatusPaused {
		t.Fatalf("эпик должен остаться на паузе: %+v, %v", epic, err)
	}

	// Возобновление: эпик снова в работе оркестратора, задача — в прежнем статусе.
	if err := store.SetEpicStatus(ctx, "E-1", board.StatusReady); err != nil {
		t.Fatalf("эпик paused -> ready: %v", err)
	}
	if task, err := store.GetTask(ctx, "T-1"); err != nil || task.Status != board.StatusReady {
		t.Fatalf("задача после возобновления = %+v, %v (ожидался возврат в ready)", task, err)
	}
	if ok, err := kr.hasWork(ctx); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("после возобновления эпик должен снова считаться работой")
	}
}

// TestKanbanPauseDoesNotBlockOtherEpics — пауза одного эпика не замораживает
// очередь лидов: остальные эпики продолжают декомпозироваться (paused-задачи
// не считаются незавершённой работой конвейера).
func TestKanbanPauseDoesNotBlockOtherEpics(t *testing.T) {
	ctx := context.Background()
	kr, store := newKanbanRunner(t)

	// E-1 — приостановлен с незавершённой задачей, E-2 — ждёт декомпозиции.
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "E-1", Title: "На паузе", AssignedRole: "Backend Lead", SequenceOrder: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "T-1", Title: "Задача", AssignedRole: "Go Developer", SequenceOrder: 1},
		EpicID:   "E-1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetEpicStatus(ctx, "E-1", board.StatusPaused); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "E-2", Title: "В работе", AssignedRole: "Backend Lead", SequenceOrder: 2},
	}); err != nil {
		t.Fatal(err)
	}

	// Конвейер считается свободным: paused-задача не блокирует очередь лидов.
	if idle, err := kr.pipelineIdle(ctx); err != nil {
		t.Fatal(err)
	} else if !idle {
		t.Fatal("пауза эпика не должна блокировать очередь лидов")
	}

	// Следующий эпик для лида — E-2 (приостановленный E-1 пропускается).
	epics, err := store.ListEpics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	epic, needDecompose, _, err := kr.nextLeadEpic(epics)
	if err != nil {
		t.Fatal(err)
	}
	if epic == nil || epic.TaskID != "E-2" || !needDecompose {
		t.Fatalf("nextLeadEpic = %+v (ожидался E-2 с декомпозицией), err = %v", epic, err)
	}
}
