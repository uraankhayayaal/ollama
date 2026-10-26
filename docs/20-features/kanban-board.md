# Kanban-доска

Общая доска проекта в Redis. Системный архитектор публикует эпики,
бизнес-аналитик уточняет требования, архитектор разлагает эпики в задачи,
специалисты (DevOps, QA Engineer, Developer) выполняют и закрывают.

Пакет `infrastructure/board/`, модель данных — `infrastructure/board/entity.go`, хранилище — `infrastructure/board/store.go`.

## Настройка

```bash
BOARD_REDIS_ADDR=localhost:56379
BOARD_REDIS_PASSWORD=
BOARD_REDIS_DB=0
BOARD_TTL=0            # 0 = без истечения; например 24h
```

Значения по умолчанию читаются в `agents/architect/config.go:22-31` —
это единственный конструктор конфига доски, поэтому значения одинаковы для
всех агентов.

> **Без Redis доска не работает**, но система не падает: команда `kanban`
> не запускается, а standalone-команды (`backend`, `plan`) продолжают
> работать — инструменты `Board*` просто не выдаются агентам
> (`tools/registry.go:34-36`).

## Сущности

### Эпик (`Epic`)

Крупная единица работы верхнего уровня. Соответствует релизной ветке
`ai/epic/<id>`.

| Поле | Тип | Назначение |
|---|---|---|
| `task_id` | string | Идентификатор |
| `title`, `description` | string | Заголовок, описание |
| `assigned_role` | string | Направление (опционально) |
| `sequence_order` | FlexInt | Порядок (терпит `"1"` из JSON) |
| `can_run_parallel` | FlexBool | Можно ли распараллелить (терпит `"true"`) |
| `dependencies` | []string | Зависимости от других эпиков |
| `contracts` | []Contract | Контракты (опционально) |
| `tasks` | []string | ID подзадач |
| `project_name`, `repositories` | string, []string | Проект, мульти-репо |
| `status` | Status | Статус (epic — `EntityTypeEpic`) |
| `created_at`, `updated_at` | time | Метки времени |
| `git_branch` | string | Релизная ветка |
| `merge_conflict_files` | []string | **Конфликты мёрджа** |
| `merged_into_main` | bool | Флаг «влито в main» |
| `architecture_summary` | string | Сводка архитектуры |
| `revision` | int | Ревизии контента |
| `requires_review` | bool | **Черновик**, ждёт бизнес-ревью |
| `priority` | int | **Приоритет** (пересортировка строк доски): чем больше — тем раньше в очереди |
| `TokenUsage` | struct | Учёт токенов |

`FlexInt`/`FlexBool` (`infrastructure/board/entity.go`) принимают JSON-строку вместо
числа/булева — модели часто присылают `"1"`/`"true"`.

> **Важно про `requires_review`:** при разборе JSON **отсутствующее поле
> трактуется как `true`**. Это защитный дефолт: старые записи и эпики из чата
> требуют бизнес-ревью. Явно снимается бизнес-аналитиком или
> `BoardUpdateEpic`.

### Задача (`Task`)

Атомарная единица работы одного специалиста. Соответствует фича-ветке
`ai/task/<id>`.

Отличия от эпика: `epic_id`, `assignee`, `assigned_role`, `dependencies`
(зависимости от других задач/эпиков), `type` (entity type для FSM).
Остальные поля те же, включая `merge_conflict_files` и `TokenUsage`.

