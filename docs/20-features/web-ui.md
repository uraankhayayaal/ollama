# Web UI

`go run . serve` — один процесс на много проектов: Kanban-доска, чат с
живым таймлайном инструментов, диффы, логи приложения и процесса.

Фронтенд — React + TypeScript в `web/`, прод-сборка встраивается в бинарь.

## Настройка

```bash
AI_WEB_ADDR=127.0.0.1:8090    # адрес HTTP-сервера
AI_WEB_PASSWORD=             # пароль входа; пусто — защита выключена
AI_WORKSPACES=               # путь к реестру проектов
LOG_DIR=                     # каталог лог-файлов
```

Позиционный аргумент после `serve` переопределяет `AI_WEB_ADDR`:

```bash
go run . serve 0.0.0.0:8090
```

### Реестр проектов

`AI_WORKSPACES` — путь к JSON-файлу вида «имя → абс. путь, тип
(temp/папка/git)». Если не задан — `$HOME/.ai-workspaces.json`, при ошибке
`UserHomeDir` — `.ai-workspaces.json` в рабочем каталоге
(`workspace/registry.go:100-107`).

Файл хранит и git-метаданные проекта: `git_remote`, `git_branch`,
`git_base`, `git_target`, `parent`, `git_branches`, `git_resolving`,
`git_merge_requests` (`workspace/registry.go:36-60`).

### Защита

Пустой `AI_WEB_PASSWORD` — вход без пароля. Непустой включается
(`server/auth.go`):

- httpOnly-сессия `ai_sid` с TTL **24 часа**.
- CSRF-токен в заголовке `X-CSRF-Token` на все мутации.
- Per-IP rate-limit.

**Лимиты заданы константами, а не переменными окружения**
(`server/server.go:116-118`):

| Что | Лимит |
|---|---|
| `/api/login` | 5 попыток в минуту |
| Все `/api/*` | 120 запросов в минуту |
| Сообщения в чат | 30 в минуту |

## Сборка фронтенда

```bash
npm --prefix web install
npm --prefix web run build      # tsc -b && vite build → web/dist
npm --prefix web run dev        # vite :5173, проксирует /api и /events на :8090
npm --prefix web run typecheck
```

Прод-сборка встраивается через `//go:embed all:dist` (`web/webui.go:10-20`)
и отдаётся `http.FileServer` (`server/static.go:9-12`) с SPA-fallback.

> **После изменения кода фронтенда нужно пересобрать `web/dist`**, иначе
> бинарь продолжит отдавать старую версию.

## Интерфейс

| Панель | Что показывает |
|---|---|
| **Доска** | Эпики и задачи (drag-and-drop, статусы, approve/reject), баги, токены, git-блок, модалки эпика/задачи/бага |
| **Чат** | История, live-таймлайн вызовов инструментов, потоковый ответ модели, карточка вопроса `AskUser` |
| **Дифф** | Изменения ветки: список файлов + патч по запросу |
| **Логи** | Хвост лога приложения и лога процесса |
| **Баннер** | Активный gate (запрос решения) |

## WebSocket

Подключение: `GET /api/projects/{id}/ws`. Хаб рассылает всем клиентам
проекта события `{type, payload}`; буфер отправки — 64 кадра, при
переполнении клиент отключается (`server/hub.go:63-87`).

| Тип | Payload |
|---|---|
| `chat` | Новое сообщение чата |
| `chat_delta` | Потоковый фрагмент ответа модели |
| `tool` | Событие вызова инструмента |
| `board` | Снимок доски (+ git) |
| `gate` | Запрос решения пользователя |
| `status` | Статус сессии |
| `log` | Строка лога процесса |
| `applog` | Строка лога приложения |
| `tokens` | Расход токенов |
| `chat_clear` | История очищена |

Клиент переподключается с растущей задержкой
(300/800/1500/3000/5000 мс) и перезапрашивает снапшоты
`board`/`status`/`gate` (`web/src/live.ts:22,32-44`).

При подключении сервер шлёт `status`, активный `gate`, `board` и `tokens`
(`session.go:657-685`). Во время оркестрации доска публикуется раз в 500 мс
(`boardFlusher`).

> **Нюанс:** тип `diff` объявлен во фронтовом union (`web/src/live.ts:36-40`),
> но бэкенд его не публикует — диффы доступны только через REST
> `GET /api/projects/{id}/diff`.

## Статусы сессии

`idle` · `running` · `waiting` · `done` · `in_progress` · `stopped` ·
`error` · `standby`.

`standby` означает «нет работы на доске — жду эпики и задачи»
(`server/server.go:312`).

## Диффы

`server/diff.go` — ленивая разборка `git diff` на
`{path, status, added, deleted}`. Статусы: `added`, `modified`, `removed`,
`renamed`, `submodule` (`:23-29`). Патч одного файла запрашивается отдельно.

Кэш `cachedDiff` с ключом `project|ref|base` (`:41-47,107`), инвалидация —
`invalidateDiffs` (`:93`).

## Команды в UI

| Действие | Что делает |
|---|---|
| Добавить проект | Регистрирует путь в реестре |
| Написать задачу в чат | Ассистент создаёт эпик, запускается оркестрация |
| «Индекс RAG» | Фоновая индексация проекта |
| Принять → MR | Создаёт Merge Request на GitHub/GitLab по remote |
| Залить в main | Релиз эпика |
| Остановить | `POST .../session/stop` |

## Метрики

```http
GET /api/metrics                          # Prometheus (text 0.0.4)
GET /api/projects/{id}/metrics            # метрики проекта
POST /api/projects/{id}/metrics/reset
```

Экспортируются `run_tokens_total`, `run_rounds_total`, `run_tool_calls_total`
и гистограммы (`runmetrics/prom.go`).

## REST

Полный список маршрутов — [REST API и WebSocket](../30-reference/rest-api.md).

## Структура фронтенда

```
web/src/
├── main.tsx, App.tsx, live.ts, download.ts, styles.scss
├── Api/Api.ts        — 40+ методов, CSRF-заголовок
├── Types/Types.ts    — типы данных доски
└── Components/
    ├── Dashboard/    — доска, board.ts, dnd.ts, tokens.ts,
    │                   EpicRow, EpicModal, TaskCard, TaskModal, Cell,
    │                   GitBlock, BugCard, BugModal, EpicActionBar, Modal
    ├── Chatboard/    — чат, AskCard
    ├── Diffboard/    — sidebyside.ts
    ├── Logboard, GateBanner, Login, RunButton, Runtimes,
    └── Tabs, TokensCounter, ToolBar, WorkspacePicker, Badge
```

## Связанное

- [Kanban-доска](kanban-board.md) — что отображается
- [Чат-ассистент](chat-assistant.md) — что делает ассистент
- [Git-flow](gitops-workflow.md) — операции с ветками и MR
- [REST API и WebSocket](../30-reference/rest-api.md)
- [Токены и метрики](tokens-and-metrics.md)
