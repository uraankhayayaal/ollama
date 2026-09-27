# План: Branch-Aware RAG — версионированный семантический поиск по веткам Git

Статус: **ПРОЕКТ** (не начат). Формат — как остальные `PLAN-*.md`: текущее
состояние (`file:line`), решения пользователя, архитектурные решения, новые
компоненты, этапы с чекбоксами, верификация. Обновлять по мере выполнения
(чекбоксы `[x]`), статус менять только после зелёной верификации.

## Цель

Дать ИИ-агентам (архитектор, лиды, специалисты, чат-ассистент) **семантический
поиск по коду с учётом текущей ветки Git**: агент в ветке `ai/epic/ARCH-01`
видит код своей ветки + актуальный код `main`, но НЕ видит код других эпиков.
Это исключает галлюцинации модели, когда она ссылается на функции, которые
есть в другой ветке, но не в текущей.

## Текущее состояние (что уже есть)

### RAG-подсистема (PLAN-2026-09-19-done-qdrant.md)

| Компонент | Файл:строка | Что есть |
|---|---|---|
| Клиент Qdrant | `rag/client.go:66` | `Client` с `EnsureCollection`, `Ping`, `Close`; коллекция `project_code_base`, метрика `Distance_Cosine` |
| Эмбеддинги | `rag/embed.go` | `OllamaEmbedder` (модель `EMBEDDING_MODEL`, по умолчанию `nomic-embed-text`) |
| Чанкинг | `rag/chunk.go:35` | `ChunkFile` — структурная нарезка: Go через `go/ast`, TS/Python по маркерам, лимит 100 строк |
| Индексация | `rag/index.go:53` | `IndexProject` (полная), `IndexFile` (частичная), `DeleteFile`; payload: `project_name`, `file_path`, `start_line`, `end_line`, `code_content`, `scope` |
| Поиск | `rag/search.go:58` | `Search` — семантический запрос с фильтром по `project_name` + `scope` |
| Статус | `rag/status.go:34` | `ProjectInfo` — подсчёт чанков проекта через Count API |
| Обход | `rag/walk.go:29` | `WalkProject` — обход дерева с игнором `.gitignore`, бинарных, скрытых файлов |
| Фоновая индексация | `server/ragindex.go` | `IndexBackground` — горутина walk + `IndexProject`, идемпотентная (удаляет старые точки проекта) |
| Авто-переиндексация | `runner/reindex.go:33` | `RAG_AUTO_REINDEX` — частичная переиндексация после мутаций агентов |
| Инструмент | `tools/codesearch.go:29` | `CodeSearch` → `{query, scope}` → топ-N чанков |

### Git-workflow проекта

| Компонент | Файл:строка | Что есть |
|---|---|---|
| Ветки эпиков | `server/gitflow.go:25` | Префикс `ai/epic/<id>` (база = `main`) |
| Ветки задач | `server/gitflow.go:27` | Префикс `ai/task/<id>` (база = ветка эпика) |
| Реестр веток | `workspace/branches.go:21` | `BranchRef{Branch, Base, Worktree}` — side-реестр в JSON workspace |
| Создание веток | `gitops/merge.go:113` | `CreateBranch` — без переключения рабочей копии |
| Merge | `gitops/merge.go:42` | `MergeBranch` — `--no-ff` |
| Merge-base | `gitops/merge.go:60` | `MergeBase` — точка отхода двух веток |
| Конфликты | `gitops/merge.go:97` | `ConflictingFiles` — через `git merge-tree` |
| Worktree задачи | `workspace/branches.go:24` | `Worktree` — каталог постоянного worktree задачи |

### Проблема (что закрывает план)

1. **RAG не знает про ветки.** `CodeSearch` ищет по всему проекту без учёта
   текущей ветки. Агент в ветке `ai/epic/ARCH-01` видит чанки из `main` И из
   других эпиков — может сослаться на функции, которых в его ветке нет.
