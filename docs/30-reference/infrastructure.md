# Инфраструктура

Порты, хранилища и файлы на диске.

## Порты

| Порт | Сервис | Протокол | Переменная |
|---|---|---|---|
| `56334` | Qdrant | **gRPC** | `QDRANT_ADDR` |
| `6333` | Qdrant | HTTP / Web UI | — |
| `56379` | Redis | TCP | `REDIS_ADDR`, `BOARD_REDIS_ADDR` |
| `8090` | Web UI | HTTP | `AI_WEB_ADDR` (дефолт `127.0.0.1:8090`) |
| `11434` | Ollama | HTTP | `OLLAMA_HOST` (внешний, не в compose) |

> **Qdrant используется только по gRPC.** HTTP-порт медленнее в разы, поэтому
> приложение обращается к `56334`, а `6333` оставлен для веб-интерфейса
> Qdrant.

## `compose.yaml`

Поднимает **ровно два сервиса**:

| Сервис | Образ | Порты (хост → контейнер) | Volumes |
|---|---|---|---|
| `qdrant` | `qdrant/qdrant:latest` | `6333:6333`, `56334:6334` | `qdrant:/qdrant/storage` |
| `redis` | `redis:7-alpine` | `56379:6379` | `redis:/data` |

Особенности:

- `container_name`: `qdrant_rag`, `ollama-redis`.
- `restart`: `always` / `unless-stopped`.
- Redis запускается с `--appendonly yes` — durability чекпоинтов планировщика.

> **Сервиса `ollama` в compose нет.** Ollama ставится на хост отдельно
> (`brew install ollama` / установщик). Не пишите `docker compose exec -it
> ollama …` — такого сервиса не существует.

```bash
docker compose up -d
docker compose ps
docker compose down        # остановить
docker compose down -v     # остановить и удалить volumes
```

## Проверка

```bash
redis-cli -p 56379 ping                              # PONG
docker compose exec qdrant sh -c 'ls /qdrant/storage'  # данные Qdrant
curl -s localhost:56334                              # Qdrant gRPC не отвечает на HTTP
```

Qdrant проверяется gRPC-клиентом (инструмент `RagIndexStatus`), а не curl.

## Redis: что в нём хранится

| Данные | Ключи |
|---|---|
| Kanban-доска | эпики, задачи, багрепорты |
| История чата | сообщения сессии |
| Чекпоинты планировщика | состояние плана по проекту |

| Переменная | Дефолт | Смысл |
|---|---|---|
| `REDIS_ADDR` | `localhost:56379` | Чекпоинты плана |
| `REDIS_PASSWORD` | — | Пароль |
| `REDIS_DB` | 0 | Номер БД |
| `BOARD_REDIS_ADDR` | `localhost:56379` | Отдельный Redis доски |
| `BOARD_REDIS_DB` | 0 | БД доски |
| `BOARD_REDIS_PASSWORD` | — | Пароль доски |
| `BOARD_TTL` | без истечения | TTL записей доски |
| `PLAN_CHECKPOINT_TTL` | без истечения | TTL чекпоинтов |

**Без Redis** система работает: чекпоинты и доска просто недоступны, остальное
(агенты, RAG, ревью) — нет.

## Qdrant: что в нём хранится

Коллекция `project_code_base` (`QDRANT_COLLECTION_NAME`). В каждой точке —
чанк кода с payload `branch`, `commit_sha`, `chunk_id`, `content_hash`,
`replaced_by`.

Подробнее — [RAG и CodeSearch](../20-features/rag.md).

Volume `qdrant` переживает перезапуск контейнера, но **не** `docker compose
down -v`.

## Файлы на диске

| Путь | Что | Настраивается |
|---|---|---|
| `temp/<проект>/` | Рабочий каталог каждого проекта | нет |
| `temp/.wt-<проект>-<taskID>` | Worktree задачи | создаётся автоматически |
| `$HOME/.ai-workspaces.json` | Реестр проектов | `AI_WORKSPACES` |
| `.ai-workspaces.json` (cwd) | Фолбэк реестра, если `$HOME` недоступен | — |
| `logs/server.log` | Лог процесса Web UI | `LOG_DIR` |
| `logs/<проект>.log` | Лог проекта | `LOG_DIR` |
| `discovered_mrs.txt` | База известных MR | `DB_FILENAME` |
| `web/dist` | Прод-сборка фронтенда | встраивается в бинарь |

> **`temp/` — рабочее хранилище, а не исходный код.** Там живут проекты,
> которые генерируют агенты. Не коммитьте содержимое `temp/` (оно игнорируется
> через `temp/.gitignore`) и не правьте файлы в нём как в исходниках.

## Sandbox

Отдельный compose для dev-образа песочницы:

```bash
SANDBOX_UID=$(id -u) SANDBOX_GID=$(id -g) docker compose -f sandbox/compose.yaml build
```

Подробнее — [Песочница](../20-features/sandbox.md).

## Деградация

Отсутствие внешнего сервиса **не ломает систему**:

| Нет | Что происходит |
|---|---|
| Redis | Нет чекпоинтов и доски; агенты работают |
| Qdrant | `CodeSearch` → `skipped`; остальное работает |
| LSP-сервер | `LspCheck` → `skipped`; остальное работает |
| Ollama (при облачном провайдере) | Не нужен |
| Docker (без песочницы) | `Run` на хосте |

Каждая деградация возвращает подсказку, а не ошибку.

## Связанное

- [Установка](../10-getting-started/installation.md) — пошаговая настройка
- [Быстрый старт](../10-getting-started/quickstart.md)
- [RAG](../20-features/rag.md)
- [Планировщик](../20-features/planner.md) — чекпоинты
- [Песочница](../20-features/sandbox.md)
