# План: Qdrant — контекстная память (RAG) для ИИ-агентов

Статус: **Ф-1, Ф-2, Ф-3 выполнены** (клиент Qdrant + эмбеддинги; чанкинг и
полная индексация; инструмент `CodeSearch` у субагентов); **Ф-4 выполнена**
(планировщик: подмешивание контекста RAG); **Ф-5 выполнена** (авто-
обновление индекса после мутаций). Формат — как в `PLAN-webui.md`
/ `PLAN-lsp.md`: статус, решения, привязка к текущему коду, этапы с чекбоксами,
верификация. Обновлять файл по мере выполнения этапов (чекбоксы `[x]`).

## Цель

Дать агентам векторную память по кодовой базе: семантический поиск релевантных
кусков кода вместо плоской карты файлов (текущий лимит — 200 файлов в
`agents/planner/project_map.go`, `maxMapFiles=200`). Закрывает главную боль: на
большом коммерческом проекте с тысячами файлов планировщик «слепнет».

## Текущее состояние (что уже есть)

| Файл / сущность | Что есть сейчас |
|---|---|
| `compose.yaml:42` | сервис `qdrant` уже задекларирован (порты `6333` HTTP / `6334` gRPC, volume `qdrant`) — код его **не использует** |
| `readme.md:235-278` | пример работы с `github.com/qdrant/go-client` (gRPC 6334, `Distance_Cosine`) — заготовка-референс |
| `models/LLMProvider.go`, `models/OllamaModel.go` | интерфейс `Generate`/`ChatStream`; **эмбеддингов нет** — механизм надо добавить |
| `tools/registry.go:79` `newTool` | реестр инструментов; `CodeSearch` пока не зарегистрирован |
| `tools/tool.go:24` `Deps` | контекст инструментов (`FileOps/Session/Board`) — для RAG нужно опциональное поле (клиент Qdrant) |
| `agents/planner/project_map.go` | `BuildProjectMap` — карта проекта (только имена+размеры), лимиты `maxMapFiles=200`, `maxMapChars=10_000` |
| `runner/autofix.go` | хук после мутирующих инструментов (ЛСП-проверка по `FileOps.touched`) — сюда же встанет частичная переиндексация |
| `main.go:49-93` | CLI-разбор команд `listen/accept/serve`; команда `index` добавляется сюда же (по образцу) |
| `tools/fileops.go` | `List`/игнор-логика; обход дерева для индексатора переиспользует её |

## Решения пользователя (зафиксировано)

1. **Стек**: Qdrant по gRPC (`QDRANT_ADDR=localhost:6334`), коллекция
   `project_code_base`, метрика `Distance_Cosine`.
2. **Модель эмбеддингов**: нативная Ollama (`EMBEDDING_MODEL`, по умолчанию
   `nomic-embed-text`) — через HTTP `/api/embeddings` клиента Ollama; без
   отдельного сервиса эмбеддингов.
3. **Чанкинг**: по структуре проекта (законченные функции/методы/структуры со
   всеми сопутствующими комментариями, лимит ~50-100 строк), не резать «вслепую»
   по символам.
4. **Индексация**: CLI `go run . index <имя_проекта>` для полного прогона +
   частичная переиндексация изменённых файлов при авто-лечении.
5. **Retrieval**: инструмент `CodeSearch` у субагентов-разработчиков; у
   планировщика — подмешивание релевантных кусков кода в контекст перед
   построением плана.
6. **Безопасность контекста**: лимиты `RAG_MAX_RESULTS` (по умолчанию 3),
   `RAG_READ_MAX_TOTAL` (150000 символов).

## Новые компоненты

| Пакет / файл | Назначение |
|---|---|
| `rag/` | клиент Qdrant + эмбеддинги (ядро фичи): `client.go` (gRPC, create-if-not-exists), `embed.go` (Ollama `/api/embeddings`), `index.go` (чанкинг + upsert/delete), `search.go` (query → топ-N, фильтры по scope) |
| `rag/chunk.go` | модуль нарезки кода: функции/методы/структуры (Go — `go/ast`; TS/Python — структурные маркеры + строковый лимит ~100) |
| `tools/codesearch.go` | инструмент `CodeSearch` (`tools/registry.go:79`): вход `query` (+ опционально `scope`), выход — топ-N чанков `file:start-end` со сниппетом |
| `main.go` (`case "index"`) | команда полной индексации: обход дерева (путь `tools.List`/`WalkDir`, игнор `.git`/`node_modules`/бинарных/`.gitignore`), chunking + пакетный upsert |
| `agents/planner/ragcontext.go` | расширение `BuildProjectMap`: семантическая выборка по тексту глобальной задачи + подмешивание кусков кода (учёт `maxMapChars`) |
| `runner/reindex.go` | частичная переиндексация: после мутирующего шага перезаписать векторы `FileOps.touched` (переиспользуем хук `runner/autofix.go`) |
| `.env.example` | `QDRANT_ADDR`, `QDRANT_COLLECTION_NAME`, `EMBEDDING_MODEL`, `RAG_MAX_RESULTS`, `RAG_READ_MAX_TOTAL` |
| `docs/PLAN-qdrant.md` | этот план |

