# PLAN: сжатие контекста с вытеснением в память (Ф-6 … Ф-11)

Развитие существующего сжатия истории агентского цикла (Speed 3,
`runner/compress.go`): сегодня старые пары assistant(toolcalls)+tool из
середины выбрасываются ДЕТЕРМИНИРОВАННО и БЕЗВОЗВРАТНО. Новый план добавляет
к позиционному сжатию «память» — вытеснение вместо уничтожения и компактные
заменители вместо пропажи фактов.

## Что уже есть

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

## Идеи-фичи

### Ф-6. Памятка о сжатии (notice)
При фактическом сжатии в «голову» (в последнее system-сообщение) дописываем
короткую памятку: история сжата, старые шаги убраны; если нужен контекст
убранных шагов — `CodeSearch`/`ReadFiles`; не изобретай — дочитай. Модель
сейчас не знает, что что-то выброшено, и не может пере-достать факт.
Флаг `CODEGEN_HISTORY_NOTICE` (по умолчанию выкл, как и само сжатие).

### Ф-7. Вытеснение выброшенного в векторную память (eviction)
Вместо безвозвратного выброса: содержимое выбрасываемых пар
(assistant + tool-результаты) индексируется в Qdrant как «эпизоды» проекта
(`rag.IndexEpisode`, payload kind=episode, псевдо-файл `@episode/…`). Позже
`CodeSearch` находит их по смыслу. Симметрия с Ф-5: там индекс синхронизируют
с мутациями файлов, здесь — с вытеснением контекста. Требует sink — сервиса
с доступом к rag-клиенту (передаётся через контекст, как `runevents.Reporter`).
Degrade: нет sink или нет Qdrant — выброс как раньше (тихо).
Флаги: `CODEGEN_HISTORY_BUDGET` (включает сжатие) + `CODEGEN_HISTORY_EVICT`.

### Ф-8. Retrieval-guided выбор кандидатов на выброс
Вместо «всегда старейшие» — ранжирование серединных юнитов по семантической
близости к текущей задаче (косин. подобие эмбеддингов). Релевантные пары
держим, далёкие выкидываем. Единица выброса — «юнит»: assistant с tool_calls +
его tool-результаты (инвариант: вызов без результата не остаётся).
Флаг `CODEGEN_HISTORY_RANK` (нужен sink с ранжировщиком).

### Ф-9. LSP-оглавления вместо сброшенного кода
Когда выбрасывается сообщение, несшее целый файл (ReadFiles/CodeSearch),
в памятку добавляется компактное documentSymbol-оглавление файла: имена +
диапазоны строк. Модель получает точные якоря и может точечно дочитать
нужный диапазон через `ReadFiles`/`LspDefinition`, а не читать файл заново.
Нужен новый метод `DocumentSymbols` в `tools/lspclient`. Флаг
`CODEGEN_HISTORY_OUTLINE` (нужен outliner из context-клиента).

### Ф-10. Абстрактивная компакция (summary-rollup)
Опциональный режим: выбрасываемая середина резюмируется в «протокол»
(решения, затронутые файлы, незакрытое) одним вызовом той же модели
(provider.ChatOnce, компактор в раннере). Резюме входит в памятку.
Не заменяет RAG: резюме — непрерывность, точные факты — CodeSearch.
Флаг `CODEGEN_HISTORY_COMPACT`; guard от tool_calls и пустого/длинного ответа.

### Ф-11. Бюджет по фактическому usage
Бюджет сейчас — символы (~4 сим/токен). Добавляем токен-лимит
`CODEGEN_HISTORY_TOKENS`: если провайдер вернул реальные `InputTokens` больше
лимита, следующий раунд сжимается жёстче (эффективный бюджет = лимит×4).
Дешевле и точнее, чем статичная оценка на старте.

### Инварианты
- Граница сжатия не разрывает юнит (assistant(toolcalls) + его tool-результаты).
- Сжатие не трогает единую очередь touched / LSP-хук (`runner/autofix.go`) —
  обрабатывается из того же дрена, от истории не зависит.
- Resume остаётся безопасным: сжатие детерминировано при одинаковом бюджете
  и опциональные шаги (RAG/LSP/компакция) не меняют чередования ролей.

## Архитектура

`runner.Generate` строит `CompressOptions` из контекстного `CompressionClient`
(как `runevents.ReporterFromContext`) + env + provider, и на каждом раунде
выполняет `CompressContext(ctx, messages, opts)` → `([Message], *CompressionReport)`.

Интерфейсы (маленькие, в `runner`):

```go
type EvictionSink interface { Evict(ctx, project string, items []EvictionItem) error }
type RelevanceRanker interface { Relevance(ctx, query string, texts []string) ([]float32, error) }
type FileOutliner interface { Outline(ctx, project string, relPath string) (string, error) }
type ContextCompactor interface { Compact(ctx, text string) (string, error) }

type CompressionClient struct {
    Project string
    Evict   EvictionSink
    Rank    RelevanceRanker
    Outline FileOutliner
}
```

Адаптеры живут в пакетах, где уже есть rag/=>lsp: `rag.Client` (IndexEpisode,
Similarity), LSP-клиент (`tools/lspclient` + `tools` адаптер оглавлений),
провайдерный компактор — в `runner`.

## Соединение
- CLI `main.go`: `rag.NewClientSafe` рядом с существующим вызовом `Generate`;
  `WithCompressionClient(ctx, …)`.
- Web `server/chatassist.go`: у ассистента уже есть rag-клиент и `FileOps` —
  добавить client в `rctx`.
- План `agents/planner/executor.go`: в `SetRAG` оборачивать провайдер, чтобы
  каждый шаг получал `CompressionClient` в контексте.

## Тесты
- `runner/compress_test.go`: сохранить существующие (базовый позиционный
  выброс). Новые: единичная целостность при ранжировании, памятка в системе,
  eviction-отчёт, компакция-фолбэк на усечение.
- Hermetic: fake-sink/ranker/outliner/compactor, не сеть.
- `rag/episodes_test.go`, `lspclient` documentSymbol — на фейковом/управляемом.

## Порядок реализации
1. Ф-6 + перестройка `runner/compress.go` (юниты, отчёт).
2. Ф-7 (rag IndexEpisode + sink), Ф-8 (Similarity + ранжировщик).
3. Ф-9 (DocumentSymbols + outliner).
4. Ф-10 (провайдерный компактор).
5. Ф-11 (usage-aware бюджет в цикле).
6. Соединение (main/server/planner), тесты, сборка.