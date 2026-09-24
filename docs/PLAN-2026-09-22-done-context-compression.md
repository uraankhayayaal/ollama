# План: сжатие контекста с вытеснением в память (Ф-6 … Ф-11)

Статус: **ВЫПОЛНЕНО** (реализовано в коммите `ecaaa99`
«Сжатие контекста с памятью (Ф-6..Ф-11)», верификация зелёная 2026-09-24).
Формат — как остальные `PLAN-*.md`: текущее состояние (`file:line`), решения,
архитектурные решения, новые компоненты, этапы с чекбоксами, верификация.

Развитие существующего сжатия истории агентского цикла (Speed 3,
`runner/compress.go`): сегодня старые пары assistant(toolcalls)+tool из
середины выбрасываются ДЕТЕРМИНИРОВАННО и БЕЗВОЗВРАТНО. Новый план добавляет
к позиционному сжатию «память» — вытеснение вместо уничтожения и компактные
заменители вместо пропажи фактов.

## Что уже есть (база, сохранена)

- `runner/compress.go`: `CompressHistory(msgs, budget)` — позиционное сжатие;
  бюджет `CODEGEN_HISTORY_BUDGET` (символы, 0 = выкл по умолчанию);
  вызов в `runner.go:589` перед каждым раундом.
- RAG: `rag/` (Qdrant, эмбеддинги Ollama nomic-embed-text), инструмент
  `CodeSearch` (`tools/codesearch.go`), авто-переиндексация `RAG_AUTO_REINDEX`
  (`runner/reindex.go`), контекст RAG у шагов плана (`agents/planner/ragcontext.go`)
  и ассистента (`agents/chatassist/ragcontext.go`).
- LSP: `tools/lspclient` (долгоживущий сервер: definition/references/hover,
  диагностики), навигационные инструменты, авто-самоисправление
  `LSP_AUTO_FIX` (`runner/autofix.go`).
- Дозаправка контекста: модель запрашивает `NEED_*` — runner подтягивает
  карты кода через `ContextSupplier` (`runner/context_refill*`).

## Решения пользователя (зафиксировано)

1. Всё расширенное сжатие — **опционально, выключено по умолчанию**
   (флаги `CODEGEN_HISTORY_*`), базовый позиционный выброс и дефолтная
   страховка по окну провайдера работают всегда.
2. Побочные шаги (RAG/LSP/компакция) **не должны ронять генерацию**: ошибки
   опциональных сервисов уходят в `CompressionReport.Errors` и в `Debugf`
   (degrade, как RAG/LSP-инструменты).
3. Адаптеры живут в пакетах, где уже есть инфраструктура (`rag`, `tools`,
   `tools/lspclient`) и передаются в цикл **через контекст** (`runctx`
   собирает их один раз) — вместо оборачивания провайдера в executor.
4. Тесты — hermetic (fake-sink/ranker/outliner/compactor, без сети) и должны
   чистить env-флаги (`TestMain`).

## Архитектура (как реализовано)

- `runner.CompressContext(ctx, msgs, opts)` — единый пайплайн: позиционное
  ядро + опциональные вытеснение/ранжирование/оглавления/компакция/памятка;
  возвращает `([]Message, *CompressionReport)`.
- `CompressionClient` (`runner/compression.go:24`) — набор опциональных
  сервисов, кладётся в контекст `WithCompressionClient`/достаётся
  `CompressionClientFromContext` (паттерн `runevents.Reporter`). Поля —
  **функции**, а не интерфейсы: адаптеры живут в `rag`/`tools` без импорта
  runner (нет циклов), тесты подставляют fake.
- `runctx.WithCompression(ctx, project, dir, ragc)` (`runctx/runctx.go:25`) —
  единая точка сборки адаптеров: Outline из LSP `Outliner`, Evict →
  `rag.EvictionEpisodes` (под псевдо-проектом `@episodes:<project>`),
  Rank → `rag.Client.Similarity`. nil-rag — только LSP-оглавления.

