package tools

// Инструменты общей Kanban-доски проекта (board.Store). Доступ определяет
// агент перечислением имён инструментов в своём наборе: кому разрешено только
// читать — тот перечисляет только read-инструменты; кому писать — write также.
// Единый источник определений — реестр (registry.go) и этот файл.

import (
	"ai/board"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Имена board-инструментов (экспортятся для агентов).
const (
	BoardListEpics     = "BoardListEpics"
	BoardGetEpic       = "BoardGetEpic"
	BoardListTasks     = "BoardListTasks"
	BoardGetTask       = "BoardGetTask"
	BoardListBugs      = "BoardListBugs"
	BoardGetBug        = "BoardGetBug"
	BoardCreateEpic    = "BoardCreateEpic"
	BoardUpdateEpic    = "BoardUpdateEpic"
	BoardDeleteEpic    = "BoardDeleteEpic"
	BoardSetEpicStatus = "BoardSetEpicStatus"
	BoardCreateTask    = "BoardCreateTask"
	BoardUpdateTask    = "BoardUpdateTask"
	BoardDeleteTask    = "BoardDeleteTask"
	BoardSetTaskStatus = "BoardSetTaskStatus"
	BoardCreateBug     = "BoardCreateBugReport"
	BoardSetBugStatus  = "BoardSetBugStatus"
	BoardReviewBug     = "BoardReviewBugReport"
)

// taskSpecProps — свойства JSON-схемы задачи/эпика (общие для многих типов).
func taskSpecProps() map[string]any {
	return map[string]any{
		"task_id": map[string]any{"type": "string", "description": "Уникальный ID записи доски (например T-01, ARCH-01)"},
		"title":   map[string]any{"type": "string", "description": "Название задачи/эпика"},
		"description": map[string]any{
			"type":        "string",
			"description": "Детальное техническое описание, включая контракты взаимодействия",
		},
		"assigned_role":    map[string]any{"type": "string", "description": "Роль исполнителя/лида направления (например Senior Go Developer, QA Lead)"},
		"sequence_order":   map[string]any{"type": "integer", "description": "Порядок выполнения (переприоритезация: чем меньше, тем раньше)"},
		"can_run_parallel": map[string]any{"type": "boolean", "description": "Может ли запись выполняться параллельно с соседними"},
		"dependencies": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": "ID записей (задач/эпиков) доски, от которых зависит эта запись",
		},
	}
}

// parseTaskSpec извлекает поля задачи из аргументов (без task_id).
func parseTaskSpec(args map[string]any) board.TaskSpec {
	var ts board.TaskSpec
	if v, ok := args["task_id"].(string); ok {
		ts.TaskID = v
	}
	ts.Title, _ = args["title"].(string)
	ts.Description, _ = args["description"].(string)
	ts.AssignedRole, _ = args["assigned_role"].(string)
	if v, ok := args["sequence_order"]; ok {
		switch n := v.(type) {
		case float64:
			ts.SequenceOrder = board.FlexInt(int(n))
		case int:
			ts.SequenceOrder = board.FlexInt(n)
		case string:
			if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
				ts.SequenceOrder = board.FlexInt(i)
			}
		}
	}
	switch v := args["can_run_parallel"].(type) {
	case bool:
		ts.CanRunParallel = board.FlexBool(v)
	case string:
		ts.CanRunParallel = board.FlexBool(strings.EqualFold(strings.TrimSpace(v), "true"))
	case float64:
		ts.CanRunParallel = board.FlexBool(v != 0)
	}
	if deps, ok := args["dependencies"].([]any); ok {
		for _, d := range deps {
			if s, ok := d.(string); ok && s != "" {
				ts.Dependencies = append(ts.Dependencies, s)
			}
		}
	}
	return ts
}

// strArg — строковый аргумент из map.
func strArg(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return strings.TrimSpace(s)
}

// boardErr приводит ошибку доски к текстовому JSON для модели.
func boardErr(action string, err error) ([]byte, error) {
	res, _ := json.Marshal(map[string]string{
		"status":  "error",
		"action":  action,
		"message": err.Error(),
	})
	return res, nil
}

