# Карта проекта

Назначение каждой папки и ключевых файлов. Навигация по *документации* —
в [оглавлении](../README.md); здесь про *код*.

Модуль Go: `ai`, версия `go 1.26.5`. Зависимости: `github.com/ollama/ollama`,
`openai-go`, `go-redis/v9`, `miniredis/v2`, `godotenv`, `qdrant/go-client`.

---

## Корень

| Файл/папка | Назначение |
|---|---|
| `main.go` | CLI: выбор провайдера, диспетчеризация команд, env-хелперы |
| `Makefile` | **Только** `ollama pull` моделей. Не билд-система проекта — не путать с `temp/<проект>/Makefile` |
| `Modelfile` | Ollama-модель по умолчанию (`FROM qwen3.6:35b-a3b-q4_K_M`, `num_ctx 131072`) |
| `compose.yaml` | Qdrant (gRPC `56334`) и Redis (`56379`) для RAG, чекпоинтов и доски |
| `.env` / `.env.example` | Конфигурация (`godotenv`). Примеры по машинам: `.env.macbook.air`, `.env.macbook.m3`, `.env.rtx5070ti` |
| `readme.md` | Краткий вход: быстрый старт и ссылка на `docs/` |
| `AGENTS.md` | Правила для агентов-помощников (opencode) |
| `docs/` | Документация (этот каталог) |
| `temp/` | Рабочее хранилище сгенерированных проектов. **Не коммитится**, не редактируется вручную |
| `archive/` | Архивные/учебные проекты |
| `logs/` | Файлы логов (создаётся автоматически) |

## Пакеты

### Агенты — `agents/`

| Файл/папка | Назначение |
|---|---|
| `AgentDefinition.go` | Интерфейс `Agent` и базовая реализация |
| `tokeneconomy.go` | Учёт расхода токенов на уровне агента |
| `architect/` | Системный архитектор: публикует эпики, ревизии, вердикты по багам. `ragcontext.go` — RAG-контекст |
| `backendlead/` | Декомпозиция бэкенда на JSON-задачи (Tech Lead) |
| `frontendlead/` | Декомпозиция UI, стейта и API-контрактов |
| `devopslead/` | Декомпозиция инфраструктуры (без LSP-инструментов) |
| `qalead/` | Тест-план по контрактам, триаж багов |
| `developer/` | Backend/frontend-разработчики. `readme.go` — генерация `readme.md` |
| `devops/` | DevOps-инженер: Docker Compose, K8s, CI/CD |
| `qaengineer/` | QA-инженер: автотесты, баг-репорты |
| `codereviewer/` | Код-ревью MR (GitLab/GitHub), `README.md` внутри |
| `chatassist/` | Чат-ассистент Web UI, `ragcontext.go` |
| `acceptor/` | Приёмка без LLM: `accept.go`, `config.go`, `detect.go` (автодетект + Makefile), `checks.go`, `run.go`, `lsp.go`, `report.go`, `plan.go` |
| `planner/` | Планировщик и оркестрация: `planner.go`, `plan.go`, `executor.go`, `kanban.go`, `lspgate.go`, `project_map.go`, `ragcontext.go`, `tokens.go`, `plan_doc.go` |
| `promptcheck/` | Проверка качества промптов |

### Агентский цикл — `runner/`

| Файл | Назначение |
|---|---|
| `runner.go` | Сам цикл раундов, `ModelLimits`, оценка токенов, параллельные вызовы инструментов |
| `compress.go` | Сжатие истории под символьный/токенный бюджет |
| `compression.go` | Ф-6…Ф-11: памятка, вытеснение, ранжирование, оглавления, компакция |
| `reindex.go` | Авто-переиндексация RAG после мутаций файлов |
| `autofix.go` | Авто-починка кода по LSP-диагностикам |
| `applog.go` | Автоподмешивание логов приложения в промпт |
| `debug.go` | Отладочный вывод |
| `runctx/` | Сборка сервисов сжатия для CLI, планировщика и сервера |
| `runevents/` | События цикла (сообщения, `message_delta`, tool-call, токены) |
| `runmetrics/` | Реестр метрик + Prometheus-экспорт (`prom.go`) |

### Инструменты — `tools/`

| Файл | Назначение |
|---|---|
| `registry.go` | Реестр инструментов: `Select`, `Add`, `Execute`, фабрика `newTool` |
| `tool.go`, `ToolDefinition.go` | Интерфейс инструмента, описание для провайдера |
| `fileops.go` | `WriteFiles`, `ReadFiles` (окна строк, лимиты), `List`, `Run` (лимиты вывода) |
| `filelock.go` | Пер-проектная блокировка файловых мутаций |
| `searchreplace.go`, `patch_ops.go`, `astpatch.go`, `gopatch.go` | Точечные правки: `SearchReplace`, `PatchFunction`, `PatchGoFunction` |
| `snapshot.go` | Снимки дерева файлов, Diff/Restore, scoped-снимки |
| `skeleton.go` | Скелетон-кеш (`path|size|mtime`) |
| `contract.go` | Снимок публичного API (Go) и его сравнение |
| `codesearch.go` | `CodeSearch` — семантический поиск по RAG |
| `ragstatus.go` | `RagIndexStatus` — состояние индекса проекта |
| `lspcheck.go` | `LspCheck` — диагностики |
| `lspnav.go` | `LspDefinition`, `LspReferences`, `LspHover` |
| `lspnative.go` | Нативные `publishDiagnostics` вместо CLI-чекеров |
| `outliner.go` | `documentSymbol` — оглавления файлов (Ф-9) |
| `lspclient/` | JSON-RPC клиент, менеджер серверов, `servers.go` — выбор сервера по стеку |
| `board_tools.go` | 17 инструментов `Board*` |
| `review_tools.go`, `reviewsession.go` | `ReviewMr`, `ApproveMr`, `NextChunk` |
| `textreview.go` | Текстовые ревью в диалоге |
| `bugreports.go` | `BoardCreateBugReport` и прочие баг-инструменты |
| `chunks.go`, `difffilter.go`, `diffindex.go` | Разбиение и фильтрация диффов |
| `stacktool.go` | `DetectStack` — стек, роли, маркеры, Makefile |
| `sandbox.go` | Исполнение `Run` в Docker или на хосте |
| `destructive.go` | Отбраковка разрушительных команд (до запуска) |
| `gitresolve.go` | `ResolveGitConflicts` — разбор конфликтов для агента |
| `webtools.go` | `WebSearch` |
| `applogs.go` | `ReadAppLogs` |
| `autofix.go` | Правки по диагностикам |
| `codegen_tools.go`, `ParseArguments.go`, `ollama.go` | Определения, разбор аргументов, маппинг в формат Ollama |
| `parallel.go` | Параллельное выполнение инструментов |
| `binpath/` | Поиск бинарников (PATH, `~/go/bin`, npm/nvm, Homebrew) |

