package tools

import (
	"ai/board"
	"context"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// boardToolNames — все board-инструменты для тестового набора.
var boardToolNames = []string{
	BoardListEpics, BoardGetEpic, BoardListTasks, BoardGetTask, BoardListBugs, BoardGetBug,
	BoardCreateEpic, BoardUpdateEpic, BoardDeleteEpic, BoardSetEpicStatus,
	BoardCreateTask, BoardUpdateTask, BoardDeleteTask, BoardSetTaskStatus,
	BoardCreateBug, BoardSetBugStatus, BoardReviewBug,
}

// newBoardToolSet создаёт полный набор board-инструментов поверх in-memory Redis.
func newBoardToolSet(t *testing.T) (*Set, *board.Store) {
	t.Helper()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "testproj"})
	return Select(boardToolNames, Deps{Board: store}), store
}

// mustExec выполняет инструмент и требует успешный ответ.
func mustExec(t *testing.T, set *Set, name string, args map[string]any) map[string]any {
	t.Helper()
	out, err := set.Execute(name, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("%s: не JSON: %v (%s)", name, err, out)
	}
	if m["status"] != "success" {
		t.Fatalf("%s: status=%v, ответ: %s", name, m["status"], out)
	}
	return m
}

// errExec выполняет инструмент и требует ответ с ошибкой; возвращает сообщение.
func errExec(t *testing.T, set *Set, name string, args map[string]any) string {
	t.Helper()
	out, err := set.Execute(name, args)
	if err != nil {
		t.Fatalf("%s: инструмент сам вернул ошибку: %v", name, err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("%s: не JSON: %v (%s)", name, err, out)
	}
	if m["status"] != "error" {
		t.Fatalf("%s: ожидался error, получено %s", name, out)
	}
	msg, _ := m["message"].(string)
	return msg
}

func (s *Set) marshalExec(t *testing.T, name string, args map[string]any, v any) {
	t.Helper()
	out, err := s.Execute(name, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if err := json.Unmarshal(out, v); err != nil {
		t.Fatalf("%s: не JSON: %v (%s)", name, err, out)
	}
}

func TestBoardToolsEpicAndTaskCRUD(t *testing.T) {
	set, store := newBoardToolSet(t)
	ctx := context.Background()

	mustExec(t, set, BoardCreateEpic, map[string]any{
		"task_id": "EPIC-01", "title": "Backend API", "description": "REST API на Go",
		"assigned_role": "Backend Lead", "sequence_order": "1", "can_run_parallel": true,
		"architecture_summary": "Go, chi, postgres",
	})

	// Дубликат эпика — ошибка.
	msg := errExec(t, set, BoardCreateEpic, map[string]any{
		"task_id": "EPIC-01", "title": "Dup", "description": "dup", "assigned_role": "Backend Lead",
	})
	if msg == "" {
		t.Fatalf("ожидалось сообщение об ошибке дубликата")
	}

	// Задачи в эпике: T-02 зависит от T-01.
	mustExec(t, set, BoardCreateTask, map[string]any{
		"epic_id": "EPIC-01", "task_id": "T-01", "title": "Модель пользователя",
		"description": "Реализовать User", "assigned_role": "Senior Go Developer",
		"sequence_order": 2,
	})
	mustExec(t, set, BoardCreateTask, map[string]any{
		"epic_id": "EPIC-01", "task_id": "T-02", "title": "Хандлер /users",
		"description": "Реализовать слой API", "assigned_role": "Senior Go Developer",
		"sequence_order": "1", "dependencies": []interface{}{"T-01"},
	})

	// Фильтрация списка задач по эпику.
	var tasks []*board.Task
	set.marshalExec(t, BoardListTasks, map[string]any{"epic_id": "EPIC-01"}, &tasks)
	if len(tasks) != 2 {
		t.Fatalf("список задач эпика = %d, ожидалось 2", len(tasks))
	}

	// Переприоритезация: T-01 становится первой по sequence_order.
	mustExec(t, set, BoardUpdateTask, map[string]any{"task_id": "T-01", "sequence_order": "0"})
	got, err := store.GetTask(ctx, "T-01")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.SequenceOrder.Int() != 0 {
		t.Fatalf("sequence_order T-01 = %d, ожидалось 0", got.SequenceOrder.Int())
	}

	// Перенос задачи в другой эпик.
	mustExec(t, set, BoardCreateEpic, map[string]any{
		"task_id": "EPIC-02", "title": "Frontend", "description": "React UI",
		"assigned_role": "Frontend Lead",
	})
	m := mustExec(t, set, BoardUpdateTask, map[string]any{"task_id": "T-02", "epic_id": "EPIC-02"})
	if m["epic_id"] != "EPIC-02" {
		t.Fatalf("перенос задачи: epic_id=%v", m["epic_id"])
	}
	epic1, _ := store.GetEpic(ctx, "EPIC-01")
	if len(epic1.Tasks) != 1 || epic1.Tasks[0] != "T-01" {
		t.Fatalf("после переноса эпик EPIC-01 должен содержать только T-01: %v", epic1.Tasks)
	}
	epic2, _ := store.GetEpic(ctx, "EPIC-02")
	if len(epic2.Tasks) != 1 || epic2.Tasks[0] != "T-02" {
		t.Fatalf("после переноса эпик EPIC-02 должен содержать только T-02: %v", epic2.Tasks)
	}

	// Эпик можно удалить (задачи не взяты в работу), его дочерние задачи удаляются.
	mustExec(t, set, BoardDeleteEpic, map[string]any{"epic_id": "EPIC-02"})
	if _, err := store.GetEpic(ctx, "EPIC-02"); err == nil {
		t.Fatalf("эпик EPIC-02 должен быть удалён")
	}
	if _, err := store.GetTask(ctx, "T-02"); err == nil {
		t.Fatalf("задача T-02 должна быть удалена каскадно")
	}
}

// chainStatus прогоняет задачу по полной цепочке новых статусов до целевого.
func chainStatus(t *testing.T, store *board.Store, id string, target board.Status) {
	t.Helper()
	ctx := context.Background()
	var steps []board.Status
	switch target {
	case board.StatusAnalysis:
		steps = []board.Status{board.StatusAnalysis}
	case board.StatusReady:
		steps = []board.Status{board.StatusAnalysis, board.StatusReady}
	case board.StatusInProgress:
		steps = []board.Status{board.StatusAnalysis, board.StatusReady, board.StatusInProgress}
	case board.StatusDone:
		steps = []board.Status{board.StatusAnalysis, board.StatusReady, board.StatusInProgress, board.StatusDone}
	case board.StatusCancelled:
		steps = []board.Status{board.StatusCancelled}
	}
	for _, st := range steps {
		if err := store.SetTaskStatus(ctx, id, st); err != nil {
			t.Fatalf("перевод %s -> %s: %v", id, st, err)
		}
	}
}

func TestBoardToolsTaskDeleteGuards(t *testing.T) {
	set, store := newBoardToolSet(t)
	ctx := context.Background()

	mustExec(t, set, BoardCreateEpic, map[string]any{
		"task_id": "EPIC-01", "title": "Backend", "description": "API",
		"assigned_role": "Backend Lead",
	})
	mustExec(t, set, BoardCreateTask, map[string]any{
		"epic_id": "EPIC-01", "task_id": "T-01", "title": "Задача",
		"description": "описание", "assigned_role": "Senior Go Developer",
	})

	// Задача в работе или выполненная: удаление запрещено. Пока задача не взята
	// в работу (analysis/ready) — удалять можно. Каждый сценарий на своей задаче,
	// чтобы не сбрасывать статус через конечный автомат.
	scenarios := []struct {
		status board.Status
		taskID string
	}{
		{board.StatusAnalysis, "T-A"},
		{board.StatusReady, "T-R"},
		{board.StatusInProgress, "T-I"},
		{board.StatusDone, "T-D"},
	}
	for _, sc := range scenarios {
		mustExec(t, set, BoardCreateTask, map[string]any{
			"epic_id": "EPIC-01", "task_id": sc.taskID, "title": "Задача",
			"description": "описание", "assigned_role": "Senior Go Developer",
		})
		chainStatus(t, store, sc.taskID, sc.status)
		if sc.status == board.StatusInProgress || sc.status == board.StatusDone {
			msg := errExec(t, set, BoardDeleteTask, map[string]any{"task_id": sc.taskID})
			if msg == "" {
				t.Fatalf("удаление в статусе %s должно быть запрещено", sc.status)
			}
		} else {
			mustExec(t, set, BoardDeleteTask, map[string]any{"task_id": sc.taskID})
			if _, err := store.GetTask(ctx, sc.taskID); err == nil {
				t.Fatalf("задача в статусе %s должна удаляться", sc.status)
			}
		}
	}
}

func TestBoardToolsReadOnlyScope(t *testing.T) {
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "testproj"})

	// Читающий участник перечисляет только read-инструменты: write недоступен.
	readOnly := []string{
		BoardListEpics, BoardGetEpic, BoardListTasks, BoardGetTask,
		BoardListBugs, BoardGetBug,
	}
	set := Select(readOnly, Deps{Board: store})

	if out, err := set.Execute(BoardCreateEpic, map[string]any{
		"task_id": "EPIC-01", "title": "X", "description": "y", "assigned_role": "Backend Lead",
	}); err == nil {
		t.Fatalf("write-инструмент не должен быть доступен читающему участнику: %s", out)
	}

	// Список эпиков с пустой доской — валидный JSON-массив.
	var epics []*board.Epic
	set.marshalExec(t, BoardListEpics, nil, &epics)
	if len(epics) != 0 {
		t.Fatalf("пустая доска должна вернуть [], получено %v", epics)
	}
}