// boardOK возвращает JSON-успех инструмента доски.
func boardOK(body map[string]any) ([]byte, error) {
	body["status"] = "success"
	return json.Marshal(body)
}

// --- Чтение ---

type boardListEpicsTool struct{ b *board.Store }

func (t *boardListEpicsTool) Name() string { return BoardListEpics }
func (t *boardListEpicsTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        BoardListEpics,
		Description: "Список всех эпиков общей Kanban-доски проекта (статусы, приоритеты, связи с задачами). Доступно на чтение всем участникам команды.",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	}
}
func (t *boardListEpicsTool) Execute(args map[string]any) ([]byte, error) {
	if t.b == nil {
		return boardErr(BoardListEpics, fmt.Errorf("доска не подключена"))
	}
	epics, err := t.b.ListEpics(context.Background())
	if err != nil {
		return boardErr(BoardListEpics, err)
	}
	data, _ := json.Marshal(epics)
	return data, nil
}

type boardGetEpicTool struct{ b *board.Store }

func (t *boardGetEpicTool) Name() string { return BoardGetEpic }
func (t *boardGetEpicTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        BoardGetEpic,
		Description: "Получить эпик доски по ID (детали, контракты, список задач).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"epic_id": map[string]any{"type": "string", "description": "ID эпика"},
			},
			"required": []string{"epic_id"},
		},
	}
}
func (t *boardGetEpicTool) Execute(args map[string]any) ([]byte, error) {
	if t.b == nil {
		return boardErr(BoardGetEpic, fmt.Errorf("доска не подключена"))
	}
	e, err := t.b.GetEpic(context.Background(), strArg(args, "epic_id"))
	if err != nil {
		return boardErr(BoardGetEpic, err)
	}
	return json.Marshal(e)
}

type boardListTasksTool struct{ b *board.Store }

func (t *boardListTasksTool) Name() string { return BoardListTasks }
func (t *boardListTasksTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        BoardListTasks,
		Description: "Список всех задач общей Kanban-доски проекта (статусы, исполнители, приоритеты). Доступно на чтение всем участникам команды.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"epic_id": map[string]any{"type": "string", "description": "Ограничить список задачами указанного эпика (необязательно)"},
			},
		},
	}
}
func (t *boardListTasksTool) Execute(args map[string]any) ([]byte, error) {
	if t.b == nil {
		return boardErr(BoardListTasks, fmt.Errorf("доска не подключена"))
	}
	ctx := context.Background()
	tasks, err := t.b.ListTasks(ctx)
	if err != nil {
		return boardErr(BoardListTasks, err)
	}
	if epicID := strArg(args, "epic_id"); epicID != "" {
		filtered := tasks[:0]
		for _, tk := range tasks {
			if tk.EpicID == epicID {
				filtered = append(filtered, tk)
			}
		}
		tasks = filtered
	}
	data, _ := json.Marshal(tasks)
	return data, nil
}

type boardGetTaskTool struct{ b *board.Store }

func (t *boardGetTaskTool) Name() string { return BoardGetTask }
func (t *boardGetTaskTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        BoardGetTask,
		Description: "Получить задачу доски по ID (детали, контракты, зависимости). Доступно на чтение всем участникам команды.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "ID задачи"},
			},
			"required": []string{"task_id"},
		},
	}
}
func (t *boardGetTaskTool) Execute(args map[string]any) ([]byte, error) {
	if t.b == nil {
		return boardErr(BoardGetTask, fmt.Errorf("доска не подключена"))
	}
	tk, err := t.b.GetTask(context.Background(), strArg(args, "task_id"))
	if err != nil {
		return boardErr(BoardGetTask, err)
	}
	return json.Marshal(tk)
}

type boardListBugsTool struct{ b *board.Store }