2. **Нет версионирования чанков.** При переиндексации файла старые чанки
   удаляются и заменяются новыми — история изменений функции между коммитами
   теряется.
3. **Нет изоляции по веткам.** Два эпика, меняющие один файл, видят изменения
   друг друга в RAG — контекст загрязнён.

## Решения пользователя (зафиксировано)

1. **Стратегия чанкирования.** Сохраняем текущий структурный чанкинг
   (`ChunkFile`). Добавляем детерминированный `chunk_id` = FNV-1a от
   `(project, file, symbol_name)` — для Go это имя функции/метода/типа,
   для остальных языков — имя определения или пустая строка (fallback).
2. **Версионирование чанков.** Каждая точка в Qdrant — версия чанка в ветке.
   При изменении чанка: старая точка получает `replaced_by = <commit_sha>`,
   новая точка создаётся с `replaced_by = null`. Это позволяет отследить
   историю функции и корректно удалять устаревшие версии.
3. **Мультитенантность.** Поле `repository` (= `project_name`) уже есть.
   Добавляем поля `branch` и `commit_sha` — фильтрация по ветке и коммиту.
4. **Поиск с учётом ветки.** Агент передаёт `branch` (или он определяется из
   контекста). Фильтр поиска: `should` (OR) — чанки текущей ветки +
   чанки `main`, которые НЕ были перезаписаны в текущей ветке.
5. **Индексация по событиям.** Вместо webhook-сервера: агенты при завершении
   работы вызывают `IndexBackground` (уже есть), а авто-переиндексация
   (`runner/reindex.go`) дополнительно проставляет `branch` и `commit_sha`.
   Для полной индексации ветки — фоновая индексация с указанием ветки.
6. **Метаданные в контексте.** Результаты `CodeSearch` дополняются маркерами
   `[Файл: ... | Ветка: ... | Коммит: ...]` — модель видит, откуда каждый чанк.
7. **Обратная совместимость.** Проекты без Git (не gitflow) работают как
   сейчас — `branch` = `main` по умолчанию, `replaced_by` = null.

## Архитектурные решения

### Р-1: Payload-схема чанка — новые поля

`rag/index.go:23` — константы payload:

```go
const (
    PayloadBranch   = "branch"
    PayloadCommit   = "commit_sha"
    PayloadChunkID  = "chunk_id"
    PayloadReplacedBy = "replaced_by"
)
```

- `branch` — имя ветки (`main`, `ai/epic/ARCH-01`, ...). Для проектов без
  Git — `main` по умолчанию.
- `commit_sha` — SHA коммита, на котором чанки были проиндексированы.
- `chunk_id` — детерминированный ID чанка (FNV-1a от `project + file +
  symbol`). Позволяет отслеживать одну функцию между коммитами.
- `replaced_by` — SHA коммита, который заменил этот чанк. `null` — чанк
  актуален в ветке.

### Р-2: Чанкинг с символами — `chunk_id`

`rag/chunk.go` — `ChunkFile` расширяется: каждый `Chunk` получает поле
`Symbol string` (имя функции/метода/типа). Для Go извлекается из
`ast.FuncDecl.Name` / `ast.GenDecl`. Для остальных языков — из маркера
определения (первое слово после `function`/`def`/`class`). Если символ
не найден — `Symbol = ""`, `chunk_id` = хэш от `(project, file, start_line)`.

### Р-3: Индексация с версионированием

`rag/index.go` — `IndexProject` и `IndexFile` принимают параметры
`branch` и `commitSHA`. Перед загрузкой новых чанков:

1. Для каждого `chunk_id` находим активную точку в ветке
   (`branch=X AND chunk_id=Y AND replaced_by=null`).
2. Находим её ID, проставляем `replaced_by = commitSHA`.
3. Загружаем новые чанки с `replaced_by = null`, `commit_sha = commitSHA`.

