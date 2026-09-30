# Установка и инфраструктура

## Go

Требуется Go **1.26.5** (см. `go.mod`).

```bash
go version
go build . ./agents/... ./tools/ ./board/
```

## Docker

Нужен для Redis и Qdrant, а также для образа песочницы. Проверка:

```bash
docker version --format '{{.Server.Version}}'
```

Песочница проверяет именно **демон** (`docker version --format …`), а не
наличие бинаря в `PATH` (`tools/sandbox.go:213-232`).

## Сервисы

```bash
docker compose up -d
```

`compose.yaml` поднимает ровно два сервиса:

| Сервис | Порт (хост) | Порт (контейнер) | Зачем |
|---|---|---|---|
| `qdrant/qdrant:latest` | `56334` (gRPC) | 6334 | RAG-индекс. Используется **только gRPC** — HTTP медленнее в разы |
| `redis:7-alpine` | `56379` | 6379 | Чекпоинты плана, Kanban-доска, история чата. `--appendonly yes` |

Проверка:

```bash
redis-cli -p 56379 ping
curl -s localhost:56334  # Qdrant gRPC не отвечает на HTTP — проверь порт
```

## Модели

### Локально: Ollama

> **Ollama не входит в `compose.yaml`** — там только Qdrant и Redis.
> Ставится отдельно на хост.

```bash
# macOS
brew install ollama && ollama serve

# Linux
curl -fsSL https://ollama.com/install.sh | sh
```

Затем скачать модели:

```bash
ollama pull qwen3-coder:30b
ollama pull qwen3:8b
```

Корневой `Makefile` содержит только эти `ollama pull` — скачать всё сразу:

```bash
make
```

`Modelfile` описывает модель по умолчанию для локального сервера:
`FROM qwen3.6:35b-a3b-q4_K_M`, `PARAMETER num_ctx 131072`.

> **Внимание, расхождение:** модель по умолчанию указана в трёх местах и они
> не согласованы — `.env.example` (`OLLAMA_MODEL=qwen3-coder:30b`),
> `Modelfile` (`qwen3.6:35b-a3b-q4_K_M`), `Makefile` (`qwen3:8b`,
> `qwen3-coder:30b`). Код берёт `OLLAMA_MODEL` из `.env`; при пустом значении
> подставляется `llama3` (`models/resolve.go:33-35`). Указывайте модель явно.

### Облачно: YandexGPT или Trim

Ничего локального ставить не нужно — достаточно ключа. См.
[LLM-провайдеры](providers.md).

## Redis

Используется в трёх независимых ролях, у каждой свой адрес:

| Роль | Переменные | Что хранит |
|---|---|---|
| Чекпоинты плана | `REDIS_ADDR`, `REDIS_PASSWORD`, `REDIS_DB` | Статусы шагов для `--resume` |
| Kanban-доска | `BOARD_REDIS_ADDR`, `BOARD_REDIS_PASSWORD`, `BOARD_REDIS_DB` | Эпики, задачи, багрепорты |
| История чата | использует адрес доски | Redis Streams + pub/sub |

По умолчанию все три смотрят на `localhost:56379`, база `0`. Проверить:

```bash
redis-cli -p 56379 keys '*'
```

Записи доски имеют TTL, если задан `BOARD_TTL` (по умолчанию — без
истечения). Чекпоинты — `PLAN_CHECKPOINT_TTL` (по умолчанию без истечения).

## Qdrant

```bash
QDRANT_ADDR=localhost:56334
QDRANT_COLLECTION_NAME=project_code_base
EMBEDDING_MODEL=nomic-embed-text
```

Коллекция создаётся автоматически при первой индексации. Модель эмбеддингов
должна быть доступна в Ollama:

```bash
ollama pull nomic-embed-text
```

Проверить индекс:

```bash
go run . index <имя_проекта>   # индексация
# статус — инструментом RagIndexStatus или в UI
```

Если Qdrant недоступен, система **не ломается**: `CodeSearch` возвращает
`skipped`, фоновая индексация — `503`, остальные домены работают.

## Образ песочницы

Нужен только при `CODEGEN_SANDBOX=container` или `=auto`. По умолчанию
команды `Run` идут на хост.

```bash
docker compose -f sandbox/compose.yaml build
```

`sandbox/Dockerfile` — `FROM golang:1.24-bookworm`, `USER ${TARGET_UID}:${TARGET_GID}`,
`WORKDIR /workspace`, `CMD ["sh"]`. Playwright добавляется отдельным слоем
(`CODEGEN_SANDBOX_PLAYWRIGHT`).

Изоляция песочницы: `cap_drop: ALL`, `no-new-privileges`, `mem_limit`, `cpus`,
`tmpfs /tmp:exec,mode=1777`.

Подробнее — [Песочница](../20-features/sandbox.md).

## LSP-серверы

Нужны для диагностик и навигации. Устанавливаются отдельно, подбираются
автоматически по стеку:

| Стек | Сервер | Установка |
|---|---|---|
| Go | `gopls` | `go install golang.org/x/tools/gopls@latest` |
| Node/TS | `typescript-language-server` | `npm i -g typescript-language-server typescript` |
| PHP | `intelephense` / `phpactor` |composer/phpactor |
| Python | `pyright-langserver` | `npm i -g pyright` |

Бинарники ищутся через `tools/binpath`: `PATH`, затем `~/go/bin`, `GOPATH/bin`,
`~/.local/bin`, npm/nvm, Homebrew, `/usr/local`. Свои каталоги — через
`LSP_BIN_PATH` (высший приоритет).

Если сервера нет — `LspCheck` возвращает `skipped`, а не ошибку.

Подробнее — [LSP](../20-features/lsp.md).

## Git и токены

Для кода-ревью и кнопки «Принять → MR»:

```bash
GITLAB_TOKEN=...    # scope: api, read_api
GITHUB_TOKEN=...    # scope: repo (или public_repo для публичных)
GITLAB_URL=...      # адрес GitLab без завершающего слэша
```

## Node.js

Только для фронтенда:

```bash
node --version
npm --prefix web install
```

## Проверка установки

```bash
go build . ./agents/... ./tools/ ./board/ && echo BUILD_OK
docker compose ps
redis-cli -p 56379 ping
curl -s -X POST http://localhost:11434/api/tags | head -c 200
```

## Связанное

- [Быстрый старт](quickstart.md)
- [Конфигурация](configuration.md)
- [Инфраструктура](../30-reference/infrastructure.md)
- [Решение проблем](../40-operations/troubleshooting.md)
