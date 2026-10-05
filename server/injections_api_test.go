package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"ai/agents/architect"
	"ai/board"

	"github.com/alicebob/miniredis/v2"
)

// newInjTestServer поднимает сервер поверх miniredis (доска нужна для
// инъекций задачи) и возвращает mux.
func newInjTestServer(t *testing.T) http.Handler {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	t.Setenv("BOARD_REDIS_ADDR", mr.Addr())
	wsPath := filepath.Join(t.TempDir(), "workspaces.json")
	srv, err := NewServer(Config{WorkspacesPath: wsPath})
	if err != nil {
		t.Fatal(err)
	}
	return srv.routes()
}

// doJSON делает запрос и декодирует ответ в out (если out != nil).
func doJSON(t *testing.T, h http.Handler, method, path string, body any, out any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(data)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if out != nil && rec.Code == http.StatusOK || out != nil && rec.Code == http.StatusCreated {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("декод ответа (%d): %v: %s", rec.Code, err, rec.Body.String())
		}
	}
	return rec
}

// Регрессия: POST возвращал id, который НИКОГДА не сохранялся (id присваивался
// после AddSessionInjection), поэтому клиентский DELETE по нему отвечал 200,
// а инъекция оставалась. Теперь id адресует ровно одну запись.
func TestSessionInjectionRoundTripKeepsReturnedID(t *testing.T) {
	h := newInjTestServer(t)

	var added struct {
		ID  string `json:"id"`
		Msg string `json:"msg"`
	}
	rec := doJSON(t, h, http.MethodPost, "/api/projects/mytrip/injections", map[string]any{
		"name": "жёстче-тесты", "target": "system", "position": "append",
		"content": "ПИШИ ТЕСТЫ",
	}, &added)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if added.ID == "" {
		t.Fatal("POST не вернул id")
	}

	var list []board.Injection
	rec = doJSON(t, h, http.MethodGet, "/api/projects/mytrip/injections", nil, &list)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
	}
	if len(list) != 1 {
		t.Fatalf("в сессии %d инъекций, want 1", len(list))
	}
	if list[0].ID != added.ID {
		t.Fatalf("id в сессии %q != id из ответа %q", list[0].ID, added.ID)
	}
	if list[0].Scope != board.InjectionScopeSession {
		t.Errorf("scope = %q, want %q", list[0].Scope, board.InjectionScopeSession)
	}
	if list[0].Position != board.InjectionPosAppend {
		t.Errorf("position = %q, want %q", list[0].Position, board.InjectionPosAppend)
	}

	// DELETE по выданному id действительно удаляет.
	rec = doJSON(t, h, http.MethodDelete, "/api/projects/mytrip/injections/"+added.ID, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodGet, "/api/projects/mytrip/injections", nil, &list)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET после удаления = %d: %s", rec.Code, rec.Body.String())
	}
	if len(list) != 0 {
		t.Fatalf("после DELETE осталось %d инъекций", len(list))
	}
}