Интерфейсы-функции (в `runner`, `CompressOptions` в `runner/compress.go:109`):

```go
type CompressOptions struct {
    Budget  int
    Project string
    Notice bool                       // Ф-6
    Evict   bool;  EvictFn   func(...EvictionItem) error   // Ф-7
    Rank    bool;  RankFn    func(...) ([]float32, error)  // Ф-8
    Outline bool;  OutlineFn func(...) (string, error)     // Ф-9
    Compact bool;  CompactFn func(...) (string, error)     // Ф-10
}
type CompressionReport struct {
    Dropped int; Evicted int
    EvictionItems []EvictionItem; Summary, Outlines, Notice string; Errors []string
}
```

Цикл: `runner/runner.go:618` вызывает `compressHistoryForRound` перед каждым
раундом; там же считается бюджет (env + Ф-11 по фактическому `InputTokens`
прошлого раунда + дефолтная страховка по окну провайдера,
`runner/compression.go:121` `providerInputCap`).

## Ф-6..Ф-11 (сверено с кодом, все чекбоксы закрыты)

- [x] **Ф-6. Памятка о сжатии (notice)** — при выбросе в последнее
      system-сообщение «головы» дописывается памятка: история сжата, дочитывай
      инструментами (CodeSearch/ReadFiles/LSP), не изобретай и не повторяй
      `NEED_*` (`runner/compress.go:492` `compressionNoticeMessage`, `:523`
      `attachNotice`; если system нет — памятка первым сообщением). Флаг
      `CODEGEN_HISTORY_NOTICE` (выкл). Тест:
      `TestCompressContextOutlineCompactNotice`, `TestCompressContextNoticeNoSystem`.
- [x] **Ф-7. Вытеснение выброшенного в векторную память (eviction)**
      — выброшенные юниты не уничтожаются: `EvictFn` → `rag.EvictionEpisodes`
      → `IndexEpisode` (`rag/episodes.go:25`) нарезает текст и грузит под
      псевдо-файлом `@episode/<source>.txt` в псевдо-проект
      `@episodes:<project>` (изоляция от кода проекта и от переиндексации).
      Degrade: нет sink/Qdrant — тихий выброс как раньше. Флаг
      `CODEGEN_HISTORY_EVICT`. Тесты: `TestCompressContextEvict`,
      `TestCompressContextEvictErrorDegrades`, `rag/episodes_test.go`.
- [x] **Ф-8. Retrieval-guided выбор кандидатов** — единица выброса «юнит»
      (assistant(tool_calls) + его tool-результаты, `splitUnits`,
      `runner/compress.go:160`); вместо «всегда старейшие» — ранжирование
      середины косинусной близостью к задаче (`rag.Client.Similarity`,
      `rag/episodes.go:54`, эмбеддеры Ollama без Qdrant); релевантные юниты
      держатся (лимит `RankLimit=6`, вписываются в остаток бюджета), ошибка
      ранжировщика — фолбэк на позиционный выброс. Флаг
      `CODEGEN_HISTORY_RANK`. Тесты: `TestCompressContextRankKeepsRelevant`,
      `TestCompressContextRankErrorFallsBack`.
- [x] **Ф-9. LSP-оглавления вместо сброшенного кода** — у выброшенного
      контента извлекаются пути файлов (маркеры `=== path ===` и JSON-поля
      `filename`/`file`, `extractFiles`, `runner/compress.go:230`) и для них
      запрашивается `DocumentSymbols` (`tools/lspclient/client.go:404`,
      `FormatOutline` `:508`, до `maxOutlineFiles=4`); оглавление (имя/тип :
      строка) кладётся в памятку как «карта» — модель точечно дочитывает.
      Адаптер — `tools/outliner.go:26` `OutlineFromOutliner`. Флаг
      `CODEGEN_HISTORY_OUTLINE`. Тесты: `TestCompressContextOutlineCompactNotice`.
