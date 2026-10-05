package tools

import (
	"context"
	"testing"

	"ai/board"
)

// Инъекции задачи задаются инструментом доски: это путь «пользователь в чате
// говорит ассистенту добавить задаче указание» — лид направления выполняет
// BoardUpdateTask с полем injections.
func TestBoardUpdateTaskSetsInjections(t *testing.T) {
	set, store := newBoardToolSet(t)
	ctx := context.Background()

	mustExec(t, set, BoardCreateEpic, map[string]any{
		"task_id": "ARC-01", "title": "Эпик", "description": "d",
	})
	mustExec(t, set, BoardCreateTask, map[string]any{
		"task_id": "FEL-01", "title": "Задача", "description": "d", "epic_id": "ARC-01",
	})

	mustExec(t, set, BoardUpdateTask, map[string]any{
		"task_id": "FEL-01",
		"injections": []map[string]any{{
			"name": "testify", "target": "system", "position": "append",
			"content": "ПИШИ ТЕСТЫ НА TESTIFY",
		}},
	})

	task, err := store.GetTask(ctx, "FEL-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Injections) != 1 {
		t.Fatalf("инъекций %d, want 1", len(task.Injections))
	}
	inj := task.Injections[0]
	if inj.Content != "ПИШИ ТЕСТЫ НА TESTIFY" {
		t.Errorf("content = %q", inj.Content)
	}
	// Дефолты проставлены, id выдан — запись адресуема и применяется.
	if inj.Scope != board.InjectionScopeRuntime {
		t.Errorf("scope = %q, want %q", inj.Scope, board.InjectionScopeRuntime)
	}
	if inj.ID == "" {
		t.Error("инъекция без id: её нельзя удалить точечно")
	}
	if !inj.IsEnabled() {
		t.Error("инъекция должна быть активна по умолчанию")
	}

	// Полный список ЗАМЕНЯЕТ прежний (инструмент не накапливает мусор).
	mustExec(t, set, BoardUpdateTask, map[string]any{
		"task_id": "FEL-01",
		"injections": []map[string]any{{
			"name": "вторая", "target": "messages", "content": "ВТОРАЯ",
		}},
	})
	task, _ = store.GetTask(ctx, "FEL-01")
	if len(task.Injections) != 1 || task.Injections[0].Name != "вторая" {
		t.Fatalf("после повторного вызова = %+v, want одна «вторая»", task.Injections)
	}

	// Пустой список снимает инъекции.
	mustExec(t, set, BoardUpdateTask, map[string]any{
		"task_id": "FEL-01", "injections": []map[string]any{},
	})
	task, _ = store.GetTask(ctx, "FEL-01")
	if len(task.Injections) != 0 {
		t.Fatalf("после пустого списка осталось %d инъекций", len(task.Injections))
	}
}

// Невалидные инъекции отвергаются с ошибкой и НЕ пишутся в задачу.
func TestBoardUpdateTaskRejectsBadInjections(t *testing.T) {
	set, store := newBoardToolSet(t)
	ctx := context.Background()

	mustExec(t, set, BoardCreateEpic, map[string]any{"task_id": "ARC-01", "title": "Эпик", "description": "d"})
	mustExec(t, set, BoardCreateTask, map[string]any{
		"task_id": "FEL-01", "title": "Задача", "description": "d", "epic_id": "ARC-01",
	})
	mustExec(t, set, BoardUpdateTask, map[string]any{
		"task_id":    "FEL-01",
		"injections": []map[string]any{{"name": "ok", "target": "system", "content": "C"}},
	})

	cases := []struct {
		name string
		inj  map[string]any
		want string
	}{
		{"без content", map[string]any{"name": "n", "target": "system"}, "content"},
		{"неизвестный target", map[string]any{"name": "n", "target": "before_tools", "content": "C"}, "target"},
		{"неизвестная position", map[string]any{"name": "n", "target": "system", "position": "куда-то", "content": "C"}, "position"},
		{"дубль id", map[string]any{"id": "d", "name": "n", "target": "system", "content": "C"}, "дубль id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{
				"task_id":    "FEL-01",
				"injections": []map[string]any{tc.inj, {"id": "d", "name": "second", "target": "system", "content": "S"}},
			}
			if tc.name == "дубль id" {
				args["injections"] = []map[string]any{
					{"id": "d", "name": "a", "target": "system", "content": "A"},
					{"id": "d", "name": "b", "target": "system", "content": "B"},
				}
			}
			msg := errExec(t, set, BoardUpdateTask, args)
			if msg == "" {
				t.Fatal("ожидалась ошибка валидации")
			}
			task, _ := store.GetTask(ctx, "FEL-01")
			if len(task.Injections) != 1 || task.Injections[0].Name != "ok" {
				t.Fatalf("после отказа инъекции изменились: %+v", task.Injections)
			}
		})
	}
}

// Схема инструмента объявляет injections: без свойства в schema модель не может
// передать поле (additionalProperties: false отверг бы вызов).
func TestBoardUpdateTaskSchemaDeclaresInjections(t *testing.T) {
	def := (&boardUpdateTaskTool{}).Definition()
	props, _ := def.Parameters["properties"].(map[string]any)
	raw, ok := props["injections"]
	if !ok {
		t.Fatal("в схеме BoardUpdateTask нет поля injections")
	}
	schema, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("injections не объект схемы: %T", raw)
	}
	if schema["type"] != "array" {
		t.Errorf("type = %v, want array", schema["type"])
	}
	items, ok := schema["items"].(map[string]any)
	if !ok {
		t.Fatal("нет описания элемента injections")
	}
	itemProps, _ := items["properties"].(map[string]any)
	for _, field := range []string{"name", "target", "position", "content", "when", "enabled", "priority", "index"} {
		if _, ok := itemProps[field]; !ok {
			t.Errorf("в элементе injections нет поля %q", field)
		}
	}
}
