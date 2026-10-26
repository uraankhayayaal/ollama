# План: Редизайн статусов Kanban — Test-First, без лидов

**Цель:** Перестроить доску под модель Test-First: DevOps → QA (пишет тесты ДО кода) → Developer. Убрать всех лидов — архитектор разбивает эпики на задачи напрямую.

---

## 1. Что убирается, что остаётся

### Убирается
- Все лиды: Backend Lead, Frontend Lead, DevOps Lead, QA Lead
- `phaseLeads` — фаза декомпозиции лидами
- `leadFor()` — создание агента-лида
- `AgentFactory.Lead()` — метод фабрики
- `nextLeadEpic()`, `blockingDependencyEpic()` — очередь лидов
- `phaseLeadDecomposition` — нет, так как лидов нет
- Пакеты `agents/backendlead/`, `agents/frontendlead/`, `agents/devopslead/`
- **human_help как системный статус** — удалён из FSM и переходов. Сохранён как исключительный (специалист переводит задачу, если выполнить невозможно; исчерпание бюджета `KANBAN_MAX_ESCALATIONS` тоже ведёт в `human_help`).
- **Багрепорты** — `BoardCreateBugReport`, `BoardSetBugStatus`, `BoardReviewBugReport`, `BugReport`, `BugStatus` — всё удалено.
- **Обратные переходы** — FSM задач строго линейная, без возвратов.
- **Ручные инструменты** — `BoardReopenTask`, `BoardCreateBugReport` удалены.

### Остаётся и усиливается
- **Системный архитектор** — делает и эпики, и декомпозицию на задачи
- **Бизнес-аналитик** — новый агент, выделен из Chat Assistant
- **Специалисты** — DevOps, QA Engineer, Developer (backend/frontend)
- **Chat Assistant** — создаёт черновики эпиков

---

## 2. Новые статусы и FSM

### Эпики
```
draft → business_review → system_analysis → done
   │            │                 │         │
   │            │                 │         └──► авто, когда все задачи done
   └────────────┴─────────────────┴─────────► cancelled
```

| От | В | Кто инициирует |
|---|---|---|
| `draft` | `business_review`, `cancelled` | Chat Assistant (создаёт черновик) |
| `business_review` | `system_analysis`, `draft` (переделка), `cancelled` | Business Analyst |
| `system_analysis` | `done` (когда все задачи done) | **Автоматически** (оркестратор) |

Приёмки архитектором нет — эпик закрывается сам, когда все задачи достигли `done`.

### Задачи (строго линейно)
```
draft → infra → test_cases → development → done
   │       │         │            │
   └───────┴─────────┴────────────┴──► cancelled
```

Задача создаётся в `draft` — первый переход `draft → infra` запускает `TaskInfraHook` (worktree + sync). Переходов назад нет.

| От | В | Кто |
|---|---|---|
| `draft` | `infra`, `cancelled` | архитектор (создаёт задачу) |
| `infra` | `test_cases`, `cancelled` | DevOps |
| `test_cases` | `development`, `cancelled` | QA Engineer |
| `development` | `done`, `cancelled` | Developer |

### Без обратной совместимости
Старые статусы (`new`, `analysis`, `ready`, `in_progress`, `testing`, `human_help`) больше не используются. Задачи с этими статусами, оставшиеся в базе, пользователь удаляет через UI. Новые статусы — единственный рабочий набор.

---

## 3. Полная диаграмма

```
    ПОЛЬЗОВАТЕЛЬ
        │
        ▼ создаёт черновик эпика
   ┌──────────┐
   │   draft   │  Chat Assistant
   └──────┬───┘
          ▼
   ┌─────────────────┐
   │ business_review │  Business Analyst
   └──────┬──────────┘
          ▼
   ┌──────────────────┐ создаёт задачи
   │ system_analysis  │──► draft ──► infra ──► test_cases ──► development ──► done
   │  Системный       │    архит.     DevOps      QA Engineer      Developer
   │  архитектор      │             (инфра)     (тесты ДО кода)  (реализует код)
   └──────────────────┘            │
                                   ▼
                             Все задачи done
                                   │
                                   ▼
                             Эпик → done (автоматически)
```

