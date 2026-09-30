# REST API и WebSocket

Базовый адрес: `http://<AI_WEB_ADDR>` (по умолчанию `127.0.0.1:8090`).

## Аутентификация

```http
POST /api/login
Content-Type: application/json

{"password": "..."}
```

При `AI_WEB_PASSWORD` пустом вход не требуется.

| Проверка | Значение |
|---|---|
| Сессия | httpOnly-cookie `ai_sid`, TTL 24 часа |
| CSRF | заголовок `X-CSRF-Token` на все мутации |
| Rate-limit `/api/login` | 5 попыток в минуту |
| Rate-limit `/api/*` | 120 запросов в минуту |
| Rate-limit сообщений чата | 30 в минуту |

Лимиты — **константы** в `server/server.go:116-118`, не переменные окружения.

```http
GET  /api/auth          → {"protected": true, "authenticated": false}
POST /api/logout
```

## Модель и провайдеры

Выбор модели — общее для всего процесса (не на проект), поэтому эндпоинты не
вложены в `/api/projects/{id}`.

| Метод | Путь | Что делает |
|---|---|---|
| `GET` | `/api/providers` | Провайдеры из `providers.json` + текущий выбор |
| `POST` | `/api/providers/select` | Сменить провайдера/модели (или сбросить к окружению) |

`GET /api/providers`:

```json
{
  "providers": [
    {"name": "ollama", "models": ["qwen3-coder:30b", "llama3.2"], "default_model": "qwen3-coder:30b", "large_model": ""}
  ],
  "current_provider": "ollama",
  "current_model": "qwen3-coder:30b",
  "current_large_model": "",
  "override": false,
  "error": ""
}
```

- провайдеры отсортированы по имени (детерминированный порядок в UI);
- `current_*` — фактически работающие модели после применения дефолтов
  `providers.json`, а не только запрошенные;
- `override: false` — выбор из `LLM_PROVIDER`/`MODEL`/`MODEL_LARGE`,
  `true` — сделан через `POST` (в UI появляется кнопка «сброс»);
- `error` непусто, если текущий выбор не удалось создать (провайдер недоступен).

`POST /api/providers/select`:

```http
POST /api/providers/select
Content-Type: application/json

{"provider": "ollama", "model": "qwen3-coder:30b", "large_model": "qwen3.6:35b-a3b"}
```

| Поле | Обяз. | Смысл |
|---|---|---|
| `provider` | да* | Имя провайдера из `providers.json` |
| `model` | да* | Модель из `models`/`default_model`/`large_model` этого провайдера |
| `large_model` | нет | Крупная модель для тяжёлых агентов; пусто — `large_model` из конфига |
| `reset` | нет | `true` — вернуть выбор из переменных окружения (поля `*` игнорируются) |

Ответ `200`:

```json
{"ok": true, "provider": "ollama", "model": "qwen3-coder:30b", "large_model": "qwen3.6:35b-a3b",
 "describe": "ollama/qwen3-coder:30b (large=qwen3.6:35b-a3b)", "applies_to_running": false}
```

Провайдер создаётся сразу, поэтому битый `base_url` или пустая модель дают
`400`/`502` с текстом причины, а не тихо ломают первый запрос к модели.
`applies_to_running: true` — оркестрация уже идёт и доработает на прежней
модели; в ответе есть `message` с тем же предупреждением.

Права: `POST` — обычная мутация под middleware `authHandler` (сессия + CSRF,
если задан `AI_WEB_PASSWORD`).

## Проекты

| Метод | Путь | Что делает |
|---|---|---|
| `GET` | `/api/projects` | Список проектов реестра |
| `POST` | `/api/projects` | Открыть проект (путь, тип, git-метаданные) |
| `GET` | `/api/projects/{id}` | Снимок доски + git-блок |

Типы проектов: `temp` (папка в `temp/`), произвольная папка, `git`-репозиторий.

## Чат

| Метод | Путь | Что делает |
|---|---|---|
| `POST` | `/api/projects/{id}/chat` | Сообщение пользователя → ассистент |
| `GET` | `/api/projects/{id}/chat` | История диалога |
| `DELETE` | `/api/projects/{id}/chat` | Очистить историю |
| `POST` | `/api/projects/{id}/continue` | Продолжить работу агентов |

Ответ `POST /chat` — `role=user` в истории; работа ассистента идёт
асинхронно, прогресс приходит в WebSocket.

## Сессия и гейты

| Метод | Путь | Что делает |
|---|---|---|
| `POST` | `/api/projects/{id}/session/stop` | Остановить оркестрацию |
| `POST` | `/api/projects/{id}/{gate}/decide` | Решение по активному гейту |
| `POST` | `/api/projects/{id}/ask/{askID}/answer` | Ответ на вопрос `AskUser` |
| `POST` | `/api/projects/{id}/accept` | Принять проект (HITL) |
| `POST` | `/api/projects/{id}/reject-branch` | Отклонить фича-ветку |

`{gate}` — подстановка в путь: активный гейт определяется сервером.

Таймаут ожидания ответа на `AskUser` — **15 минут** (`server/ask.go:115`),
затем `context.DeadlineExceeded`.

## Задачи и эпики

