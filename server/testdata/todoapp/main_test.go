// Интеграционные тесты todo-приложения: реальный PostgreSQL через TEST_DATABASE_URL.
//
// Без TEST_DATABASE_URL тесты пропускаются (t.Skip), поэтому файл безопасен в
// окружении без БД. В e2e-прогоне тест задаёт TEST_DATABASE_URL на контейнер
// PostgreSQL — и эти тесты (плюс сборка) обязаны пройти зелёными.
package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
)

// testDB открывает БД, применяет схему и очищает таблицу перед каждым тестом.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL не задан — интеграционный тест с БД пропущен")
	}
	db, err := openDB(dsn)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	if err := ensureSchema(db); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}
	if _, err := db.Exec(`TRUNCATE todos RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// postForm отправляет POST-форму на сервер и возвращает ответ.
func postForm(t *testing.T, ts *httptest.Server, path string, form url.Values) *http.Response {
	t.Helper()
	resp, err := ts.Client().PostForm(ts.URL+path, form)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// bodyOk читает тело и закрывает ответ.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func TestAddAndListTodo(t *testing.T) {
	db := testDB(t)
	ts := httptest.NewServer(newMux(db))
	defer ts.Close()

	// Пустой список: страница отдаёт HTML и пустой <ul>.
	body := readBody(t, mustGet(t, ts, "/"))
	if !strings.Contains(body, `class="todos"`) {
		t.Fatalf("на главной нет списка .todos: %s", body)
	}

	resp := postForm(t, ts, "/todos", url.Values{"title": {"Купить молоко"}})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /todos → %d", resp.StatusCode)
	}

	body = readBody(t, mustGet(t, ts, "/"))
	if !strings.Contains(body, "Купить молоко") {
		t.Fatalf("добавленный todo не отображается: %s", body)
	}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM todos`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("в БД %d записей, ожидалась 1", n)
	}
}

func TestToggleTodo(t *testing.T) {
	db := testDB(t)
	ts := httptest.NewServer(newMux(db))
	defer ts.Close()

	_ = postForm(t, ts, "/todos", url.Values{"title": {"Сделать зарядку"}}).Body.Close()

	var id int64
	if err := db.QueryRow(`SELECT id FROM todos`).Scan(&id); err != nil {
		t.Fatalf("id: %v", err)
	}

	resp := postForm(t, ts, "/todos/"+itoa(id)+"/toggle", nil)
	_ = resp.Body.Close()

	var done bool
	if err := db.QueryRow(`SELECT done FROM todos WHERE id=$1`, id).Scan(&done); err != nil {
		t.Fatalf("done: %v", err)
	}
	if !done {
		t.Fatal("после toggle done = false, ожидалось true")
	}

	// Персистентность: состояние видно и в HTML-списке после перезагрузки.
	body := readBody(t, mustGet(t, ts, "/"))
	if !strings.Contains(body, `class="done"`) {
		t.Fatalf("выполненный todo не помечен .done: %s", body)
	}
}

func TestDeleteTodo(t *testing.T) {
	db := testDB(t)
	ts := httptest.NewServer(newMux(db))
	defer ts.Close()

	_ = postForm(t, ts, "/todos", url.Values{"title": {"Удалить меня"}}).Body.Close()

	var id int64
	if err := db.QueryRow(`SELECT id FROM todos`).Scan(&id); err != nil {
		t.Fatalf("id: %v", err)
	}
	_ = postForm(t, ts, "/todos/"+itoa(id)+"/delete", nil).Body.Close()

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM todos`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("после delete осталось %d записей", n)
	}
}

func mustGet(t *testing.T, ts *httptest.Server, path string) *http.Response {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s → %d", path, resp.StatusCode)
	}
	return resp
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
