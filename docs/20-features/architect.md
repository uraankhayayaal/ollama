# Архитектор

Верхний уровень проектирования. Публикует эпики на доску, ревизует
черновики, выступает экспертом по багам, предлагает cross-functional
рекомендации.

Работает в **трёх режимах**:

| Режим | Конструктор | Когда |
|---|---|---|
| Базовый | `agents/architect/agent.go:98` | `phaseArchitect` — создание эпиков |
| Ревьюер | `AsReviewer()` (`agent.go:127`) | `phaseArchitectReview` — ревизия черновиков |
| Эксперт по багам | `AsBugExpert()` (`agent.go:119`) | `phaseBugs` — вердикты по багам |

Отдельной CLI-команды нет — доступен через `kanban` и `serve`.

## Настройка

```bash
RAG_ARCHITECT_CONTEXT=1    # RAG-контекст архитектора (вкл по умолчанию)
```

> Флага нет в `.env.example`, но он активен по умолчанию
> (`agents/architect/ragcontext.go:93-100`). Выключается `0`/`false`/`off`/`no`.

## Инструменты

`List`, `ReadFiles`, `LspDefinition`, `LspReferences`, `LspHover`,
`CodeSearch`, `RagIndexStatus`, `DetectStack` + доска:

`BoardCreateEpic`, `BoardUpdateEpic`, `BoardDeleteEpic`, `BoardSetEpicStatus`,
`BoardReviewBugReport`.

Набор пересобирается на лету при `SetRAG` и `SetArchitectExtras`
(`agents/architect/agent.go:106-114`).

## Публикация бэклога

`submit_architecture_backlog` (`agents/architect/agent.go:455-501`):

| Поле | Обязательное | Что делает |
|---|---|---|
| `architecture_summary` | нет | Сводка архитектуры |
| `tasks[]` | **да** (пустой массив — ошибка) | Эпики к созданию |
| `opportunities[]` | нет | Кросс-функциональные рекомендации |

Ответ: `created_epics`, `skipped_dups`, `total_epics`,
`architecture_summary`, `opportunities`, `skipped_opportunities`.

Каждый созданный эпик получает `requires_review = false` (`:479`) —
архитектор сам знает, что эпик продуман.

### Opportunities

```json
{"target_role": "Frontend", "suggestion": "нужен экран для загрузки файлов"}
```

Валидация: непустые `target_role` и `suggestion` — иначе запись попадает в
`skipped_opportunities`. Валидные складываются в `Summary` каждого эпика
секцией «КРОСС-ФУНКЦИОНАЛЬНЫЕ ВОЗМОЖНОСТИ» и в описание эпика исправления
(экспертиза багов).

Смысл: архитектор видит, что задача бэкенда требует работы для фронтенда, и
фиксирует это как отдельную рекомендацию, а не как задачу своей роли.

## Ревизия черновиков

Фаза `phaseArchitectReview` (`agents/planner/kanban.go:693-741`):

1. Собираются эпики с `requires_review = true`.
2. Исключаются терминальные и **в `human_help`** — «помощь человека»
   замораживает ревизию (`:817`).
3. Архитектор запускается в режиме `AsReviewer()` с промптом
   `epicReviewPrompt` (`:746-755`).
4. Флаг снимается, `LeadSyncedRev` выравнивается с `Revision`, чтобы лид
   принял эпик без ресинхронизации (`:733-739`).

`nextLeadEpic` черновики пропускает (`:1068`) — до ревизии они лиду не
выдаются.

## Экспертиза по багам

В фазе `phaseBugs` архитектор в режиме `AsBugExpert()` выносит вердикт по
багрепорту через `BoardReviewBugReport`:

- `fix` — создаётся эпик на исправление.
- `feature` — это фича, не баг.
- `wont_fix` — не чиним.

## Что требуется от архитектора

Системный промпт (`architectureSystemPrompt`) требует:

1. **Корректность задачи** — задавать вопросы (`AskUser`, с
   `recommended=true`), а не угадывать. Без согласия — явные допущения в
   `architecture_summary`.
2. **Обязательный эпик «Makefile проекта»** — эталонный контракт целей.
   Назначение роли: Backend Lead / DevOps Lead.
3. **Паттерны проектирования** — REST, 12-factor, KISS; анти-паттерны:
   GraphQL, devcontainer как замена документации.
4. **RAG-индекс** — предложить построить его в фоне: `RagIndexStatus` →
   `AskUser` → `IndexBackground`, и продолжить проектирование, не дожидаясь.
5. **Cross-functional opportunities** — что смежные роли должны будут
   сделать.

## Фоновая индексация

Архитектор может предложить построить RAG-индекс, не блокируя работу:

- `Session.IndexBackground` (`server/ragindex.go:61-100`) — single-flight,
  горутина с отдельным контекстом.
- Отчёт — в `chat.RoleStatus` и лог проекта.
- REST: `POST /api/projects/{id}/index?branch=`.
- Коды: `200` / `409` (уже идёт) / `503` (RAG недоступен).

Подробнее — [RAG](rag.md#запуск-индексации).

## Стек проекта

Перед проектированием архитектор вызывает `DetectStack` и получает:

```json
{
  "kind": "go",
  "stack": "monorepo",
  "roles": ["Frontend", "Backend", "DevOps", "QA"],
  "makefile": true,
  "markers": ["go.mod", "package.json", "Makefile"]
}
```

Это определяет набор направлений (консольный проект — без Frontend Lead) и
требование к Makefile. Подробнее — [Стеки и языки](stacks-and-languages.md).

## Деградация в CLI

В консольном `kanban` архитектору выдаётся **только** RAG
(`main.go:245-250`) — `AskUser` и `IndexBackground` доступны лишь в
Web UI-сессии (`server/session.go:287,293`).

Это осознанно: в консоли нет UI для вопросов и фоновых операций.

## Связанное

- [Kanban-доска](kanban-board.md) — куда публикуются эпики
- [Агенты и роли](agents-and-roles.md) — права архитектора
- [RAG](rag.md) — контекст и фоновая индексация
- [Makefile-контракт](makefile-contract.md) — обязательный эпик
- [Стеки и языки](stacks-and-languages.md)
