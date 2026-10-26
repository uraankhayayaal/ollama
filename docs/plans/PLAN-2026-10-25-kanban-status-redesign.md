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
- **human_help** — статус и колонка. Когда бюджет эскалаций исчерпан, задача переходит в `cancelled` (а не в `human_help`). Удаляется из FSM, `ValidateTransition`, board-tools, escalate.go, всех промптов и тестов.

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
infra → test_cases → development → done
   │         │            │
   └─────────┴────────────┴──► cancelled (при исчерпании бюджета)
```

Переходов назад нет. Если специалист не справился — дорабатывает в рамках своего статуса. Не получается — задача уходит в `cancelled`.

| От | В | Кто |
|---|---|---|
| `infra` | `test_cases`, `cancelled` | DevOps |
| `test_cases` | `development`, `cancelled` | QA |
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
   │ business_review │  Business Analyst (новый агент)
   └──────┬──────────┘
          ▼
   ┌──────────────────┐ создаёт задачи
   │ system_analysis  │──► infra ──► test_cases ──► development ──► done
   │  Системный       │   DevOps      QA Engineer      Developer
   │  архитектор      │   (инфра)     (тесты ДО кода)  (реализует код)
   └──────────────────┘          │
                                 ▼
                           Все задачи done
                                 │
                                 ▼
                           Эпик → done (автоматически)
```

---

## 4. Изменения по файлам

### 4.1 Domain — статусы и FSM (`domain/`)

| Файл | Изменение |
|---|---|
| `status.go` | Убрать `StatusHumanHelp` из констант, `Label()`, `Valid()`, FSM. Новые статусы: `StatusDraft`, `StatusBusinessReview`, `StatusSystemAnalysis`, `StatusInfra`, `StatusTestCases`, `StatusDevelopment`. Обновить `Label()`, `Valid()`, `Terminal()`. |
| `status.go` | Подпись: `ValidateTransition(from, to Status, entityType EntityType) error` — две FSM (для эпиков и задач). |
| `status.go` | `StatusError` — добавить поле `EntityType` |
| `status.go` | Удалить `human_help` из всех переходов FSM. Эскалация при исчерпании бюджета переводит задачу в `cancelled`. |

**FSM эпиков:**
- `draft → business_review, cancelled`
- `business_review → system_analysis, draft, cancelled`
- `system_analysis → done, cancelled`

Эпик переходит в `done` автоматически — оркестратор проверяет, что все задачи эпика `done`, и ставит эпик в `done`. Нет статуса `acceptance`.

**FSM задач (строго линейно, без возвратов):**
- `infra → test_cases, cancelled`
- `test_cases → development, cancelled`
- `development → done, cancelled`

### 4.2 Board store (`infrastructure/board/`)

| Файл | Изменение |
|---|---|
| `entity.go` | `ValidateTransition(from, to, entityType)` — алиас передаёт тип. Только `EntityType.Task` и `EntityType.Epic`. Убрать `Legacy`. |
| `store_task.go` | `SetTaskStatus` — передаёт `EntityType.Task`. Убрать маппинг legacy. |
| `store_epic.go` | `SetEpicStatus` — передаёт `EntityType.Epic`. Убрать маппинг legacy. |

### 4.3 Agent Factory (`application/planner/`)

| Файл | Изменение |
|---|---|
| `agentfactory.go` | Убрать `Lead(project, role, prompt) Agent`. Добавить `BusinessAnalyst(project, prompt string, store BoardStore) Agent`. |

### 4.4 Канбан-оркестрация (`application/planner/`)