Это идемпотентно: повторная индексация с тем же `commit_sha` не плодит
дубликаты (проверка по `commit_sha` перед проставлением `replaced_by`).

### Р-4: Поиск с учётом ветки

`rag/search.go` — `SearchParams` расширяется полем `Branch`. Фильтр:

```go
// Условие 1: актуальные чанки текущей ветки
filter1 := &qdrant.Filter{Must: []*qdrant.Condition{
    qdrant.NewMatchKeyword(PayloadProject, project),
    qdrant.NewMatchKeyword(PayloadBranch, branch),
    qdrant.NewIsEmpty(PayloadReplacedBy), // replaced_by = null
}}

// Условие 2: чанки main, не перезаписанные в текущей ветке
// (исключаем чанки, которые уже есть в ветке с replaced_by=null)
filter2 := &qdrant.Filter{
    Must: []*qdrant.Condition{
        qdrant.NewMatchKeyword(PayloadProject, project),
        qdrant.NewMatchKeyword(PayloadBranch, "main"),
        qdrant.NewIsEmpty(PayloadReplacedBy),
    },
    MustNot: []*qdrant.Condition{
        // Исключаем chunk_id, которые уже актуальны в текущей ветке
        qdrant.NewHasID(excludeChunkIDs),
    },
}

filter := &qdrant.Filter{
    Must:   []*qdrant.Condition{qdrant.NewMatchKeyword(PayloadProject, project)},
    Should: []*qdrant.Condition{
        qdrant.NewFilter(filter1),
        qdrant.NewFilter(filter2),
    },
}
```

`excludeChunkIDs` — запрос к Qdrant: `project=X AND branch=Y AND replaced_by=null`
→ собрать `chunk_id` → исключить из условия 2.

### Р-5: Инструмент `CodeSearch` — параметр `branch`

`tools/codesearch.go` — `CodeSearchParams` получает опциональное поле
`branch`. Если не указано — определяется из контекста:

- Если проект — gitflow (есть ветка эпика/задачи) — берём текущую ветку.
- Иначе — `main`.

Результаты дополняются метаданные: `branch`, `commit_sha`, `chunk_id`.
Формат сниппета: `[Файл: src/auth.go | Ветка: ai/epic/ARCH-01 | Коммит: a1b2c3d]`.

### Р-6: Определение текущей ветки агентом

`tools/codesearch.go` — функция `detectBranch(ops *FileOps) string`:
1. Проверяем `git rev-parse --abbrev-ref HEAD` в `OutputDir`.
2. Если пусто или не git-репозиторий — возвращаем `main`.
3. Кэшируем результат (ветка не меняется в рамках одного шага агента).

### Р-7: Индексация при завершении работы

`runner/reindex.go` — авто-переиндексация после мутаций дополнительно:
1. Определяет текущую ветку (`detectBranch`).
2. Проставляет `branch` и `commit_sha` для новых чанков.
3. Для изменённых чанков — версионирование (проставляет `replaced_by`).

### Р-8: Удаление файла в ветке

При удалении файла (не переименовании): все чанки файла в ветке получают
`replaced_by = <commit_sha>`. Поиск их не возвращает (условие
`replaced_by = null`).

## Новые компоненты

| Файл | Назначение |
|---|---|
| `rag/chunk.go` (правка) | `Chunk.Symbol`, `ChunkID(project, file, symbol)` |
| `rag/index.go` (правка) | `IndexProject`/`IndexFile` с параметрами `branch`, `commitSHA`; версионирование чанков |
| `rag/search.go` (правка) | `SearchParams.Branch`, фильтр `should` с исключением |
| `rag/branch.go` (новый) | `DetectBranch(dir) (string, error)` — определение текущей ветки |
| `tools/codesearch.go` (правка) | `CodeSearchParams.Branch`, метаданные в результатах |
| `runner/reindex.go` (правка) | Версионирование при авто-переиндексации |
| `docs/PLAN-2026-09-27-branch-aware-rag.md` | этот план |

