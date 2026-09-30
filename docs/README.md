# Документация проекта

Мультиагентная система автоматической разработки: агенты проектируют,
генерируют, собирают, тестируют и ревьюят приложения, ведут Kanban-доску и
работают с Merge Request'ами.

> **Точка входа для новичка:** [Быстрый старт](10-getting-started/quickstart.md)
> **Полный список возможностей с настройкой:** [Фичи и домены](20-features/README.md)
> **Все переменные окружения:** [Справочник env](30-reference/environment-variables.md)

---

## Как устроена документация

Документация разбита по разделам. Номер в имени папки задаёт порядок
изучения — от «что это вообще» до «как расширять».

| Папка | Что внутри | Для кого |
|---|---|---|
| [`00-overview/`](00-overview/) | Что это, архитектура, карта проекта, глоссарий, сквозной путь задачи | Нужно понять систему целиком |
| [`10-getting-started/`](10-getting-started/) | Быстрый старт, установка, конфигурация, LLM-провайдеры | Нужно запустить и настроить |
| [`20-features/`](20-features/) | Домены и фичи: как работают, как настраиваются, какие инструменты | Нужно использовать или менять конкретную фичу |
| [`30-reference/`](30-reference/) | Справочники: env-переменные, CLI, REST/WS, инструменты, инфраструктура | Нужна точная таблица фактов |
| [`40-operations/`](40-operations/) | Эксплуатация, разработка, тесты, добавление новой фичи | Нужно чинить, развивать, ревьюить |
| [`plans/`](plans/) | Исторические планы реализации (23 файла: 22 плана + 1 обзорный анализ) | Нужна история «как это делалось» |

**Правило навигации:** `00-overview` → `10-getting-started` → нужный домен в
`20-features` → точность в `30-reference`.

---

## Оглавление

### 00. Обзор

- [Что это за проект](00-overview/what-is-it.md) — назначение, ключевые идеи, границы
- [Архитектура](00-overview/architecture.md) — слои, компоненты, потоки данных, веб-контур
- [Карта проекта](00-overview/project-map.md) — назначение каждой папки и файла
- [Как проходит задача](00-overview/how-it-works.md) — сквозной путь от промпта до merge
- [Глоссарий](00-overview/glossary.md) — термины: эпик, скоуп, волна, чанк, RAG, LSP

### 10. Начало работы

- [Быстрый старт](10-getting-started/quickstart.md) — от нуля до первого проекта
- [Установка и инфраструктура](10-getting-started/installation.md) — Ollama, Redis, Qdrant, Docker
- [Конфигурация](10-getting-started/configuration.md) — `.env`, приоритеты, примеры под машины
- [LLM-провайдеры](10-getting-started/providers.md) — Ollama, YandexGPT, Trim, двухуровневая маршрутизация

### 20. Фичи и домены

Полный каталог с кратким описанием — в [оглавлении фич](20-features/README.md).

| Домен | Документ | Ключевая настройка |
|---|---|---|
| Агенты и роли | [agents-and-roles.md](20-features/agents-and-roles.md) | `CODEGEN_*`, `*_NO_OVERWRITE`, `*_MAX_FILES` |
| Kanban-доска | [kanban-board.md](20-features/kanban-board.md) | `BOARD_REDIS_*`, `BOARD_TTL` |
| Архитектор | [architect.md](20-features/architect.md) | `RAG_ARCHITECT_CONTEXT` |
| Планировщик | [planner.md](20-features/planner.md) | `CODEGEN_PARALLEL`, `CODEGEN_ROLLBACK_ON_FAIL`, `REDIS_*` |
| Приёмка | [acceptor.md](20-features/acceptor.md) | `ACCEPT_*` |
| RAG и CodeSearch | [rag.md](20-features/rag.md) | `QDRANT_*`, `EMBEDDING_MODEL`, `RAG_*` |
| LSP | [lsp.md](20-features/lsp.md) | `LSP_*`, `LSP_BIN_PATH` |
| Web UI | [web-ui.md](20-features/web-ui.md) | `AI_WEB_ADDR`, `AI_WEB_PASSWORD` |
| Git-flow | [gitops-workflow.md](20-features/gitops-workflow.md) | `GITOPS_SUBMODULES`, `*_TOKEN` |
| Чат-ассистент | [chat-assistant.md](20-features/chat-assistant.md) | `RAG_ASSISTANT_CONTEXT` |
| Код-ревью MR | [code-review.md](20-features/code-review.md) | `REVIEW_*` |
| Сжатие контекста | [context-compression.md](20-features/context-compression.md) | `CODEGEN_HISTORY_*` |
| Токены и метрики | [tokens-and-metrics.md](20-features/tokens-and-metrics.md) | `LLM_PRICE_*`, `LLM_CURRENCY` |
| Песочница | [sandbox.md](20-features/sandbox.md) | `CODEGEN_SANDBOX*` |
| Makefile-контракт | [makefile-contract.md](20-features/makefile-contract.md) | `ACCEPT_*_CMD` |
| Стеки и языки | [stacks-and-languages.md](20-features/stacks-and-languages.md) | `CODEGEN_LANG` |
| Логи и события | [logging-and-events.md](20-features/logging-and-events.md) | `LOG_DIR`, `APP_LOG_*` |
| Сабмодули и мульти-репо | [submodules-multirepo.md](20-features/submodules-multirepo.md) | `GITOPS_SUBMODULES` |
| Инструменты | [tools.md](20-features/tools.md) | `CODEGEN_READ_*`, `CODEGEN_RUN_*` |
| Контекстные диффы | [contextual-diff.md](20-features/contextual-diff.md) | — |

### 30. Справочники

- [Переменные окружения](30-reference/environment-variables.md) — полный справочник с умолчаниями
- [CLI-команды](30-reference/cli.md) — все команды `go run .`
- [REST API и WebSocket](30-reference/rest-api.md) — маршруты, события, коды ответов
- [Инструменты агентов](30-reference/agent-tools.md) — все function-calling инструменты
- [Инфраструктура](30-reference/infrastructure.md) — порты, хранилища, файлы на диске

### 40. Эксплуатация и разработка

- [Разработка](40-operations/development.md) — сборка, проверки, структура кода
- [Добавление новой фичи](40-operations/adding-a-feature.md) — чеклист и правила ведения документации
- [Тестирование](40-operations/testing.md) — как запускать, что покрыто, известные флаки
- [Решение проблем](40-operations/troubleshooting.md) — симптом → причина → решение

### История

- [Планы реализации](plans/README.md) — 22 плана и обзорный анализ, по которым собирались домены

---

## Соглашения по содержанию

- **Язык** — русский, как в коде и промптах.
- **Факты сверяются с кодом.** Для env-переменных указывается значение по
  умолчанию из кода, а не из документации. Если есть расхождение с
  `.env.example` — это отмечается явно.
- **Ссылки на код** — в формате `путь/файл.go:строка`, чтобы можно было
  перейти и проверить.
- **Доменный документ** описывает *поведение* (как работает) и *настройку*
  (чем управляется), но не дублирует справочник env — на него ведёт ссылка.
- **Правила ведения документации** — в
  [Добавлении новой фичи](40-operations/adding-a-feature.md#обновление-документации).
