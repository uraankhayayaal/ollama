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

### Шаг 1: BoardListTasks — параметр `short` ✅

- [ ] 1.1 В `BoardListTasks.Definition().Parameters` — добавить опциональный `"short": {"type": "boolean"}` в свойства
- [ ] 1.2 В `BoardListTasks.Execute` — проверить `boolArg(args, "short")`: при `true` очистить `Description` у каждой задачи
- [ ] 1.3 `BoardListTasks.Execute` — параметр `epic_id` уже фильтрует по эпику — оставить
- [ ] **Гейт:** `go build ./...`, `go test ./tools/...`

### Шаг 2: Обновить промпты архитектора

- [ ] 2.1 В `architectureSystemPrompt` (строки 274-275) — добавить инструкцию: `BoardListTasks({\"short\": true})` для обзора доски, `BoardGetTask` для деталей
- [ ] 2.2 В `architectDecomposePrompt` (`kanban_architect_decompose.go:96-127`) — добавить в правило 2: «получай контекст доски через BoardListTasks с short=true»
- [ ] **Гейт:** `go build ./...`

### Шаг 3: Аналогично — Business Analyst

- [ ] 3.1 Проверить, что `businessanalyst/agent.go` не имеет аналогичной проблемы с контекстом
- [ ] При необходимости — добавить аналогичную инструкцию в промпт BA

---

## 5. Проверка

```bash
# Проверить, что BoardListTasks с short=true не содержит описаний:
curl -s -X POST http://127.0.0.1:8090/api/tools/BoardListTasks \
  -H 'Content-Type: application/json' \
  -d '{"args": {"project": "mytrip", "short": true}}' | python3 -c "
import sys, json
tasks = json.load(sys.stdin)
print(f'Задач: {len(tasks)}, сумм. длин description: {sum(len(t.get(\"description\",\"\")) for t in tasks)}')
"
```

Ожидаемый результат: `сумм. длин description: 0` (при `short=true`).

---

## 6. Риски

| Риск | Смягчение |
|---|---|
| Архитектор не использует `short=true` | Добавить явную инструкцию в промпт. Модели склонны следовать параметрам в описании инструмента. |
| Описание задачи нужно для декомпозиции | Промпт указывает: для декомпозиции конкретного эпика — используй `BoardGetEpic` (там description полный). |