### RAG — `rag/`

| Файл | Назначение |
|---|---|
| `index.go` | Индексация, версионирование по веткам, `pruneSuperseded`, `pointID` |
| `search.go` | Поиск: `activeBranchCond`, `dropOverriddenByBranch`, overfetch |
| `chunk.go` | Разбиение на чанки, `ChunkID`, `chunkKey` |
| `client.go` | Qdrant gRPC-клиент (`Scroll`, `SetPayload`, `Upsert`, `Query`), `QdrantStore` |
| `branch.go` | `MainBranch`, `DetectBranch`, `DetectCommit`, `DetectIndexOptions` |
| `status.go` | `ProjectInfo` — состояние индекса (только активные версии) |
| `walk.go` | Обход проекта с учётом `.gitignore`, бинарников, скрытых файлов |
| `embed.go` | Эмбеддинги через Ollama |
| `episodes.go` | Эпизоды вытеснения (Ф-7) |

### Сервер и веб — `server/`, `web/`

| Файл/папка | Назначение |
|---|---|
| `server.go` | Маршруты REST, статика, вычисление статуса проекта |
| `hub.go` | WebSocket-хаб, `Emit` — внутренняя шина |
| `session.go` | Оркестрация в Web UI, AskUser, фоновая индексация, снимки |
| `actions.go` | Серверные мосты-инструменты (`KanbanStart`, `TaskMerge`, `EpicRelease`, `AskUser`, `IndexBackground`) |
| `ask.go` | Структурированные вопросы пользователю |
| `gitflow.go`, `gitflow_auto.go`, `gitflow_resolve.go`, `gitflow_ar.go` | Ветки, авто-merge, синхронизация main, LLM-резолв конфликтов |
| `diff.go` | Ленивые диффы с кэшем |
| `auth.go`, `ratelimit.go` | Логин, сессия, CSRF, rate-limit |
| `applog.go`, `logbroker.go` | Логи приложения и процесса |
| `metrics.go` | Метрики проекта |
| `ragindex.go` | Фоновая индексация RAG (single-flight) |
| `chatassist.go` | Точка входа чат-ассистента |
| `static.go` | Отдача встроенного фронтенда |
| `web/src/` | React-приложение: `Api/`, `Types/`, `Components/` (Dashboard, Chatboard, Diffboard, Logboard, GateBanner, Login и др.), `live.ts` |
| `web/webui.go` | `//go:embed all:dist` |

### Git, форджи, доска, хранилища

| Файл/папка | Назначение |
|---|---|
| `gitops/` | Git-изоляция: `gitops.go`, `merge.go`, `taskmerge.go`, `conflicts.go`, `submodule.go`, `count.go`, `cli.go` |
| `forges/` | Реестр форджей (`registry.go`), интерфейс `Forge` (`forges.go`), `github/`, `gitlab/`, `local.go`, `scope.go`, `ignore.go` |
| `board/` | `entity.go` (Epic/Task/BugReport, статусы, переходы), `store.go` (Redis), `tokens.go` |
| `checkpoint/` | Чекпоинты плана в Redis |
| `chat/` | Redis Streams: история чата и pub/sub |
| `workspace/` | Реестр проектов (`registry.go`), карта веток (`branches.go`) |
| `projects/` | `ProjectDir` (`temp/<имя>`), `config.go` (CODEGEN_*) |
| `tokens/` | `scoped.go`, `pricing.go`, `predict.go`, `history.go`, `store.go` |
| `models/` | `resolve.go` (выбор провайдера), `OllamaModel.go`, `AlisaDefinition.go`, `TrimProvider.go`, `layered.go`, `ModelSettings.go` |
| `logging/` | Файлы логов, `LOG_DIR` |
| `langdetect/` | Определение языка по диффу (21 язык) |
| `stackdetect/` | Определение стека по маркерам проекта |
| `repositories/` | Клонированные репозитории |
| `services/mrlistener/` | Слушатель новых MR (`listen`) |
| `sandbox/` | `Dockerfile` и `compose.yaml` для образа песочницы |

## Связанное

- [Архитектура](architecture.md)
- [Что это за проект](what-is-it.md)
- [Инфраструктура](../30-reference/infrastructure.md)
- [Инструменты агентов](../30-reference/agent-tools.md)
