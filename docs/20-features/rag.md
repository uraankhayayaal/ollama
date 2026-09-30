# RAG и CodeSearch

Семантический поиск по коду через Qdrant. Агент может спросить «где здесь
обрабатывается авторизация» и получить релевантные фрагменты вместо
перебора файлов.

**Версионирование по веткам Git** — ключевая особенность: агент в фича-ветке
видит актуальный код этой ветки, а не `main`.

## Настройка

```bash
# Подключение
QDRANT_ADDR=localhost:56334            # gRPC-порт Qdrant
QDRANT_COLLECTION_NAME=project_code_base
EMBEDDING_MODEL=nomic-embed-text

# Лимиты выдачи
RAG_MAX_RESULTS=3          # чанков в ответе
RAG_READ_MAX_TOTAL=150000  # суммарных символов сниппетов
RAG_SEARCH_TIMEOUT=30s     # таймаут цикла Ping+embed+query

# Поведение по ролям
RAG_PLANNER_CONTEXT=1      # вкл по умолчанию
RAG_ARCHITECT_CONTEXT=1    # вкл по умолчанию (нет в .env.example)
RAG_ASSISTANT_CONTEXT=1    # вкл по умолчанию (нет в .env.example)

# Автопереиндексация
RAG_AUTO_REINDEX=0         # выкл по умолчанию
```

Используется **только gRPC** — HTTP-порт Qdrant медленнее в разы
(`QDRANT_ADDR=localhost:56334`).
Модель эмбеддингов должна быть доступна в Ollama:

```bash
ollama pull nomic-embed-text
```

> Ollama **не входит в `compose.yaml`** (там только Qdrant и Redis), поэтому
> `docker compose exec -it ollama …` не работает.

## Что индексируется

`rag/walk.go` — детерминированный обход (с сортировкой). Пропускаются:

- Каталоги `.git`, скрытые, `forges.IsIgnoredDir` (`rag/walk.go:51-52`).
- Совпадения `.gitignore` (собственный парсер с инверсией `!` и
  каталоговыми шаблонами, `:106-231`).
- Скрытые файлы (`:61-63`).
- Файлы больше 2 МБ и пустые (`:72-74`, `maxIndexFileSize`).
- Бинарные (NUL в префиксе 1024 байт, `:90-104`).

**Чанк** — до 100 строк (`rag/chunk.go:21`), нарезка структурная для
Go/TS/JS/Python.

**Scope** — первый сегмент пути; корень → `root` (`rag/index.go:503-513`).

## Версионирование по веткам

### Payload точки Qdrant

```json
{
  "project_name": "...", "file_path": "...", "scope": "...",
  "start_line": 1, "end_line": 100, "code_content": "...",
  "branch": "ai/task/42", "commit_sha": "a1b2c3d",
  "chunk_id": "...", "content_hash": "...",
  "replaced_by": ""
}
```

`replaced_by` пусто → версия активна. Непусто → версия вытеснена.

### `chunk_id`

`ChunkID(project, relPath, symbol)` — FNV-1a64 в hex
(`rag/chunk.go:371-385`). Ключ идентичности — **символ определения**;
при линейной нарезке — `L<start_line>` (`chunkKey:387-394`).

Один `chunk_id` делится версиями одной функции **между коммитами и ветками** —
это позволяет находить перекрытия.

### `pointID`

FNV-1a64 от `project|relPath|branch|commit|startLine|contentHash`
(`rag/index.go:474-491`). Включение коммита и хеша обязательно: иначе
терялись бы незакоммиченные правки агента (главный сценарий
`ReindexFiles` после мутации).

### Индексация

`IndexOptions{Branch, CommitSHA}` (`rag/index.go:70-81`). Пусто → `MainBranch`
+ пустой коммит (обратная совместимость для проектов без Git).

`upsertChunks` (`rag/index.go:220-274`):

1. Scroll активных версий.
2. `SetPayload(replaced_by = commit)` — **с `Wait: true`**, иначе гонка с
   prune и поиском.
3. Upsert новых точек с пустым `replaced_by`.

`pruneSuperseded` (`:352-366`) хранит **одно предыдущее поколение**;
устаревшие прошлого коммита удаляются.

### Поиск

`rag/search.go:83`:

- Фильтр «актуальный чанк ветки» (`activeBranchCond:206-224`):
  `Must: replaced_by is_empty` + `Should: branch == <ветка>` (плюс
  `branch is_null` для точек, собранных до версионирования).
