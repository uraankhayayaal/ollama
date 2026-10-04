# Планы реализации

Архив планов, по которым собирались домены системы: **23 плана + 1
обзорный анализ**. Документы описывают, *как это делалось* — с фазами,
чеклистами и находками.

## Как пользоваться

- **Текущее поведение** — в `docs/20-features/` и `docs/30-reference/`.
  Планы отвечают на вопрос «почему так», но не «как сейчас».
- **Планы не редактируются.** Это история. Если план расходится с кодом —
  правьте документацию по коду, а не план.
- **Новый план** создаётся здесь же, но текущая документация обновляется
  сразу вместе с кодом.

## Анализ

| Файл | О чём |
|---|---|
| [ANALYSIS-dashboard-board.md](ANALYSIS-dashboard-board.md) | Как работает доска: эпики, задачи, роли, gitflow. Анализ + рекомендации, без пунктов для выполнения |

## Планы по дате

| Файл | Домен | Статус |
|---|---|---|
| [PLAN-2026-09-17-done-webui.md](PLAN-2026-09-17-done-webui.md) | Web UI: доска + чат + дифф, полный HITL | Выполнено |
| [PLAN-2026-09-19-done-bash.md](PLAN-2026-09-19-done-bash.md) | Токено-эффективные Bash-команды агентов | Выполнено |
| [PLAN-2026-09-19-done-epic-task-token.md](PLAN-2026-09-19-done-epic-task-token.md) | Учёт и прогноз токенов по эпикам и задачам | Выполнено |
| [PLAN-2026-09-19-done-lsp.md](PLAN-2026-09-19-done-lsp.md) | LSP-интеграция: диагностика и навигация | Выполнено |
| [PLAN-2026-09-19-done-owerview-for-prom.md](PLAN-2026-09-19-done-owerview-for-prom.md) | «Промышленные зоны роста» — радар и дорожная карта | Выполнено |
| [PLAN-2026-09-19-done-qdrant.md](PLAN-2026-09-19-done-qdrant.md) | Qdrant: контекстная память (RAG) | Выполнено |
| [PLAN-2026-09-20-done-dashboard-workflow.md](PLAN-2026-09-20-done-dashboard-workflow.md) | Процесс доски: тикер и событийная связь доска↔чат | Выполнено |
| [PLAN-2026-09-21-done-assistant.md](PLAN-2026-09-21-done-assistant.md) | «Умный ассистент»: намерения по смыслу, RAG, действия | Выполнено |
| [PLAN-2026-09-21-done-dashboard-logs-timestamp.md](PLAN-2026-09-21-done-dashboard-logs-timestamp.md) | Экспорт логов и чатов: timestamp в имени файла | Выполнено |
| [PLAN-2026-09-22-done-assistant-ask-user.md](PLAN-2026-09-22-done-assistant-ask-user.md) | «Спроси пользователя» — структурированные вопросы | Выполнено |
| [PLAN-2026-09-22-done-context-compression.md](PLAN-2026-09-22-done-context-compression.md) | Сжатие контекста Ф-6…Ф-11 | Выполнено |
| [PLAN-2026-09-22-done-dashboard-chat-create.md](PLAN-2026-09-22-done-dashboard-chat-create.md) | Создание эпиков и задач через чат | Выполнено |
| [PLAN-2026-09-22-done-dashboard-gitflow-auto.md](PLAN-2026-09-22-done-dashboard-gitflow-auto.md) | Автоматизация git-workflow: ветки, MR, коммиты | Выполнено |
| [PLAN-2026-09-22-done-php.md](PLAN-2026-09-22-done-php.md) | Поддержка стека PHP: детекция, сборка, LSP, приёмка | Выполнено |
| [PLAN-2026-09-22-done-submodule.md](PLAN-2026-09-22-done-submodule.md) | Git submodule и мульти-репо | Выполнено |
| [PLAN-2026-09-23-done-dashboard-events.md](PLAN-2026-09-23-done-dashboard-events.md) | События доски ↔ чат | Выполнено |
| [PLAN-2026-09-24-done-architect-intelligence.md](PLAN-2026-09-24-done-architect-intelligence.md) | «Умный системный архитектор»: RAG, роли, смежные системы | Выполнено |
| [PLAN-2026-09-24-done-contextual-diff.md](PLAN-2026-09-24-done-contextual-diff.md) | Контекстные диффы веток задач и эпиков | Выполнено |
| [PLAN-2026-09-24-done-makefile.md](PLAN-2026-09-24-done-makefile.md) | Makefile проекта: единая точка входа + инфра-слой DevOps | Выполнено |
| [PLAN-2026-09-24-done-merge-conflict-board.md](PLAN-2026-09-24-done-merge-conflict-board.md) | Конфликты мёрджа на доске + антизацикливание ассистента | Выполнено |
| [PLAN-2026-09-25-done-e2e-findings.md](PLAN-2026-09-25-done-e2e-findings.md) | Находки ручных E2E (дефекты Ф-1..Ф-7) | Выполнено |
| [PLAN-2026-09-27-done-branch-aware-rag.md](PLAN-2026-09-27-done-branch-aware-rag.md) | Branch-Aware RAG: версионированный поиск по веткам | Выполнено |
| [PLAN-2026-09-28-done-rag-main-freshness.md](PLAN-2026-09-28-done-rag-main-freshness.md) | Актуальность RAG между эпиками: индекс `main` + рабочая копия агентов | Выполнено |
| [PLAN-2026-10-03-done-loop-breaker.md](PLAN-2026-10-03-done-loop-breaker.md) | Разрыв петли агента + эскалация на другую модель + честный фейл | Выполнено |
| [PLAN-2026-10-04-todo-marketing-site.md](PLAN-2026-10-04-todo-marketing-site.md) | Маркетинговый сайт: мультиязычность, метрики, CTA на SaaS | В работе |

## Что из планов важно знать

Некоторые планы содержат находки, которые **расходятся с финальным кодом**.
Код — источник истины; расхождения отмечены в доменных документах.

| Расхождение | Где отмечено |
|---|---|
| `REVIEW_MAX_ROUNDS=12` в плане, **3** в коде | [Код-ревью](../20-features/code-review.md) |
| План сохранял `done` при конфликте, код **откатывает в `in_progress`** | [Git-flow](../20-features/gitops-workflow.md) |
| `.env.example` не описывает `LSP_*`, `RAG_*_CONTEXT`, `CODEGEN_*` часть | [Справочник env](../30-reference/environment-variables.md) |
| `compose.yaml` не содержит сервиса `ollama` | [Инфраструктура](../30-reference/infrastructure.md) |

Полезные разделы планов: «Находки», «Что осталось пользователю» (ручной E2E),
«Ключевые решения».

## Связанное

- [Оглавление документации](../README.md)
- [Фичи и домены](../20-features/README.md)
- [Карта проекта](../00-overview/project-map.md)