func (t *boardListBugsTool) Name() string { return BoardListBugs }
func (t *boardListBugsTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        BoardListBugs,
		Description: "Список багрепортов общей Kanban-доски проекта (статусы, вердикты). Доступно на чтение всем участникам команды.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{"type": "string", "description": "Фильтр по статусу багрепорта (необязательно): new, confirmed, slop, fix, feature, wont_fix, fixed"},
			},
		},
	}
}
func (t *boardListBugsTool) Execute(args map[string]any) ([]byte, error) {
	if t.b == nil {
		return boardErr(BoardListBugs, fmt.Errorf("доска не подключена"))
	}
	ctx := context.Background()
	bugs, err := t.b.ListBugReports(ctx)
	if err != nil {
		return boardErr(BoardListBugs, err)
	}
	if st := strArg(args, "status"); st != "" {
		filtered := bugs[:0]
		for _, b := range bugs {
			if string(b.Status) == st {
				filtered = append(filtered, b)
			}
		}
		bugs = filtered
	}
	data, _ := json.Marshal(bugs)
	return data, nil
}

type boardGetBugTool struct{ b *board.Store }

func (t *boardGetBugTool) Name() string { return BoardGetBug }
func (t *boardGetBugTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        BoardGetBug,
		Description: "Получить багрепорт доски по ID.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"bug_id": map[string]any{"type": "string", "description": "ID багрепорта"},
			},
			"required": []string{"bug_id"},
		},
	}
}
func (t *boardGetBugTool) Execute(args map[string]any) ([]byte, error) {
	if t.b == nil {
		return boardErr(BoardGetBug, fmt.Errorf("доска не подключена"))
	}
	r, err := t.b.GetBugReport(context.Background(), strArg(args, "bug_id"))
	if err != nil {
		return boardErr(BoardGetBug, err)
	}
	return json.Marshal(r)
}

// --- Эпики (CRUD — Системный архитектор) ---

type boardCreateEpicTool struct{ b *board.Store }

func (t *boardCreateEpicTool) Name() string { return BoardCreateEpic }
func (t *boardCreateEpicTool) Definition() ToolDefinition {
	props := taskSpecProps()
	props["architecture_summary"] = map[string]any{"type": "string", "description": "Сводка архитектурного решения, передаваемая лиду для декомпозиции"}
	return ToolDefinition{
		Name:        BoardCreateEpic,
		Description: "Создать новый эпик (крупную задачу верхнего уровня) на Kanban-доске. Эпик будет распределён между лидами направлений. Используется Системным архитектором.",
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             []string{"task_id", "title", "description", "assigned_role"},
			"additionalProperties": false,
		},
	}
}
func (t *boardCreateEpicTool) Execute(args map[string]any) ([]byte, error) {
	if t.b == nil {
		return boardErr(BoardCreateEpic, fmt.Errorf("доска не подключена"))
	}
	ts := parseTaskSpec(args)
	ctx := context.Background()
	for _, dep := range ts.Dependencies {
		if err := t.b.ReferenceExists(ctx, dep); err != nil {
			return boardErr(BoardCreateEpic, fmt.Errorf("зависимость %q: %w", dep, err))
		}
	}
	epic := &board.Epic{TaskSpec: ts, Summary: strArg(args, "architecture_summary")}
	if err := t.b.CreateEpic(ctx, epic); err != nil {
		return boardErr(BoardCreateEpic, err)
	}
	return boardOK(map[string]any{"epic_id": epic.TaskID})
}

type boardUpdateEpicTool struct{ b *board.Store }