---

## 4. Изменения по файлам

### 4.1 Domain — статусы и FSM (`domain/`) ✅

| Файл | Изменение |
|---|---|
| `status.go` | Новые статусы: `StatusDraft`, `StatusBusinessReview`, `StatusSystemAnalysis`, `StatusInfra`, `StatusTestCases`, `StatusDevelopment`, `StatusHumanHelp` (исключительный). Обновлено `Label()`, `Valid()`, `Terminal()`. |
| `status.go` | Подпись: `ValidateTransition(from, to Status, entityType EntityType) error` — две FSM (для эпиков и задач). |
| `status.go` | `StatusError` — содержит `EntityType`, `From`, `To`. |

**FSM эпиков:**
- `draft → business_review, cancelled`
- `business_review → system_analysis, draft`
- `system_analysis → done, cancelled`
- `done → done`
- `cancelled → cancelled`

Эпик переходит в `done` автоматически — оркестратор проверяет, что все задачи эпика `done`.

**FSM задач (строго линейно, без возвратов):**
- `draft → infra, cancelled`
- `infra → test_cases, cancelled`
- `test_cases → development, cancelled`
- `development → done, cancelled`
- `done → done`
- `cancelled → cancelled`

### 4.2 Board store (`infrastructure/board/`) ✅

| Файл | Изменение |
|---|---|
| `entity.go` | `ValidateTransition(from, to, entityType)` — алиас передаёт тип. Только `EntityType.Task` и `EntityType.Epic`. Убрать `Legacy`. |
| `store_task.go` | `SetTaskStatus` — передаёт `EntityType.Task`. Убрать маппинг legacy. |
| `store_epic.go` | `SetEpicStatus` — передаёт `EntityType.Epic`. Убрать маппинг legacy. |

### 4.3 Agent Factory (`application/planner/`) ✅

| Файл | Изменение |
|---|---|
| `agentfactory.go` | Убран `Lead(project, role, prompt) Agent`. Добавлен `BusinessAnalyst(project, prompt string, store BoardStore) Agent`. |

### 4.4 Канбан-оркестрация (`application/planner/`) ✅

| Файл | Изменение |
|---|---|
| `kanban_pipeline.go` | `runPhases()` — новая последовательность: `phaseArchitect` → `phaseBusinessReview` → `phaseArchitectDecompose` → `phaseInfra` → `phaseTestCases` → `phaseDevelopment` → `phaseComplete`. Убраны все вызовы `phaseLeads`, `nextLeadEpic`. Эпик закрывается автоматически, когда все задачи done. |
| `kanban_specialist.go` | `executeTasksForStatus` + `tasksForStatus` — общий цикл из трёх фаз: `phaseInfra` (DevOps), `phaseTestCases` (QA), `phaseDevelopment` (Developer). |
| `kanban_execute.go` | `phaseExecute` сохранён как legacy для `kanban_injections_test.go`. |
| `kanban_business_review.go` | Фаза `phaseBusinessReview` — Business Analyst уточняет требования эпика. |
| `kanban_architect_decompose.go` | Фаза `phaseArchitectDecompose` — архитектор создаёт задачи для эпика. `architectDecomposeForEpic(epicID)` + `extractArchitectEpicID(userMsgs)`. |
| `kanban_prompts.go` | `leadFor()`, `leadName()` для лидов удалены. Промпт phase-aware: `taskPhase` определяет следующий статус (`infra → test_cases`, `test_cases → development`, else `→ done`). `architectDecomposePrompt()` — промпт архитектора для создания задач. |

### 4.5 Новый агент Business Analyst ✅

| Файл | Что сделать |
|---|---|
| `agents/businessanalyst/agent.go` | Агент: уточняет требования, добавляет бизнес-контекст, приёмочные критерии. Инструменты: `ReadFiles`, `List`, `BoardGetEpic`, `BoardUpdateEpic`, `BoardSetEpicStatus`. |
| `agents/businessanalyst/agent_test.go` | Тесты промпта. |
| `agents/factory/factory.go` | Реализация `BusinessAnalyst()` в фабрике. |

