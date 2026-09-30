# ai

Мультиагентная система автоматической разработки: агенты проектируют,
генерируют, собирают, тестируют и ревьюят приложения, ведут Kanban-доску и
работают с Merge Request'ами.

**Полная документация — [`docs/README.md`](docs/README.md).**

## Быстрый старт

```bash
# 1. Инфраструктура: Qdrant + Redis
docker compose up -d

# 2. Ollama ставится отдельно (в compose её нет)
brew install ollama && ollama serve      # macOS
ollama pull qwen3-coder:30b

# 3. Конфигурация
cp .env.example .env

# 4. Работать
go run . serve                             # Web UI на :8090
```

Подробно — [Быстрый старт](docs/10-getting-started/quickstart.md).

## Команды

| Команда | Что делает |
|---|---|
| `go run . serve` | Web UI: доска, чат, диффы, логи |
| `go run . plan <проект> <промпт>` | Декомпозиция и исполнение по волнам |
| `go run . kanban <проект> <промпт>` | Оркестрация на доске |
| `go run . backend\|frontend\|devops\|qa <проект> <промпт>` | Отдельный специалист |
| `go run . *lead <проект> <промпт>` | Декомпозиция под направление |
| `go run . accept <проект>` | Детерминированная приёмка без LLM |
| `go run . index <проект>` | Индексация проекта в Qdrant |
| `go run . review <URL_MR>` | Ревью Merge Request |
| `go run . listen` | Слушатель новых MR (GitLab) |

Полный список — [CLI](docs/30-reference/cli.md).

## Документация

| Раздел | Что внутри |
|---|---|
| [00-overview](docs/00-overview/) | Что это, архитектура, сквозной путь задачи, глоссарий |
| [10-getting-started](docs/10-getting-started/) | Быстрый старт, установка, конфигурация, провайдеры |
| [20-features](docs/20-features/) | Домены: как работают и как настраиваются |
| [30-reference](docs/30-reference/) | env, CLI, REST/WS, инструменты, инфраструктура |
| [40-operations](docs/40-operations/) | Разработка, тесты, добавление фичи, решение проблем |
| [plans](docs/plans/) | Архив планов реализации |

## Разработка

```bash
go build . ./agents/... ./tools/ ./board/
go vet   . ./agents/... ./tools/ ./board/
go test  . ./agents/... ./tools/ ./board/
npm --prefix web run build
```

> `go build ./...` падает на артефактах в `temp/` — там хранятся проекты,
> сгенерированные агентами, а не исходный код.

Подробнее — [Разработка](docs/40-operations/development.md).

## Про `temp/`

Каталог `temp/` — **рабочее хранилище проектов агентов**, а не исходный код.
Он игнорируется git. Не коммитьте его содержимое и не правьте файлы в нём как
в исходниках.

## Правила для агентов

Подробности — [`AGENTS.md`](AGENTS.md).