## Интеграции с существующим кодом

- **`tools.Deps`** (`tools/tool.go:34`) — уже имеет `RAG`. Для определения
  ветки используем `FileOps.OutputDir` — новых зависимостей не нужно.
- **`rag.Client`** — интерфейс `QdrantStore` (`rag/client.go:55`) не меняем:
  новые поля payload не требуют изменений в gRPC-клиенте.
- **`CodeSearch`** — `tools/codesearch.go:29` — добавляем параметр `branch`
  и метаданные в результат. Обратная совместимость: `branch` опционален,
  при отсутствии — `main`.
- **`IndexBackground`** (`server/ragindex.go`) — фоновая индексация
  дополнительно принимает `branch` (по умолчанию — текущая ветка проекта).
- **`ResolveGitConflicts`** (`tools/gitresolve.go`) — при резолве конфликтов
  переиндексация идёт с проставлением `replaced_by` для старых чанков.
- **Hermetic-тесты** — fake-реализация `QdrantStore` обновляется: новые поля
  payload не ломают существующие тесты (они используют `WithPayload` и
  фильтры по `project_name`).

## API (изменения)

```
CodeSearch (схема) — новое опциональное поле:
    "branch": "ai/epic/ARCH-01"   # по умолчанию — текущая ветка проекта

CodeSearch (результат) — новые поля:
    { "file": "...", "start_line": 1, "end_line": 26, "score": 0.86,
      "snippet": "...", "branch": "ai/epic/ARCH-01", "commit_sha": "a1b2c3d",
      "chunk_id": "abc123" }

IndexBackground (REST, опционально):
    POST /api/projects/{id}/index?branch=ai/epic/ARCH-01
```

## Этапы и чеклист

### Ф-1 — Payload-схема и чанкинг с символами
- [ ] `rag/chunk.go`: `Chunk.Symbol` — извлечение имени функции/метода/типа
      для Go (из `ast`), TS/Python (из маркера), fallback — пустая строка
- [ ] `rag/chunk.go`: `ChunkID(project, file, symbol)` — FNV-1a от
      `project + file + symbol` (или `start_line` если symbol пустой)
- [ ] `rag/index.go`: константы payload `PayloadBranch`, `PayloadCommit`,
      `PayloadChunkID`, `PayloadReplacedBy`
- [ ] `rag/index.go`: `upsertChunks` — загрузка с `branch`, `commit_sha`,
      `chunk_id`, `replaced_by=null`
- [ ] Тесты: `ChunkFile` возвращает символы для Go; `ChunkID`
      детерминирован; payload содержит новые поля

### Ф-2 — Версионирование чанков при индексации
- [ ] `rag/index.go`: `IndexProject` и `IndexFile` принимают `branch`,
      `commitSHA`; перед загрузкой новых чанков — поиск активных версий
      по `chunk_id` и проставление `replaced_by`
- [ ] `rag/index.go`: идемпотентность — повторная индексация с тем же
      `commit_sha` не плодит дубликаты
- [ ] `rag/index.go`: удаление файла — все чанки файла в ветке получают
      `replaced_by`
- [ ] Тесты: версионирование (старая точка получает replaced_by), повторная
      индексация идемпотентна, удаление файла помечает чанки

### Ф-3 — Поиск с учётом ветки
- [ ] `rag/search.go`: `SearchParams.Branch`; фильтр `should` с исключением
      чанков, актуальных в текущей ветке
- [ ] `rag/search.go`: запрос `excludeChunkIDs` — поиск актуальных `chunk_id`
      в ветке для исключения из условия 2
- [ ] `rag/branch.go`: `DetectBranch(dir) (string, error)` — определение
      текущей ветки через `git rev-parse --abbrev-ref HEAD`
- [ ] Тесты: фильтр по ветке (чанки текущей ветки + main без перезаписанных),
      изоляция между ветками, fallback на main для проектов без Git