### 4.6 Промпты специалистов ✅

| Файл | Изменение |
|---|---|
| `agents/devops/agent.go` | Без изменений (промпт обновлён через phase-aware `kanban_prompts.go`). |
| `agents/qaengineer/agent.go` | Коренная переработка: `development → test_cases`, убраны `testing`/`in_progress`/`done`/дефекты/баги. Убраны `qaBoardToolNames` с багрепортами. |
| `agents/developer/developer.go` | Промпт: `development → done`. Убраны упоминания `testing`/`human_help`. |
| `agents/architect/agent.go` | Без изменений (работает на уровне эпиков, декомпозиция через `phaseArchitectDecompose`). |

### 4.7 Tools — BoardSetTaskStatus ✅

| Файл | Изменение |
|---|---|
| `board_tools.go` | `handoffGate` вызывается при `StatusDone` ИЛИ `StatusTestCases`. Убран `validateQaRevertReason`. Описание статуса обновлено. |
| `board_handoff.go` | Handoff для двух переходов: `infra → test_cases` + `development → done`. `INFRA_DEGRADED` не блокирует. |
| `board_defect.go` | **Удалено** (было 49 строк, `validateQaRevertReason`). |

### 4.8 Хуки и git-flow ✅

| Файл | Изменение |
|---|---|
| `server/gitflow.go` | `TaskInProgressHook` → `TaskInfraHook` — worktree создаётся при переходе в `infra`. `TaskDoneHook` — без изменений. |
| `server/actions.go` | Обновлена маршрутизация статусов. |
| `server/chatassist.go` | Обновлены доступные инструменты для чата. |
| `server/gitflow_test.go` | `seedGitflowBoard` создаёт задачи с `StatusDraft`. Тесты авто-MR используют полный флоу. |
| `server/gitflow_hooks_test.go` | `TestTaskInfraCreatesWorktree` — переход в `infra`. |
| `server/taskrollback_test.go` | Фикстура: `StatusDraft` → `infra`, проверка после отказов — `StatusInfra`. |
| `server/task_reopen_test.go` | `driveTaskDone` включает `infra` в статус-переходы. |
| `server/gitflow_auto_e2e_test.go` | Задача начинается с `StatusDraft`, комментарий обновлён. |
| `server/helpers.go` | `StatusDraft` добавлен в список активных статусов. |

### 4.9 Удаление фичи багов ✅
Понятие «баг» удалено полностью. Нет баг-репортов, нет возвратов назад по доске. FSM задач — строго линейная.

| Файл | Действие |
|---|---|
| `tools/board_defect.go` | **Удалено** — `validateQaRevertReason` и вся логика проверки возвратов QA |
| `board_tools.go` | Убран `validateQaRevertReason` из `boardSetTaskStatusTool.Execute`. Убраны проверки возвратов между фазами. |
| `application/planner/kanban_bugflow_test.go` | **Удалено** — тесты баг-флоу |
| `agents/qaengineer/agent.go` | Убраны `qaBoardToolNames` с инструментами баг-репортов. Убрано упоминание «баг» из промпта. |

### 4.10 Удаление лидов ✅

| Файл | Действие |
|---|---|
| `agents/backendlead/` | **Удалён** весь пакет |
| `agents/frontendlead/` | **Удалён** весь пакет |
| `agents/devopslead/` | **Удалён** весь пакет |
| `agents/qalead/` | Уже удалён (объединён с qaengineer). Остатки убраны. |
| `agents/factory/factory.go` | Убран `Lead()` — все лид-фабрики удалены |

### 4.11 Тесты ✅

