# Инструменты агентов

Полный реестр function-calling инструментов (`tools/registry.go:105-190`).

## Правила реестра

| Правило | Смысл |
|---|---|
| Неизвестное имя | `panic` на этапе конструирования агента (fail-fast) |
| `deps.Board == nil` | `Board*` **не выдаются вообще** — автоматическая деградация standalone-агентов (`registry.go:34-36`) |
| Серверные мосты | `AskUser`, `IndexBackground`, `KanbanStart`, `TaskMerge`, `EpicRelease`, `BranchReject` добавляются через `Set.Add` — объявить в `tools` нельзя из-за цикла импортов |
| Набор агента | Собирается через `tools.Select(names, deps)` |

## Файловые операции

| Инструмент | Что делает | Ключевые параметры |
|---|---|---|
| `WriteFiles` | Записать несколько файлов | `files[]` |
| `ReadFiles` | Прочитать файлы (с окном строк) | `files[]`, `lines` |
| `ReadMap` | Карта проекта: путь, размер, язык | — |
| `DeleteFiles` | Удалить файлы | `files[]` |
| `AppendFile` | Дописать в конец | `path`, `content` |
| `Run` | Выполнить команду | `command` |
| `List` | Дерево файлов с фильтром по scope | — |
| `ReadAppLogs` | Запустить приложение, прочитать логи | `source`, `command`, `lines`, `wait` |

## Точечные правки

| Инструмент | Что делает |
|---|---|
| `SearchReplace` | Блоки `<<<<<<< SEARCH ... ======= ... >>>>>>> REPLACE`; при промахе файл не меняется |
| `PatchGoFunction` | Семантическая замена Go-функции через `go/ast` + `go/format` |
| `PatchFunction` | То же для других языков |

## Навигация и анализ

| Инструмент | Что делает |
|---|---|
| `LspCheck` | Диагностики по файлам или всему проекту |
| `LspDefinition` | Переход к определению |
| `LspReferences` | Поиск ссылок |
| `LspHover` | Документация символа |
| `DetectStack` | Стек, роли, маркеры, наличие Makefile |
| `WebSearch` | Поиск в интернете |

## Поиск по коду

| Инструмент | Что делает | Требует |
|---|---|---|
| `CodeSearch` | Семантический поиск по коду | Qdrant |
| `RagIndexStatus` | Состояние индекса | Qdrant |

## Git

| Инструмент | Что делает |
|---|---|
| `ResolveGitConflicts` | `status` → `start` → `apply` |

## Ревью

| Инструмент | Что делает |
|---|---|
| `ReviewMr` | Дифф MR + публикация замечаний |
| `ApproveMr` | Апрув (блокируется при критичных) |
| `NextChunk` | Следующая часть большого диффа |

Требуют `deps.Session`.

## Доска (17 инструментов)

### Чтение

| Инструмент | Что делает |
|---|---|
| `BoardListEpics` | Список эпиков |
| `BoardGetEpic` | Эпик по ID |
| `BoardListTasks` | Список задач |
| `BoardGetTask` | Задача по ID |
| `BoardListBugs` | Список багрепортов |
| `BoardGetBug` | Багрепорт по ID |

### Эпики

| Инструмент | Что делает | Деструктивен |
|---|---|---|
| `BoardCreateEpic` | Создать эпик | — |
| `BoardUpdateEpic` | Обновить эпик | — |
| `BoardDeleteEpic` | Удалить эпик | **да** |
| `BoardSetEpicStatus` | Сменить статус эпика | — |

### Задачи

| Инструмент | Что делает | Деструктивен |
|---|---|---|
| `BoardCreateTask` | Создать задачу | — |
| `BoardUpdateTask` | Обновить задачу | — |
| `BoardDeleteTask` | Удалить задачу | **да** |
| `BoardSetTaskStatus` | Сменить статус задачи (в т.ч. `human_help` — обязательный аргумент `reason`: причина оседает в `pause_reason`) | — |

### Баги

| Инструмент | Что делает |
|---|---|
| `BoardCreateBug` | Создать багрепорт |
| `BoardSetBugStatus` | Сменить статус бага |
| `BoardReviewBug` | Вердикт по багу |

> **Go-имена vs строковые имена.** Константы в коде —
> `BoardCreateBug` и `BoardReviewBug`, но значения строк — 
> `BoardCreateBugReport` и `BoardReviewBugReport`. В вызовах моделей и в
> `tools.Select` используются **строковые** имена.

## Серверные мосты (не в реестре `tools`)

| Инструмент | Опасность | Что делает |
|---|---|---|
| `AskUser` | безопасен | Структурированные вопросы с вариантами |
| `IndexBackground` | безопасен | Фоновая индексация RAG |
| `KanbanStart` | безопасен | Запуск оркестрации |
| `TaskMerge` | **деструктивен** | Влить задачу в релизную ветку |
| `EpicRelease` | **деструктивен** | Выпустить эпик |
| `BranchReject` | **деструктивен** | Отклонить фича-ветку |

Деструктивные требуют подтверждения. Обёртка возвращает
`status=confirm` **без траты раунда** (`server/actions.go:115-131`);
слова подтверждения — в `confirmTokens` (`actions.go:378-389`), включая
`lf` — русское «да» в латинской раскладке.

## Кто что получает

| Агент | Инструменты |
|---|---|
| Backend-разработчик | Файловые, правки, `Run`, `LspCheck`, `CodeSearch`, `ReadAppLogs` |
| Frontend-разработчик | То же |
| DevOps | То же |
| QA-инженер | То же + `ReadAppLogs` обязательно |
| Лиды | Только чтение + `LspDefinition` / `LspReferences` / `LspHover` (без `LspCheck`) |
| Архитектор | Чтение, `CodeSearch`, `RagIndexStatus`, `DetectStack`, `BoardCreateEpic` / `Update` / `Delete` / `SetStatus`, `BoardReviewBug` |
| Планировщик | Навигация без `LspCheck` (`agents/planner/planner.go:14`) |
| Ревьюер | `ReviewMr`, `NextChunk`, `ApproveMr` |
| Чат-ассистент | Чтение, `CodeSearch`, `WebSearch`, 15 инструментов доски, мосты |
| Архитектор (CLI) | Только RAG — `AskUser`/`IndexBackground` в консоли недоступны |

## Формат ответа

| `status` | Значение |
|---|---|
| `success` | Выполнено |
| `skipped` | Не применимо (нет сервера, нет проекта, сервис недоступен) |
| `error` | Ошибка выполнения |
| `confirm` | Требуется подтверждение (серверные мосты) |

## Добавить инструмент

1. Реализовать `tools.Tool`.
2. Объявить имя константой.
3. Добавить кейс в `newTool` (`tools/registry.go:105`).
4. Выдать агенту в его `tools.Select([...])`.
5. Покрыть тестом.
6. Обновить эту таблицу и [чеклист](../40-operations/adding-a-feature.md).

## Связанное

- [Инструменты](../20-features/tools.md) — механика
- [Агенты и роли](../20-features/agents-and-roles.md)
- [Kanban-доска](../20-features/kanban-board.md)
