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

### Шаг 1: Проверить наличие `Tools.Remove()`

- [ ] 1.1 Проверить, есть ли метод `Remove(name string)` в `ToolSet` (`tools/tools.go` или аналог)
- [ ] 1.2 Если нет — добавить метод (одна строка: `delete(s.tools, name)`)
- [ ] **Гейт:** `go build ./...`

### Шаг 2: Снять `BoardCreateEpic` в `phaseArchitectDecompose`

- [ ] 2.1 В `kanban_architect_decompose.go` — после `arch = k.prepareArchitect(arch)` добавить `arch.Tools.Remove(tools.BoardCreateEpic)`
- [ ] 2.2 Проверить, что `Tools` — доступное поле `*architect.Architect` (или `services.Agent`)
- [ ] **Гейт:** `go build ./...`

### Шаг 3: Добавить валидацию новых эпиков

- [ ] 3.1 До вызова `k.generate` — сохранить список существующих эпиков (`beforeEpics`)
- [ ] 3.2 После `k.generate` — проверить `ListEpics` на новые эпики
- [ ] 3.3 При обнаружении суб-эпика — удалить его с логированием `Warnf`
- [ ] **Гейт:** `go build ./...`, `go test ./application/planner/...`

### Шаг 4: Обновить `architectDecomposePrompt`

- [ ] 4.1 Добавить пример: `{"tasks": [{"task_id": "T-01", ...}]}` с вызовом `BoardCreateTask`
- [ ] 4.2 Явный запрет: `НЕ используй BoardCreateEpic — создавай только задачи (BoardCreateTask)`
- [ ] 4.3 Добавить пример правильного вызова: `BoardCreateTask({"task_id": "T-01", "epic_id": "%s", ...})`
- [ ] **Гейт:** `go build ./...`

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
| `Tools.Remove()` ломает другие фазы архитектора | Убираем только в `phaseArchitectDecompose` (контекстно). Другие фазы (`phaseArchitect`) имеют полный набор. |
| Модель игнорирует запрет в промпте | Физически убираем `BoardCreateEpic` из инструментов — модель не сможет его вызвать. Промпт дублирует запрет для ясности. |
| Суб-эпики уже созданы до фикса | Валидация-очистка (шаг 3) удаляет их при следующем проходе оркестратора. |
