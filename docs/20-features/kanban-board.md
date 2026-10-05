# Kanban-доска

Общая доска проекта в Redis. Системный архитектор публикует эпики, лиды
разлагают их в задачи, специалисты выполняют и закрывают. Приёмка и QA
регистрируют багрепорты.

Пакет `board/`, модель данных — `board/entity.go`, хранилище — `board/store.go`.

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
| `assigned_role` | string | Владелец направления |
| `sequence_order` | FlexInt | Порядок (терпит `"1"` из JSON) |
| `can_run_parallel` | FlexBool | Можно ли распараллелить (терпит `"true"`) |
| `dependencies` | []string | Зависимости от других эпиков |
| `contracts` | []Contract | Контракты (опционально) |
| `tasks` | []string | ID подзадач |
| `project_name`, `repositories` | string, []string | Проект, мульти-репо |
| `status` | Status | Статус |
| `created_at`, `updated_at` | time | Метки времени |
| `git_branch` | string | Релизная ветка |
| `merge_conflict_files` | []string | **Конфликты мёрджа** |
| `merged_into_main` | bool | Флаг «влито в main» |
| `architecture_summary` | string | Сводка архитектуры |
| `revision`, `lead_synced_rev` | int | Ревизии для синхронизации с лидом |
| `requires_review` | bool | **Черновик**, ждёт ревизии архитектора |
| `TokenUsage` | struct | Учёт токенов |

`FlexInt`/`FlexBool` (`board/entity.go:439-476`) принимают JSON-строку вместо
числа/булева — модели часто присылают `"1"`/`"true"`.

> **Важно про `requires_review`:** при разборе JSON **отсутствующее поле
> трактуется как `true`** (`board/entity.go:290-306`). Это защитный дефолт:
> старые записи и эпики из чата требуют ревизии архитектора. Явно снимается
> либо `submit_architecture_backlog`, либо `BoardUpdateEpic`.

### Задача (`Task`)

Атомарная единица работы одного специалиста. Соответствует фича-ветке
`ai/task/<id>`.

Отличия от эпика: `epic_id`, `assignee`, `resume_status` (из какого статуса
снята на паузе). Остальные поля те же, включая `merge_conflict_files` и
`TokenUsage`.