У задачи есть `injections` — промпт-инъекции этой задачи. Они принадлежат ровно
одной задаче и применяются к промпту её исполнителя со следующего обращения к
модели, в том числе если цикл уже идёт (агентский цикл перечитывает задачу перед
каждым запросом). Задаются через
`POST /api/projects/{id}/tasks/{tid}/injections`, полем `injections` в
`PUT .../tasks/{tid}` или инструментом доски `BoardUpdateTask` — то есть указание
можно дать прямо из чата. Формат записи и условия — [Промпт-инъекции](chat-assistant.md#промпт-инъекции-injections).

### Комментарии (`Comments`)

Задача и эпик хранят массив комментариев — уточнения, замечания, результаты
проверок. Каждый комментарий содержит `author`, `type` (`user`, `qa`,
`system`), `body` и `created_at`. Пишутся через `BoardAddComment`,
чтаются через `BoardGetTask`/`BoardGetEpic`, обновляются через
`BoardUpdateTask`/`BoardUpdateEpic` (список заменяется целиком).

## Статусы и переходы

### Эпики (`domain/status.go`)

Финальный автомат эпиков — строго линейный, без возвратов:

```
draft ──► business_review ──► system_analysis ──► done
 │            │                    │
 └────────────┴────────────────────┴──► cancelled
```

Точные разрешённые переходы:

| Из | В |
|---|---|
| `draft` | `business_review`, `cancelled` |
| `business_review` | `system_analysis`, `draft` |
| `system_analysis` | `done`, `cancelled` |
| `done` | `done` |
| `cancelled` | `cancelled` |

### Задачи (`domain/status.go`)

Финальный автомат задач — строго линейный, без возвратов:

```
draft ──► infra ──► test_cases ──► development ──► done
 │          │          │               │
 └──────────┴──────────┴───────────────┴──► cancelled
```

Точные разрешённые переходы:

| Из | В |
|---|---|
| `draft` | `infra`, `cancelled` |
| `infra` | `test_cases`, `cancelled` |
| `test_cases` | `development`, `cancelled` |
| `development` | `done`, `cancelled` |
| `done` | `done` |
| `cancelled` | `cancelled` |

- Тип сущности (`EntityTypeEpic` / `EntityTypeTask`) определяет, какие
  переходы разрешены — один и тот же статус может иметь разные переходы
  для эпика и задачи (`domain/status.go`).
- Переход в тот же статус — идемпотентный «переход», не ошибка.
- Неизвестный статус → `StatusError`.
- Нет лидов, нет багрепортов, нет обратных переходов.
- `human_help` — исключительный статус: специалист сам переводит задачу,
  если выполнить её невозможно (нет кода/зависимостей, блокер). Причина
  оседает в `pause_reason`. Исчерпание бюджета автономии
  (`KANBAN_MAX_ESCALATIONS`) тоже ведёт в `human_help`.
- **Handoff Gate** (gate before `infra → test_cases` и `development → done`):
  проверяет чистоту дерева (`git status --short`), сборку (`go build ./...`)
  и ветку задачи (не main). `INFRA_DEGRADED` (нет go, нет песочницы) не
  блокирует. (`tools/board_handoff.go`).
- **Garage Guard** (guard on `development → done`): задача не может стать
  `done`, если у неё нет коммитов (пустая ветка, чистый worktree).
  (`infrastructure/board/done_guard.go`).

## Хуки

`infrastructure/board/store.go` — колбэки, вызываемые **после** сохранения, вне
блокировок. Ошибка git-шага не ломает переход статуса.

| Хук | Что делает сервер |
|---|---|
| `EpicCreatedHook` | `autoCreateEpicBranch` — создать релизную ветку от `git_base` |
| `TaskCreatedHook` | `autoCreateTaskBranch` — создать фича-ветку от ветки эпика |
| `TaskInfraHook` | `taskWorktree` — sync эпика с main + rebase задачи + создать worktree |
| `TaskDoneHook` | `autoCommitAndMergeTask` — коммит → авто-MR → merge |
| `EpicDoneHook` | `syncEpicWithMain` — фоновая синхронизация с main |

Устанавливаются в `server/gitflow.go`. Подробнее —
[Git-flow](gitops-workflow.md).

## Инструменты

13 инструментов (`tools/board_tools.go`):

| Группа | Инструменты |
|---|---|
| Чтение | `BoardListEpics`, `BoardGetEpic`, `BoardListTasks`, `BoardGetTask` |
| Эпики | `BoardCreateEpic`, `BoardUpdateEpic`, `BoardDeleteEpic`, `BoardSetEpicStatus` |
| Задачи | `BoardCreateTask`, `BoardUpdateTask`, `BoardDeleteTask`, `BoardSetTaskStatus` |
| Комментарии | `BoardAddComment` |

`BoardCreateEpic` **всегда** ставит `requires_review: true` — эпики из чата
требуют бизнес-ревью. `BoardUpdateEpic` умеет его снимать.

`BoardSetTaskStatus` при переходе `infra → test_cases` и `development → done`
запускает **Handoff Gate**: проверяет чистоту дерева, сборку и ветку задачи.
При `development → done` дополнительно проверяется **Done Guard** (коммиты не
пустые).

## Токены

`TokenUsage` встроен в Epic и Task (`infrastructure/board/entity.go:173-201`), дублирует
данные из `shared/tokens/scoped.go`:

```json
{"tokens_in": 0, "tokens_out": 0, "tokens_total": 0, "token_estimate": 0}
```

Прогноз хранится отдельно от факта, чтобы UI мог показать отклонение.
Подробнее — [Токены и метрики](tokens-and-metrics.md).

## Пересортировка эпиков (приоритет)

Порядок строк доски = приоритет эпиков в очереди оркестратора. Единый
компаратор `board.EpicLess` (`infrastructure/board/order.go`) использует и снимок
доски (`boardView` сортирует строки), и оркестратор (`phaseArchitect`,
`blockingDependencyEpic`), поэтому «что видно» = «что берут в работу»:

1. **Фаза** — инфраструктура (DevOps) → приложение (backend/frontend) →
   тестирование, QA Last (`epicPhaseRank`). Фаза первична: строка с большим
   приоритетом, но позже фазой, не может обогнать предыдущую фазу;
2. **`priority`** по убыванию — задаётся пересортировкой строк (Web UI);
3. `sequence_order` (порядок от архитектора);
4. `task_id` — тайбрейкер для детерминизма.

Ручная пересортировка — `POST /api/projects/<id>/epics/reorder`
(`handleReorderEpics`, `server/server.go`), тело `{"order": ["EPIC-1", …]}`,
первый ID получает наибольший приоритет (`Store.ReorderEpics`,
`infrastructure/board/store.go`). Нюансы:

- ревизию эпика **не** увеличивает (приоритет — не содержание);
- неизвестный ID — 404 (частичный результат уже применён: клиент получит
  ошибку и перечитает доску);
- пропущенные эпики сохраняют прежний приоритет;
- при активной оркестрации сервер **мягко рестартует** её (стоп → старт,
  `Session.Restart`), чтобы новая очередь применилась немедленно; ответ
  `{ok, updated, restarting}` возвращается сразу, статус рестарта виден по
  WS-событиям `status` (`stopped` → `running`).

В Web UI перетаскивайте строки за «⠿» или нажимайте стрелки ↑/↓ в заголовке
эпика. Перенос между фазами игнорируется и доска возвращается в эффективный
порядок.

## Соглашения

- JSON-ключи — `snake_case`.
- `assigned_role` не выдумывается: если роль неизвестна, запись считается
  грязной и пропускается.
- `description` эпика из чата — черновик: требует бизнес-ревью.
- Статусы меняются только через `Set*Status` — прямая запись поля `status`
  в `Update*` не является путём смены состояния.
- Нет лидов, нет багрепортов, нет обратных переходов.
- Нет обратной совместимости с legacy-статусами (`new`, `analysis`,
  `ready`, `in_progress`, `testing`, `human_help`).

## Связанное

- [Архитектор](architect.md) — кто создаёт эпики
- [Бизнес-аналитик](business-analyst.md) — кто уточняет требования
- [Агенты и роли](agents-and-roles.md) — права по ролям
- [Git-flow](gitops-workflow.md) — хуки и ветки
- [Web UI](web-ui.md) — визуализация доски
- [Инфраструктура](../30-reference/infrastructure.md) — Redis и хранение