func (t *boardUpdateEpicTool) Name() string { return BoardUpdateEpic }
func (t *boardUpdateEpicTool) Definition() ToolDefinition {
	props := map[string]any{}
	for k, v := range taskSpecProps() {
		if k == "task_id" {
			continue
		}
		props[k] = v
	}
	props["epic_id"] = map[string]any{"type": "string", "description": "ID эпика для обновления"}
	props["architecture_summary"] = map[string]any{"type": "string", "description": "Сводка архитектурного решения"}
	return ToolDefinition{
		Name:        BoardUpdateEpic,
		Description: "Обновить эпик доски: изменить описание/контракты, переприоритетизировать (sequence_order), изменить зависимости или роль лида. Уведомляет лида направления о необходимости ревизии задач (используется при мониторинге изменений).",
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             []string{"epic_id"},
			"additionalProperties": false,
		},
	}
}
func (t *boardUpdateEpicTool) Execute(args map[string]any) ([]byte, error) {
	if t.b == nil {
		return boardErr(BoardUpdateEpic, fmt.Errorf("доска не подключена"))
	}
	ctx := context.Background()
	e, err := t.b.GetEpic(ctx, strArg(args, "epic_id"))
	if err != nil {
		return boardErr(BoardUpdateEpic, err)
	}
	if v, ok := args["title"].(string); ok {
		e.Title = v
	}
	if v, ok := args["description"].(string); ok {
		e.Description = v
	}
	if v, ok := args["assigned_role"].(string); ok {
		e.AssignedRole = v
	}
	if v, ok := args["architecture_summary"].(string); ok {
		e.Summary = v
	}
	if v, ok := args["sequence_order"]; ok {
		if n, err := flexIntVal(v); err == nil {
			e.SequenceOrder = board.FlexInt(n)
		}
	}
	if v, ok := args["can_run_parallel"]; ok {
		if b, err := flexBoolVal(v); err == nil {
			e.CanRunParallel = board.FlexBool(b)
		}
	}
	if v, ok := args["dependencies"].([]any); ok {
		var deps []string
		for _, d := range v {
			if s, ok := d.(string); ok && s != "" {
				deps = append(deps, s)
			}
		}
		for _, dep := range deps {
			if err := t.b.ReferenceExists(ctx, dep); err != nil {
				return boardErr(BoardUpdateEpic, fmt.Errorf("зависимость %q: %w", dep, err))
			}
		}
		e.Dependencies = deps
	}
	if v, ok := args["task_id"].(string); ok && v != "" {
		e.TaskID = v
	}
	e.Revision++
	if err := t.b.SaveEpic(ctx, e); err != nil {
		return boardErr(BoardUpdateEpic, err)
	}
	return boardOK(map[string]any{"epic_id": e.TaskID, "revision": e.Revision})
}

type boardDeleteEpicTool struct{ b *board.Store }

func (t *boardDeleteEpicTool) Name() string { return BoardDeleteEpic }
func (t *boardDeleteEpicTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        BoardDeleteEpic,
		Description: "Удалить эпик с доски вместе с его задачами. Нельзя удалить эпик, задачи которого уже взяты специалистами в работу или выполнены.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"epic_id": map[string]any{"type": "string", "description": "ID эпика"},
			},
			"required":             []string{"epic_id"},
			"additionalProperties": false,
		},
	}
}
func (t *boardDeleteEpicTool) Execute(args map[string]any) ([]byte, error) {
	if t.b == nil {
		return boardErr(BoardDeleteEpic, fmt.Errorf("доска не подключена"))
	}
	if err := t.b.DeleteEpic(context.Background(), strArg(args, "epic_id")); err != nil {
		return boardErr(BoardDeleteEpic, err)
	}
	return boardOK(map[string]any{"deleted": strArg(args, "epic_id")})
}

type boardSetEpicStatusTool struct{ b *board.Store }

func (t *boardSetEpicStatusTool) Name() string { return BoardSetEpicStatus }
func (t *boardSetEpicStatusTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        BoardSetEpicStatus,
		Description: "Перевести эпик в новый статус: new, analysis, ready, in_progress, done, cancelled. Переходы валидируются конечным автоматом доски.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"epic_id": map[string]any{"type": "string"},
				"status":  map[string]any{"type": "string", "description": "new | analysis | ready | in_progress | done | cancelled"},
			},
			"required":             []string{"epic_id", "status"},
			"additionalProperties": false,
		},
	}
}
func (t *boardSetEpicStatusTool) Execute(args map[string]any) ([]byte, error) {
	if t.b == nil {
		return boardErr(BoardSetEpicStatus, fmt.Errorf("доска не подключена"))
	}
	ctx := context.Background()
	id := strArg(args, "epic_id")
	st := board.Status(strArg(args, "status"))
	if err := t.b.SetEpicStatus(ctx, id, st); err != nil {
		return boardErr(BoardSetEpicStatus, err)
	}
	return boardOK(map[string]any{"epic_id": id, "status": string(st)})
}

