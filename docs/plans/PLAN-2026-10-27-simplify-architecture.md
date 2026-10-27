# ПЛАН: Упрощение — ликвидация доски, раннера, оркестратора

## Целевое состояние

```
Пользователь (чат) → Ассистент (планировщик) → Разработчик (агент с FileOps + Git)
```

**Никаких эпиков, задач, досок, статусов.** Агент работает с файлами и git напрямую.

## Что уходит полностью

| Что | Что это |
|-----|---------|
| **Доска** | `infrastructure/board/`, `domain/epic.go`, `domain/task.go`, `domain/board_types.go`, `domain/status.go`, `application/board/`, `tools/board_tools.go`, `tools/board_handoff.go` |
| **Раннер** | `runner/` целиком — автоматическое выполнение задач |
| **Оркестратор** | `application/planner/` — kanban pipeline, phases, hooks, watchdog |
| **Агенты-спецы** | `agents/devops/`, `agents/qaengineer/`, `agents/architect/`, `agents/businessanalyst/`, `agents/acceptor/` |
| **Серверная доска** | `server/board.go`, `server/session.go` (board/runner части), ~50 роутов доски |
| **Фронтенд доска** | `Dashboard/`, `GateBanner/`, `Chatboard/` — вся матрица |

## Что остаётся

| Что | Зачем |
|-----|-------|
| **Чат** | `agents/chatassist/` — ассистент общается с пользователем |
| **Разработчик** | `agents/developer/` — пишет код, тестирует |
| **FileOps** | `tools/fileops.go` — чтение/запись файлов, shell, LSP |
| **Git** | `application/gitflow/` → инструменты агента: ветки, worktree, MR, merge, rollback |
| **RAG** | `tools/rag*`, `application/rag/` — поиск по коду |
| **Acceptor** | `agents/acceptor/` — build/vet/test gate |
| **Сервер** | Минимальный: чат + auth + git API (ветки, diff, MR) |
| **Frontend** | Чат + git панель (ветки, статус, diff) |

## Git как инструменты агента

**Было:** board.Store → хуки (TaskInfraHook, TaskDoneHook и т.д.) → `application/gitflow/` → git операции.

**Станет:** агент вызывает git напрямую через инструменты:

```go
// Инструменты git для агента (в tools/git_tools.go)
type GitTools struct {
    "GitBranch"       // создать/переключить ветку
    "GitCommit"       // коммит
    "GitPush"         // пуш ветки
    "GitMerge"        // мёрдж ветки в другую
    "GitWorktree"     // создать/удалить worktree
    "GitMR"           // создать MR через Forge (GitHub/GitLab)
    "GitRollback"     // откат к checkpoint
    "GitResolve"      // разрешить конфликт (LLM)
    "GitDiff"         // diff между ветками
    "GitStatus"       // статус рабочей копии
}
```

**`application/gitflow/`** — переименовывается в `tools/git/` (или остаётся как утилиты внутри tools), становится набором функций без серверных хендлеров:
- `CreateBranch(repo, name, base)` — создать ветку
- `WorktreeAdd(repo, branch, path)` — worktree
- `MergeBranch(repo, source, target)` — мёрдж
- `CreateMR(project, source, target)` — MR
- и т.д.

Серверные хендлеры git (`server/gitflow*.go`) остаются как REST API для UI (создать ветку по кнопке, посмотреть diff), но **не связаны с доской** — работают с чистыми git-репозиториями.

---

## Порядок выполнения

### Шаг 1: Удалить раннер (runner/)
- [ ] Удалить `runner/` целиком
- [ ] Пофиксить `main.go` — убрать runner импорт
- [ ] Убрать `runner` subcommand

### Шаг 2: Удалить оркестратор (application/planner/)
- [ ] Удалить `application/planner/` целиком (kanban_*.go)
- [ ] Удалить `application/board/` целиком
- [ ] Удалить `application/hitl/` целиком (gate)
- [ ] Убрать `board.NewStore`, `board.LoadConfig` из `main.go`

### Шаг 3: Удалить доску (domain + infrastructure/board/)
- [ ] Удалить `domain/board_types.go`, `domain/epic.go`, `domain/task.go`, `domain/status.go`
- [ ] Удалить `infrastructure/board/` целиком
- [ ] Удалить `tools/board_tools.go`, `tools/board_handoff.go`
- [ ] Заменить `domain.Status`, `domain.Epic`, `domain.Task` → убрать полностью

### Шаг 4: Удалить агентов-спецы
- [ ] Удалить `agents/devops/`, `agents/qaengineer/`, `agents/architect/`, `agents/businessanalyst/`
- [ ] Пофиксить `agents/factory/factory.go` — убрать mapping

### Шаг 5: Rewrite сервер
- [ ] `server/session.go` — убрать board store, runner, gate, boardRev
- [ ] `server/server.go` — убрать ~50 роутов доски, оставить чат + git + auth
- [ ] Удалить `server/board.go` целиком
- [ ] `server/gitflow*.go` — отсоединить от domain типов (чистые git операции)
- [ ] `server/chatassist.go` — убрать board.NewDomainStore, boardWriteToolNames
- [ ] `server/projects.go` — убрать board.NewStore
- [ ] Удалить `server/taskreopen.go`, `server/taskrollback.go`

### Шаг 6: Rewrite chatassist
- [ ] `agents/chatassist/agent.go` — убрать все Board* из assistantToolNames
- [ ] Удалить `application/chatassist/boardprompt.go`
- [ ] Оставить: FileOps, LSP, CodeSearch, WebSearch, GitTools

### Шаг 7: Git → инструменты агента
- [ ] `application/gitflow/` → переписать как утилиты (без board-зависимостей)
- [ ] Создать `tools/git_tools.go` — git инструменты для LLM-агентов
- [ ] Серверные git-хендлеры остаются как REST API для UI (без триггеров от доски)

### Шаг 8: Rewrite main.go
- [ ] Убрать импорты: board, planner, runner, hitl
- [ ] Убрать subcommands: kanban, runner
- [ ] Оставить: chat, dev, и т.д.

### Шаг 9: Frontend
- [ ] Удалить `Dashboard/`, `GateBanner/`, `Chatboard/`
- [ ] Переписать `App.tsx` — чат + git панель (ветки, diff, статус)
- [ ] Переписать `Types/Types.ts` — убрать EpicRow/TaskRow/BoardView
- [ ] Переписать `Api/Api.ts` — убрать board API, оставить чат + git

### Шаг 10: Тесты
- [ ] Удалить все board-тесты (infrastructure/board/*_test.go, server/board_test*.go, tools/board_*.go)
- [ ] Удалить agent-тесты удалённых агентов
- [ ] Пофиксить server/session_test*.go, server/actions_test.go
- [ ] go build, go vet, go test, make depgate

---

## Риски

1. **Git без триггеров** — агент сам решает, когда создавать ветку/MR. Промпт должен диктовать纪律.
2. **Без оркестратора** — нет автоматического цикла "создать задачу → выделить разработчика → проверить → мёрджнуть". Всё управляется через чат и промпты.
3. **Acceptor без доски** — acceptor вызывается агентом (как инструмент), не триггером от смены статуса.