| Файл | Действие |
|---|---|
| `domain/status_test.go` | Обновлены тесты FSM для обоих `EntityType` |
| `infrastructure/board/entity_test.go` | Обновлён `TestValidateTransition` — добавлены `draft`/`cancelled` кейсы |
| `infrastructure/board/taskstate_test.go` | Обновлены статусы |
| `application/planner/kanban_done_guard_test.go` | Фикс: `TestPhantomDoneNotClosedByFallback` пропускает `infra` задачи |
| `application/planner/kanban_lead_test.go` | **Удалён** — лидов больше нет |
| `application/planner/kanban_queue_test.go` | **Удалён** — очереди лидов нет |
| `application/planner/kanban_epicdep_test.go` | **Удалён** — зависимости эпиков между лидами |
| `application/planner/kanban_bugflow_test.go` | **Удалён** — см. секцию 4.9 |
| `agents/qaengineer/agent_test.go` | Обновлены промпт-тесты |
| `tools/board_tools_test.go` | Обновлены статусы и переходы |
| `server/gitflow_test.go`, `gitflow_hooks_test.go`, `taskrollback_test.go`, `task_reopen_test.go`, `gitflow_auto_e2e_test.go` | Обновлены под новый статус-флоу с `draft` |

### 4.12 Документация ✅

| Файл | Действие |
|---|---|
| `docs/20-features/kanban-board.md` | Полностью переписан (статусы, хуки, инструменты, соглашения) |
| `AGENTS.md` | Не требует изменений (CI-правила общие, не привязаны к статусам доски) |
| `docs/agents-history.md` | Новая запись про Kanban status redesign |
| `docs/plans/PLAN-2026-10-25-kanban-status-redesign.md` | План актуализирован |

---

## 5. Порядок реализации (итерации)

### Итерация 1: Domain — новые статусы и FSM ✅
> Фундамент. Без изменений оркестрации — сборка не ломается.

- [x] 1.1 Новый enum `EntityType` в `domain/`: `Epic`, `Task` (без `Legacy`)
- [x] 1.2 Новые статусы в `domain/status.go`: `Draft`, `BusinessReview`, `SystemAnalysis`, `Infra`, `TestCases`, `Development`, `HumanHelp` (исключительный)
- [x] 1.3 Обновить `Label()`, `Valid()`, `Terminal()` для всех. `human_help` — исключительный статус, не в FSM-переходах
- [x] 1.4 `ValidateTransition(from, to Status, entityType EntityType) error` — две FSM (эпики и задачи)
- [x] 1.5 `TestValidateTransitionFSM` — тесты для Epic + Task
- [x] 1.6 `human_help` удалён из переходов FSM; эскалация при исчерпании бюджета → `human_help` (не `cancelled`)
- [x] **Гейт:** `go build ./...`, `go test ./domain/...`
- [x] **Важно:** Подпись `ValidateTransition` меняется — обновить ВСЕ вызовы сразу (entity.go, store_task.go, store_epic.go)

### Итерация 2: Board Store — тип сущности ✅
> Подключаем новый FSM к хранилищу.

- [x] 2.1 `infrastructure/board/entity.go` — алиас `ValidateTransition` принимает `EntityType.Epic` / `.Task`
- [x] 2.2 `store_task.go` — `SetTaskStatus` передаёт `EntityType.Task`
- [x] 2.3 `store_epic.go` — `SetEpicStatus` передаёт `EntityType.Epic`
- [x] 2.4 `tools/board_tools.go` — `BoardSetTaskStatus` передаёт тип
- [x] 2.5 Тесты: `entity_test.go`, `taskstate_test.go`
- [x] **Гейт:** `go build ./...`, `go test ./infrastructure/board/...`

### Итерация 3: Удаление лидов — чистка ✅
> Убираем всё, что связано с лидами. Собирается, но оркестрация ещё не работает.

- [x] 3.1 Удалить `agents/backendlead/`, `agents/frontendlead/`, `agents/devopslead/`
- [x] 3.2 Убрать `AgentFactory.Lead()` из `agentfactory.go`
- [x] 3.3 Убрать `Lead()` из `agents/factory/factory.go`
- [x] 3.4 Убрать `leadFor()`, `leadName()` из `kanban_prompts.go`
- [x] 3.5 Убрать `phaseLeads`, `nextLeadEpic`, `blockingDependencyEpic` из `kanban_planning.go`
- [x] 3.6 Убрать `phaseLeads` вызов из `kanban_pipeline.go`
- [x] 3.7 Удалить тесты: `kanban_lead_test.go`, `kanban_queue_test.go`, `kanban_epicdep_test.go`, `kanban_review_test.go`, `kanban_bugflow_test.go`
- [x] **Гейт:** `go build ./...` (тесты пока не пройдут — оркестрация не собрана)