| Файл | Изменение |
|---|---|
| `kanban.go` | `Run()` — новая последовательность фаз: `phaseArchitect` → `phaseBusinessReview` → `phaseArchitectDecompose` → `phaseInfra` → `phaseTestCases` → `phaseDevelopment` → `phaseComplete`. Убрать все вызовы `phaseLeads`, `nextLeadEpic`. Эпик закрывается автоматически, когда все задачи done. |
| `kanban_ready.go` | **Переименовать** → `kanban_routing.go`. Убрать `phaseReady`, `taskPhase`, `phasePrereqSatisfied` — задачи маршрутизируются по статусу. |
| `kanban_execute.go` | `phaseExecute` → разбить на 3 фазы: `phaseInfra` (DevOps), `phaseTestCases` (QA), `phaseDevelopment` (Developer). Убрать `phaseTesting` вызовы из execute. |
| `kanban_testing.go` | **Удалить**. `phaseTesting`, `testTask` больше не нужны — тестирование часть `phaseTestCases`. Закрытие эпика автоматическое в `phaseComplete`. |
| `kanban_planning.go` | `phaseArchitect` → расширить: архитектор создаёт **и эпики, и задачи**. Убрать `phaseLeads`, `nextLeadEpic`, `blockingDependencyEpic`, `leadFail`. |
| `kanban_prompts.go` | Убрать `leadFor()`, `leadName()`, `isRole()` для лидов. Обновить `taskPrompt()` — разные инструкции для каждого нового статуса. Добавить `architectDecomposePrompt()` — промпт для архитектора при создании задач. |
| `NEW: kanban_business_review.go` | Фаза `phaseBusinessReview` — Business Analyst уточняет требования эпика. |
| `NEW: kanban_architect_decompose.go` | Фаза `phaseArchitectDecompose` — архитектор создаёт задачи для эпика. |

### 4.5 Новый агент Business Analyst

| Файл | Что делать |
|---|---|
| `NEW: agents/businessanalyst/agent.go` | Агент: уточняет требования, добавляет бизнес-контекст, приёмочные критерии. Инструменты: `ReadFiles`, `List`, `BoardGetEpic`, `BoardUpdateEpic`, `BoardSetEpicStatus`. |
| `NEW: agents/businessanalyst/config.go` | Конфиг агента. |
| `NEW: agents/businessanalyst/agent_test.go` | Тесты промпта. |
| `agents/factory/factory.go` | Реализация `BusinessAnalyst()` в фабрике. |

### 4.6 Промпты специалистов

| Файл | Изменение |
|---|---|
| `agents/devops/agent.go` | Промпт: «Создай инфраструктуру (Docker Compose, Makefile), которая запускается на пустом коде. Создай команды build/run/test. Проверь, что всё работает — переведи в `test_cases`». |
| `agents/qaengineer/agent.go` | **Коренная переработка.** Промпт: «Пиши тесты ДО кода. Используй команды DevOps. Оставляй stub-комментарии `// TODO(dev): ...` для разработчика. Тесты должны запускаться, но падать с понятными сообщениями. Переведи в `development`». Убрать правила `testing → in_progress`/`done`. Убрать `qaBoardToolNames` с багрепортами — понятия «баг» нет. |
| `agents/developer/developer.go` | Промпт: «Реализуй код, чтобы тесты прошли. Следуй stub-комментариям от QA. Переведи в `done` когда всё зелёное». Убрать упоминание `testing`. |
| `agents/architect/agent.go` | Расширить промпт: архитектор создаёт задачи со статусами `infra`, `test_cases`, `development` и назначает исполнителей. Приёмки нет — после декомпозиции архитектор уходит. |

### 4.7 Tools — BoardSetTaskStatus

| Файл | Изменение |
|---|---|
| `board_tools.go` | `boardSetTaskStatusTool.Execute` — `handoffGate` вызывать при `infra → test_cases` и `development → done`. Убрать `validateQaRevertReason` (см. секцию 4.9). Определить `EntityType` по статусу. |
| `board_handoff.go` | handoffGate: проверка, что build-команды DevOps работают (при `infra → test_cases`); сборка + тесты (при `development → done`). |
| `board_defect.go` | **Удалить** (см. секцию 4.9). |

### 4.8 Хуки и git-flow

| Файл | Изменение |
|---|---|
| `server/gitflow.go` | `TaskInProgressHook` → переименовать в `TaskInfraHook` — worktree создаётся при переходе в `infra`. `TaskDoneHook` — без изменений. |
| `server/actions.go` | Обновить маршрутизацию статусов. |
| `server/chatassist.go` | Обновить доступные инструменты для чата. |

### 4.9 Удаление фичи багов
Понятие «баг» удаляется полностью. Нет баг-репортов, нет возвратов назад по доске. FSM задач — строго линейная.

