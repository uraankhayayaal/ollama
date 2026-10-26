# План: Фикс #1 — Ограничение контекста BoardListTasks

**Проблема:** `BoardListTasks` возвращает все описания задач проекта (~155K символов для 58 задач). При 23 эпиках и вызове архитектором через `BoardListTasks` — общий контекст переполняет лимит LLM (98305 > 98304 токенов), сервер падает с OOM.

---

## 1. Диагностика

```
curl /api/projects/mytrip → 23 эпика, 58 задач, 155443 символов в описаниях
architectureSystemPrompt ~ 40KB (системный промпт архитектора)
BoardListTasks → JSON всех задач без обрезки
```

Каждый раунд агента добавляет в контекст: системный промпт + результат BoardListTasks + результат BoardListEpics + история вызовов. Через 3-4 раунда — 98305+ токенов, LLM отклоняет запрос.

---

## 2. Решение

### Вариант A: Параметр `short` в BoardListTasks (рекомендуемый)

Добавить опциональный параметр `short` в `BoardListTasks.Execute`. При `short=true` возвращать только метаданные задачи без `description`:

```go
func (t *boardListTasksTool) Execute(args map[string]any) ([]byte, error) {
    ...
    if args["short"] == true {
        for _, tk := range tasks {
            tk.Description = "" // не шлём описание в контекст
        }
    }
    ...
}
```

Архитектор получает контекст: ID, статус, заголовок, роль — без 155K описаний. Полное описание читается `BoardGetTask` по необходимости.

### Вариант B: Обрезка description (фолбэк)

Альтернатива: в `BoardListTasks` обрезать `description` до первых 500 символов:

```go
func (t *boardListTasksTool) Execute(args map[string]any) ([]byte, error) {
    ...
    for _, tk := range tasks {
        if len(tk.Description) > 500 {
            tk.Description = tk.Description[:500] + "..."
        }
    }
    ...
}
```

**Выбор:** Вариант A (чётче, не обрезает середину описаний).

---

## 3. Изменения по файлам

| Файл | Изменение |
|---|---|
| `tools/board_tools.go` (строки 244-280) | `BoardListTasks` — добавить параметр `short` в `Parameters`, обработать в `Execute` |
| `agents/architect/agent.go` (строки 274-300) | Обновить промпт архитектора: «BoardListTasks с `short=true` для быстрого обзора доски; BoardGetTask для деталей задачи» |
| `agents/businessanalyst/agent.go` | Аналогично — обновить упоминание `BoardListTasks` с параметром `short` |

---

## 4. Порядок реализации

### Шаг 1: BoardListTasks — параметр `short`

- [x] 1.1 В `BoardListTasks.Definition().Parameters` — добавить `"short": {"type": "boolean", "description": "Не включать поле description"}`
- [x] 1.2 В `BoardListTasks.Execute` — `boolArg(args, "short")`: при `true` обнулять `Description` у каждой задачи
- [x] 1.3 `epic_id` — фильтр по эпику остаётся
- [x] 1.4 `boolArg` — добавить вспомогательную функцию рядом с `strArg`
- [x] 1.5 Тесты: `TestBoardListTasksShort`, `TestBoardListTasksEpicFilter`
- [x] **Гейт:** `go build ./...` ✓, `go test ./tools/...` ✓

### Шаг 2: Обновить промпты архитектора

- [x] 2.1 В `architectureSystemPrompt` — `BoardListTasks(short=true)` для обзора, `BoardGetTask` для деталей
- [x] 2.2 В плане работы архитектора — `BoardListTasks(short=true)` без перегрузки контекста
- [x] 2.3 В `epicReviewSystemPrompt` — аналогично `BoardListTasks(short=true)`
- [x] **Гейт:** `go build ./...` ✓

### Шаг 3: Business Analyst

- [x] 3.1 `businessanalyst/agent.go` — НЕ использует `BoardListTasks` → не нужен

---

## 5. Проверка

```bash
# Unit-тесты:
go test -v -run TestBoardListTasks ./tools/...
# → TestBoardListTasksShort PASS, TestBoardListTasksEpicFilter PASS
```

Ожидаемый результат: description пустой при `short=true`, все другие поля (task_id, title, status, epic_id, assigned_role) остаются.

---

## 6. Риски

| Риск | Смягчение |
|---|---|
| Архитектор не использует `short=true` | Промпт содержит явную инструкцию; LLM следует параметрам из определения инструмента |
| Описание задачи нужно для декомпозиции | Промпт указывает: для деталей — `BoardGetTask` (полное description); для обзора — `short=true` |

---

## 7. Зависимости

Снижает контекст архитектора с ~155K → ~5K символов (~39K → ~1.3K токенов). В комбинации с предпроверкой (фикс #4, `maxInputTokens = 85000`) — контекст укладывается в лимит без сжатия.