У задачи есть `injections` — промпт-инъекции этой задачи. Они принадлежат ровно
одной задаче и применяются к промпту её исполнителя со следующего обращения к
модели, в том числе если цикл уже идёт (агентский цикл перечитывает задачу перед
каждым запросом). Задаются через
`POST /api/projects/{id}/tasks/{tid}/injections`, полем `injections` в
`PUT .../tasks/{tid}` или инструментом доски `BoardUpdateTask` — то есть указание
можно дать прямо из чата. Формат записи и условия — [Промпт-инъекции](chat-assistant.md#промпт-инъекции-injections).

### Багрепорт (`BugReport`)

`bug_id`, `project_name`, `title`, `description`, `reporter_role`,
`task_id`, `epic_id`, `status`, `verdict`, `fix_epic_id`, `created_at`,
`updated_at`.

### Бэклог (`Backlog`)

`architecture_summary` + `tasks[]` + `opportunities[]`.
`Opportunity{target_role, suggestion}` — кросс-функциональная рекомендация
(«эта задача создаёт работу для смежной роли»), `board/entity.go:149-162`.

## Статусы и переходы

### Задачи и эпики (`board/entity.go:26-118`)

```
new ──────► analysis ──────► ready ──────► in_progress ──────► done
 │              │              │               │                  ▲
 │              │              │               └──► paused ──────┘
 ▼              ▼              ▼                   (resume)
cancelled    cancelled      cancelled              │
 ▲              ▲              ▲                   ▼
 └──────────────┴──────────────┴─────────────── (cancelled)
```

Точные разрешённые переходы:

| Из | В |
|---|---|
| `new` | `analysis`, `paused`, `cancelled` |
| `analysis` | `ready`, `paused`, `cancelled` |
| `ready` | `in_progress`, `paused`, `cancelled` |
| `in_progress` | `done`, `paused`, `cancelled` |
| `paused` | `ready` (возобновление), `cancelled` |

- Терминальны только `done` и `cancelled` (`entity.go:63-65`).
- Переход в тот же статус — идемпотентный «переход», не ошибка.
- Неизвестный статус → `StatusError`.
- `paused` **замораживает** ревизию: `phaseArchitectReview` пропускает
  эпики на паузе (`agents/planner/kanban.go:710`).

### Багрепорты (`board/entity.go:313-394`)

```
new ──► confirmed ──► fix ──► fixed
 │           │
 │           └──► slop · feature · wont_fix
 └──► slop
```

Терминальны: `slop`, `feature`, `wont_fix`, `fixed`.

## Хуки

`board/store.go:53-83` — колбэки, вызываемые **после** сохранения, вне
блокировок. Ошибка git-шага не ломает переход статуса.

| Хук | Что делает сервер |
|---|---|
| `EpicCreatedHook` | `autoCreateEpicBranch` — создать релизную ветку от `git_base` |
| `TaskCreatedHook` | `autoCreateTaskBranch` — создать фича-ветку от ветки эпика |
| `TaskInProgressHook` | `taskWorktree` — подготовить worktree |
| `TaskDoneHook` | `autoCommitAndMergeTask` — коммит → авто-MR → merge |
| `EpicDoneHook` | `syncEpicWithMain` — фоновая синхронизация с main |

Устанавливаются в `server/gitflow.go:485-517`. Подробнее —
[Git-flow](gitops-workflow.md).

## Инструменты

17 инструментов (`tools/board_tools.go:20-48`):

| Группа | Инструменты |
|---|---|
| Чтение | `BoardListEpics`, `BoardGetEpic`, `BoardListTasks`, `BoardGetTask`, `BoardListBugs`, `BoardGetBug` |
| Эпики | `BoardCreateEpic`, `BoardUpdateEpic`, `BoardDeleteEpic`, `BoardSetEpicStatus` |
| Задачи | `BoardCreateTask`, `BoardUpdateTask`, `BoardDeleteTask`, `BoardSetTaskStatus` |
| Баги | `BoardCreateBugReport`, `BoardSetBugStatus`, `BoardReviewBugReport` |

> **Нюанс именования:** Go-константы `BoardCreateBug` и `BoardReviewBug`
> имеют строковые значения `BoardCreateBugReport` и `BoardReviewBugReport`
> (`tools/board_tools.go:35,37`). Поиск по константам покажет «несуществующие»
> инструменты — ищите по строковым значениям.

`BoardCreateEpic` **всегда** ставит `requires_review: true`
(`tools/board_tools.go:411`); `BoardUpdateEpic` умеет его снимать
(`:503`).

## Токены

`TokenUsage` встроен в Epic и Task (`board/entity.go:173-201`), дублирует
данные из `tokens/scoped.go`:

```json
{"tokens_in": 0, "tokens_out": 0, "tokens_total": 0, "token_estimate": 0}
```

Прогноз хранится отдельно от факта, чтобы UI мог показать отклонение.
Подробнее — [Токены и метрики](tokens-and-metrics.md).

## Соглашения

- JSON-ключи — `snake_case`.
- `assigned_role` не выдумывается: если роль неизвестна, запись считается
  грязной и пропускается.
- `description` эпика из чата — черновик: требует ревизии архитектора.
- Статусы меняются только через `Set*Status` — прямая запись поля `status`
  в `Update*` не является путём смены состояния.

## Связанное

- [Архитектор](architect.md) — кто создаёт эпики
- [Агенты и роли](agents-and-roles.md) — права по ролям
- [Git-flow](gitops-workflow.md) — хуки и ветки
- [Web UI](web-ui.md) — визуализация доски
- [Инфраструктура](../30-reference/infrastructure.md) — Redis и хранение