## Интеграции с существующим кодом

- **Реестр инструментов** — `tools/registry.go:79` (`newTool`): добавить
  `CodeSearch`; `tools.Deps` (`tools/tool.go:24`) — опциональный `*rag.Client`
  (никогда не падает: если Qdrant недоступен — `skipped` с подсказкой
  «используй ReadMap/ReadFiles», по образцу LSP degrade в `tools/lspcheck.go`).
- **Оркестрация** — `runner/runner.go` + `runner/autofix.go`: рядом с ЛСП-хуком
  вызывается переиндексация затронутых файлов (после мутации). Управляется
  флагом (по умолчанию выкл, пока RAG не собран).
- **Планировщик** — `agents/planner/project_map.go`: в `BuildProjectMap` при
  доступном RAG добавлять блок «релевантный код по задаче» (запрос к
  `rag.Search` с фильтром по проекту), не ломая текущий лимит (`maxMapChars`).
- **Разработчики** — наборы инструментов в `agents/developer`: добавить
  `CodeSearch` в `devToolNames`; строка промпта — «при чтении чужого кода
  используй CodeSearch по смыслу вместо сплошного чтения».
- **Фильтры по scope** — `rag.Search` принимает `scope` (server/frontend/…):
  маппинг из scope шага плана/роли агента (по образцу `agents/planner/lspgate.go`,
  функция `scopeSourceFiles`).

## API инструмента `CodeSearch`

```json
{
  "query": "где валидируется токен сессии",
  "scope": "server"
}
```

→ `{ "status": "success|skipped", "results": [
     { "file": "server/internal/auth/token.go", "start_line": 12, "end_line": 26,
       "score": 0.86, "snippet": "func ..." } ] }`

Лимит выдачи — `RAG_MAX_RESULTS`, суммарный объём текста — `RAG_READ_MAX_TOTAL`.

## Этапы и чеклист

### Ф-1 — Инфраструктура: клиент Qdrant + эмбеддинги
- [x] `rag/client.go`: gRPC-клиент (`QDRANT_ADDR`, порт 6334), инициализация и
      create-if-not-exists коллекции `project_code_base` (`Distance_Cosine`,
      размерность из модели эмбеддингов)
- [x] `rag/embed.go`: `Embed(text) ([]float32, error)` через Ollama —
      HTTP `/api/embeddings` (модель `EMBEDDING_MODEL`, по умолчанию
      `nomic-embed-text`)
- [x] Hermetic-тесты: fake-клиент Qdrant (интерфейс), маршрут эмбеддингов;
      degrade при недоступном Qdrant
- [x] `.env.example`: `QDRANT_ADDR`, `QDRANT_COLLECTION_NAME`, `EMBEDDING_MODEL`

### Ф-2 — Чанкинг и полная индексация
- [x] `rag/chunk.go`: структурная нарезка (функции/методы/структуры Go через
      `go/ast`; TS/Python — по маркерам определения + лимит ~100 строк);
      юнит-тесты на куски
- [x] `main.go`: команда `go run . index <имя_проекта>` — обход дерева
      (путь `filepath.WalkDir`, как `agents/planner/project_map.go`), игнор
      `.git`/`node_modules`/бинарных/`.gitignore`, chunking + пакетный upsert
- [x] `rag/index.go`: upsert точек с payload (`file_path`/`start_line`/`end_line`/
      `code_content`/`scope`), удаление старых векторов файла при переиндексации
- [x] Тесты: fake-клиент, содержимое payload, инкрементальность (повторный
      прогон не дублирует векторы)

### Ф-3 — Инструмент `CodeSearch` для субагентов
- [x] `tools/codesearch.go`: `query` → `Embed` → gRPC-поиск → топ-N с фильтром
      по `scope`; лимиты `RAG_MAX_RESULTS`/`RAG_READ_MAX_TOTAL`
- [x] Регистрация в `tools/registry.go:79`; `Deps` — опциональный `*rag.Client`;
      `skipped` при недоступном Qdrant (degrade)
- [x] `agents/developer`: `CodeSearch` в `devToolNames`, строка промпта
      («ищи по смыслу, не читай всё подряд»)
- [x] Hermetic-тесты инструмента (fake-search), формат ответа, `skipped`

