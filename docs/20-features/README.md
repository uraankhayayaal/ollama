# Фичи и домены

Каждый домен — самодостаточный документ: **что это**, **как работает**,
**как настраивается**. Полные таблицы переменных — в
[справочнике env](../30-reference/environment-variables.md), здесь только
управляющие.

## Как читать

1. Найдите домен в таблице ниже.
2. Прочитайте «Как это работает» — чтобы понять механику.
3. Настройте через указанные переменные (полные таблицы и умолчания — в
   [справочнике env](../30-reference/environment-variables.md)).
4. При необходимости — раздел «Связанное» внизу документа.

## Каталог

### Оркестрация и агенты

| Домен | Что это | Ключевые переменные |
|---|---|---|
| [Агенты и роли](agents-and-roles.md) | Архитектор, лиды, специалисты, ревьюер, ассистент — кто чем владеет и какие инструменты получает | `CODEGEN_*`, `*_NO_OVERWRITE`, `*_MAX_FILES`, `*_PLAN_FILE` |
| [Планировщик](planner.md) | Команда `plan`: декомпозиция, волны, скоупы, откат, чекпоинты, resume | `CODEGEN_PARALLEL`, `CODEGEN_ROLLBACK_ON_FAIL`, `CODEGEN_CONTRACT_ENFORCE`, `REDIS_*`, `PLAN_*` |
| [Приёмка](acceptor.md) | Детерминированная проверка собранного приложения без LLM | `ACCEPT_*` |
| [Makefile-контракт](makefile-contract.md) | Makefile проекта как единая точка входа команд для субагентов | `ACCEPT_*_CMD` (приоритет) |

### Доска и веб

| Домен | Что это | Ключевые переменные |
|---|---|---|
| [Kanban-доска](kanban-board.md) | Эпики, задачи, багрепорты, статусы, переходы, хуки | `BOARD_REDIS_*`, `BOARD_TTL` |
| [Архитектор](architect.md) | Публикация эпиков, ревизия черновиков, эксперт по багам, cross-functional opportunities | `RAG_ARCHITECT_CONTEXT` |
| [Web UI](web-ui.md) | Доска + чат + дифф + логи, REST/WS, авторизация | `AI_WEB_ADDR`, `AI_WEB_PASSWORD`, `AI_WORKSPACES`, `LOG_DIR` |
| [Чат-ассистент](chat-assistant.md) | Намерения по смыслу, AskUser, создание эпиков и задач из чата | `RAG_ASSISTANT_CONTEXT` |
| [Токены и метрики](tokens-and-metrics.md) | Учёт расхода по скоупам, предсказание, оценка стоимости, Prometheus | `LLM_PRICE_IN`, `LLM_PRICE_OUT`, `LLM_CURRENCY` |

### Git и ревью

| Домен | Что это | Ключевые переменные |
|---|---|---|
| [Git-flow](gitops-workflow.md) | Эпик = релизная ветка, задача = фича-ветка, авто-коммиты/merge, конфликты | `GITOPS_SUBMODULES`, `GITOPS_SUBMODULE_DEPTH`, `GITHUB_TOKEN`, `GITLAB_TOKEN` |
| [Контекстные диффы](contextual-diff.md) | Ленивые диффы веток задач и эпиков в UI | — |
| [Код-ревью MR](code-review.md) | Ревью Merge Request с блокировкой апрува, ревью по частям | `REVIEW_*` |
| [Сабмодули и мульти-репо](submodules-multirepo.md) | Вложенные репозитории, одна задача → несколько проектов | `GITOPS_SUBMODULES`, `GITOPS_SUBMODULE_DEPTH` |

### Контекст и качество кода

| Домен | Что это | Ключевые переменные |
|---|---|---|
| [RAG и CodeSearch](rag.md) | Семантический поиск по коду, версионированный по веткам Git | `QDRANT_*`, `EMBEDDING_MODEL`, `RAG_*` |
| [LSP](lsp.md) | Диагностики и навигация «глазами IDE», step-gate, авто-починка | `LSP_*`, `LSP_BIN_PATH`, `ACCEPT_LSP` |
| [Сжатие контекста](context-compression.md) | Ф-6…Ф-11: памятка, вытеснение в RAG, ранжирование, оглавления, компакция | `CODEGEN_HISTORY_*`, `CODEGEN_SKELETON_CACHE` |
| [Песочница](sandbox.md) | Изоляция команд `Run` в контейнере, отбраковка разрушителей | `CODEGEN_SANDBOX*` |
| [Стеки и языки](stacks-and-languages.md) | Go, Node/TS, Python, PHP: детект, LSP, сборка, приёмка | `CODEGEN_LANG` |
| [Логи и события](logging-and-events.md) | Файловые логи, логи приложения, события в UI в реальном времени | `LOG_DIR`, `APP_LOG_AUTO_FEED`, `APP_LOG_MAX_FEED_ROUNDS` |
| [Инструменты](tools.md) | Файловые операции, точечные правки, stub-healing, скоупы | `CODEGEN_READ_*`, `CODEGEN_RUN_TIMEOUT`, `CODEGEN_RUN_MAX_OUTPUT` |

## Карта «переменная → домен»

| Префикс | Домен |
|---|---|
| `LLM_*`, `OLLAMA_*`, `YANDEX_*`, `TRIM_*` | [Провайдеры](../10-getting-started/providers.md) |
| `CODEGEN_*` (общие) | [Инструменты](tools.md), [Планировщик](planner.md) |
| `CODEGEN_SANDBOX*` | [Песочница](sandbox.md) |
| `CODEGEN_HISTORY_*` | [Сжатие контекста](context-compression.md) |
| `ACCEPT_*` | [Приёмка](acceptor.md) |
| `BOARD_*` | [Kanban-доска](kanban-board.md) |
| `REDIS_*`, `PLAN_*` | [Планировщик](planner.md) |
| `QDRANT_*`, `EMBEDDING_MODEL`, `RAG_*` | [RAG](rag.md) |
| `LSP_*` | [LSP](lsp.md) |
| `REVIEW_*` | [Код-ревью](code-review.md) |
| `AI_WEB_*`, `AI_WORKSPACES` | [Web UI](web-ui.md) |
| `GITOPS_*`, `GITLAB_*`, `GITHUB_*` | [Git-flow](gitops-workflow.md), [Код-ревью](code-review.md) |
| `LOG_DIR`, `APP_LOG_*` | [Логи и события](logging-and-events.md) |
| `*_MAX_FILES`, `*_NO_OVERWRITE`, `*_PLAN_FILE`, `*_REPORT_FILE` | [Агенты и роли](agents-and-roles.md) |
| `LLM_PRICE_*`, `LLM_CURRENCY` | [Токены и метрики](tokens-and-metrics.md) |

## Связанное

- [Архитектура](../00-overview/architecture.md)
- [Как проходит задача](../00-overview/how-it-works.md)
- [Переменные окружения](../30-reference/environment-variables.md)
- [Планы реализации](../plans/README.md) — история по каждому домену