| Файл | Действие |
|---|---|
| `tools/board_defect.go` | **Удалить** — `validateQaRevertReason` и вся логика проверки возвратов QA |
| `board_tools.go` | Убрать `validateQaRevertReason` из `boardSetTaskStatusTool.Execute`. Убрать проверки возвратов между фазами. |
| `application/planner/kanban_pipeline.go` | Убрать `phaseBugs()` — фаза обработки баг-репортов |
| `application/planner/kanban_bugflow_test.go` | **Удалить** — тесты баг-флоу |
| `agents/qaengineer/agent.go` | Убрать `qaBoardToolNames` с инструментами баг-репортов. Убрать упоминание «баг» из промпта. |
| `server/e2e_todo_test.go` | Убрать тесты с баг-флоу (если есть). |

### 4.10 Удаление лидов

| Файл | Действие |
|---|---|
| `agents/backendlead/` | **Удалить** весь пакет |
| `agents/frontendlead/` | **Удалить** весь пакет |
| `agents/devopslead/` | **Удалить** весь пакет |
| `agents/qalead/` | Уже удалён (объединён с qaengineer). Убрать остатки. |
| `agents/factory/factory.go` | Убрать `Lead()` — все лид-фабрики удалить |

### 4.11 Тесты

| Файл | Действие |
|---|---|
| `domain/status_test.go` | Новые тесты FSM для обоих `EntityType`. Убрать тесты legacy, `human_help`, `StatusPaused`. |
| `infrastructure/board/entity_test.go` | Обновить `TestValidateTransition` |
| `infrastructure/board/taskstate_test.go` | Обновить статусы |
| `application/planner/kanban_test.go` | Новый флоу оркестрации |
| `application/planner/kanban_lead_test.go` | **Удалить** — лидов больше нет |
| `application/planner/kanban_ready_test.go` | Переименовать → `kanban_phase_test.go` |
| `application/planner/kanban_execute_test.go` | Обновить или разделить |
| `application/planner/kanban_testing_test.go` | **Удалить** — `phaseTesting` больше не существует |
| `application/planner/kanban_queue_test.go` | **Удалить** — очереди лидов нет |
| `application/planner/kanban_epicdep_test.go` | **Удалить** — зависимости эпиков между лидами |
| `application/planner/kanban_review_test.go` | **Удалить** — ревизия лидом |
| `application/planner/kanban_bugflow_test.go` | **Удалить** — см. секцию 4.9 (удаление фичи багов) |
| `agents/qaengineer/agent_test.go` | Обновить промпт-тесты |
| `tools/board_tools_test.go` | Обновить статусы и переходы |
| `tools/board_tools_test.go` | Убрать `TestBoardSetTaskStatusHumanHelpReason` |

### 4.12 Документация

| Файл | Действие |
|---|---|
| `docs/20-features/kanban-board.md` | Полная переработка |
| `AGENTS.md` | Обновить флоу, убрать лидов |
| `docs/agents-history.md` | Новая запись |

---

## 5. Порядок реализации (итерации)

### Итерация 1: Domain — новые статусы и FSM
> Фундамент. Без изменений оркестрации — сборка не ломается.

- [ ] 1.1 Новый enum `EntityType` в `domain/`: `Epic`, `Task` (без `Legacy`)
- [ ] 1.2 Новые статусы в `domain/status.go`: `Draft`, `BusinessReview`, `SystemAnalysis`, `Infra`, `TestCases`, `Development`
- [ ] 1.3 Обновить `Label()`, `Valid()`, `Terminal()` для всех. Убрать `StatusHumanHelp`, `StatusPaused`
- [ ] 1.4 `ValidateTransition(from, to Status, entityType EntityType) error` — две FSM (эпики и задачи)
- [ ] 1.5 `TestValidateTransitionFSM` — тесты для Epic + Task
- [ ] 1.6 Удалить `human_help` из всех переходов. Эскалация переводит задачу в `cancelled`
- [ ] **Гейт:** `go build ./...`, `go test ./domain/...`
- [ ] **Важно:** Подпись `ValidateTransition` меняется — обновить ВСЕ вызовы сразу (entity.go, store_task.go, store_epic.go)

### Итерация 2: Board Store — тип сущности
> Подключаем новый FSM к хранилищу.

- [ ] 2.1 `infrastructure/board/entity.go` — алиас `ValidateTransition` принимает `EntityType.Epic` / `.Task`
- [ ] 2.2 `store_task.go` — `SetTaskStatus` передаёт `EntityType.Task`
- [ ] 2.3 `store_epic.go` — `SetEpicStatus` передаёт `EntityType.Epic`
- [ ] 2.4 `tools/board_tools.go` — `BoardSetTaskStatus` передаёт тип
- [ ] 2.5 Тесты: `entity_test.go`, `taskstate_test.go`
- [ ] **Гейт:** `go build ./...`, `go test ./infrastructure/board/...`

