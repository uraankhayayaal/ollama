# План: Branch-Aware RAG — версионированный семантический поиск по веткам Git

Статус: **РЕАЛИЗОВАНО (Ф-1..Ф-7)**, остался ручной E2E на живом Qdrant
(инфраструктура и модель эмбеддингов — у пользователя; в CI/hermetic-прогонах
недоступны). Формат — как остальные `PLAN-*.md`: текущее состояние
(`file:line`), решения пользователя, архитектурные решения, новые компоненты,
этапы с чекбоксами, верификация. Верификация 2026-09-27:
`go build`/`go vet`/`go test` (в т.ч. `./rag/`, `./runner/`, `./server/`) и
`npm run build` (web/) — зелёные.

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
    PayloadContentHash = "content_hash"   // добавлено при реализации, см. ниже
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
- `content_hash` — отпечаток содержимого чанка (FNV-1a 64 в hex), добавлен при
  реализации: агент мутирует рабочее дерево **до** коммита, поэтому один и тот
  же `commit_sha` может прийти с ДРУГИМ содержимым. Без отпечатка проверка
  «уже проиндексировано» по одному лишь коммиту пропускала бы правку, и
  авто-переиндексация (Ф-5) оставляла бы агенту устаревший код. ID точки
  (`pointID`) тоже включает отпечаток — иначе новая версия затирала бы старую.

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

Это идемпотентно: повторная индексация с тем же `commit_sha` и тем же
`content_hash` не плодит дубликаты (такие чанки пропускаются, эмбеддинг не
повторяется).

Реализация (`rag/index.go`, функция `upsertChunks`):

- активные точки ветки читаются одним `Scroll` по фильтру
  `project + branch + replaced_by is empty` (постранично, предел
  `scrollMaxPoints`) и сводятся в карту `chunk_id → версия`;
- прежние версии помечаются `SetPayload(replaced_by=commit)` пакетами по ID
  (`Wait: true` — старая версия должна уйти из выдачи ДО подчистки);
- новая версия грузится `Upsert` с `branch`/`commit_sha`/`chunk_id`/
  `content_hash` и БЕЗ `replaced_by` (нет поля = `replaced_by` пуст);
- точки без `chunk_id` (индекс, собранный до версионирования) получают
  синтетический ключ `L<start_line>` — версионирование и исключение
  перекрытых версий их тоже затрагивают;
- `pruneSuperseded` подчищает версии файла, устаревшие НЕ текущим коммитом
  (остаётся одна предыдущая версия — след чанка до последнего изменения), иначе
  индекс рос бы бесконечно;
- пустой `commit_sha` (проект без Git) — прежние точки файла удаляются по ID
  целиком, как раньше (решение №7), `replaced_by` не проставляется.

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

**Реализация отличается от наброска выше** (`rag/search.go`): список
исключений не попадает в фильтр запроса (в наброске `NewHasID` — это ID
ТОЧЕК Qdrant, а не `chunk_id`, то есть условие просто не сработало бы).
Вместо этого:

- фильтр запроса — `Must(project, scope)` + `Should(активные ветка,
  активные main)`, legacy-точки без поля `branch` считаются кодом main;
- перекрытые версии отсекаются ВТОРЫМ проходом по результатам
  (`dropOverriddenByBranch`): отдельным `Scroll` собираются активные точки
  ветки **по файлам результатов** (десятки чанков, а не весь индекс проекта),
  и из выдачи убираются результаты main с тем же `chunk_id`;
- запрос к Qdrant шире итоговой выдачи (`branchOverfetch` = 3, минимум 12) —
  часть результатов может отсеяться как перекрытая;
- порядок сборки выдачи: перекрытие → лимит результатов → лимит
  `RAG_READ_MAX_TOTAL`. Иначе лимиты потратились бы на дубли одного кода.

### Р-5: Инструмент `CodeSearch` — параметр `branch`

`tools/codesearch.go` — `CodeSearchParams` получает опциональное поле
`branch`. Если не указано — определяется из контекста:

- Если проект — gitflow (есть ветка эпика/задачи) — берём текущую ветку.
- Иначе — `main`.

Результаты дополняются метаданные: `branch`, `commit_sha`, `chunk_id`.
Формат сниппета: `[Файл: src/auth.go | Ветка: ai/epic/ARCH-01 | Коммит: a1b2c3d]`.

### Р-6: Определение текущей ветки агентом