func TestBoardToolsBugPipeline(t *testing.T) {
	set, store := newBoardToolSet(t)
	ctx := context.Background()

	// Эпик и задача, в которой QA-инженер найдёт дефект.
	mustExec(t, set, BoardCreateEpic, map[string]any{
		"task_id": "EPIC-01", "title": "Backend", "description": "API",
		"assigned_role": "Backend Lead",
	})
	mustExec(t, set, BoardCreateTask, map[string]any{
		"epic_id": "EPIC-01", "task_id": "T-01", "title": "Хандлер users",
		"description": "REST", "assigned_role": "Senior Go Developer",
	})

	// QA-инженер нашёл дефект при выполнении задачи.
	mustExec(t, set, BoardCreateBug, map[string]any{
		"bug_id": "BUG-01", "title": "GET /users возвращает 500",
		"description": "При статусе 500; контракт нарушен", "task_id": "T-01",
		"epic_id": "EPIC-01", "reporter_role": "QA Engineer",
	})
	r, err := store.GetBugReport(ctx, "BUG-01")
	if err != nil {
		t.Fatalf("GetBugReport: %v", err)
	}
	if r.Status != board.BugStatusNew {
		t.Fatalf("статус нового багрепорта = %s, ожидался new", r.Status)
	}

	// QA Lead подтверждает (не нейрослоп).
	mustExec(t, set, BoardSetBugStatus, map[string]any{"bug_id": "BUG-01", "status": "confirmed"})
	r, _ = store.GetBugReport(ctx, "BUG-01")
	if r.Status != board.BugStatusConfirmed {
		t.Fatalf("после триажа статус = %s", r.Status)
	}

	// QA Lead не может ставить статус мимо цепочки (например done).
	msg := errExec(t, set, BoardSetBugStatus, map[string]any{"bug_id": "BUG-01", "status": "done"})
	if msg == "" {
		t.Fatalf("QA Lead не должен ставить произвольные статусы багрепорта")
	}

	// Архитектор: вердикт fix + создание эпика исправления в одном вызове.
	m := mustExec(t, set, BoardReviewBug, map[string]any{
		"bug_id": "BUG-01", "verdict": "fix",
		"epic_task_id": "FIX-01", "epic_title": "Починить /users",
		"epic_description": "Исправить обработку ошибок БД", "epic_assigned_role": "Backend Lead",
		"epic_sequence_order": "2",
	})
	if m["verdict"] != "fix" || m["fix_epic_id"] != "FIX-01" {
		t.Fatalf("вердикт fix: %v", m)
	}

	r, _ = store.GetBugReport(ctx, "BUG-01")
	if r.Status != board.BugStatusFix || r.FixEpicID != "FIX-01" {
		t.Fatalf("после экспертизы: status=%s fix_epic=%s", r.Status, r.FixEpicID)
	}
	fix, err := store.GetEpic(ctx, "FIX-01")
	if err != nil {
		t.Fatalf("эпик исправления не создан: %v", err)
	}
	if fix.Revision != 1 {
		t.Fatalf("ревизия нового эпика = %d, ожидалась 1", fix.Revision)
	}

	// Эпик исправления выполнен — багрепорт закрывается автоматически.
	if err := store.SetEpicStatus(ctx, "FIX-01", board.StatusAnalysis); err != nil {
		t.Fatalf("эпик: %v", err)
	}
	if err := store.SetEpicStatus(ctx, "FIX-01", board.StatusReady); err != nil {
		t.Fatalf("эпик: %v", err)
	}
	if err := store.SetEpicStatus(ctx, "FIX-01", board.StatusInProgress); err != nil {
		t.Fatalf("эпик: %v", err)
	}
	if err := store.SetEpicStatus(ctx, "FIX-01", board.StatusDone); err != nil {
		t.Fatalf("эпик done: %v", err)
	}
	n, err := store.MarkBugsFixedForEpic(ctx, "FIX-01")
	if err != nil || n != 1 {
		t.Fatalf("MarkBugsFixedForEpic: n=%d err=%v", n, err)
	}
	r, _ = store.GetBugReport(ctx, "BUG-01")
	if r.Status != board.BugStatusFixed {
		t.Fatalf("багрепорт после фикса: status=%s, ожидался fixed", r.Status)
	}

	// Исправленный багрепорт пропускается списком с фильтром по статусу.
	var fixed []*board.BugReport
	set.marshalExec(t, BoardListBugs, map[string]any{"status": "fixed"}, &fixed)
	if len(fixed) != 1 {
		t.Fatalf("после фикса список fixed = %d, ожидался 1", len(fixed))
	}
}