### Ф-4 — Планировщик: подмешивание контекста RAG
- [x] `agents/planner/ragcontext.go`: в `BuildProjectMap` — семантическая
      выборка по тексту задачи (+ фильтр по проекту), блок «релевантный код»
      в карте (в рамках `maxMapChars`). Функция `BuildProjectMapRAG` +
      `stepRAGContext` (контекст по шагу в шаге плана) — фолбэк на обычную
      карту при недоступном RAG (degrade, `RAG_PLANNER_CONTEXT` по умолчанию вкл)
- [x] Scope-фильтры шагов: связка `scope` планировщика → фильтр Qdrant
      (по образцу `lspgate.go` `scopeSourceFiles`) — `ragScopeFromScope`:
      первый сегмент пути шага → payload `scope` (server/frontend/root);
      разнородный scope → весь проект
- [x] Тесты: с RAG и без (фолбэк на текущую карту), лимиты не превышаются
      (`ragcontext_test.go`: nil/ошибка/выключен/пустая задача/нет проекта,
      maxMapChars, scope-маппинг, контекст по шагу)

### Ф-5 — Авто-обновление при изменении кода
- [x] `runner/reindex.go`: после мутирующих инструментов переиндексировать
      `FileOps.touched` (переиспользуем хук `runner/autofix.go`); управление —
      env-флаг `RAG_AUTO_REINDEX` (по умолчанию `0`). Очередь touched дренится
      ОДИН раз (`AutoFixer.TakeTouched`) и раздаётся LSP-хуку и reindex
      (`Reindexer.ReindexTouched` → helper `runner.ReindexFiles`); удалённые
      мутацией файлы чистятся из индекса. Реализация интерфейса `Reindexer` —
      `agents/developer` (`base.ReindexTouched` через `rag.ScopeForPath`)
- [x] Тесты: мутация → переиндекс затронутых файлов, отсутствие дублей,
      выкл флагом (`runner/reindex_test.go`: координация двух хуков на одном
      дренаже, `ReindexFiles` — переиндекс/удаление/degrade на фейк-клиенте)
- [x] Документация в `readme.md` (env-таблица RAG: `RAG_AUTO_REINDEX`) и
      `.env.example` (блок RAG) — это и есть env-таблица RAG

### Ф-6 — Верификация
- [x] `go build . ./agents/... ./tools/ ./board/ ./rag/ ./runner/`
- [x] `go vet . ./agents/... ./tools/ ./board/ ./rag/ ./runner/`
- [x] `go test . ./agents/... ./tools/ ./board/ ./rag/ ./runner/`
  → все зелёные (`cached`/`ok`), сборок 15 пакетов, 0 ошибок
- [x] Ручной E2E на реальном проекте `my-rust-app` (33 файла, 60 чанков, dim=768):
  - `index my-rust-app` → 33 файла, 60 чанков; повторный прогон = 60 (инкрементальность, нет дублей)
  - `Search(query="инициализация HTTP-сервера", scope="")` → 3 релевантных чанка (score 0.67–0.70)
  - `Search(query="обработка HTTP", scope="server")` → 1 результат, `server/` (scope-фильтр корректен)
  - `BuildProjectMapRAG` → блок «Релевантный код по задаче» успешно подмешан в карту (2762 символа)
  - Degrade без индекса (несуществующий проект) → 0 результатов, без ошибки (фолбэк безопасен)
  - Degrade при недоступном Qdrant → `MapRAG` возвращается к обычной карте без RAG-блока

Статус **Ф-1–Ф-6: весь RAG-photoContext_FEATURE**.

## Верификация

```
go build . ./agents/... ./tools/ ./board/ ./rag/ ./runner/
go vet  . ./agents/... ./tools/ ./board/ ./rag/ ./runner/
go test . ./agents/... ./tools/ ./board/ ./rag/ ./runner/
# Ручной E2E (qdrant поднят через compose.yaml):
#   go run . index <имя_проекта> → в UI/RAG-инструменте виден результат поиска
```

Известные нюансы репозитория (не убирать):
- `go build ./...` падает на `temp/moon-distance/internal/middleware/cors.go`
  (артефакт сгенерированного проекта) — использовать перечень выше.
- Не форматировать `agents/acceptor/checks.go`, `agents/acceptor/run.go`.
- Комментарии/промпты/UI-тексты — на русском.
- Qdrant/эмбеддинг-модель могут отсутствовать: degrade обязателен
  (`skipped`), как ЛСП-серверы.

## Как продолжить

1. Открыть этот файл, прочитать «Решения пользователя» и «Этапы и чеклист».
2. Взять первый незачёркнутый пункт Ф-1, выполнить, отметить `[x]`, закоммитить.
3. После каждой фазы — верификация (блок выше) и краткая запись в этот файл.
