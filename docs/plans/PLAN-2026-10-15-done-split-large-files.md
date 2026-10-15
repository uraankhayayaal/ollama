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

### G2: `tools/fileops.go` → под-файлы по операциям ✅

Было: один файл `tools/fileops.go` (1707 строк, 63 декларации). Стало:

| Файл | Строк | Содержимое |
|---|---|---|
| `tools/fileops.go` | 425 | `FileOps`, сеттеры (`SetScope`/`SetWriteAllowlist`/`SetOutputDir`/`SetProjectName`), политики доступа (`allowed`/`dirHasScope`/`writeAllowed`/`testsWriteAllowed`/`writeDenyMsg`/`relPath`/`ResolvePath`), `Write`/`AppendTo`/`Remove`/`ReadResult`, `ensureParentDirs`/`deepestExistingPath` |
| `tools/fileops_parse.go` | 319 | разбор аргументов: `FileItem`/`BulkParams`, `parseFileItems`/`parseFileMap`/`mapToFileItems`/`parseDecodedFiles`/`parsePathList`, `repairEscapedQuotes`/`findMatchingBrace`/`decodedFieldValue`/`nextKeyBoundary`, `WriteFiles` |
| `tools/fileops_read.go` | 397 | чтение: `ReadParams`/`FileContentResult`, лимиты, `readFileChunk`/`readFileLines`/`ReadFiles`, `trimPartialRune` |
| `tools/fileops_write.go` | 138 | `DeleteParams`/`DeleteFiles`, `AppendFile`, `List` |
| `tools/fileops_run.go` | 465 | `RunParams`/`runTimeout`, `runCommand`/`runCommandWorkspace`/`runCommandSandbox`/`finishRunResult`, `Run`, `missingToolHint`/`longRunningHint`, `runOutputLimit`/`truncateRunOutput` |

Проверено: канонический набор зелёный, число деклараций сохранено (63),
`gofmt` по правкам чист.

### G3: `application/planner/executor.go` → под-файлы по фазам ✅

Было: один файл `application/planner/executor.go` (1555 строк, 64 декларации).
Стало:

| Файл | Строк | Содержимое |
|---|---|---|
| `application/planner/executor.go` | 395 | ядро жизненного цикла: `Executor`/`NewExecutor`, сеттеры, `SetCheckpoint`/`MetricSink`, `Run`, `runWave`/`runOneStep`, чекпойнт-статусы (`markRunning`/`markDone`/`markFailed`/`persistStatus`), `parallelEnabled`/`planProject`/`toDomainTaskSpecs` |
| `application/planner/executor_audit.go` | 186 | аудит/откат: `trackStepMetrics`/`metricScope`/`metricFloat`, `rollbackEnabled`/`contractAuditEnabled`, `auditContractStep`/`auditFileChanges`/`rollbackStep` |
| `application/planner/executor_steps.go` | 397 | выполнение шагов: `executeStep`/`newStepAgent`, `runCodingAgent`, `runLeadStep`, `leadTaskPrompt`/`printDecomposedTasks`/`sortTaskSpecs` |
| `application/planner/executor_state.go` | 127 | персистентность плана/раундов: `persistPlan`/`writePlan`, `clearRoundState`, `truncatedStepError`, `loadResumeState`/`persistRoundState` |
| `application/planner/executor_acceptance.go` | 497 | приёмка: `runAcceptorAgent`, scope-хелперы (`acceptanceDir`/`topLevelScopeDir`/`leadStepScope`/`effectiveCreationScope`), `runAcceptanceLoop`/`planFixSteps`, `leadWithHint`/`leadHintAgent`, `fixStepID`/`uniqueSlash`/`bullet`/`findStep`/`chatNameForSpec` |

Проверено: канонический набор зелёный (в т.ч. `application/planner` без
базовых падений), число деклараций сохранено (64), `gofmt` по правкам чист.

### G4: `infrastructure/board/store.go` → под-файлы по сущностям ✅

Было: один файл `infrastructure/board/store.go` (1298 строк, 67 деклараций).
Стало:

| Файл | Строк | Содержимое |
|---|---|---|
| `infrastructure/board/store.go` | 202 | ядро: `ErrNotFound`/`ErrExists`/`ErrDependency`, `StoreConfig`/`Store`, key-хелперы, `NewStore`/`NewStoreNoCheck`/`Close`/`Client`/`Project`, `SetRepositories`/`applyRepositories`/`setTTL`, `now`, `removeString` |
| `infrastructure/board/store_epic.go` | 318 | эпики: `CreateEpic`/`GetEpic`/`SaveEpic`/`ListEpics`/`ReorderEpics`/`SetEpicStatus`, `setEpicTasksStatus`/`pauseEpicTasks`/`resumeEpicTasks`, `DeleteEpic`, `checkNoDependents` |
| `infrastructure/board/store_task.go` | 489 | задачи: CRUD (`CreateTask`/`GetTask`/`SaveTask`/`ListTasks`/`TasksByEpic`/`DeleteTask`), `PatchTask`/`MoveTask`/`SetTaskStatus`/`ReopenTask`, инъекции и комментарии задач |
| `infrastructure/board/store_bug.go` | 128 | баги: `CreateBugReport`/`GetBugReport`/`SaveBugReport`/`ListBugReports`/`SetBugStatus`/`MarkBugsFixedForEpic` |
| `infrastructure/board/store_meta.go` | 144 | meta и счётчики: `referenceExists`/`ReferenceExists`, `GetMeta`/`SaveMeta`, `EpicCounts`/`TaskCounts`/`AllDone`, `FirstNonZeroStatus`/`FirstNonZeroBugStatus` |

Проверено: канонический набор зелёный, число деклараций сохранено (67),
`gofmt` по правкам чист.

## Итог

Этап 7 завершён: четыре крупных файла (`server/server.go` 1833,
`tools/fileops.go` 1707, `application/planner/executor.go` 1555,
`infrastructure/board/store.go` 1298) разрезаны на 22 под-файла внутри тех же
пакетов. Ни один под-файл не превышает 500 строк, публичный API и поведение
не менялись, канонический набор зелёный.