// --- Задачи (CRUD — лиды направлений) ---

type boardCreateTaskTool struct{ b *board.Store }

func (t *boardCreateTaskTool) Name() string { return BoardCreateTask }
func (t *boardCreateTaskTool) Definition() ToolDefinition {
	props := taskSpecProps()
	props["epic_id"] = map[string]any{"type": "string", "description": "ID эпика, в который добавляется задача"}
	return ToolDefinition{
		Name:        BoardCreateTask,
		Description: "Создать задачу в эпике на Kanban-доске. Задача выполняемая специалистом. Используется лидами направлений при декомпозиции эпика.",
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             []string{"epic_id", "task_id", "title", "description", "assigned_role"},
			"additionalProperties": false,
		},
	}
}
func (t *boardCreateTaskTool) Execute(args map[string]any) ([]byte, error) {
	if t.b == nil {
		return boardErr(BoardCreateTask, fmt.Errorf("доска не подключена"))
	}
	ts := parseTaskSpec(args)
	ctx := context.Background()
	for _, dep := range ts.Dependencies {
		if err := t.b.ReferenceExists(ctx, dep); err != nil {
			return boardErr(BoardCreateTask, fmt.Errorf("зависимость %q: %w", dep, err))
		}
	}
	epicID := strArg(args, "epic_id")
	task := &board.Task{
		TaskSpec: ts,
		EpicID:   epicID,
		Assignee: boardAssignee(ts.AssignedRole),
	}
	if err := t.b.CreateTask(ctx, task); err != nil {
		return boardErr(BoardCreateTask, err)
	}
	return boardOK(map[string]any{"task_id": task.TaskID, "epic_id": epicID})
}

// boardAssignee возвращает ключ специалиста для задачи (как в оркестраторе):
// роль из assigned_role; пустая роль — "developer".
func boardAssignee(role string) string {
	if strings.TrimSpace(role) == "" {
		return "developer"
	}
	return role
}

type boardUpdateTaskTool struct{ b *board.Store }

func (t *boardUpdateTaskTool) Name() string { return BoardUpdateTask }
func (t *boardUpdateTaskTool) Definition() ToolDefinition {
	props := map[string]any{}
	for k, v := range taskSpecProps() {
		if k == "task_id" {
			continue
		}
		props[k] = v
	}
	props["epic_id"] = map[string]any{"type": "string", "description": "ID эпика (перенести задачу в другой эпик)"}
	return ToolDefinition{
		Name:        BoardUpdateTask,
		Description: "Обновить задачу доски: переприоритетизировать (sequence_order), изменить описание/контракты, роль, зависимости или перенести в другой эпик. Используется лидами направлений.",
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             []string{"task_id"},
			"additionalProperties": false,
		},
	}
}
func (t *boardUpdateTaskTool) Execute(args map[string]any) ([]byte, error) {
	if t.b == nil {
		return boardErr(BoardUpdateTask, fmt.Errorf("доска не подключена"))
	}
	ctx := context.Background()
	tk, err := t.b.GetTask(ctx, strArg(args, "task_id"))
	if err != nil {
		return boardErr(BoardUpdateTask, err)
	}
	if v, ok := args["title"].(string); ok {
		tk.Title = v
	}
	if v, ok := args["description"].(string); ok {
		tk.Description = v
	}
	if v, ok := args["assigned_role"].(string); ok {
		tk.AssignedRole = v
		tk.Assignee = boardAssignee(v)
	}
	if v, ok := args["sequence_order"]; ok {
		if n, err := flexIntVal(v); err == nil {
			tk.SequenceOrder = board.FlexInt(n)
		}
	}
	if v, ok := args["can_run_parallel"]; ok {
		if b, err := flexBoolVal(v); err == nil {
			tk.CanRunParallel = board.FlexBool(b)
		}
	}
	if raw, ok := args["dependencies"]; ok {
		var deps []string
		if list, isList := raw.([]any); isList {
			for _, d := range list {
				if s, ok := d.(string); ok && s != "" {
					deps = append(deps, s)
				}
			}
		}
		for _, dep := range deps {
			if err := t.b.ReferenceExists(ctx, dep); err != nil {
				return boardErr(BoardUpdateTask, fmt.Errorf("зависимость %q: %w", dep, err))
			}
		}
		tk.Dependencies = deps
	}
	if epicID := strArg(args, "epic_id"); epicID != "" && epicID != tk.EpicID {
		if err := t.b.MoveTask(ctx, tk.TaskID, epicID); err != nil {
			return boardErr(BoardUpdateTask, err)
		}
		return boardOK(map[string]any{"task_id": tk.TaskID, "epic_id": epicID})
	}
	if err := t.b.SaveTask(ctx, tk); err != nil {
		return boardErr(BoardUpdateTask, err)
	}
	return boardOK(map[string]any{"task_id": tk.TaskID})
}

