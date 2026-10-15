# Разработка

Как собирать, проверять и где что лежит в коде.

## Требования

| Что | Версия |
|---|---|
| Go | 1.26.5 (`go.mod`) |
| Node.js | для `web/` |
| Docker | для Redis, Qdrant, песочницы |

## Сборка и проверки

Рабочая тройка (обходит известный блокер в `temp/`):

```bash
go build . ./agents/... ./tools/ ./infrastructure/board/
go vet   . ./agents/... ./tools/ ./infrastructure/board/
go test  . ./agents/... ./tools/ ./infrastructure/board/
```

Расширенный набор (когда нужно проверить всё):

```bash
go build ./agents/... ./tools/ ./infrastructure/board/ ./infrastructure/rag/ ./server/ ./workspace/
go vet   ./agents/... ./tools/ ./infrastructure/board/ ./infrastructure/rag/ ./server/ ./workspace/
go test  ./agents/... ./tools/ ./infrastructure/board/ ./infrastructure/rag/ ./server/ ./workspace/
```

> `go build ./...` падает на `temp/moon-distance/internal/middleware/cors.go`
> — это артефакт сгенерированного агентами проекта, а не исходный код.

Фронтенд:

```bash
npm --prefix web install       # один раз
npm --prefix web run typecheck
npm --prefix web run build      # tsc -b && vite build → web/dist
npm --prefix web run dev        # vite :5173, проксирует /api на :8090
```

> **После изменения фронтенда обязательно `npm --prefix web run build`** —
> иначе бинарь отдаёт старую версию из `web/dist` (встраивается через
> `//go:embed`).

Форматирование:

```bash
gofmt -l <файлы-которые-вы-трогали>   # форматируйте только свои файлы
gofmt -w <файл>
```

> `gofmt -l .` сейчас показывает **29 файлов** с предсуществующими
> отклонениями (часто — просто отсутствующая завершающая строка, иногда —
> настоящие перестановки в тестах). Это не гейт и не ваша регрессия.
> Запускайте `gofmt -l` на своих файлах, иначе в коммит попадёт
> форматирование чужого кода.

## Структура кода

| Папка | Что внутри |
|---|---|
| `main.go` | CLI: разбор команды, выбор агента, таймаут, индекс, приёмка |
| `agents/` | Реализации агентов: архитектор, лиды, специалисты, ревьюер, ассистент, приёмка, планировщик |
| `tools/` | Function-calling инструменты, файловые операции, LSP-обвязка, песочница |
| `runner/` | Агентский цикл: генерация, раунды, хуки, сжатие, автопочинка, переиндексация |
| `infrastructure/llm/` | LLM-провайдеры: Ollama, YandexGPT, Trim, layered, `ModelLimits` |
| `infrastructure/rag/` | RAG: чанки, индекс, поиск, ветки, Qdrant-клиент |
| `infrastructure/board/` | Эпики, задачи, багрепорты, статусы, токены |
| `server/` | Web UI: REST, WebSocket, git-flow, чат, логи, метрики |
| `infrastructure/workspace/` | Реестр проектов, ветки, worktree |
| `infrastructure/git/` | Обёртки над git: клон, ветки, worktree, diff, сабмодули |
| `infrastructure/forges/` | Форджи: GitHub, GitLab, local |
| `shared/langdetect/` | Определение языка диффа |
| `infrastructure/stackdetect/` | Определение стека проекта |
| `infrastructure/checkpoint/` | Чекпоинты плана в Redis |
| `infrastructure/chat/` | История чата в Redis |
| `infrastructure/board/`, `infrastructure/runmetrics/`, `shared/tokens/` | Токены, метрики, цены |
| `shared/projects/` | Конфиг проекта, `ProjectDir` |
| `runctx/`, `infrastructure/runevents/` | Контекст цикла и события |
| `web/` | Фронтенд React + TypeScript |
| `docs/` | Эта документация |
| `temp/` | **Хранилище проектов агентов**, не исходный код |

Полная карта с назначением каждой папки — [Карта проекта](../00-overview/project-map.md).

## Слои зависимостей

```
main.go / server/
      ↓
agents/  runner/            ← оркестрация
      ↓
tools/   infrastructure/rag/   infrastructure/board/      ← инструменты и хранилища
      ↓
infrastructure/llm/  infrastructure/git/  infrastructure/forges/    ← внешний мир
```

Правило: **`tools` не знает про `server`**. Серверные мосты добавляются
через `Set.Add`, потому что объявить их в реестре `tools` нельзя — получился
бы цикл импортов.

## Как добавить фичу

Полный чеклист — [Добавление новой фичи](adding-a-feature.md).

Коротко:

1. Определить слой (инструмент / агент / серверный эндпоинт).
2. Реализовать.
3. Покрыть тестом.
4. Обновить документацию.

## Отладка

```bash
LLM_DEBUG=1 go run . plan <проект> "..."     # лог запросов и ответов LLM
LLM_DEBUG=1 go run . review <URL>            # то же для ревью
tail -f logs/<проект>.log                    # лог проекта
```

| Что | Где смотреть |
|---|---|
| Поведение модели | `LLM_DEBUG=1` |
| Ошибка цикла | `logs/<проект>.log` |
| Панель логов | `panic` приложения, занятый порт |
| Метрики | `GET /api/metrics` |
| Диагностики кода | `LspCheck` |

Подробнее — [Логи и события](../20-features/logging-and-events.md).

## Соглашения

- Язык документации и комментариев — русский.
- Имена переменных окружения — константами рядом с местом чтения.
- Поведение при отсутствии внешнего сервиса — `skipped`, не `error`.
- Ссылка на код в документации — `путь/файл.go:строка`.

## Связанное

- [Тестирование](testing.md)
- [Добавление новой фичи](adding-a-feature.md)
- [Решение проблем](troubleshooting.md)
- [Карта проекта](../00-overview/project-map.md)
