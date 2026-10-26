# План: Фикс #2 — Запрет суб-эпиков при декомпозиции архитектором

**Проблема:** При декомпозиции эпика (например MYTRIP-V2) архитектор создаёт суб-эпики (MYTRIP-V2-T1..T5) через `BoardCreateEpic` вместо задач через `BoardCreateTask`. Эти суб-эпики затем обрабатываются оркестратором как самостоятельные эпики, входя в цикл `business_review → system_analysis` и вызывая рекурсивную декомпозицию.

---

## 1. Диагностика

```
# MYTRIP-V2 (эпик) → декомпозиция архитектором:
#   Ожидается: MYTRIP-V2-T1..T5 как TASK (BoardCreateTask, epic_id=MYTRIP-V2)
#   Получено: MYTRIP-V2-T1..T5 как EPIC (BoardCreateEpic)
#   Результат: каждый суб-эпик проходит через оркестратора:
#     MYTRIP-V2-T1 → business_review → system_analysis → декомпозиция → MYTRIP-V2-T1-B1..B3 (task)
#     MYTRIP-V2-T2 → business_review → system_analysis → декомпозиция → MYTRIP-V2-T2-1..5 (sub-epic!) → рекурсия
```

Причина в промпте: `architectDecomposePrompt` говорит «НЕ создавай эпики», но архитектор имеет доступ к `BoardCreateEpic` в своих инструментах. Модель видит на доске 23 эпика и решает создать подуровень.

---

## 2. Решение

### 2.1 Отключить `BoardCreateEpic` у архитектора при декомпозиции

В `phaseArchitectDecompose` архитектор создаётся через `agentFactory().Architect(...)`. Вместо полного набора инструментов — использовать подмножество **без** `BoardCreateEpic`:

```go
arch := agentFactory().Architect(k.store.Project(), k.architectDecomposePrompt(e), k.store)
arch = k.prepareArchitect(arch)
// Убираем BoardCreateEpic — архитектор в декомпозиции создаёт только задачи
arch.Tools.Remove(tools.BoardCreateEpic)
```

### 2.2 Добавить валидацию после декомпозиции

После `k.generate` проверить, что на доске не появились новые эпики:

```go
beforeEpics := map[string]bool{}
for _, e := range epics {
    beforeEpics[e.TaskID] = true
}

// ... после k.generate ...

freshEpics, _ := k.store.ListEpics(ctx)
for _, e := range freshEpics {
    if !beforeEpics[e.TaskID] {
        // Архитектор создал эпик — удалить его
        k.store.DeleteEpic(ctx, e.TaskID)
        k.log.Warnf("[декомпозиция] удалён суб-эпик %s (создан архитектором вместо задачи)", e.TaskID)
    }
}
```

### 2.3 Обновить промпт

Добавить в `architectDecomposePrompt` пример вызова `BoardCreateTask` и чётко запретить `BoardCreateEpic`:

```
Используй ТОЛЬКО BoardCreateTask для создания задач.
НЕ используй BoardCreateEpic — этот инструмент недоступен.
```

---

## 3. Изменения по файлам

| Файл | Изменение |
|---|---|
| `application/planner/kanban_architect_decompose.go` (строки 16-92) | `phaseArchitectDecompose` — снять `BoardCreateEpic` после создания архитектора; добавить валидацию новых эпиков |
| `application/planner/kanban_architect_decompose.go` (строки 96-127) | `architectDecomposePrompt` — добавить запрет `BoardCreateEpic` и пример вызова `BoardCreateTask` |
| `agents/architect/agent.go` (строка 43) | Проверить, что `BoardCreateEpic` есть в `defaultArchitectToolNames` — если есть, снимать в декомпозиции |
| `tools/board_tools.go` | Убедиться, что `Tools.Remove()` корректно работает (если нужен метод `Remove` в `ToolSet`) |

---

## 4. Порядок реализации

### Шаг 1: Добавить `Tools.Remove()`

- [x] 1.1 Добавить `Remove(name string)` в `tools.Set` (`tools/registry.go`) — `delete(byName, name)` + удалить из `order`
- [x] **Гейт:** `go build ./...` ✓

### Шаг 2: Снять `BoardCreateEpic` в `phaseArchitectDecompose`

- [x] 2.1 Добавить `RemoveToolsFromAgent(a services.Agent, names []string) services.Agent` в `AgentFactory` интерфейс (`application/planner/agentfactory.go`)
- [x] 2.2 Реализовать в `agents/factory/factory.go` — type assertion на `toolRemover` (интерфейс с `ToolsSet() *tools.Set`)
- [x] 2.3 Добавить `ToolsSet() *tools.Set` в `agents.Base` (экспорт существующего поля)
- [x] 2.4 В `phaseArchitectDecompose`: `arch = factory.RemoveToolsFromAgent(arch, DecomposeToolRemoval)`
- [x] 2.5 `DecomposeToolRemoval = []string{tools.BoardCreateEpic}` — константа в `agentfactory.go`
- [x] **Гейт:** `go build ./...` ✓, `go test ./application/planner/...` ✓

### Шаг 3: Добавить валидацию новых эпиков

- [x] 3.1 До `k.generate` — сохранить `beforeEpics` (map по TaskID всех эпиков доски)
- [x] 3.2 После `k.generate` — `ListEpics` и сравнение с `beforeEpics`
- [x] 3.3 При обнаружении суб-эпика — `DeleteEpic` + `Warnf` логирование
- [x] **Гейт:** `go build ./...` ✓, `go test ./application/planner/...` ✓

### Шаг 4: Обновить `architectDecomposePrompt`

- [x] 4.1 Явный запрет: `НЕ создавай эпики — инструмент BoardCreateEpic недоступен`
- [x] 4.2 Дублирование контекста: `Ты разбиваешь ЭПИК на ЗАДАЧИ, а не на под-эпики`
- [x] **Гейт:** `go build ./...` ✓

---

## 5. Тесты

```go
func TestArchitectDecomposeNoSubEpics(t *testing.T) {
    // Создаём эпик, декомпозиция должна создать задачи, а не суб-эпики
    // Проверяем, что после phaseArchitectDecompose нет новых эпиков
}
```

---

## 6. Риски

| Риск | Смягчение |
|---|---|
| `RemoveToolsFromAgent` ломает другие фазы архитектора | Убираем только в `phaseArchitectDecompose` (контекстно). Другие фазы (`phaseArchitect`, `phaseArchitectReviewer`) имеют полный набор. |
| Агент не удовлетворяет `toolRemover` | `RemoveToolsFromAgent` возвращает агент без изменений — деградация безопасна. Все агенты из `agents/factory` имеют `*agents.Base` и удовлетворяют интерфейсу. |
| Модель игнорирует запрет в промпте | Физически убираем `BoardCreateEpic` из инструментов — модель не сможет его вызвать. Валидация-очистка (шаг 3) — второй слой защиты. |
| Суб-эпики уже созданы до фикса | Валидация-очистка (шаг 3) удаляет их при следующем проходе оркестратора. |

---

## 7. Результат

- `BoardCreateEpic` физически недоступен архитектору при декомпозиции
- Промпт явно запрещает создание эпиков (дублирование для ясности модели)
- Валидация-очистка удаляет суб-эпики даже при обходе защиты
- Двойная защита: снятие инструмента + post-hoc валидация
- Изменения не затрагивают другие фазы архитектора (обычный режим, ревизия)
- Гейты: `go build ./...` ✓, `go test ./application/planner/...` ✓
