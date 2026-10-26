package tools

import (
	"ai/infrastructure/board"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// newBoardToolSet создаёт полный набор board-инструментов поверх in-memory Redis.
func newBoardToolSet(t *testing.T) (*Set, *board.Store) {
	t.Helper()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "testproj"})
	return Select(boardToolNames, Deps{Board: board.NewDomainStore(store)}), store
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

	// Дубликат эпика — ошибка (с подсказкой модели, как выйти из тупика).
	msg := errExec(t, set, BoardCreateEpic, map[string]any{
		"task_id": "EPIC-01", "title": "Dup", "description": "dup", "assigned_role": "Backend Lead",
	})
	if msg == "" {
		t.Fatalf("ожидалось сообщение об ошибке дубликата")
	}
	for _, hint := range []string{"существует", "BoardUpdateEpic", "BoardListEpics"} {
		if !strings.Contains(msg, hint) {
			t.Fatalf("сообщение дубликата должно подсказывать выход (%q): %q", hint, msg)
		}
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
	case board.StatusInfra:
		steps = []board.Status{board.StatusInfra}
	case board.StatusTestCases:
		steps = []board.Status{board.StatusInfra, board.StatusTestCases}
	case board.StatusDevelopment:
		steps = []board.Status{board.StatusInfra, board.StatusTestCases, board.StatusDevelopment}
	case board.StatusDone:
		steps = []board.Status{board.StatusInfra, board.StatusTestCases, board.StatusDevelopment, board.StatusDone}
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

	// Задача в работе (test_cases/development) или отменённая (cancelled):
	// удаление запрещено. Задачи не взятые в работу (draft/infra) и завершённые
	// (done) — можно удалить. Каждый сценарий на своей задаче.
	scenarios := []struct {
		status board.Status
		taskID string
		allow  bool // true — удаление разрешено, false — запрещено
	}{
		{board.StatusInfra, "T-I", true},
		{board.StatusTestCases, "T-T", false},
		{board.StatusDevelopment, "T-D", false},
		{board.StatusDone, "T-F", true},
	}
	for _, sc := range scenarios {
		mustExec(t, set, BoardCreateTask, map[string]any{
			"epic_id": "EPIC-01", "task_id": sc.taskID, "title": "Задача",
			"description": "описание", "assigned_role": "Senior Go Developer",
		})
		chainStatus(t, store, sc.taskID, sc.status)
		if sc.allow {
			mustExec(t, set, BoardDeleteTask, map[string]any{"task_id": sc.taskID})
			if _, err := store.GetTask(ctx, sc.taskID); err == nil {
				t.Fatalf("задача в статусе %s должна удаляться", sc.status)
			}
		} else {
			msg := errExec(t, set, BoardDeleteTask, map[string]any{"task_id": sc.taskID})
			if msg == "" {
				t.Fatalf("удаление в статусе %s должно быть запрещено", sc.status)
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
	}
	set := Select(readOnly, Deps{Board: board.NewDomainStore(store)})

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

// TestSelectSkipsBoardToolsWithoutStore — без подключённого board.Store
// (например, лид в standalone-режиме plan) board-инструменты не попадают
// в набор: их вызов заведомо вернёт «доска не подключена», а модель лишь
// бесполезно жжёт раунды.
func TestSelectSkipsBoardToolsWithoutStore(t *testing.T) {
	set := Select(boardToolNames, Deps{})
	if len(set.byName) != 0 {
		t.Fatalf("без доски board-инструменты не должны попасть в набор, got %d", len(set.byName))
	}
	if _, ok := set.Get(BoardListEpics); ok {
		t.Fatal("BoardListEpics не должно быть в наборе без доски")
	}
}

// TestBoardToolCallWithoutStoreSoftError — модель (например, лид, чей промпт
// упоминает доску) всё равно вызывает board-инструмент, которого нет в наборе
// (план-режим): Execute должен вернуть мягкую ошибку «доска не подключена»,
// а не фатал "function ... not in tool set". По промпту лид переходит к
// JSON-декомпозиции. По-настоящему неизвестный инструмент по-прежнему падает.
func TestBoardToolCallWithoutStoreSoftError(t *testing.T) {
	set := Select([]string{"List", "ReadFiles"}, Deps{})

	out, err := set.Execute(BoardCreateTask, map[string]any{
		"task_id": "BEL-01", "title": "X", "description": "y", "epic_id": "E-1",
		"assigned_role": "Senior Go Developer",
	})
	if err != nil {
		t.Fatalf("board-инструмент без доски должен вернуть мягкую ошибку, а не фатал: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("не JSON: %v (%s)", err, out)
	}
	if m["status"] != "error" {
		t.Fatalf("ожидался статус error, got %s", out)
	}
	if msg, _ := m["message"].(string); msg != "доска не подключена" {
		t.Fatalf("ожидалось «доска не подключена», got %q", msg)
	}

	if _, err := set.Execute("NopeTool", nil); err == nil {
		t.Fatal("неизвестный инструмент должен возвращать фатал not in tool set")
	}
}

// Модель обратилась к несуществующему эпику (выдумала ID): ответ должен
// перечислять реальные записи доски, иначе модель продолжает перебирать
// несуществующие идентификаторы и оркестрация стоит на месте.
func TestBoardNotFoundListsExistingIDs(t *testing.T) {
	set, store := newBoardToolSet(t)
	ctx := context.Background()

	if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{
		TaskID: "CHAT-001", Title: "Оптимизация логгирования", Description: "d", AssignedRole: "DevOps Lead",
	}}); err != nil {
		t.Fatalf("создание эпика: %v", err)
	}
	if err := store.CreateTask(ctx, &board.Task{TaskSpec: board.TaskSpec{
		TaskID: "DOL-01", Title: "Compose", Description: "d", AssignedRole: "DevOps Engineer",
	}, EpicID: "CHAT-001"}); err != nil {
		t.Fatalf("создание задачи: %v", err)
	}

	msg := errExec(t, set, BoardGetEpic, map[string]any{"epic_id": "REWRITE-01"})
	for _, want := range []string{"CHAT-001", "DOL-01", "Не выдумывай ID"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("в сообщении не хватает %q: %s", want, msg)
		}
	}

	// Пустая доска: подсказка не перечисляет ID, а сообщает, что их нет.
	empty, _ := newBoardToolSet(t)
	if msg := errExec(t, empty, BoardGetTask, map[string]any{"task_id": "T-01"}); !strings.Contains(msg, "нет ни эпиков, ни задач") {
		t.Fatalf("для пустой доски ожидалось сообщение об отсутствии записей, got %q", msg)
	}
}

// BoardSetTaskStatus(human_help) — честный выход специалиста из невыполнимой
// задачи (нет исходного кода/зависимостей, блокер). Без причины инструмент
// отказывает; с причиной она оседает в записи задачи (карточка UI) и
// очищается при выходе из human_help, чтобы не висеть после возобновления.

// BoardListTasks(short=true) — экономит контекст: description у всех задач пустой.
func TestBoardListTasksShort(t *testing.T) {
	set, store := newBoardToolSet(t)
	ctx := context.Background()

	epicID := "E-SHORT"
	if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{
		TaskID: epicID, Title: "Эпик", Description: "epic desc", AssignedRole: "Architect",
	}}); err != nil {
		t.Fatalf("создание эпика: %v", err)
	}
	for i := 1; i <= 3; i++ {
		desc := strings.Repeat("x", 5000)
		if err := store.CreateTask(ctx, &board.Task{TaskSpec: board.TaskSpec{
			TaskID: fmt.Sprintf("T-%d", i), Title: fmt.Sprintf("T%d", i),
			Description: desc, AssignedRole: "Developer",
		}, EpicID: epicID}); err != nil {
			t.Fatalf("создание задачи: %v", err)
		}
	}

	// Full: description должен быть не пустым
	out, err := set.Execute(BoardListTasks, map[string]any{})
	if err != nil {
		t.Fatalf("BoardListTasks: %v", err)
	}
	var fullTasks []map[string]any
	if err := json.Unmarshal(out, &fullTasks); err != nil {
		t.Fatalf("не JSON: %v", err)
	}
	if len(fullTasks) != 3 {
		t.Fatalf("ожидаем 3 задачи, got %d", len(fullTasks))
	}
	var totalDescLen int
	for _, tk := range fullTasks {
		if d, _ := tk["description"].(string); d != "" {
			totalDescLen += len(d)
		}
	}
	if totalDescLen == 0 {
		t.Fatal("при short=false description должен быть не пустым")
	}

	// Short: description должен быть пустым
	out2, err := set.Execute(BoardListTasks, map[string]any{"short": true})
	if err != nil {
		t.Fatalf("BoardListTasks short: %v", err)
	}
	var shortTasks []map[string]any
	if err := json.Unmarshal(out2, &shortTasks); err != nil {
		t.Fatalf("не JSON: %v", err)
	}
	if len(shortTasks) != 3 {
		t.Fatalf("ожидаем 3 задачи при short=true, got %d", len(shortTasks))
	}
	for _, tk := range shortTasks {
		if d, _ := tk["description"].(string); d != "" {
			t.Fatalf("при short=true description должен быть пустым, got %q", d)
		}
	}
	// Другие поля должны остаться
	for _, tk := range shortTasks {
		if id, _ := tk["task_id"].(string); id == "" {
			t.Fatal("task_id должен остаться при short=true")
		}
		if title, _ := tk["title"].(string); title == "" {
			t.Fatal("title должен остаться при short=true")
		}
		if status, _ := tk["status"].(string); status == "" {
			t.Fatal("status должен остаться при short=true")
		}
	}
}