- [x] **Ф-10. Абстрактивная компакция (summary-rollup)** — выброшенная середина
      резюмируется «протоколом» (решения/файлы/незакрытое) одним вызовом
      провайдера: `providerCompactor.Compact` (`runner/compression.go:91`) на
      нейтральном агенте `compactAgent` (нет инструментов, не форсирует
      tool_choice), лимит 150 слов + обрезка. Guard: пустой ответ / tool_calls
      → ошибка (не фатал). Резюме входит в памятку; не заменяет RAG. Флаг
      `CODEGEN_HISTORY_COMPACT`.
- [x] **Ф-11. Бюджет по фактическому usage** — `CODEGEN_HISTORY_TOKENS`
      (токены, 0 = выкл): если провайдер в прошлый раунд вернул
      `InputTokens > tokenBudget`, следующий раунд сжимается жёстче
      (эффективный бюджет = лимит×4 символов), `runner/compression.go:146`.
      Тест: `TestCompressClientUsageAwareBudget`.
- [x] **Страховка по умолчанию (вне флагов)** — даже без `CODEGEN_HISTORY_*`
      разросшаяся история не падает с 400 `exceed_context_size`: лимит входа =
      окно провайдера минус резерв под вывод (`ModelLimitsProvider`),
      срабатывает по фактическому usage и упреждающей оценке.
      Тест: `TestCompressHistoryProviderWindowDefault`.

### Инварианты (подтверждены тестами)
- Граница сжатия не разрывает юнит (assistant(toolcalls) + его tool-результаты,
  `splitUnits`); хвост никогда не открывается tool-юнитом;
  «висящие» tool-сообщения приклеиваются к предыдущему юниту.
- Сжатие не трогает очередь touched / LSP-хук (`runner/autofix.go`) —
  счётчики `allToolCalls`/`requiredDone` от истории не зависят.
- Resume безопасен: базовое сжатие детерминировано, опциональные шаги
  не меняют чередование ролей.

## Новые компоненты

| Файл | Назначение |
|---|---|
| `runner/compress.go` (перестройка) | юниты, `CompressContext`, отчёт, вытеснение/ранжирование/оглавления/памятка (Ф-6..Ф-9), совместимый `CompressHistory` |
| `runner/compression.go` | `CompressionClient` + контекст, `compressHistoryForRound` (бюджет Ф-11 + окно провайдера), `providerCompactor` (Ф-10) |
| `runctx/runctx.go` | единая сборка адаптеров сжатия (RAG-вытеснение/ранжирование, LSP-оглавления) для всех точек входа |
| `rag/episodes.go` | `IndexEpisode` / `EvictionEpisodes` (эпизоды под `@episodes:<project>`), `Similarity` (ранжирование без Qdrant) |
| `tools/outliner.go` | `OutlineFromOutliner` — функция оглавлений из LSP Outliner (Ф-9) |
| `tools/lspclient/client.go` (правка) | `DocumentSymbols` (`:404`), `Symbol`, `FormatOutline` (`:508`) |
| `server/chatassist.go:62`, `server/session.go:221` (правки) | `runctx.WithCompression` в контекст ассистента и канбан-сессии/агентов |
| `main.go:241,283,648` (правки) | CLI-канбан, второй CLI-режим и исполнитель плана — `runctx.WithCompression` |
| `runner/compress_test.go`, `runner/compression_test.go`, `rag/episodes_test.go` | hermetic-тесты (fake-sink/ranker/outliner/compactor), чистка env в `TestMain` |
| `docs/PLAN-2026-09-22-done-context-compression.md` | этот план (был `*-todo-*`, закрыт) |

## Соединение (фактическое)

- `runctx.WithCompression` — единственная точка; подключается
  у всех точек входа: CLI-канбан (`main.go:241`), второй/свободный CLI
  (`main.go:283`), исполнитель плана (`main.go:648` → контекст каждого шага),
  чат-ассистент (`server/chatassist.go:62`), канбан-сессии Web (архитектор,
  лиды, специалисты — `server/session.go:221`).