Фактически определение ветки/коммита вынесено в `rag/branch.go`
(`DetectBranch`, `DetectCommit`, `DetectIndexOptions`, константа `MainBranch`),
а `tools` (`rag.DetectBranch` + кэш в `codeSearchTool`) — тонкая обёртка.
Общий код нужен и `server`, и `runner`, и `main.go` (CLI-индексация);
git-вызовы инъектируются (`rag.gitRunner`) для hermetic-тестов.

`tools/codesearch.go` — функция `detectBranch()`:
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
| `rag/chunk.go` (правка) | `Chunk.Symbol`, `ChunkID(project, file, symbol)`, `chunkKey` |
| `rag/index.go` (правка) | `IndexOptions{Branch, CommitSHA}`; версионирование чанков, `Scroll`/`SetPayload` активных версий, `pruneSuperseded` |
| `rag/search.go` (правка) | `SearchParams.Branch`, `SearchResult.Branch/CommitSHA/ChunkID`, фильтр `should` + второй проход |
| `rag/branch.go` (новый) | `MainBranch`, `DetectBranch`, `DetectCommit`, `DetectIndexOptions` |
| `rag/client.go` (правка) | `QdrantStore`: +`Scroll`, +`SetPayload` |
| `rag/status.go` (правка) | `ProjectInfo` считает только активные версии (`replaced_by` пуст) |
| `rag/episodes.go` (правка) | эпизоды индексируются в `main` без версии (детерминированный ID) |
| `tools/codesearch.go` (правка) | `CodeSearchParams.Branch`, `detectBranch` с кэшем, шапка `[Файл: … | Ветка: … | Коммит: …]`, `ragLimits`/`ragSearchTimeout` (вынесены при переработке файла) |
| `runner/reindex.go` (правка) | `ReindexFiles(..., rag.IndexOptions)` — версионирование при авто-переиндексации |
| `agents/developer/developer.go` (правка) | `ReindexTouched` передаёт `rag.DetectIndexOptions(OutputDir)`; в системный промпт — правило «поиск ветко-осознанный» |
| `server/ragindex.go`, `server/actions.go`, `server/server.go` (правка) | `IndexBackground(ctx, branch)`, параметр `branch` в мосте и в `POST /api/projects/{id}/index?branch=` |
| `main.go` (правка) | CLI-индексация: `rag.DetectIndexOptions` печатает ветку/коммит |
| `docs/PLAN-2026-09-27-done-branch-aware-rag.md` | этот план |

## Интеграции с существующим кодом

- **`tools.Deps`** (`tools/tool.go:34`) — уже имеет `RAG`. Для определения
  ветки используем `FileOps.OutputDir` — новых зависимостей не нужно.
- **`rag.Client`** — интерфейс `QdrantStore` (`rag/client.go`) расширен
  `Scroll`/`SetPayload`: без них версионирование невозможно (найти прежнюю
  точку чанка и пометить её `replaced_by`). Методы есть в самом gRPC-клиенте
  Qdrant, реализация — тонкая обёртка, поэтому fake-магазины в тестах
  пришлось дополнить.
- **`CodeSearch`** — `tools/codesearch.go:29` — добавляем параметр `branch`
  и метаданные в результат. Обратная совместимость: `branch` опционален,
  при отсутствии — `main`.
- **`IndexBackground`** (`server/ragindex.go`) — фоновая индексация
  дополнительно принимает `branch` (по умолчанию — текущая ветка проекта).
- **`ResolveGitConflicts`** (`tools/gitresolve.go`) — при резолве конфликтов
  переиндексация идёт с проставлением `replaced_by` для старых чанков.
- **Hermetic-тесты** — `rag/memstore_test.go` (новый): in-memory `QdrantStore`
  с семантикой фильтров Qdrant (must/must_not/should, match, is_null, is_empty,
  has_id) и косинусным ранжированием. На нём проверяется поведение, ради
  которого план и делался: какая версия чанка активна, что попадает в выдачу
  поиска по ветке, изоляция веток, идемпотентность. `fakeStore` (client_test.go)
  дополнен `Scroll`/`SetPayload`; `textEmbedder` даёт чанкам разные векторы.

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
- [x] `rag/chunk.go`: `Chunk.Symbol` — извлечение имени функции/метода/типа
      для Go (из `ast`), TS/Python (из маркера), fallback — пустая строка
- [x] `rag/chunk.go`: `ChunkID(project, file, symbol)` — FNV-1a от
      `project + file + symbol` (или `start_line` если symbol пустой)
- [x] `rag/index.go`: константы payload `PayloadBranch`, `PayloadCommit`,
      `PayloadChunkID`, `PayloadReplacedBy`