type boardDeleteTaskTool struct{ b *board.Store }

func (t *boardDeleteTaskTool) Name() string { return BoardDeleteTask }
func (t *boardDeleteTaskTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        BoardDeleteTask,
		Description: "Удалить задачу с доски. Удалять можно только задачи, которые ещё НЕ взяты специалистом в работу (статус не in_progress/done). Используется лидами направлений при ревизии задач.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "ID задачи"},
			},
			"required":             []string{"task_id"},
			"additionalProperties": false,
		},
	}
}
func (t *boardDeleteTaskTool) Execute(args map[string]any) ([]byte, error) {
	if t.b == nil {
		return boardErr(BoardDeleteTask, fmt.Errorf("доска не подключена"))
	}
	if err := t.b.DeleteTask(context.Background(), strArg(args, "task_id")); err != nil {
		return boardErr(BoardDeleteTask, err)
	}
	return boardOK(map[string]any{"deleted": strArg(args, "task_id")})
}

type boardSetTaskStatusTool struct{ b *board.Store }

func (t *boardSetTaskStatusTool) Name() string { return BoardSetTaskStatus }
func (t *boardSetTaskStatusTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        BoardSetTaskStatus,
		Description: "Перевести задачу в новый статус: new, analysis, ready, in_progress, done, cancelled. Используется специалистами (выполнил -> done) и лидами (перепланирование).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string"},
				"status":  map[string]any{"type": "string", "description": "new | analysis | ready | in_progress | done | cancelled"},
			},
			"required":             []string{"task_id", "status"},
			"additionalProperties": false,
		},
	}
}
func (t *boardSetTaskStatusTool) Execute(args map[string]any) ([]byte, error) {
	if t.b == nil {
		return boardErr(BoardSetTaskStatus, fmt.Errorf("доска не подключена"))
	}
	ctx := context.Background()
	id := strArg(args, "task_id")
	st := board.Status(strArg(args, "status"))
	if err := t.b.SetTaskStatus(ctx, id, st); err != nil {
		return boardErr(BoardSetTaskStatus, err)
	}
	return boardOK(map[string]any{"task_id": id, "status": string(st)})
}

// --- Багрепорты ---

type boardCreateBugReportTool struct{ b *board.Store }