- Исполнитель плана не оборачивает провайдер в `SetRAG` (как предлагал
  черновик плана): контекст `WithCompression` уже несёт `CompressionClient`
  всем агентам цикла.

## API / Переменные окружения (новые, все выкл по умолчанию)

```
CODEGEN_HISTORY_BUDGET   — бюджет в символах (0 = выкл; сжатие неактивно без него,
                           во включённом виде есть только страховка по окну провайдера)
CODEGEN_HISTORY_TOKENS   — токен-лимит по фактическому usage (Ф-11, 0 = выкл)
CODEGEN_HISTORY_NOTICE   — памятка модели о сжатии (Ф-6)
CODEGEN_HISTORY_EVICT    — вытеснение выброшенного в RAG-эпизоды (Ф-7)
CODEGEN_HISTORY_RANK     — семантический выбор кандидатов (Ф-8)
CODEGEN_HISTORY_OUTLINE  — LSP-оглавления убранных файлов (Ф-9)
CODEGEN_HISTORY_COMPACT  — компакция середины моделью (Ф-10)
```

## Этапы и чеклист

- [x] Ф-6: памятка (notice) + перестройка `runner/compress.go` (юниты, отчёт)
- [x] Ф-7: `rag/episodes.go` (IndexEpisode/EvictionEpisodes) + sink в цикле
- [x] Ф-8: `rag.Similarity` + ранжировщик (keep релевантных, фолбэк на позиционный)
- [x] Ф-9: `DocumentSymbols` в `lspclient` + `tools/outliner.go`
- [x] Ф-10: `providerCompactor` (нейтральный агент, guard от tool_calls)
- [x] Ф-11: usage-aware бюджет в `compressHistoryForRound`
- [x] Соединение: `runctx` + main/server (+ страховка по окну провайдера)
- [x] Тесты: `runner/compress_test.go` (базовые сохранены),
      `runner/compression_test.go` (9 новых), `rag/episodes_test.go` — hermetic
- [x] Полировка: env-флаги `CODEGEN_HISTORY_*` задокументированы
      в `docs/project-map.md` и `.env.example`

## Верификация (2026-09-24, зелёная)

```
go build . ./agents/... ./tools/ ./board/ ./rag/ ./runctx/ ./runner/ ./workspace/
go vet  . ./agents/... ./tools/ ./board/ ./rag/ ./runctx/ ./runner/ ./workspace/
go test . ./agents/... ./tools/ ./board/ ./rag/ ./runctx/ ./runner/ ./workspace/
go test ./server/ ./agents/planner/
npm run build   # web/
```

Результат: сборка/vet — 0 ошибок; тесты зелёные; в `./server/` те же 2
пред-существующих флака чат-ассистента, что и на чистой базе
(`TestChatAssistantCreatesBugAndTask`, `TestChatAssistantDeleteTaskAfterConfirm`) —
к сжатию контекста отношения не имеют.

Известные нюансы репозитория (не убирать):
- `go build ./...` падает на `temp/moon-distance/internal/middleware/cors.go`
  (артефакт сгенерированного проекта) — использовать перечень выше.
- Не форматировать `agents/acceptor/checks.go`, `agents/acceptor/run.go`.
- Комментарии/промпты/UI-тексты — на русском.

## Связанные планы

- `PLAN-2026-09-19-done-lsp.md` — LSP-навигация: Ф-9 использует `lspclient`
  (`DocumentSymbols` добавлен к long-running серверу).
- `PLAN-2026-09-19-done-qdrant.md` — RAG/Qdrant: Ф-7 добавляет
  «эпизоды» (псевдо-проект `@episodes:<project>`) к `IndexProject`;
  Ф-8 — `Similarity` на тех же эмбеддерах без новых коллекций.
- `PLAN-2026-09-24-wip-architect-intelligence.md` — независимая сессия
  (архитектор + RAG/AskUser/fоновая индексация); сжатие в канбан-сессиях
  подключается через тот же `runctx.WithCompression` (`server/session.go:221`).