### Ф-4 — Инструмент `CodeSearch` — параметр `branch` и метаданные
- [ ] `tools/codesearch.go`: `CodeSearchParams.Branch`; при отсутствии —
      `detectBranch(ops)`
- [ ] `tools/codesearch.go`: результаты дополняются `branch`, `commit_sha`,
      `chunk_id`; сниппет — `[Файл: ... | Ветка: ... | Коммит: ...]`
- [ ] `tools/codesearch.go`: текст промпта агента — «ты находишься в ветке
      X; при поиске кода всегда указывай текущую ветку»
- [ ] Тесты: `branch` передаётся в `Search`, метаданные в результатах,
      fallback на main, маркеры версии в сниппетах

### Ф-5 — Интеграция с авто-переиндексацией
- [ ] `runner/reindex.go`: при частичной переиндексации — версионирование
      чанков (проставление `replaced_by` для старых версий)
- [ ] `runner/reindex.go`: `branch` и `commit_sha` определяются из контекста
      проекта
- [ ] Тесты: после мутации файла старые чанки получают `replaced_by`,
      новые — `replaced_by=null`; поиск по ветке возвращает только актуальные

### Ф-6 — Фоновая индексация с веткой
- [ ] `server/ragindex.go`: `IndexBackground` принимает `branch`
      (по умолчанию — текущая ветка проекта); передаёт в `IndexProject`
- [ ] `tools/codesearch.go`: `CodeSearch` при отсутствии `branch` —
      `detectBranch` из `OutputDir`
- [ ] Тесты: фоновая индексация с веткой, поиск по ветке возвращает чанки
      только из этой ветки + main без перезаписанных

### Ф-7 — Верификация и полировка
- [ ] `go build . ./agents/... ./tools/./board/ ./rag/ ./server/ ./workspace/`
- [ ] `go vet  . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/`
- [ ] `go test . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/`
- [ ] `npm run build` (web/) — зелёный
- [ ] Ручной E2E: проект с gitflow, два эпика с общим файлом — поиск из
      ветки эпика А не видит изменения эпика Б

## Верификация

```
go build . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/
go vet  . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/
go test . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/
npm run build   # web/
```

Известные нюансы репозитория (не убирать):
- `go build ./...` падает на `temp/moon-distance/internal/middleware/cors.go`
  (артефакт сгенерированного проекта) — использовать перечень выше.
- Не форматировать `agents/acceptor/checks.go`, `agents/acceptor/run.go`.
- Комментарии/промпты/UI-тексты — на русском.
- Все новые возможности — опциональные (nil-safe): консоль без Git и без
  Qdrant не должна падать, degrade как у LSP и CodeSearch (skipped).

## Связанные планы

- `PLAN-2026-09-19-done-qdrant.md` — RAG/Qdrant: базовая инфраструктура,
  которую расширяет этот план.
- `PLAN-2026-09-24-done-architect-intelligence.md` — интеграция RAG
  с архитектором: `CodeSearch`, `RagIndexStatus`, фоновая индексация.
- `PLAN-2026-09-24-done-merge-conflict-board.md` — merge-конфликты
  и gitflow: версионирование чанков дополняет работу с ветками.
- `PLAN-2026-09-22-done-context-compression.md` — сжатие контекста:
  лимиты выдачи `CodeSearch` сохраняются.

## Как продолжить

1. Прочитать «Решения пользователя» и «Этапы и чеклист».
2. Взять первый незачёркнутый пункт Ф-1, выполнить, отметить `[x]`,
   закоммитить.
3. После каждой фазы — верификация (блок выше) и краткая запись в этот файл.
4. Ф-1..Ф-2 — фундамент (payload, версионирование), Ф-3..Ф-4 — поиск
   и интеграция с инструментами, Ф-5..Ф-6 — авто-переиндексация и фоновая
   индексация. Порядок менять можно, но релиз каждой фазы должен проходить
   верификацию.