// DELETE несуществующей инъекции — 404, а не 200: иначе клиент считал бы
// удаление успешным, а текст продолжал бы уходить в модель.
func TestSessionInjectionDeleteUnknownIs404(t *testing.T) {
	h := newInjTestServer(t)
	rec := doJSON(t, h, http.MethodDelete, "/api/projects/mytrip/injections/нет-такой", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE неизвестного id = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// Повторный POST с тем же id правит запись, а не плодит дубли (иначе id
// перестаёт адресовать ровно одну инъекцию).
func TestSessionInjectionPostWithIDUpserts(t *testing.T) {
	h := newInjTestServer(t)
	body := map[string]any{
		"id": "моя-инъекция", "name": "стиль", "target": "system",
		"content": "ПЕРВАЯ ВЕРСИЯ",
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/projects/mytrip/injections", body, nil); rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body.String())
	}
	body["content"] = "ВТОРАЯ ВЕРСИЯ"
	if rec := doJSON(t, h, http.MethodPost, "/api/projects/mytrip/injections", body, nil); rec.Code != http.StatusCreated {
		t.Fatalf("POST (повторный) = %d: %s", rec.Code, rec.Body.String())
	}

	var list []board.Injection
	doJSON(t, h, http.MethodGet, "/api/projects/mytrip/injections", nil, &list)
	if len(list) != 1 {
		t.Fatalf("после повторного POST инъекций %d, want 1: %+v", len(list), list)
	}
	if list[0].Content != "ВТОРАЯ ВЕРСИЯ" {
		t.Errorf("content = %q, want ВТОРАЯ ВЕРСИЯ", list[0].Content)
	}
}

// Неизвестное поле — 400, а не «успех с молчаливой потерей параметра».
func TestSessionInjectionRejectsUnknownField(t *testing.T) {
	h := newInjTestServer(t)
	rec := doJSON(t, h, http.MethodPost, "/api/projects/mytrip/injections", map[string]any{
		"name": "опечатка", "target": "system", "content": "X", "psoition": "append",
	}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST с опечаткой в поле = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// Невалидные значения отсекаются на записи, а не на применении.
func TestSessionInjectionRejectsInvalidValues(t *testing.T) {
	h := newInjTestServer(t)
	cases := []struct {
		name string
		body map[string]any
	}{
		{"без имени", map[string]any{"target": "system", "content": "X"}},
		{"без content", map[string]any{"name": "n", "target": "system"}},
		{"неизвестный target", map[string]any{"name": "n", "target": "before_tools", "content": "X"}},
		{"неизвестная position", map[string]any{"name": "n", "target": "system", "position": "куда-то", "content": "X"}},
		{"неизвестный scope", map[string]any{"name": "n", "target": "system", "scope": "мистика", "content": "X"}},
		{"inject_at_index без индекса", map[string]any{"name": "n", "target": "messages", "position": "inject_at_index", "index": -3, "content": "X"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doJSON(t, h, http.MethodPost, "/api/projects/mytrip/injections", tc.body, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("POST = %d, want 400: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// enabled=false сохраняется и не применяется: запись остаётся в списке, но
// помечена выключенной (в отличие от прежнего `disabled`, которого в API не
// было вовсе).
func TestSessionInjectionEnabledFalseStored(t *testing.T) {
	h := newInjTestServer(t)
	rec := doJSON(t, h, http.MethodPost, "/api/projects/mytrip/injections", map[string]any{
		"name": "выключенная", "target": "system", "content": "НЕ ПРИМЕНЯТЬ", "enabled": false,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body.String())
	}
	var list []board.Injection
	doJSON(t, h, http.MethodGet, "/api/projects/mytrip/injections", nil, &list)
	if len(list) != 1 {
		t.Fatalf("инъекций %d, want 1", len(list))
	}
	if list[0].IsEnabled() {
		t.Error("инъекция с enabled=false оказалась активной")
	}
}

// Инъекции задачи: POST/DELETE по эндпоинтам задачи и полная замена списка
// через PUT /tasks/{tid}. Раньше Task.Injections был полем только для чтения:
// задать инъекции задачи было нечем.
func TestTaskInjectionsEndpoints(t *testing.T) {
	h := newInjTestServer(t)
	if err := createInjTask(t, "mytrip", "FEL-02"); err != nil {
		t.Fatal(err)
	}

	var added struct {
		ID         string            `json:"id"`
		Injections []board.Injection `json:"injections"`
	}
	rec := doJSON(t, h, http.MethodPost, "/api/projects/mytrip/tasks/FEL-02/injections", map[string]any{
		"name": "testify", "target": "system", "content": "ПИШИ НА TESTIFY",
	}, &added)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST task injection = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if added.ID == "" {
		t.Fatal("POST не вернул id")
	}
	if len(added.Injections) != 1 || added.Injections[0].Scope != board.InjectionScopeRuntime {
		t.Fatalf("ответ = %+v, want одна runtime-инъекция", added.Injections)
	}

	// Инъекция видна в задаче (поле injections в снимке доски).
	task := taskFromBoard(t, h, "mytrip", "FEL-02")
	if len(task.Injections) != 1 || task.Injections[0].Content != "ПИШИ НА TESTIFY" {
		t.Fatalf("в задаче инъекции = %+v", task.Injections)
	}

	// PUT с полным списком ЗАМЕНЯЕТ прежний.
	var patched board.Task
	rec = doJSON(t, h, http.MethodPut, "/api/projects/mytrip/tasks/FEL-02", map[string]any{
		"injections": []map[string]any{
			{"name": "вторая", "target": "messages", "position": "append", "content": "ВТОРАЯ"},
		},
	}, &patched)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(patched.Injections) != 1 || patched.Injections[0].Name != "вторая" {
		t.Fatalf("после PUT инъекции = %+v, want одна «вторая»", patched.Injections)
	}
	// Инъекция, заданная без id, всё равно получает адресуемый id — иначе её
	// нельзя удалить точечно.
	if patched.Injections[0].ID == "" {
		t.Fatal("инъекция из PUT сохранена без id")
	}
	task = patched

	// DELETE возвращает 404 для несуществующего id и 200 для существующего.
	rec = doJSON(t, h, http.MethodDelete, "/api/projects/mytrip/tasks/FEL-02/injections/нет", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE чужого id = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodDelete, "/api/projects/mytrip/tasks/FEL-02/injections/"+task.Injections[0].ID, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	task = taskFromBoard(t, h, "mytrip", "FEL-02")
	if len(task.Injections) != 0 {
		t.Fatalf("после DELETE осталось %d инъекций", len(task.Injections))
	}
}

// PUT с невалидной инъекцией отвечает 400 и НЕ меняет задачу.
func TestTaskInjectionsPatchValidation(t *testing.T) {
	h := newInjTestServer(t)
	if err := createInjTask(t, "mytrip", "FEL-03"); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, h, http.MethodPut, "/api/projects/mytrip/tasks/FEL-03", map[string]any{
		"injections": []map[string]any{{"name": "плохая", "target": "system"}},
	}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT без content = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodPut, "/api/projects/mytrip/tasks/FEL-03", map[string]any{
		"injections": []map[string]any{{"name": "плохая", "target": "system", "content": "X", "oops": 1}},
	}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT с неизвестным полем = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodPut, "/api/projects/mytrip/tasks/FEL-03", map[string]any{
		"injections": []map[string]any{
			{"id": "d1", "name": "a", "target": "system", "content": "A"},
			{"id": "d1", "name": "b", "target": "system", "content": "B"},
		},
	}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT с дублем id = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// taskFromBoard достаёт задачу из снимка доски (одно GET вместо отсутствующего
// GET /tasks/{tid}).
func taskFromBoard(t *testing.T, h http.Handler, project, taskID string) board.Task {
	t.Helper()
	var snap boardSnapshot
	rec := doJSON(t, h, http.MethodGet, "/api/projects/"+project, nil, &snap)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET board = %d: %s", rec.Code, rec.Body.String())
	}
	for _, task := range snap.Tasks {
		if task != nil && task.TaskID == taskID {
			return *task
		}
	}
	t.Fatalf("задача %s не найдена на доске", taskID)
	return board.Task{}
}

// createInjTask создаёт на доске проектных записей эпик и задачу: CreateTask
// требует epic_id, поэтому эпик заводим тут же.
func createInjTask(t *testing.T, project, taskID string) error {
	t.Helper()
	ctx := context.Background()
	store, err := board.NewStore(ctx, architect.LoadConfig().StoreConfig(project))
	if err != nil {
		return err
	}
	defer store.Close()
	epicID := "ARC-01"
	if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{TaskID: epicID, Title: "Эпик для инъекций"}}); err != nil {
		return err
	}
	return store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: taskID, Title: "Задача " + taskID},
		EpicID:   epicID,
	})
}