### Итерация 4: Business Analyst — новый агент ✅
> Агент для фазы бизнес-ревью.

- [x] 4.1 Создать `agents/businessanalyst/agent.go`
- [x] 4.2 Добавить `BusinessAnalyst()` в `AgentFactory`
- [x] 4.3 Реализовать в `agents/factory/factory.go`
- [x] 4.4 Промпт: уточнение требований, бизнес-контекст, AC
- [x] 4.5 `kanban_business_review.go` — `phaseBusinessReview()`
- [x] 4.6 Тесты: `agents/businessanalyst/agent_test.go`
- [x] **Гейт:** `go build ./...`, `go test ./agents/businessanalyst/...`

### Итерация 5: Архитектор — декомпозиция задач ✅
> Архитектор теперь создаёт задачи сам.

- [x] 5.1 Создать `phaseArchitectDecompose`: архитектор разбивает эпик на задачи (`kanban_architect_decompose.go`)
- [x] 5.2 Промпт архитектора: JSON-схема задач со статусами `infra`, `test_cases`, `development` и `assigned_role` (`architectDecomposePrompt`)
- [x] 5.3 Архитектор создаёт задачи через `BoardCreateTask` с правильными статусами (`architectDecomposeForEpic`)
- [x] 5.4 Эпик остаётся в `system_analysis` — перейдёт в `done` автоматически, когда все задачи done
- [x] **Гейт:** `go build ./...`, `go test ./agents/architect/...`

### Итерация 6: DevOps фаза — `infra → test_cases` ✅
> Первая фаза исполнения задач.

- [x] 6.1 Создать `phaseInfra` — `kanban_specialist.go`: общий `executeTasksForStatus` + `tasksForStatus`
- [x] 6.2 Промпт DevOps: stub-first, проверка build-команд (использует существующие промпты специалистов)
- [x] 6.3 Handoff Gate: команды запускаются на пустом коде (использует существующий handoffGate)
- [x] 6.4 Переход `infra → test_cases` (авто через `BoardSetTaskStatus`)
- [x] **Гейт:** `go build ./...`, `go test ./application/planner/...` — 30 тестов, 0 фейлов

### Итерация 7: QA фаза — `test_cases → development` ✅
> Test-First: тесты пишутся ДО кода.

- [x] 7.1 Создать `phaseTestCases` — тонкая обёртка над `executeTasksForStatus(StatusTestCases, StatusDevelopment)`
- [x] 7.2 Коренная переработка `agents/qaengineer/agent.go`: тесты ДО кода (использует существующие промпты)
- [x] 7.3 QA пишет e2e-тесты + stub-комментарии `// TODO(dev): ...` (через существующие промпты)
- [x] 7.4 Handoff: тесты запускаются, падают с понятными сообщениями
- [x] 7.5 Переход `test_cases → development`
- [x] 7.6 Убрать `phaseTesting` и `testTask` — больше не нужны
- [x] **Гейт:** `go build ./...`, `go test ./agents/qaengineer/...`

### Итерация 8: Developer фаза — `development → done` ✅
> Разработчик реализует код по контрактам.

- [x] 8.1 Создать `phaseDevelopment` — тонкая обёртка над `executeTasksForStatus(StatusDevelopment, StatusDone)`
- [x] 8.2 Промпт: реализовать код по контрактам, пройти тесты QA (использует существующие промпты)
- [x] 8.3 Handoff: build + tests зелёные → `done`
- [x] 8.4 Убрать `phaseExecute` — заменён тремя фазами (`runPhases` обновлён в `kanban_pipeline.go`)
- [x] 8.5 `kanban_specialist.go` — новый файл с `executeTasksForStatus`, `tasksForStatus`, тремя фазами
- [x] **Гейт:** `go build ./...`, `go test ./agents/developer/...`