- [x] `rag/index.go`: `upsertChunks` — загрузка с `branch`, `commit_sha`,
      `chunk_id`, `replaced_by=null`
- [x] Тесты: `ChunkFile` возвращает символы для Go; `ChunkID`
      детерминирован; payload содержит новые поля

### Ф-2 — Версионирование чанков при индексации
- [x] `rag/index.go`: `IndexProject` и `IndexFile` принимают `branch`,
      `commitSHA`; перед загрузкой новых чанков — поиск активных версий
      по `chunk_id` и проставление `replaced_by`
- [x] `rag/index.go`: идемпотентность — повторная индексация с тем же
      `commit_sha` не плодит дубликаты
- [x] `rag/index.go`: удаление файла — все чанки файла в ветке получают
      `replaced_by`
- [x] Тесты: версионирование (старая точка получает replaced_by), повторная
      индексация идемпотентна, удаление файла помечает чанки

### Ф-3 — Поиск с учётом ветки
- [x] `rag/search.go`: `SearchParams.Branch`; фильтр `should` (активные ветка +
      активные main, legacy-точки без `branch` считаются main)
- [x] `rag/search.go`: сбор актуальных `chunk_id` ветки (`Scroll` по файлам
      результатов) и исключение перекрытых версий main — второй проход вместо
      списка исключений в фильтре (см. Р-4)
- [x] `rag/branch.go`: `DetectBranch(dir)`, `DetectCommit(dir)`,
      `DetectIndexOptions(dir)`, `MainBranch` — `git rev-parse`, вне git main
- [x] Тесты: `TestSearchBranchUnionFilter`, `TestSearchDropsOverriddenMainVersion`
      (ветка не видит чужие ветки, перекрытый main отсечён),
      `TestSearchMainIgnoresFeatureBranch`, `TestDetectIndexOptions`

### Ф-4 — Инструмент `CodeSearch` — параметр `branch` и метаданные
- [x] `tools/codesearch.go`: `CodeSearchParams.Branch`; при отсутствии —
      `detectBranch(ops)`
- [x] `tools/codesearch.go`: результаты дополняются `branch`, `commit_sha`,
      `chunk_id`; сниппет — `[Файл: ... | Ветка: ... | Коммит: ...]`
- [x] `tools/codesearch.go`: текст промпта агента — «ты находишься в ветке
      X; при поиске кода всегда указывай текущую ветку»
- [x] Тесты: `branch` передаётся в `Search`, метаданные в результатах,
      fallback на main, маркеры версии в сниппетах

### Ф-5 — Интеграция с авто-переиндексацией
- [x] `runner/reindex.go`: `ReindexFiles(..., opts rag.IndexOptions)` —
      версионирование чанков (прежние версии получают `replaced_by`)
- [x] `agents/developer/developer.go`: `ReindexTouched` определяет
      `rag.DetectIndexOptions(OutputDir)` (ветка worktree + HEAD)
- [x] Тесты: `TestReindexFiles` (опции индексации прокидываются в каждый вызов),
      `TestIndexFileSameCommitNewContentReindexed` (незакоммиченная правка
      попадает в индекс), `TestIndexFileVersioningReplacesOldChunk`

### Ф-6 — Фоновая индексация с веткой
- [x] `server/ragindex.go`: `IndexBackground` принимает `branch`
      (по умолчанию — текущая ветка проекта); передаёт в `IndexProject`
- [x] `tools/codesearch.go`: `CodeSearch` при отсутствии `branch` —
      `detectBranch` из `OutputDir`
- [x] Тесты: фоновая индексация с веткой, поиск по ветке возвращает чанки
      только из этой ветки + main без перезаписанных

### Ф-7 — Верификация и полировка
- [x] `go build . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/ ./runner/`
- [x] `go vet  . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/ ./runner/`
- [x] `go test . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/ ./runner/`
- [x] `npm run build` (web/) — зелёный
- [ ] Ручной E2E (пользователь, живой Qdrant): проект с gitflow, два эпика с
      общим файлом — поиск из ветки эпика А не видит изменения эпика Б

## Верификация

```
go build . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/ ./runner/
go vet  . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/ ./runner/
go test . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/ ./runner/
npm run build   # web/
```