// BoardListTasks(epic_id) — фильтр по эпику работает.
func TestBoardListTasksEpicFilter(t *testing.T) {
	set, store := newBoardToolSet(t)
	ctx := context.Background()

	for _, eid := range []string{"E-1", "E-2"} {
		if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{
			TaskID: eid, Title: eid, Description: "d", AssignedRole: "Architect",
		}}); err != nil {
			t.Fatalf("создание эпика %s: %v", eid, err)
		}
		for j := 1; j <= 2; j++ {
			if err := store.CreateTask(ctx, &board.Task{TaskSpec: board.TaskSpec{
				TaskID: fmt.Sprintf("%s-T%d", eid, j), Title: fmt.Sprintf("T%d", j),
				Description: "desc", AssignedRole: "Developer",
			}, EpicID: eid}); err != nil {
				t.Fatalf("создание задачи: %v", err)
			}
		}
	}

	// Без фильтра — все 4 задачи
	out, err := set.Execute(BoardListTasks, map[string]any{})
	if err != nil {
		t.Fatalf("BoardListTasks: %v", err)
	}
	var allTasks []map[string]any
	if err := json.Unmarshal(out, &allTasks); err != nil {
		t.Fatalf("не JSON: %v", err)
	}
	if len(allTasks) != 4 {
		t.Fatalf("ожидаем 4 задачи без фильтра, got %d", len(allTasks))
	}

	// С фильтром epic_id=E-1 — только 2 задачи
	out2, err := set.Execute(BoardListTasks, map[string]any{"epic_id": "E-1"})
	if err != nil {
		t.Fatalf("BoardListTasks filter: %v", err)
	}
	var filterTasks []map[string]any
	if err := json.Unmarshal(out2, &filterTasks); err != nil {
		t.Fatalf("не JSON: %v", err)
	}
	if len(filterTasks) != 2 {
		t.Fatalf("ожидаем 2 задачи для E-1, got %d", len(filterTasks))
	}
	for _, tk := range filterTasks {
		if epid, _ := tk["epic_id"].(string); epid != "E-1" {
			t.Fatalf("ожидаем epic_id=E-1, got %s", epid)
		}
	}
}

