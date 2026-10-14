package server

import (
	"net/http"
	"strings"
	"testing"

	"ai/infrastructure/board"
)

// Регрессия: PUT /tasks/{tid} с полем comments раньше молча терял комментарий —
// обработчик не разбирал это поле, поэтому добавленный в модалке задачи
// комментарий не сохранялся и не возвращался в ответе. Теперь список
// заменяется целиком, как в инструменте BoardUpdateTask.
func TestTaskCommentsPatchPersists(t *testing.T) {
	h := newInjTestServer(t)
	if err := createInjTask(t, "mytrip", "BEL-01"); err != nil {
		t.Fatal(err)
	}

	var patched board.Task
	rec := doJSON(t, h, http.MethodPut, "/api/projects/mytrip/tasks/BEL-01", map[string]any{
		"comments": []map[string]any{
			{
				"id":         "tmp-1791730262509",
				"author":     "user",
				"type":       "user",
				"body":       "Проверить реализацию, если что доработать",
				"created_at": "2026-10-11T14:51:02.509Z",
			},
		},
	}, &patched)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT comments = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(patched.Comments) != 1 {
		t.Fatalf("ответ PUT: comments = %+v, want 1", patched.Comments)
	}
	if patched.Comments[0].Body != "Проверить реализацию, если что доработать" {
		t.Fatalf("тело комментария = %q", patched.Comments[0].Body)
	}
	// Оптимистичный id фронтенда (tmp-…) не должен закрепляться в хранилище:
	// сервер выдаёт свой, иначе повторные PUT с тем же tmp-id перезаписывают
	// комментарий.
	if strings.HasPrefix(patched.Comments[0].ID, "tmp-") || patched.Comments[0].ID == "" {
		t.Fatalf("сохранённый id = %q, want серверный", patched.Comments[0].ID)
	}
	if patched.Comments[0].TaskID != "BEL-01" {
		t.Fatalf("task_id комментария = %q", patched.Comments[0].TaskID)
	}

	// Комментарий виден и на доске, и по отдельному эндпоинту.
	task := taskFromBoard(t, h, "mytrip", "BEL-01")
	if len(task.Comments) != 1 {
		t.Fatalf("на доске comments = %+v", task.Comments)
	}
	var list board.Comments
	rec = doJSON(t, h, http.MethodGet, "/api/projects/mytrip/tasks/BEL-01/comments", nil, &list)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET comments = %d: %s", rec.Code, rec.Body.String())
	}
	if len(list) != 1 {
		t.Fatalf("GET comments = %+v, want 1", list)
	}
}

// PUT с невалидным комментарием отвечает 400 и НЕ меняет задачу.
func TestTaskCommentsPatchValidation(t *testing.T) {
	h := newInjTestServer(t)
	if err := createInjTask(t, "mytrip", "BEL-02"); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, h, http.MethodPut, "/api/projects/mytrip/tasks/BEL-02", map[string]any{
		"comments": []map[string]any{
			{"author": "user", "type": "user", "body": ""},
		},
	}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT с пустым телом = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodPut, "/api/projects/mytrip/tasks/BEL-02", map[string]any{
		"comments": []map[string]any{
			{"author": "user", "type": "неизвестный", "body": "x"},
		},
	}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT с чужим типом = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	task := taskFromBoard(t, h, "mytrip", "BEL-02")
	if len(task.Comments) != 0 {
		t.Fatalf("после невалидных PUT на доске комментарии = %+v", task.Comments)
	}
}