Результат 2026-09-27: всё зелёное. Новые hermetic-тесты:
`rag`: `TestIndexProjectPayload`, `TestIndexProjectSameCommitIdempotent`,
`TestIndexFileSameCommitNewContentReindexed`, `TestIndexFileVersioningReplacesOldChunk`,
`TestIndexFileBranchIsolation`, `TestIndexFileWithoutCommitHardDeletes`,
`TestDeleteFileMarksSuperseded`, `TestIndexProjectVanishedFileSuperseded`,
`TestSearchBranchUnionFilter`, `TestSearchDropsOverriddenMainVersion`,
`TestSearchMainIgnoresFeatureBranch`, `TestDetectBranch/DetectCommit/DetectIndexOptions`,
`TestChunkSymbolGoTSPython`, `TestChunkIDStableAcrossCommits`,
`TestChunkSymbolNoSymbolForCall`, `TestPointIDDeterministic`;
`tools`: `TestCodeSearchExplicitBranch`, `TestCodeSearchBranchAutoDetected`,
`TestCodeSearchBranchWithoutOutputDir`, `TestCodeSnippetHeaderNoCommit`;
`runner`: `TestReindexFiles` (опции ветки/коммита);
`server`: `TestIndexBackgroundBridgeBranch`, `TestRESTProjectIndex` (ветка из
query-параметра), `TestIndexProjectRAGHermetic`, `TestSessionIndexBackground`
(отчёт финальной индексации, а не «запущена»).

Найденные при тестах и исправленные дефекты реализации:

1. `IndexProject` искал исчезнувшие файлы по КЛЮЧАМ карты `chunk_id`, а не по
   значениям (поле `file`): удалённые мутацией файлы не помечались устаревшими
   и оставались в выдаче поиска (тест
   `TestIndexProjectVanishedFileSuperseded`).
2. `activeBranchCond` для main собирал `Must: branch=main` + `Should: is_null(branch)`
   — взаимоисключающие условия, из-за чего поиск из main не возвращал NOTHING
   (тесты на `memStore` с реальной семантикой фильтров).
3. `SetPayload(replaced_by)` был без `Wait` — гонка между обновлением payload и
   `pruneSuperseded`/поиском.
4. Проверка идемпотентности по одному `commit_sha` теряла незакоммиченные
   правки агента (главный сценарий авто-переиндексации) — введены
   `content_hash` в payload и в `pointID`.

Плюс один флак самого теста (не реализации): `memStore.codeOf(chunk_id)` брал
первую активную точку без учёта ветки — при двух активных версиях одного
чанка (в `main` и в ветке эпика) результат зависел от порядка обхода map.
`TestIndexFileBranchIsolation` падал примерно в 30% прогонов; хелпер стал
`codeOf(branch, chunkID)`. Напоминание: `chunk_id` НЕ уникален внутри проекта
между ветками (он делится версиями), уникальной точку делает пара
`(branch, chunk_id, commit_sha, content_hash)`.

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

## Что осталось пользователю (ручной E2E)

Живой Qdrant + модель эмбеддингов + проект с gitflow:

1. `go run . index <проект> [--branch <ветка>]` — индекс собрать на `main`.
2. Создать два эпика, в каждом задачу, правившие ОДИН И ТОТ ЖЕ файл
   (`RAG_AUTO_REINDEX=1`, чтобы переиндексация шла после мутаций).
3. В задаче эпика А вызвать CodeSearch (или дать агенту искать изменённую
   функцию) и убедиться: в выдаче есть своя версия файла и `main`, но НЕТ
   изменений эпика Б; в шапке сниппета — `[Файл: … | Ветка: ai/epic/<А> | …]`.
4. `POST /api/projects/<id>/index?branch=ai/epic/<Б>` — индекс ветки Б;
   повторить поиск из А: изменения Б по-прежнему не видны.
5. Проверить `RagIndexStatus` архитектора: `chunks` считает только активные
   версии (устаревшие поколения в счёт не идут).

## Известные нюансы (не блокеры)

- Payload-индексы Qdrant для `branch`/`chunk_id`/`replaced_by` не создаются:
  при личных объёмах (десятки тысяч точек) фильтр идёт сканом, что заметно
  только на очень больших индексах. Если вырастет — добавить
  `PayloadSchema` с keyword-индексами в `EnsureCollection`.
- `QdrantStore` расширен `Scroll`/`SetPayload`: любая внешняя реализация
  интерфейса (вне этого репозитория) требует дописать эти два метода.
- Версии одного чанка, помеченные устаревшими ОДНИМ коммитом (несколько
  незакоммиченных правок подряд до коммита), хранятся до следующего коммита —
  `pruneSuperseded` подчищает их при первой индексации следующего коммита.