### Итерация 3: Удаление лидов — чистка
> Убираем всё, что связано с лидами. Собирается, но оркестрация ещё не работает.

- [ ] 3.1 Удалить `agents/backendlead/`, `agents/frontendlead/`, `agents/devopslead/`
- [ ] 3.2 Убрать `AgentFactory.Lead()` из `agentfactory.go`
- [ ] 3.3 Убрать `Lead()` из `agents/factory/factory.go`
- [ ] 3.4 Убрать `leadFor()`, `leadName()` из `kanban_prompts.go`
- [ ] 3.5 Убрать `phaseLeads`, `nextLeadEpic`, `blockingDependencyEpic` из `kanban_planning.go`
- [ ] 3.6 Убрать `phaseLeads` вызов из `kanban_pipeline.go`
- [ ] 3.7 Удалить тесты: `kanban_lead_test.go`, `kanban_queue_test.go`, `kanban_epicdep_test.go`, `kanban_review_test.go`, `kanban_bugflow_test.go`
- [ ] **Гейт:** `go build ./...` (тесты пока не пройдут — оркестрация не собрана)

### Итерация 4: Business Analyst — новый агент
> Агент для фазы бизнес-ревью.

- [ ] 4.1 Создать `agents/businessanalyst/agent.go`
- [ ] 4.2 Добавить `BusinessAnalyst()` в `AgentFactory`
- [ ] 4.3 Реализовать в `agents/factory/factory.go`
- [ ] 4.4 Промпт: уточнение требований, бизнес-контекст, AC
- [ ] 4.5 `kanban_business_review.go` — `phaseBusinessReview()`
- [ ] 4.6 Тесты: `agents/businessanalyst/agent_test.go`
- [ ] **Гейт:** `go build ./...`, `go test ./agents/businessanalyst/...`

### Итерация 5: Архитектор — декомпозиция задач
> Архитектор теперь создаёт задачи сам.

- [ ] 5.1 Расширить `phaseArchitect` или создать `phaseArchitectDecompose`: архитектор разбивает эпик на задачи
- [ ] 5.2 Промпт архитектора: JSON-схема задач со статусами `infra`, `test_cases`, `development` и `assigned_role`
- [ ] 5.3 Архитектор сам создаёт задачи через `BoardCreateTask` с правильными статусами
- [ ] 5.4 Эпик остаётся в `system_analysis` — перейдёт в `done` автоматически, когда все задачи done
- [ ] **Гейт:** `go build ./...`, `go test ./agents/architect/...`

### Итерация 6: DevOps фаза — `infra → test_cases`
> Первая фаза исполнения задач.

- [ ] 6.1 Создать `phaseInfra` — DevOps берёт задачи со статусом `infra`
- [ ] 6.2 Промпт DevOps: stub-first, проверка build-команд
- [ ] 6.3 Handoff Gate: команды запускаются на пустом коде
- [ ] 6.4 Переход `infra → test_cases` (авто через `BoardSetTaskStatus`)
- [ ] **Гейт:** `go build ./...`, `go test ./application/planner/...`

### Итерация 7: QA фаза — `test_cases → development`
> Test-First: тесты пишутся ДО кода.

- [ ] 7.1 Создать `phaseTestCases` — QA берёт задачи со статусом `test_cases`
- [ ] 7.2 Коренная переработка `agents/qaengineer/agent.go`: тесты ДО кода
- [ ] 7.3 QA пишет e2e-тесты + stub-комментарии `// TODO(dev): ...`
- [ ] 7.4 Handoff: тесты запускаются, падают с понятными сообщениями
- [ ] 7.5 Переход `test_cases → development`
- [ ] 7.6 Убрать `phaseTesting` и `testTask` — больше не нужны
- [ ] **Гейт:** `go build ./...`, `go test ./agents/qaengineer/...`

### Итерация 8: Developer фаза — `development → done`
> Разработчик реализует код по контрактам.

- [ ] 8.1 Создать `phaseDevelopment` — Developer берёт задачи со статусом `development`
- [ ] 8.2 Промпт: реализовать код по контрактам, пройти тесты QA
- [ ] 8.3 Handoff: build + tests зелёные → `done`
- [ ] 8.4 Убрать `phaseExecute` — заменён тремя фазами
- [ ] **Гейт:** `go build ./...`, `go test ./agents/developer/...`

