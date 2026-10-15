# PLAN-2026-10-15: Разбиение крупных файлов (Этап 7)

Продолжение `PLAN-2026-10-12-todo-clean-architecture.md` после завершения
Этапа 6. Кодовая база уже разложена по пакетам (domain/application/
infrastructure), но внутри пакетов остались слишком крупные файлы: их трудно
читать, ревьюить и сопровождать. Этап 7 — чисто механическое разбиение таких
файлов на логические под-файлы **внутри того же пакета**, без изменения API и
поведения. Бизнес-логику не переносим — только «разрезаем по швам».

## Проблема

| Файл | Строк | Проблема |
|---|---|---|
| `server/server.go` | 1833 | Все HTTP-хендлеры + типы + конструктор в одном файле |
| `tools/fileops.go` | 1707 | Все файловые операции в одном файле |
| `application/planner/executor.go` | 1555 | Все фазы выполнения плана в одном файле |
| `infrastructure/board/store.go` | 1298 | Все операции доски в одном файле |

## Критерий

Каждый под-файл < 500 строк. Поведение и публичный API не меняются.
Канонический набор (`go build`/`go vet`/`go test` с `-count=1`) зелёный,
`gofmt` по правкам чист, число функций/деклараций сохраняется.

## Срезы

### G1: `server/server.go` → под-файлы по доменам ✅

Было: один файл `server/server.go` (1833 строки, 57 функций). Стало:

| Файл | Строк | Содержимое |
|---|---|---|
| `server/server.go` | 305 | `Config`/`Server`, `NewServer`, `Run`, `routes`, `getOrCreate`, `session` |
| `server/projects.go` | 166 | `handleListProjects`, `handleOpenProject`, `writeProjectMeta`, `writeProjectMetaFromInfo`, `isGitURL`, `dirBase` |
| `server/providers.go` | 224 | `handleGetProviders`, `selectableModels`, `handleSelectProvider`, `writeSelectionResult`, `provider`, `sessionFlags`, `anySessionRunning` |
| `server/board.go` | 427 | доска/эпики/задачи: `handleGetBoard`, `handleUpdateTask`, `handleDeleteTask`, `removeTaskBranch`, `handleSetEpicStatus`, `handleReorderEpics`, `handleDeleteEpic`, `handleListBugs`, `decodeInjections`, `decodeTaskComments` |
| `server/chat.go` | 386 | чат/сессия: `handlePostChat`, `handleChatLogAnalysis`, `logAnalysisWhere`, `handleContinue`, `handleProjectIndex`, `handleChatHistory`, `handleClearChat`, `handleGetTokens`, `handleGateDecide`, `handleStop`, `handleAskAnswer`, `handleWS` |
| `server/injections.go` | 260 | инъекции и комментарии (9 хендлеров) |
| `server/helpers.go` | 128 | `contains`, `computeStatus`, `statusDetail`, `projectMeta`, `writeJSON`, `writeErr`, `decodeBody`, `decodeBodyStrict` |

Проверено: `go build`/`go vet`/`go test -count=1` по каноническому набору
зелёный, число функций сохранено (57), `gofmt` по правкам чист.

### G2: `tools/fileops.go` → под-файлы по операциям

### G3: `application/planner/executor.go` → под-файлы по фазам

### G4: `infrastructure/board/store.go` → под-файлы по сущностям
