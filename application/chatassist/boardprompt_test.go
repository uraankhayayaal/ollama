package chatassist

import (
	"strings"
	"testing"

	iboard "ai/infrastructure/board"
)

func TestOrchestrationStatus(t *testing.T) {
	if got := OrchestrationStatus(false, true); !strings.Contains(got, "ожидает решения человека") {
		t.Fatalf("gating = %q", got)
	}
	if got := OrchestrationStatus(true, false); !strings.Contains(got, "идёт выполнение задачи") {
		t.Fatalf("running = %q", got)
	}
	if got := OrchestrationStatus(false, false); !strings.Contains(got, "ничего не выполняется") {
		t.Fatalf("idle = %q", got)
	}
	// Затвор важнее запуска.
	if got := OrchestrationStatus(true, true); !strings.Contains(got, "ожидает решения человека") {
		t.Fatalf("running+gating = %q", got)
	}
}

func TestBoardRevisionLine(t *testing.T) {
	got := BoardRevisionLine(7)
	if !strings.Contains(got, "Ревизия доски: 7") {
		t.Fatalf("line = %q", got)
	}
}

func TestBoardSummaryEmpty(t *testing.T) {
	got := BoardSummary(BoardView{})
	for _, want := range []string{"Эпиков нет.", "Задач нет.", "Багов нет."} {
		if !strings.Contains(got, want) {
			t.Fatalf("пустая доска: нет %q:\n%s", want, got)
		}
	}
}

func TestBoardSummaryFull(t *testing.T) {
	task := &iboard.Task{
		TaskSpec: iboard.TaskSpec{TaskID: "task-1", Title: "Форма входа", AssignedRole: "Dev"},
		EpicID:   "epic-a",
		Status:   iboard.StatusInProgress,
		Assignee: "Алиса",
	}
	conflictTask := &iboard.Task{
		TaskSpec:           iboard.TaskSpec{TaskID: "task-c", Title: "Фича"},
		EpicID:             "epic-c",
		MergeConflictFiles: []string{"my-rust-app"},
	}
	v := BoardView{
		Meta: &iboard.Meta{ProjectName: "proj", Task: "Сделать логин", Status: iboard.StatusInProgress},
		Epics: []*iboard.Epic{
			{TaskSpec: iboard.TaskSpec{TaskID: "epic-a", Title: "Логин"}, Status: iboard.StatusInProgress, Tasks: []string{"task-1"}},
			{TaskSpec: iboard.TaskSpec{TaskID: "epic-c", Title: "Чистка кода"}, MergeConflictFiles: []string{"src/main.rs"}},
		},
		Tasks: []*iboard.Task{task, conflictTask},
		Bugs:  []*iboard.BugReport{{BugID: "B-1", Status: iboard.BugStatusConfirmed}},
	}
	got := BoardSummary(v)
	for _, want := range []string{
		"Исходная задача проекта",
		"Сделать логин",
		"Эпики:",
		"Логин",
		"Задачи по статусам:",
		"В работе сейчас:",
		"Форма входа",
		"исполнитель: Алиса",
		"Конфликты мёрджа",
		"эпики (main ↔ релизная ветка): epic-c [src/main.rs]",
		"задачи (ветка ↔ релиз эпика): task-c [my-rust-app]",
		"Баги:",
		"подтверждён: 1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("сводка не содержит %q:\n%s", want, got)
		}
	}
}