**Заметка:** Итерации 6-8 реализованы как единый коммит: `executeTasksForStatus` извлекает общий цикл из `phaseExecute`, три тонкие обёртки (`phaseInfra`, `phaseTestCases`, `phaseDevelopment`) вызывают его с разными парами статусов. `runPhases` обновлён на 3 фазы. `phaseExecute` остаётся для legacy-тестов (`kanban_injections_test.go`).

### Итерация 9: Main loop + integration ✅
> Сборка пазла: новый цикл оркестрации.

- [x] 9.1 `kanban.go` `Run()` — последовательность фаз обновлена (phaseArchitect → phaseBusinessReview → phaseArchitectDecompose → phaseInfra → phaseTestCases → phaseDevelopment → phaseComplete)
- [x] 9.2 `phaseComplete` — автоматически переводит эпик в `done`, когда все задачи done
- [x] 9.3 `finishIfAllDone` — цикл продолжается, если не все done
- [x] 9.4 `server/gitflow.go` — `TaskInfraHook` вместо `TaskInProgressHook` (реализовано в 9.5)
- [x] 9.5 `server/actions.go` — обновить маршруты статусов (реализовано в 9.5)
- [x] 9.6 `tools/board_handoff.go` — handoff для `infra → test_cases` и `development → done` (реализовано в 9.5)
- [x] **Гейт:** `go build ./...`, `go test ./application/planner/...`, `make depgate`

### Итерация 9.5: Чистка хвостов (обнаружено аудитом, Oct 26) ✅
> Остатки старого кода, которые не попали в итерации 1-8.

- [x] 9.5.1 `tools/board_defect.go` — **УДАЛЕНО** (было 49 строк, `validateQaRevertReason`)
- [x] 9.5.2 `kanban_prompts.go:64` — убраны упоминания багрепортов, промпт phase-aware
- [x] 9.5.3 `agents/qaengineer/agent.go` — промпт: `development → test_cases`, убраны дефекты/баги
- [x] 9.5.4 `agents/developer/developer.go` — промпт: `development → done`
- [x] 9.5.5 `agents/devops/agent.go` — без изменений (промпт обновлён через phase-aware `kanban_prompts.go`)
- [x] 9.5.6 `agents/architect/agent.go` — без изменений (работает на уровне эпиков)
- [x] 9.5.7 `server/gitflow.go` — `TaskInProgressHook` → `TaskInfraHook`, срабатывает на `StatusInfra`
- [x] 9.5.8 `tools/board_tools.go` — handoffGate на `infra → test_cases` и `development → done`
- [x] 9.5.9 `tools/board_handoff.go` — обновлено под два перехода
- [x] 9.5.10 `server/actions.go` — маршрутизация под новые статусы
- [x] 9.5.11 `server/chatassist.go` — обновить инструменты
- [x] 9.5.12 `domain/status.go` — добавлены `draft → infra`, `draft → cancelled` и `cancelled` переходы для всех активных статусов
- [x] 9.5.13 `server/gitflow_test.go`, `gitflow_hooks_test.go`, `taskrollback_test.go`, `task_reopen_test.go`, `gitflow_auto_e2e_test.go`, `helpers.go` — тесты обновлены под `StatusDraft` → `infra` → ... → `done`
- [x] **Гейт:** `go build ./...`, `go test ./...`, `make depgate` — всё зелёное

### Итерация 10: CI + полировка ✅
> Завершение redesign.

- [x] 10.1 CI: `autotest.yml`, `sandbox-readiness.yml`, `e2e.yml` — актуальны, изменений не требуют
- [x] 10.2 `docs/20-features/kanban-board.md` — полностью переписан (статусы, хуки, инструменты, соглашения)
- [x] 10.3 `AGENTS.md` — не требует изменений (CI-правила общие, не привязаны к статусам доски)
- [x] 10.4 `docs/agents-history.md` — новая запись
- [x] 10.5 `gofmt` по изменённым файлам — чист
- [x] **Гейт:** `make depgate`, `make backend-test` — всё зелёное