| Метод | Путь | Что делает |
|---|---|---|
| `PUT` | `/api/projects/{id}/tasks/{tid}` | Обновить задачу |
| `DELETE` | `/api/projects/{id}/tasks/{tid}` | Удалить задачу |
| `DELETE` | `/api/projects/{id}/epics/{eid}` | Удалить эпик |
| `POST` | `/api/projects/{id}/epics/{eid}/status` | Сменить статус эпика |
| `GET` | `/api/projects/{id}/bugs` | Список багрепортов |

## Git-flow

| Метод | Путь | Что делает |
|---|---|---|
| `POST` | `/api/projects/{id}/epics/{eid}/branch` | Создать релизную ветку |
| `POST` | `/api/projects/{id}/tasks/{tid}/branch` | Создать фича-ветку |
| `POST` | `/api/projects/{id}/tasks/{tid}/merge` | Влить задачу в релиз |
| `POST` | `/api/projects/{id}/epics/{eid}/release` | Выпустить эпик в main |
| `POST` | `/api/projects/{id}/epics/{eid}/rebase` | Rebase эпика на main |
| `POST` | `/api/projects/{id}/epics/{eid}/resolve` | Снять признак конфликта |
| `GET` | `/api/projects/{id}/epics/{eid}/resolve` | Статус резолва |
| `POST` | `/api/projects/{id}/tasks/{tid}/resolve` | LLM-авторезолв задачи |
| `POST` | `/api/projects/{id}/epics/{eid}/auto-resolve` | LLM-авторезолв эпика |
| `POST` | `/api/projects/{id}/epics/{eid}/mr` | Создать MR эпика |
| `POST` | `/api/projects/{id}/tasks/{tid}/mr` | Создать MR задачи |

### Конфликты мёрджа

`POST .../tasks/{tid}/merge` при конфликте:

- пишет `task.MergeConflictFiles`;
- **не меняет статус**;
- возвращает детерминированную строку со списком файлов и точкой резолва.

Ответ содержит поле `merge_conflict_files`.

`GET .../epics/{eid}/resolve` — статус LLM-авторезолва: до 3 попыток
(`resolveMaxAttempts = 3`, `server/gitflow_ar.go:55`).

## Диффы

```http
GET /api/projects/{id}/diff?ref=<ветка>&vs=<base>
GET /api/projects/{id}/diff?ref=<ветка>&vs=<base>&file=<путь>
```

| Параметр | Обяз. | Смысл |
|---|---|---|
| `ref` | нет | Зарегистрированная ветка (в т.ч. worktree) |
| `vs` | нет | Точка отхода; пусто = `git_base` |
| `file` | нет | Патч одного файла |

Коды: `400` — `vs` ≠ `git_base` или `ref` не зарегистрирован; `404` — файл
не найден в диффе; `200` — сводка или патч.

Подробнее — [Контекстные диффы](../20-features/contextual-diff.md).

## RAG, токены, метрики, логи

| Метод | Путь | Что делает |
|---|---|---|
| `POST` | `/api/projects/{id}/index?branch=` | Фоновая индексация |
| `GET` | `/api/projects/{id}/tokens` | Расход токенов |
| `GET` | `/api/projects/{id}/metrics` | Снимок метрик (JSON) |
| `POST` | `/api/projects/{id}/metrics/reset` | Сброс метрик |
| `GET` | `/api/metrics` | Prometheus, `text 0.0.4` |
| `GET` | `/api/projects/{id}/applog?lines=300` | Логи рантайма |
| `GET` | `/api/projects/{id}/logs` | Файловые логи процесса |

`POST /index` коды: `200` — запущено; `409` — индексация уже идёт;
`503` — RAG недоступен.

## WebSocket

```http
GET /api/projects/{id}/ws
```

| Тип | Payload | Когда |
|---|---|---|
| `chat` | Сообщение | Новое сообщение чата |
| `chat_delta` | Фрагмент | Потоковый ответ модели |
| `chat_clear` | — | История очищена |
| `tool` | Событие инструмента | Вызов инструмента |
| `board` | Снимок доски | Изменение доски |
| `gate` | Запрос решения | Активный гейт |
| `status` | Статус сессии | Смена статуса |
| `log` | Строка | Строка лога процесса |
| `applog` | Строка | Строка лога рантайма |
| `tokens` | Расход | Обновление расхода |

При подключении сервер сразу шлёт `status`, активный `gate`, `board` и
`tokens` (`server/session.go:657-685`).

> Тип `diff` объявлен во фронтовом union (`web/src/live.ts:36-40`), но
> **не публикуется бэкендом**: диффы только через REST.

## Коды ответов

| Код | Значение |
|---|---|
| `200` | Успех |
| `400` | Некорректные параметры (например, `vs` ≠ `git_base`) |
| `401` | Не авторизован / неверный CSRF |
| `404` | Проект / файл не найден |
| `409` | Конфликт состояния (индексация идёт, вопрос уже активен) |
| `429` | Превышен rate-limit |
| `502` | Провайдер LLM не собрался (например, битый `base_url`) |
| `503` | Внешний сервис недоступен (RAG) |

## Связанное

- [Web UI](../20-features/web-ui.md)
- [Провайдеры и выбор модели](../10-getting-started/providers.md)
- [Контекстные диффы](../20-features/contextual-diff.md)
- [Git-flow](../20-features/gitops-workflow.md)
- [Токены и метрики](../20-features/tokens-and-metrics.md)
- [Логи и события](../20-features/logging-and-events.md)
