// todoapp — небольшое todo-приложение: HTML-интерфейс + PostgreSQL.
//
// Это «продукт», который e2e-тест заставляет оркестрацию собрать: агент-
// разработчик (в scripted-режиме) пишет эти файлы инструментом WriteFiles,
// тестировщик запускает go test, а e2e-тест затем independently проверяет
// зелёный прогон и реальный UI приложения в браузере.
package main

import (
	"context"
	"database/sql"
	"errors"
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

// schema — DDL таблицы todos. Идемпотентна: применяется на старте и в тестах.
const schema = `CREATE TABLE IF NOT EXISTS todos (
    id         BIGSERIAL PRIMARY KEY,
    title      TEXT NOT NULL,
    done       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);`

// Todo — запись списка.
type Todo struct {
	ID    int64
	Title string
	Done  bool
}

// pageTmpl — единственная страница приложения: форма добавления + список.
var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Todo</title>
<style>
body{font-family:system-ui,-apple-system,sans-serif;max-width:640px;margin:2rem auto;padding:0 1rem;color:#111}
h1{font-size:1.4rem}
.add{display:flex;gap:.5rem;margin:1rem 0}
.add input{flex:1;padding:.5rem;font-size:1rem}
button{padding:.5rem .8rem;font-size:1rem;cursor:pointer}
ul.todos{padding-left:0;list-style:none}
ul.todos li{display:flex;gap:.5rem;align-items:center;padding:.45rem 0;border-bottom:1px solid #eee}
li.done .title{text-decoration:line-through;color:#888}
.title{flex:1}
</style>
</head>
<body class="app">
<header class="app-header"><h1>Todo</h1></header>
<form class="add" method="post" action="/todos">
  <input type="text" name="title" placeholder="Что сделать?" autocomplete="off" required>
  <button type="submit" class="btn primary">Добавить</button>
</form>
<ul class="todos">
{{- range .}}
  <li class="{{if .Done}}done{{end}}">
    <form method="post" action="/todos/{{.ID}}/toggle"><button type="submit" class="toggle" title="Переключить">{{if .Done}}✓{{else}}○{{end}}</button></form>
    <span class="title">{{.Title}}</span>
    <form method="post" action="/todos/{{.ID}}/delete"><button type="submit" class="delete" title="Удалить">Удалить</button></form>
  </li>
{{- end}}
</ul>
</body>
</html>`))

// newMux собирает HTTP-маршруты приложения поверх БД. Выделено из main,
// чтобы тесты гоняли реальный роутер через httptest без сети и портов.
func newMux(db *sql.DB) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		todos, err := listTodos(r.Context(), db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := pageTmpl.Execute(w, todos); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	mux.HandleFunc("POST /todos", func(w http.ResponseWriter, r *http.Request) {
		title := strings.TrimSpace(r.FormValue("title"))
		if title != "" {
			if _, err := db.ExecContext(r.Context(), `INSERT INTO todos(title) VALUES($1)`, title); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	mux.HandleFunc("POST /todos/{id}/toggle", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "некорректный id", http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), `UPDATE todos SET done = NOT done WHERE id=$1`, id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	mux.HandleFunc("POST /todos/{id}/delete", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "некорректный id", http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), `DELETE FROM todos WHERE id=$1`, id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	return mux
}

// listTodos читает список в порядке добавления.
func listTodos(ctx context.Context, db *sql.DB) ([]Todo, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, title, done FROM todos ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Todo
	for rows.Next() {
		var t Todo
		if err := rows.Scan(&t.ID, &t.Title, &t.Done); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// openDB открывает пул соединений и проверяет доступность БД.
func openDB(dsn string) (*sql.DB, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("пустой DSN: задайте DATABASE_URL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// ensureSchema применяет DDL (идемпотентно).
func ensureSchema(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := db.ExecContext(ctx, schema)
	return err
}

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://postgres:todo@127.0.0.1:5432/todo?sslmode=disable"
	}
	db, err := openDB(dsn)
	if err != nil {
		log.Fatalf("todoapp: подключение к БД: %v", err)
	}
	defer db.Close()
	if err := ensureSchema(db); err != nil {
		log.Fatalf("todoapp: схема: %v", err)
	}

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	log.Printf("todoapp: слушаю http://%s", addr)
	if err := http.ListenAndServe(addr, newMux(db)); err != nil {
		log.Fatalf("todoapp: %v", err)
	}
}