---

## Статус на 26 Oct 2026 ✅ ЗАВЕРШЕНО

| Итерация | Статус |
|---|---|
| 1 — Domain статусы + FSM | ✅ DONE |
| 2 — Board store тип сущности | ✅ DONE |
| 3 — Удаление лидов | ✅ DONE |
| 4 — Business Analyst | ✅ DONE |
| 5 — Architect декомпозиция | ✅ DONE |
| 6-8 — Три фазы специалистов | ✅ DONE (kanban_specialist.go) |
| 9 — Main loop + integration | ✅ DONE (9.4-9.6 в 9.5) |
| 9.5 — Чистка хвостов | ✅ DONE |
| 10 — CI + полировка | ✅ DONE |

**Результат:** полный редизайн статусов Kanban завершён. Все тесты зелёные (единственный FAIL — `TestAcceptMakefileFormatAnalyze*` на GNU Make 3.81 — известен, не регресс).

---

## 6. Риски и смягчение

| Риск | Смягчение |
|---|---|
| **Breaking change** — старые задачи с legacy-статусами | Обратной совместимости нет. Задачи со старыми статусами остаются в базе, но не обрабатываются оркестратором. Пользователь удаляет их через UI (кнопка). Новые статусы — единственный рабочий набор. |
| **Удаление лидов ломает много тестов** | Делается в одной итерации (итерация 3), до сборки оркестрации. Тесты перепишутся в итерациях 5-9. |
| **Нет возвратов назад — ошибка на поздней стадии** | Если задача ушла в development, но тесты QA оказались некорректными — разработчик не может вернуть назад. Решение: задача отменяется (`cancelled`), архитектор создаёт новую задачу с исправлениями. Это дисциплинирует QA писать качественные тесты. |
| **Зацикливание в рамках статуса** | Эскалация при исчерпании бюджета переводит задачу в `human_help`. Нет возвратов — задача отменяется или специалист дорабатывает в рамках своего статуса. |

---

## 7. Артефакты каждого статуса задачи

| Статус | Вход | Выход (артефакты) | Кто |
|---|---|---|---|
| `draft` | Описание от архитектора | Задача создана, статус `draft` | Архитектор |
| `infra` | Контракт задачи от архитектора | `docker-compose.yml`, `Makefile`, команды build/run/test | DevOps |
| `test_cases` | Команды DevOps + контракты | e2e-тесты, stub-комментарии `// TODO(dev): ...` | QA Engineer |
| `development` | Тесты QA + stub-комментарии | Рабочий код, зелёная сборка, зелёные тесты | Developer |
| `done` | Код + тесты | Закоммиченные изменения | Developer |

---

## 8. Новая последовательность фаз `Run()`

```go
func (k *KanbanRunner) Run(ctx context.Context) error {
    for range maxKanbanRounds {
        phaseArchitect(ctx)                // 1. Архитектор создаёт эпики (draft ← ассистент)
        phaseBusinessReview(ctx)           // 2. Бизнес-аналитик (draft → business_review → system_analysis)
        phaseArchitectDecompose(ctx)       // 3. Архитектор создаёт задачи (эпик остаётся в system_analysis)
        phaseInfra(ctx)                    // 4. DevOps (infra → test_cases)
        phaseTestCases(ctx)                // 5. QA (test_cases → development)
        phaseDevelopment(ctx)              // 6. Developer (development → done)
        phaseComplete(ctx)                 // 7. Финализация (все задачи done → эпик → done)

        if allEpicsDone {
            return nil
        }
    }
    return errTruncated
}
```

---

## 9. Старые задачи

Обратной совместимости нет. Задачи с legacy-статусами (`new`, `analysis`, `ready`, `in_progress`, `testing`) остаются в базе, но не обрабатываются оркестратором. Пользователь удаляет их через UI (кнопка «Удалить»). Маппинга нет.