func (t *boardCreateBugReportTool) Name() string { return BoardCreateBug }
func (t *boardCreateBugReportTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        BoardCreateBug,
		Description: "Создать багрепорт на общей доске. Используется QA-специалистами при обнаружении реальной проблемы (расхождения с контрактом, падение, некорректное поведение). Далее багрепорт ревьюится QA Lead и Системным архитектором.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"bug_id":        map[string]any{"type": "string", "description": "Уникальный ID багрепорта (например BUG-01)"},
				"title":         map[string]any{"type": "string", "description": "Краткое название проблемы"},
				"description":   map[string]any{"type": "string", "description": "Детальное описание: шаги воспроизведения, фактические и ожидаемые результаты, ссылка на контракт"},
				"task_id":       map[string]any{"type": "string", "description": "ID задачи, в которой найден баг"},
				"epic_id":       map[string]any{"type": "string", "description": "ID эпика задачи"},
				"reporter_role": map[string]any{"type": "string", "description": "Твоя роль (например QA Engineer)"},
			},
			"required":             []string{"bug_id", "title", "description", "epic_id"},
			"additionalProperties": false,
		},
	}
}
func (t *boardCreateBugReportTool) Execute(args map[string]any) ([]byte, error) {
	if t.b == nil {
		return boardErr(BoardCreateBug, fmt.Errorf("доска не подключена"))
	}
	r := &board.BugReport{
		BugID:        strArg(args, "bug_id"),
		Title:        strArg(args, "title"),
		Description:  strArg(args, "description"),
		TaskID:       strArg(args, "task_id"),
		EpicID:       strArg(args, "epic_id"),
		ReporterRole: strArg(args, "reporter_role"),
	}
	if err := t.b.CreateBugReport(context.Background(), r); err != nil {
		return boardErr(BoardCreateBug, err)
	}
	return boardOK(map[string]any{"bug_id": r.BugID})
}

type boardSetBugStatusTool struct{ b *board.Store }

func (t *boardSetBugStatusTool) Name() string { return BoardSetBugStatus }
func (t *boardSetBugStatusTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        BoardSetBugStatus,
		Description: "Изменить статус багрепорта в цепочке ревью. QA Lead подтверждает проблему (confirmed) или отклоняет как нейрослоп (slop). Обрабатывает только багрепорты в статусе new.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"bug_id":  map[string]any{"type": "string", "description": "ID багрепорта"},
				"status":  map[string]any{"type": "string", "description": "confirmed — реальная проблема, slop — нейрослоп"},
				"comment": map[string]any{"type": "string", "description": "Обоснование вердикта (необязательно)"},
			},
			"required":             []string{"bug_id", "status"},
			"additionalProperties": false,
		},
	}
}
func (t *boardSetBugStatusTool) Execute(args map[string]any) ([]byte, error) {
	if t.b == nil {
		return boardErr(BoardSetBugStatus, fmt.Errorf("доска не подключена"))
	}
	ctx := context.Background()
	id := strArg(args, "bug_id")
	st := board.BugStatus(strArg(args, "status"))
	if st != board.BugStatusConfirmed && st != board.BugStatusSlop {
		return boardErr(BoardSetBugStatus, fmt.Errorf("QA Lead может ставить только confirmed или slop"))
	}
	r, err := t.b.GetBugReport(ctx, id)
	if err != nil {
		return boardErr(BoardSetBugStatus, err)
	}
	if err := t.b.SetBugStatus(ctx, id, st); err != nil {
		return boardErr(BoardSetBugStatus, err)
	}
	return boardOK(map[string]any{"bug_id": id, "status": string(st), "old_status": string(r.Status)})
}

type boardReviewBugReportTool struct{ b *board.Store }