- Выдача: активные точки ветки **или** активные `main`; чужие ветки не видны.
- `dropOverriddenByBranch` — второй проход: скролл активных точек ветки **по
  файлам результатов**, отсечение перекрытых версий `main` по `chunk_id`.
- Overfetch ×3, минимум 12 (`branchOverfetch`, `:37-41`).
- Порядок сборки: перекрытие → лимит → `maxTotal`.

> **Тонкость:** `should` внутри `filter` означает «хотя бы одно условие».
> Если бы условия ветки попали в `Must`, поиск из `main` не возвращал бы
> ничего. Это была реальная ошибка, пойманная тестами
> (`rag/memstore_test.go`).

## Инструменты

### `CodeSearch`

`tools/codesearch.go:38`. Выдаётся разработчику, архитектору, чат-ассистенту,
планировщику (в виде RAG-контекста).

Параметры:

| Параметр | Обяз. | Описание |
|---|---|---|
| `query` | да | Что ищем |
| `scope` | нет | Ограничить область поиска |
| `branch` | нет | Ветка; пусто → ветка рабочего каталога агента |

Поведение:

- Проект = базовое имя `OutputDir` (`projectFromOutputDir:224-231`).
- Ветка кэшируется через `sync.Once` + `rag.DetectBranch` (`:233-251`).
- `Ping` перед поиском — при недоступности возвращается `skipped`.
- Каждый сниппет получает шапку происхождения:
  `[Файл: … | Ветка: … | Коммит: a1b2c3d]` (`:194-196,205-216`).

Ответ: `{status, branch, results: [{file, start_line, end_line, score, snippet, branch, commit_sha, chunk_id}]}`.

Деградация: без клиента RAG, без проекта или при недоступном Qdrant →
`skipped` с подсказкой, а не ошибка (`:125-182`).

### `RagIndexStatus`

`tools/ragstatus.go:21`. Без параметров. Ответ:
`{"status":"ok","indexed":bool,"chunks":int}`.

Считаются только **активные** версии (`rag/status.go:21-29`).

### `IndexBackground`

`server/actions.go:202-224` — серверный мост (в Web UI, недоступен из CLI).
Фоновая индексация: `Session.IndexBackground` (`server/ragindex.go:61-100`).

- Single-flight: повторный вызов при идущей индексации — ошибка.
- Контекст **не наследуется** от вызывающего (иначе `context canceled` на
  первом embed) — используется `context.Background()` с `cancel` (`:87-94`).
- Отчёт — в `chat.RoleStatus` и лог проекта.
- Клиент создаётся фабрикой `buildProjectIndexer` (подменяется в тестах).

## Запуск индексации

### CLI

```bash
go run . index <имя_проекта>
```

`main.go:430-476`: `projects.ProjectDir` → `rag.NewClientSafe` →
`EnsureCollection` → `WalkProject` → `IndexProject` → `Close`.

### REST

```http
POST /api/projects/{id}/index?branch=<ветка>
```

Коды: `200` — запущено; `409` — индексация уже идёт; `503` — RAG недоступен
(`server/server.go:694-722`).

### Автоматически

`RAG_AUTO_REINDEX=1` — после каждой мутации разработчик вызывает
`ReindexTouched` (`runner/reindex.go:69-102`): файлы, затронутые раундом,
переиндексируются, исчезнувшие удаляются.

Первая ошибка индексации останавливает только индексацию — раннер логирует
и продолжает генерацию (degrade).

## Определение ветки

`rag/branch.go`:

| Функция | Что делает |
|---|---|
| `MainBranch` | Имя основной ветки |
| `DetectBranch(dir)` | Текущая ветка рабочего каталога |
| `DetectCommit(dir)` | Текущий коммит |
| `DetectIndexOptions(dir)` | `{Branch, CommitSHA}` для индексации |

Git-вызовы идут через инъектируемый `rag.gitRunner` — это позволяет тестам
подменять git.

## Проверить индекс

```bash
go run . index <проект>
redis-cli -p 56379 ping    # Qdrant проверяется отдельно
```

В Web UI — кнопка «Индекс RAG» в шапке проекта.

## Связанное

- [Сжатие контекста](context-compression.md) — Ф-7 вытесняет историю в RAG
- [Git-flow](gitops-workflow.md) — откуда берутся ветки
- [Планировщик](planner.md) — RAG-контекст планирования
- [Архитектор](architect.md) — предложение построить индекс
- [Инфраструктура](../30-reference/infrastructure.md) — Qdrant