### Итерация 9: Main loop + integration
> Сборка пазла: новый цикл оркестрации.

- [ ] 9.1 Обновить `kanban.go` `Run()` — полная новая последовательность фаз
- [ ] 9.2 `phaseComplete` — автоматически переводит эпик в `done`, когда все задачи done
- [ ] 9.3 Проверка: если задачи не все done — эпик остаётся в `system_analysis`, цикл продолжается
- [ ] 9.4 `server/gitflow.go` — `TaskInfraHook` вместо `TaskInProgressHook`
- [ ] 9.5 `server/actions.go` — обновить маршруты статусов
- [ ] 9.6 `tools/board_handoff.go` — handoff для `infra → test_cases` и `development → done`
- [ ] **Гейт:** `go build ./...`, `go test ./application/planner/...`, `make depgate`

### Итерация 10: CI + полировка
- [ ] 10.1 CI: `autotest.yml`, `sandbox-readiness.yml`, `e2e.yml`
- [ ] 10.2 `docs/20-features/kanban-board.md` — полная переработка
- [ ] 10.3 `AGENTS.md` — обновить флоу
- [ ] 10.4 `docs/agents-history.md` — новая запись
- [ ] 10.5 `gofmt` по изменённым файлам
- [ ] **Гейт:** `make depgate`, `make backend-test`

---

## 6. Риски и смягчение

| Риск | Смягчение |
|---|---|
| **Breaking change** — старые задачи с legacy-статусами | Обратной совместимости нет. Задачи со старыми статусами остаются в базе, но не обрабатываются оркестратором. Пользователь удаляет их через UI (кнопка). Новые статусы — единственный рабочий набор. |
| **Удаление лидов ломает много тестов** | Делается в одной итерации (итерация 3), до сборки оркестрации. Тесты перепишутся в итерациях 5-9. |
| **Нет возвратов назад — ошибка на поздней стадии** | Если задача ушла в development, но тесты QA оказались некорректными — разработчик не может вернуть назад. Решение: задача отменяется (`cancelled`), архитектор создаёт новую задачу с исправлениями. Это дисциплинирует QA писать качественные тесты. |
| **Зацикливание в рамках статуса** | Эскалация при исчерпании бюджета переводит задачу в `cancelled`. Нет `human_help` — задача просто отменяется. |

---

## 7. Артефакты каждого статуса задачи

| Статус | Вход | Выход (артефакты) | Кто |
|---|---|---|---|
| `infra` | Контракт задачи от архитектора | `docker-compose.yml`, `Makefile`, команды build/run/test, работающие на stub'ах | DevOps |
| `test_cases` | Команды DevOps + контракты | e2e-тесты, stub-комментарии `// TODO(dev): ...` | QA Engineer |
| `development` | Тесты QA + stub-комментарии | Рабочий код, зелёная сборка, зелёные тесты | Developer |
| `done` | Код + тесты | Закоммиченные изменения | Developer |

---

## 8. Новая последовательность фаз `Run()`

```go
func (k *KanbanRunner) Run(ctx context.Context) error {
    for range maxKanbanRounds {
        // 1. Архитектор создаёт эпики (если их нет)
        phaseArchitect(ctx)         // draft ← создаёт ассистент

        // 2. Бизнес-аналитик уточняет требования
        phaseBusinessReview(ctx)    // draft → business_review → system_analysis

        // 3. Архитектор разбивает эпики на задачи
        phaseArchitectDecompose(ctx) // создаёт задачи (эпик остаётся в system_analysis)

        // 4-6. Три конвейера исполнения (параллельно по задачам)
        phaseInfra(ctx)             // infra → test_cases
        phaseTestCases(ctx)         // test_cases → development
        phaseDevelopment(ctx)       // development → done

        // 7. Финализация
        phaseComplete(ctx)          // все задачи done → эпик → done (автоматически)

        if allEpicsDone {
            return nil
        }
    }
    return errTruncated
}
```

---

## 9. Старые задачи

Обратной совместимости нет. Задачи с legacy-статусами (`new`, `analysis`, `ready`, `in_progress`, `testing`, `human_help`) остаются в базе, но не обрабатываются оркестратором. Пользователь удаляет их через UI (кнопка «Удалить»). Маппинга нет.