func (t *boardReviewBugReportTool) Name() string { return BoardReviewBug }
func (t *boardReviewBugReportTool) Definition() ToolDefinition {
	props := map[string]any{
		"bug_id":  map[string]any{"type": "string", "description": "ID багрепорта для экспертизы"},
		"verdict": map[string]any{"type": "string", "description": "fix — чинить (создать эпик исправления); feature — это фича, не баг; wont_fix — чинить не будем"},
		"comment": map[string]any{"type": "string", "description": "Обоснование экспертизы"},
	}
	for k, v := range taskSpecProps() {
		if k == "task_id" {
			continue
		}
		props["epic_"+k] = v
	}
	props["epic_task_id"] = map[string]any{"type": "string", "description": "ID нового эпика исправления (например FIX-01); обязателен при verdict=fix"}
	props["epic_architecture_summary"] = map[string]any{"type": "string", "description": "Сводка для лида нового эпика исправления"}
	return ToolDefinition{
		Name:        BoardReviewBug,
		Description: "Экспертиза багрепорта Системным архитектором: решить, на какой стороне чинить и стоит ли вообще (можно фича), и при необходимости создать эпик исправления. Обрабатывает только багрепорты в статусе confirmed.",
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             []string{"bug_id", "verdict"},
			"additionalProperties": false,
		},
	}
}
func (t *boardReviewBugReportTool) Execute(args map[string]any) ([]byte, error) {
	if t.b == nil {
		return boardErr(BoardReviewBug, fmt.Errorf("доска не подключена"))
	}
	ctx := context.Background()
	id := strArg(args, "bug_id")
	verdict := strings.ToLower(strArg(args, "verdict"))
	r, err := t.b.GetBugReport(ctx, id)
	if err != nil {
		return boardErr(BoardReviewBug, err)
	}

	switch verdict {
	case "fix":
		epicID := strArg(args, "epic_task_id")
		if epicID == "" {
			return boardErr(BoardReviewBug, fmt.Errorf("при verdict=fix обязателен epic_task_id для эпика исправления"))
		}
		if _, err := t.b.GetEpic(ctx, epicID); err == nil {
			return boardErr(BoardReviewBug, fmt.Errorf("эпик %q уже существует — выбери другой epic_task_id", epicID))
		} else if !errors.Is(err, board.ErrNotFound) {
			return boardErr(BoardReviewBug, err)
		}
		ts := parseTaskSpec(epicArgs(args))
		ts.TaskID = epicID
		for _, dep := range ts.Dependencies {
			if err := t.b.ReferenceExists(ctx, dep); err != nil {
				return boardErr(BoardReviewBug, fmt.Errorf("зависимость %q: %w", dep, err))
			}
		}
		epic := &board.Epic{
			TaskSpec: ts,
			Summary:  strArg(args, "epic_architecture_summary"),
			Revision: 1,
		}
		if err := t.b.CreateEpic(ctx, epic); err != nil {
			return boardErr(BoardReviewBug, err)
		}
		r.Verdict = "fix"
		r.FixEpicID = epicID
	case "feature", "wont_fix":
		r.Verdict = verdict
	default:
		return boardErr(BoardReviewBug, fmt.Errorf("неизвестный вердикт %q: используй fix, feature или wont_fix", verdict))
	}
	if err := t.b.SetBugStatus(ctx, id, board.BugStatus(verdict)); err != nil {
		return boardErr(BoardReviewBug, err)
	}
	r.Status = board.BugStatus(verdict)
	if err := t.b.SaveBugReport(ctx, r); err != nil {
		return boardErr(BoardReviewBug, err)
	}
	return boardOK(map[string]any{"bug_id": id, "status": r.Status, "verdict": r.Verdict, "fix_epic_id": r.FixEpicID})
}

// epicArgs передвигает поля вида "epic_title"/"epic_description" в обычные
// поля TaskSpec parseTaskSpec.
func epicArgs(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		if strings.HasPrefix(k, "epic_") {
			out[strings.TrimPrefix(k, "epic_")] = v
		}
	}
	return out
}

// flexIntVal конвертирует значение (число/строка) в int.
func flexIntVal(v any) (int, error) {
	switch n := v.(type) {
	case float64:
		return int(n), nil
	case int:
		return n, nil
	case string:
		s := strings.TrimSpace(n)
		if s == "" {
			return 0, fmt.Errorf("пустое число")
		}
		i, err := strconv.Atoi(s)
		return i, err
	}
	return 0, fmt.Errorf("не число: %v", v)
}

// flexBoolVal конвертирует значение (bool/строка/число) в bool.
func flexBoolVal(v any) (bool, error) {
	switch b := v.(type) {
	case bool:
		return b, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(b)) {
		case "true", "1", "yes":
			return true, nil
		case "false", "0", "no":
			return false, nil
		}
	case float64:
		return b != 0, nil
	}
	return false, fmt.Errorf("не булев: %v", v)
}
