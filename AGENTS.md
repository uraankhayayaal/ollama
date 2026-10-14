# AGENTS.md — правила для агентов (opencode / помощники)

## Состояние последней сессии (Clean Architecture Этап 6, срезы F1–F3ac — `server` → `application/usecases`/`application/gitflow`/`application/rag`/`application/ask`/`application/logs`/`application/chatassist`/`application/hitl`/`application/board`/`domain/log`/`domain/diff`/`domain`)

Продолжен Этап 6 (`PLAN-2026-10-12-todo-clean-architecture.md`, «Интерфейсы и
адаптеры»; критерий — `server/` без бизнес-логики). Срез **F1** (коммит
`bc9a7a7`) — ручной откат задачи и прогноз токенов вынесены из HTTP-слоя:
`application/usecases/rollback.go` (`RollbackTarget`/`RollbackTask`,
порт `WorktreeRollbacker`, доменные ошибки `ErrTaskNotFound`/`ErrNoWorktree`/
`ErrBadTarget`/`ErrNoSuchCommit`) и `application/usecases/tokenestimate.go`
(`NewTokenEstimator`, TTL-обучение предиктора), тесты
`application/usecases/rollback_test.go`; `server/taskrollback.go` и
`server/tokenestimate.go` стали тонкими обёртками.

Срез **F2** — детерминированная обработка логов вынесена из `server/` в новый
пакет **`domain/log`** (stdlib-only; `go list -deps ./domain/log` чист от
проектных пакетов). Перенос: `domain/log/digest.go` владеет типами
`LogLine`/`LogFinding`/`LogDigest` (+`Empty`/`Text`), константами
`LogDigestWindow`/`LogDigestMax`/`LogDigestSample`, функциями
`ParseLogLine`/`LogHeuristicLevel`/`IsLogStackFrame`/`LevelRank`/
`TruncateLogText`/`BuildLogDigest`/`MergeLogFindings`(
+ `FilterLogLines`/`FilterLogByLevel` — из `server/loganalysis.go`). Тесты
переехали `server/logdigest_test.go` → `domain/log/digest_test.go` (`git mv`,
package `log`, имена экспортированы). `server/logdigest.go` (временный
shim-алиас) удалён. `server/loganalysis.go` импортирует `logpkg "ai/domain/log"`,
держит только серверную обвязку (чтение файлов, выборка, `LogDigestPrompt`,
cooldown); `server/actions_logs.go` зовёт `logpkg.TruncateLogText`;
`server/chatassist.go` — `LogDigestPrompt(a)`. Проверено: канонический набор
(`go build`/`go vet`/`go test` по перечню ниже) зелёный, `-race` по
`domain/log` чист, `gofmt -l` по правкам чист (предсуществующие отклонения
`server/chatassist.go` не трогались). Документация: раздел «Этап 6» и шапка
плана (`PLAN-2026-10-12`), `docs/20-features/chat-assistant.md` (путь
`buildLogDigest` → `domain/log`), `docs/00-overview/project-map.md` (новая
секция `domain/`/`application/`).

Срез **F3a** — гард «phantom done» (`board.Store.TaskDoneGuard`, инцидент
FEL-04) вынесен из `server/` в `application/usecases/doneguard.go`:
`GuardTaskDone` + `IsOrchestratorArtifact`, порт
`DoneGuardSource{IsGitProject; TaskBranch; EpicBase; CommitsAhead; DirtyPaths;
Warn}`. `server/gitflow_done_guard.go` стал адаптером (`doneGuardSource` поверх
`reg`/`repoOf`/`worktreeWorkPaths`, env `KANBAN_DONE_REQUIRES_COMMIT`); гард
по-прежнему внедряется в `attachGitHooks` и зовётся через тонкий
`s.guardTaskDone`. Тесты: новые герметичные
`application/usecases/doneguard_test.go` (заглушка источника; пустая задача
блокируется, коммит/правки/выключение пропускают, ошибка git → warn+разрешение,
таблица артефактов); серверные интеграционные
`server/gitflow_done_guard_test.go` не менялись и проходят через адаптер.
Верификация: канонический набор зелёный, `-race` по новым тестам чист, `gofmt`
по правкам чист. План обновлён (таблица срезов + шапка).

Срез **F3b** — цикл LLM-разрешения конфликтов вынесен из `server/` в новый
пакет **`application/gitflow`** (`resolve.go`): `Resolver`/`ResolveWithLLM`,
`buildDeveloperPrompt`, `flattenOutputDir`, `verify` (детерминированная приёмка
через порт), `clearConflictField`; порты
`Store{SaveEpic;SaveTask}`, `Developer{SpecialistForRole;Generate}`,
`Acceptor{Accept}`, `Markers func(dir,files)`. Заодно удалён мёртвый код
прежнего JSON-пути `buildPrompt`/`collectFiles`/`writeResolved` (не вызывались
ниоткуда). `server/gitflow_ar.go` стал HTTP-обвязкой: адаптеры
`resolveDeveloper` (planner.SpecialistForRole + llm.LLMProvider) и
`resolveAcceptor` (acceptor.Accept с фиксированным Config), `attemptLLMResolve`
(store/provider + stage/commit/push) и хендлеры `handleTaskLLMResolve`/
`handleEpicLLMResolve`/`tryTaskLLMResolve`. Тесты: герметичные
`application/gitflow/resolve_test.go` (10 шт.: пустой список, очистка полей
доски, повтор с ошибкой приёмки в промпте, остаточные маркеры → бюджет, провал
приёмки, ошибки LLM/пустой ответ, перенос `OUTPUTDIR/`, порядок задач, частичное
снятие конфликтов); старые тесты `server/gitflow_ar_e2e_test.go` не менялись.
Верификация: канонический набор зелёный (26 пакетов), `-race` по
`application/gitflow` чист, `gofmt` по правкам чист. Документация: план
(шапка + таблица F3b), `docs/00-overview/project-map.md` (новая строка
`application/gitflow/`), `docs/20-features/gitops-workflow.md`,
`docs/30-reference/rest-api.md`, `docs/40-operations/troubleshooting.md`
(ссылки на `resolveMaxAttempts`/`ResolveWithLLM` → `application/gitflow`).

Срез **F3c** — политика открытия MR вынесена из `server/gitflow_mr.go` в
`application/gitflow/mr.go`: `MRCreator`/`CreateTaskMR`/`CreateEpicMR` +
`ErrEmptyBranch`; порты `MRGateway{CountCommits;EnsureRemoteBranch;PushBranch}`,
`Forge{CreateMergeRequest}`, `MRRegistry{SetTaskMR;SetEpicMR}`. Правило: пустая
ветка (нет коммитов относительно базы) → `ErrEmptyBranch` до push (серверный
`errMREmptyBranch` стал алиасом доменной ошибки, `errors.Is` в хендлерах и
авто-шаге работает как раньше); ошибка самого git при подсчёте НЕ блокирует MR.
`server/gitflow_mr.go` оставил адаптеры `serverMRGateway`/`serverMRForge`/
`serverMRRegistry`, `createTaskMRLocked`/`createTaskMROnce`, `ensureRemoteBase`,
хендлеры `handleCreateEpicMR`/`handleCreateTaskMR` и read-model `gitStatus`.
Удалён `branchHasCommits` (переехал в use-case). Тесты: новые герметичные
`application/gitflow/mr_test.go` (8 шт.); серверные `gitflow_mr_test.go`/
`gitflow_submodule_test.go` не менялись. Верификация: канонический набор зелёный
(26 пакетов), `-race` по `application/gitflow` чист, `gofmt` по правкам чист.
Документация: план (шапка + таблица F3c), `docs/00-overview/project-map.md`,
`docs/20-features/gitops-workflow.md` (секция «Создание MR»). Остаток Этапа 6 —
F3+: остальные git-flow хуки, сессии/оркестрация, RAG-хуки. Коммита нет
(спросить пользователя).

Срез **F3d** — раскладка worktree, артефактная политика `.gitignore` и
презентация git-ошибки вынесены из `server/gitflow_auto.go` в новый
`application/gitflow/worktree.go`: `TaskWorktreePath`/`EpicWorktreePath`/
`ChildTaskKey`, `GitignoreHeader`/`GitignorePatterns`/`GitignoreFileName`/
`TaskGitignore`/`EnsureTaskGitignore` (идемпотентное создание/дополнение
`.gitignore` worktree: сохранение проектных строк, дописывание недостающих
паттернов артефактов; `vendor/` намеренно не в списке) и `GitflowErrBoard`
(workflow-scope → подсказка, иначе первая строка без секретов, усечение до 200
рун). `server/gitflow_auto.go` оставил тонкие обёртки (`taskWorktreePath`/
`epicWorktreePath` как методы, `childTaskKey`/`taskGitignore`/
`ensureTaskGitignore`/`gitflowErrBoard`, константа-алиас `taskGitignoreHeader`),
поэтому тесты `server/gitflow_done_guard_test.go`, `gitflow_hooks_test.go`,
`gitflow_test.go` не менялись. Тесты: новые герметичные
`application/gitflow/worktree_test.go` (пути, ключ задачи, содержимое
`.gitignore`, создание/дополнение/идемпотентность/пустой корень, политика
`GitflowErrBoard`). Обновлены комментарии-указатели в `application/usecases/
doneguard.go` и `infrastructure/git/gitops.go`. Верификация: канонический набор
зелёный (26 пакетов), `-race` по `application/gitflow` чист, `gofmt` по правкам
чист. Документация: план (шапка + таблица F3d), `docs/00-overview/project-map.md`,
`docs/40-operations/troubleshooting.md`. Остаток Этапа 6 — F3+: остальные
git-flow хуки (авто-коммит/merge, sync main), сессии/оркестрация, RAG-хуки.
Коммита нет (спросить пользователя).

Срез **F3e** — авто-шаги завершения задачи вынесены из `server/gitflow_auto.go`
в `application/gitflow/finalize.go`: `TaskFinalizer.Finalize` (авто-коммит
worktree → авто-MR задачи ДО мёрджа → штатный мёрдж ветки в релиз эпика →
снятие worktree, с LLM-авторезолвом конфликта) и порт `TaskFinalizerHost`
(`Project`/`TaskBranch`/`CommitWorktree`/`CreateTaskMR`/`BranchAlreadyMerged`/
`MergeTaskBranch`/`ResolveTaskConflict`/`SaveTask`/`RemoveTaskWorktree`/
`EmitBoard`/`AppendStatus`), типы `Project`/`BranchRef`/`MergeOutcome`, доменная
ошибка `ErrMergeConflict`. `server/gitflow_auto.go` оставил адаптер
`taskFinalizerHost` (поверх `reg`/`repoOf`/`commitTaskWorktree`/
`createTaskMROnce`/`mergeTaskBranch`/`tryTaskLLMResolve`/`store`/`srvEmitBoard`/
`session`): `mergeLock` берётся в адаптере (`ResolveTaskConflict`), разбор
`*git.MergeConflictError` → `ErrMergeConflict`+`ConflictFiles` тоже в адаптере.
Порядок и тексты сообщений сохранены. Тесты: новые герметичные
`application/gitflow/finalize_test.go` (пропуск не-git и без ветки, полный цикл,
«уже влита в релиз», ошибка авто-MR не мешает мёрджу, конфликт разрешён LLM →
worktree снят, конфликт не разрешён → задача в `in_progress` без снятия
worktree + статус в чат, не-конфликтная ошибка мёрджа, пустой worktree, пустая
задача). Серверные `gitflow_test.go`/`gitflow_mr_test.go`/
`gitflow_auto_e2e_test.go`/`gitflow_ar_e2e_test.go` (зовут
`srv.autoCommitAndMergeTask`) не менялись. Верификация: канонический набор
зелёный (26 пакетов), `-race` по `application/gitflow` чист, `gofmt` по правкам
чист. Документация: план (шапка + таблица F3e), `docs/00-overview/project-map.md`.
Остаток Этапа 6 — F3+: `syncEpicMain*`/`gitflow.go`/`gitflow_resolve.go`,
сессии/оркестрация, RAG-хуки. Коммита нет (спросить пользователя).

Срез **F3f** — авто-синхрон релизной ветки эпика с main вынесен из
`server/gitflow_auto.go` в `application/gitflow/sync.go`: `EpicMainSyncer`
(`SyncEpicMain` под `mergeLock` / `SyncMainForTask` без лока) — быстрый путь
`MergeFeature`, иначе настоящий merge main во временном worktree + авто-резолв
тривиальных блоков + push; сложные конфликты не продвигают ветку. Порт
`EpicMainHost` (`ProjectInfo`/`EpicBranch`/`Repo`/`DropEpicWorktreeLocked`/
`Locked`/`AttemptLLMResolve`/`TrivialResolve`/`SetEpicMergeConflict`/`Notify`/
`Status`) + типы `EpicRepo`/`WorktreeOps`. `server/gitflow_sync.go` — адаптер
`epicMainHost` + обёртки `serverEpicRepo`/`serverWorktreeOps` над `*git.Repo`;
`server/gitflow_auto.go` оставил тонкие `syncEpicMainOnce`/`syncEpicMainForTask`
(`autoResolveMainSync` удалён — логика в use-case). Тесты: новые герметичные
`application/gitflow/sync_test.go` (пропуск плохого состояния, быстрый путь,
ошибка мёрджа, тривиальный резолв, сложный конфликт с LLM/без, `SyncMainForTask`
без лока). Серверные `gitflow_auto_e2e_test.go` (зовут `srv.syncEpicMainOnce`)
не менялись. Верификация: канонический набор зелёный (26 пакетов), `-race` по
`application/gitflow` чист, `gofmt` по правкам чист. Документация: план (шапка +
таблица F3f), `docs/00-overview/project-map.md`. Остаток Этапа 6 — F3+:
`gitflow.go`/`gitflow_resolve.go`, сессии/оркестрация, RAG-хуки. Коммита нет
(спросить пользователя).

Срез **F3g** — политика ветвления вынесена из `server/gitflow.go` в
`application/gitflow/branches.go`: `Brancher` (`EnsureBranch` — идемпотентное
создание ветки от базы, `CreateEpicBranch`/`CreateTaskBranch` — ветка от
релизной ветки эпика + запись `BranchRef` в side-реестр + проставление
`git_branch` на доске, `AutoCreateEpicBranch`/`AutoCreateTaskBranch` —
best-effort авто-ветки хуков, `RemoveEpicBranches` — снятие веток+MR эпика и
задач), доменные ошибки `ErrEpicBranchMissing`/`ErrBranchCreate`, префиксы
`EpicBranchPrefix`/`TaskBranchPrefix`, порты `BranchRepo`
(`BranchExists`/`CreateBranch`) и `BranchRegistry` (`SetEpicBranch`/`EpicBranch`/
`SetTaskBranch`/`DeleteEpicBranch`/`DeleteTaskBranch`/`DeleteEpicMR`/
`DeleteTaskMR`) + `Store` (`SaveEpic`/`SaveTask`) и колбэк `Changed`. Авто-методы
возвращают bool (успех), чтобы сервер по-прежнему слал на доску
«gitflow: авто-ветка …» только при создании; отсутствие ветки эпика в авто-ветке
задачи логируется `Detailf`, прочие сбои — `Warnf` (как раньше). `server/
gitflow.go` оставил адаптер `branchRegistry` поверх `*workspace.Registry`,
хелпер `s.brancher(...)` и `boardStore`; свободная `ensureBranch` удалена,
`epicBranchPrefix` — алиас `gitflow.EpicBranchPrefix` (мёртвый
`taskBranchPrefix` убран), хендлеры/хуки переведены на use-case с сохранением
кодов (404 unknown epic, 400 не-git/пустая база/`ErrEpicBranchMissing`, 502
`ErrBranchCreate`, 500 прочее). Тесты: новые герметичные
`application/gitflow/branches_test.go` (создание/идемпотентность/пустая база/
сбой git/ветка задачи от эпика/нет ветки эпика/авто-хуки/снятие веток/
`EnsureBranch`); серверные `gitflow_test.go`/`gitflow_hooks_test.go`/
`taskrollback_test.go` не менялись и зелёные. Верификация: канонический набор
зелёный (26 пакетов), `-race` по `application/gitflow` чист, `gofmt` по правкам
чист. Документация: план (шапка + таблица F3g), `docs/00-overview/project-map.md`.
Остаток Этапа 6 — F3+: `gitflow.go` (releaseEpic/mergeTaskBranch),
`gitflow_resolve.go`, сессии/оркестрация, RAG-хуки. Коммита нет (спросить
пользователя).

Срез **F3h** — политика вливания ветки задачи в релизную ветку эпика
(`TaskMerger.MergeTaskBranch`) вынесена из `server/gitflow.go` в
`application/gitflow/release.go`: проверка задачи/эпика/проекта (порт
`MergeProject`: `IsGit`/`Kind`/`Base`/`PushURL`), недостающих веток эпика и
задачи, снятие worktree эпика (`DropEpicWorktreeLocked` — 4.1: архитектор держит
релизную ветку checked-out), `MergeFeature` под пер-проектным локом (`Locked`) и
инвалидация диффов на успехе. Порт `MergeTaskHost` + `MergeRepo`
(`MergeFeature`). `server/gitflow.go` оставил адаптер `mergeTaskHost` (поверх
`reg`/`repoOf`/`mergeLock`/`dropEpicWorktreeLocked`/`invalidateDiffs`) и тонкую
обёртку `s.mergeTaskBranch`; три потребителя (HTTP `handleMergeTask`, ассистент
`Session.TaskMerge`, finalizer `gitflow_auto.go`) не менялись — `errors.As`
`*git.MergeConflictError` и коды (409 conflicts / 400 прочее) сохранены. Тесты:
новые герметичные `application/gitflow/release_test.go` (успех с проверкой
аргументов слияния, nil/без эпика, не-git/нет проекта/нет репозитория, нет ветки
эпика/задачи, конфликт не инвалидирует диффы). Верификация: канонический набор
зелёный (26 пакетов), `-race` по `application/gitflow` чист, `gofmt` по правкам
чист. Документация: план (шапка + таблица F3h), `docs/00-overview/project-map.md`.
Остаток Этапа 6 — F3+: `gitflow.go` (`releaseEpic`), `gitflow_resolve.go`,
сессии/оркестрация, RAG-хуки. Коммита нет (спросить пользователя).

Срез **F3i** — политика релиза релизной ветки эпика в main (`Releaser.ReleaseEpic`)
вынесена из `server/gitflow.go` в `application/gitflow/release.go`: проверка
git-проекта/базы/статуса `done`/ветки эпика, `MergeFeature` под пер-проектным
локом, инвалидация диффов и хук «main изменился» (`MainChanged`: при
`already=true` — только проверка индекса `main`, иначе `afterMainChanged`). Порт
`ReleaseHost` (`Project`/`Repo`/`EpicStatus`(found,err)/`EpicBranch`/`Locked`/
`InvalidateDiffs`/`MainChanged`). Вместо серверного `apiError` use-case
возвращает доменные sentinel-ошибки (`ErrProjectNotFound`/`ErrNotGitProject`/
`ErrNoBaseBranch`/`ErrEpicNotFound`/`ErrEpicNotDone`/`ErrRepoUnavailable`/
`ErrBoardUnavailable`/`ErrMergeFailed`), которые `handleReleaseEpic` маппит в
коды (404/400/503/502/409 conflict с `*git.MergeConflictError`). Тип `apiError`
и серверная константа `epicBranchPrefix` удалены. `mergeTaskHost` (адаптер
F3h) вырос до обоих портов — добавлены `EpicStatus` (открывает `boardStore`,
`board.ErrNotFound` → found=false) и `MainChanged`. Обёртка
`s.releaseEpic` — тонкая; три потребителя (`handleReleaseEpic`,
`Session.EpicRelease`, resolve-флоу) не меняли вызовы. Тесты: новые герметичные
`application/gitflow/release_test.go` (успех, `AlreadyMerged`, все sentinel'ы
состояния, конфликт vs `ErrMergeFailed`); серверные
`gitflow_test.go`/`actions_test.go` не менялись и зелёные (проверяют коды).
Верификация: канонический набор зелёный (26 пакетов), `-race` по
`application/gitflow` чист, `gofmt` по правкам чист. Документация: план (шапка +
таблица F3i), `docs/00-overview/project-map.md`. Остаток Этапа 6 — F3+:
`gitflow_resolve.go`, сессии/оркестрация, RAG-хуки. Коммит F3i `606becf`.

Срез **F3j** — конфликтный резолв «main ↔ релизная ветка эпика»
(`ConflictResolver.StartRebase`/`FinishResolve`) вынесен из
`server/gitflow_resolve.go` в новый `application/gitflow/conflict.go`:
прогноз конфликтов `merge-tree` (чисто → `Status:"ok"`, идите `POST .../release`),
постоянный конфликтный worktree `.conflict-<проект>-<эпик>`, снятие worktree
эпика (4.1 — архитектор держит релизную ветку checked-out), настоящий merge
main → релизной ветки в worktree, авто-резолв тривиальных блоков
(`TrivialResolve`) + stage, обязательная приёмка (`Accept`) и финальный
`MergeFeature` main ← релизной ветки с push под пер-проектным локом. Порт
`ConflictHost` (`Project`/`Repo`/`EpicStatus`/`EpicBranch`/`ResolveState`/
`SetResolveState`/`ClearResolveState`/`Worktree`/`Locked`/
`DropEpicWorktreeLocked`/`TrivialResolve`/`Markers`/`Accept`/
`SetEpicMergeConflict`) + `ConflictRepo` (`MergeFeature`/`MergeBase`/`MergeTree`/
`Root`/`AddWorktree`/`RemoveWorktree`) и `ConflictWorktree` (`MergeBranch`/
`UnmergedFiles`/`Stage`/`Dirty`/`Commit`/`CommitAllowEmpty`); исходы несут
строковый `Status` (rebase: `resolving`/`ok`/`busy`; resolve: `ok`/
`still_conflicts`/`acceptance_failed`/`no_resolve`/`busy`/`conflicts`),
сентинелы `ErrResolveBusy`/`ErrNoResolve`/`ErrResolveStateSave` (прочие ошибки —
переиспользованы из F3i). `server/gitflow_resolve.go` оставил адаптер
`conflictHost` (встраивает `mergeTaskHost`; `Repo`/`Worktree` — через
`s.repoOf`/`git.RepoFromState`, `Accept` — `acceptor.Accept(dir, LoadConfig())`)
и `serverConflictRepo` поверх `*git.Repo` (`Root()` — геттер над полем);
хендлеры `handleEpicRebase`/`handleEpicResolve` сериализуют исходы и маппят
коды, `writeConflictErr` — sentinel'ы (404/503/500/502/400); `handleResolveStatus`
не менялся, `unionPaths` переехал в use-case. Побочные эффекты (session-сообщение,
`clearEpicMergeConflict`/`setEpicMergedIntoMain`, `srvEmitBoard`) остались в
хендлерах. Тесты: новые герметичные `application/gitflow/conflict_test.go`
(16 шт.: rebase no-conflicts/repeat/busy/все sentinel'ы состояния/сбой прогноза/
сбой записи состояния с cleanup; resolve ok dirty/clean, no_resolve/нет worktree/
busy/still_conflicts/acceptance_failed/final conflict/merge fatal); серверные
`gitflow_resolve_test.go` (742 строки, коды + JSON `status`) не менялись.
Верификация: канонический набор зелёный (26 пакетов), `-race` по
`application/gitflow` чист, `gofmt` по правкам чист. Документация: план (шапка +
таблица F3j), `docs/00-overview/project-map.md`. Остаток Этапа 6 — F3+:
сессии/оркестрация, остальные RAG-хуки. Коммиты F3g `fd6c49b`, F3h `f3ed4e6`,
F3i `606becf`, F3j `1c84b84` сделаны.

Срез **F3k** — RAG-хуки вынесены из `server/ragref.go` (490) и
`server/ragindex.go` (264) в новый пакет **`application/rag`**: `ref.go` —
`LoadRefItems`/`LoadRefDiff` (чтение содержимого ветки из git-объектов:
`git ls-tree`+`git show`, `git diff base...ref`+`git cat-file`),
`ParseLsTreeEntry`/`IndexablePath`/`IndexableContent`/`RefFileSize` (отбор
путей/файлов), `BranchIndexUpToDate` (пропуск свежего индекса), типы
`RefSnapshot`{Branch,CommitSHA,Items}/`Indexer`(`EnsureCollection`/`IndexProject`/
`BranchIndexed`)/`MaxRefFileBytes=2_000_000`; `index.go` — `CollectIndexItems`
(обход каталога через `WalkerFn`) и `IndexProjectRAG` (коллекция + `IndexProject`).
В `server` осталась оркестрация фоновых прогонов (`Session.startIndexBackground`,
single-flight по ветке, отчёты в чат), git-flow хуки
(`refreshRagIndexBranch`/`refreshRagIndexDir`/`refreshRagTaskBranch`/
`afterMainChanged`), синхрон рабочей копии (`syncAgentBranchWithMain`/
`syncAgentBranchAsync`/`headSHA`/`abortMerge`) — это срез F3m. Адаптеры:
`server/ragindex.go` — `type projectIndexer = apprag.Indexer`,
`buildProjectIndexer`/`startIndexBackground` без изменений (индексатор-кловер
`projectIndexerCloser`); `server/ragref.go` — `var loadRefItems = apprag.LoadRefItems`,
`var loadRefDiff = apprag.LoadRefDiff`, `refItemsLoader`/`refDiffLoader` возвращают
`apprag.RefSnapshot`; загрузчики-проверки вызывают `apprag.BranchIndexUpToDate`
(лог сохранён), `IndexDirBackground` — `apprag.IndexProjectRAG`. Тесты:
новые герметичные `application/rag/ref_test.go` (`refGit`-исполнитель,
`TestLoadRefItemsParsesAndFilters`, `TestLoadRefItemsBinaryAndErrors`,
`TestLoadRefDiffOnlyChanged`) и `application/rag/index_test.go`
(`TestCollectIndexItemsHermetic`, `TestIndexProjectRAGHermetic`,
`TestBranchIndexUpToDate`, `fakeProjectIndexer`, `writeTestFile`); серверные
`ragref_test.go`/`ragindex_test.go` потеряли переехавшие тесты и хелперы
(`refGit`/`lsTreeEntry`), `refSnapshot`→`apprag.RefSnapshot`, остальные
хуки/REST/сессия без изменений. Верификация: канонический набор зелёный (26
пакетов), `-race` по `application/rag` чист, `gofmt` по правкам чист.
Документация: план (шапка + таблица F3k + narrative),
`docs/00-overview/project-map.md` (новая строка `application/rag/`). Остаток
Этапа 6 — F3+: git-flow хуки, сессии/оркестрация, остальные RAG-хуки. Коммит
F3k `ceecbfd`.

Срез **F3l** — политика открытия git-проекта вынесена из `server/git.go` в
`application/gitflow/open.go`: `ProjectOpener.Open` (идемпотентное открытие —
`Registry.Get` → возврат meta; имя из URL `GitProjectName`; клон в
`temp/<имя>`; распознавание «полу-клона» `discardHalfClone` — обычный `.git`,
тот же origin через `SameGitRemote`, нет коммитов, чистое дерево → каталог
убирается и клонируется заново; регистрация проекта и сабмодулей через
`SubmoduleProjectName`). Порт `OpenRegistry` (`Get`/`Add`) реализуется
`*workspace.Registry` напрямую; сентинелы `ErrGitURLName`/`ErrDirNotEmpty`
(+типизированный `DirNotEmptyError` с `Is`, сообщение с путём)/
`ErrCloneFailed`/`ErrRegisterFailed`; `OpenedProject{Name,Info}`;
`RemoveAll`-хук для тестов. `server/git.go` оставил тонкий
`handleOpenGitProject` (маппинг 400 `ErrGitURLName` / 409 `ErrDirNotEmpty` /
502 `ErrCloneFailed` / 400 `ErrRegisterFailed` / 500 default →
`writeProjectMetaFromInfo`) и адаптер `s.projectOpener()` (поверх `s.gitExec`/
`s.reg`/`projects.ProjectDir`); обёртки `gitProjectName`/`submoduleProjectName`
сохранены (их зовут `git_test.go`/`gitflow_submodule_test.go` и обработчики
accept/reject), удалены локальные `discardHalfClone`/`sameGitRemote`/
`dirIsEmpty`. Тесты: новые герметичные `application/gitflow/open_test.go`
(`fakeExec`/`fakeOpenRegistry`: `GitProjectName`/`SubmoduleProjectName`/
`SameGitRemote`; плохой URL → `ErrGitURLName`; существующий проект идемпотентен
без клона; чужой непустой каталог → `ErrDirNotEmpty` с сохранением содержимого;
авточистка полу-клона (`--verify HEAD` падает, `--verify main` проходит) →
клон и регистрация; сбой клона → `ErrCloneFailed` и каталог удалён; сбой
реестра → `ErrRegisterFailed`). Серверные `git_test.go`/
`gitflow_submodule_test.go` не менялись. Верификация: канонический набор
зелёный (26 пакетов), `-race` по `application/gitflow` чист, `gofmt` по правкам
чист. Документация: план (шапка + таблица F3l + narrative),
`docs/00-overview/project-map.md` (строка `application/gitflow/` расширена
`ProjectOpener`/`open.go`). Остаток Этапа 6 — F3+: сессии/оркестрация,
остальные git-flow/RAG-хуки и прочие хендлеры `server/`. Коммит F3l `6a49fb5`.

Срез **F3m** — оркестрация фоновой индексации RAG вынесена из
`server/ragindex.go` в новый `application/rag/background.go`:
`BackgroundIndexer` (single-flight по слоту `indexSlotKey` — ветка/путь
источника; запуск прогона в горутине; контекст сбрасывается на
`context.Background()` — индексация не наследует отменённый контекст
вызывающего; отчёты о старте/завершении/актуальности/ошибке), порты `IndexSink`
(`Info`/`Warn`/`Status`) и `IndexerFactory` (`Indexer`+`io.Closer`), тип
`IndexRun`, `Wait()`. `server/ragindex.go` оставил `ragIndexSink` (лог проекта
+ чат), `sess.newProjectIndexer` (поверх `buildProjectIndexer`) и прогон
`IndexDirBackground` (плюс серверный `branchSuffix` для REST-ответа);
`server/ragref.go` — прогоны `IndexRefBackground`/`IndexRefDiffBackground`.
Поле `Session.indexing` заменено на `bgIndex` (`*apprag.BackgroundIndexer`),
создаётся в `newSession`. Тесты: новые герметичные
`application/rag/background_test.go` (`recordingSink`/`fakeCloser`/
`bgBlockingIndexer`: слот, single-flight + закрытие отказавшего клиента,
пропуск/ошибка/сбой фабрики, параллельные ветки); серверные
`ragindex_test.go`/`ragref_test.go` не менялись (переопределяют
`buildProjectIndexer`/`loadRefItems`/`loadRefDiff`) и зелёные. Верификация:
канонический набор зелёный (26 пакетов), `-race` по `application/rag` чист,
`gofmt` по правкам чист. Документация: план (шапка + таблица F3m + narrative),
`docs/00-overview/project-map.md` (строка `application/rag/`). Остаток Этапа 6
— F3+: `server/session.go` (оркестрация/HITL/событийность), git-flow хуки,
`chatassist`/`ask`/`actions`/`logs`/`diff`/`server`. Коммит F3m `492f3df`.

Срез **F3n** — серверная логика структурированного вопроса `AskUser` вынесена
из `server/ask.go` в новый пакет **`application/ask`**: `ParseQuestions` (разбор
и валидация пачки вопросов из аргументов инструмента), `Pending` (состояние
одного активного вопроса на сессию — блокирующий цикл `Ask`↔`Answer`),
`Summarize` (протокол ответов), `StrAny`/`BoolAny`; порт `Announcer`
(`Ask`/`Status`/`Info`). `server/ask.go` оставил инструмент-мост `askTool`
(`tools.Tool`), интерфейс `AskBackend`, схему `Definition`, таймаут
`askAnswerTimeout` и адаптер `askAnnouncer` (карточка-вардинг в чат `role=ask`,
статус-строка, лог); `Session` держит `pending *appask.Pending` (создаётся в
`newSession`) и делегирует `AskUser`/`AnswerAsk`. Тесты: новые герметичные
`application/ask/ask_test.go` (`recAnnouncer`: разбор/дефолты/ошибки схемы,
блокирующий цикл, охрана «один активный вопрос» и валидация ответа, отмена по
контексту с повторным вопросом, `Summarize`); серверные `ask_test.go` не
менялись и зелёные (идут через `Session`/REST). Верификация: канонический набор
зелёный (27 пакетов), `-race` по `application/ask` чист, `gofmt` по правкам
чист. Документация: план (шапка + таблица F3n + narrative),
`docs/00-overview/project-map.md` (новая строка `application/ask/`),
`docs/20-features/chat-assistant.md` (ссылки на `application/ask`). Остаток
Этапа 6 — F3+: `server/session.go` (оркестрация/HITL/событийность), git-flow
хуки, `chatassist`/`actions`/`logs`/`diff`/`server`. Коммит F3n `fcdc298`.

Срез **F3o** — логика панели логов вынесена из `server/logs.go` в новый пакет
**`application/logs`**: `CollectProject` (сбор `*.log` по каталогам, отбор
«своих» из общего каталога, дедупликация, сортировка), `CollectFiles`,
`ReadTail` (хвост с ограничением `MaxBytes`), `Filter`, `FileExists`, типы
`Entry`/`Response`, константы `MaxBytes`/`ClearMarker`. В `server/logs.go`
остались HTTP-хендлеры (`handleGetLogs`/`handleDeleteLogs`), `logDirs`/
`logsDir`, подписка брокера и алиасы типов (`logFileEntry`/`logsResponse`,
`logMaxBytes`) — серверные `logs_test.go`/`actions_logs_test.go` не менялись;
`server/actions_logs.go` и `server/loganalysis.go` продолжают звать
`collectProjectLogs` (тонкая обёртка над `applogs.CollectProject`). Тесты:
новые герметичные `application/logs/logs_test.go` (отбор/дедупликация/приоритет
общего каталога, только `*.log` и пропуск каталогов, обрезка хвоста и пустой
файл). Верификация: канонический набор зелёный (28 пакетов), `-race` по
`application/logs` чист, `gofmt` по правкам чист. Документация: план (шапка +
таблица F3o + narrative), `docs/00-overview/project-map.md` (новая строка
`application/logs/`), `docs/20-features/logging-and-events.md` (ссылки на
`application/logs`), `docs/20-features/chat-assistant.md`. Остаток Этапа 6 —
F3+: `server/session.go` (оркестрация/HITL/событийность), git-flow хуки,
`chatassist`/`actions`/`diff`/`server`. Коммит F3o `2b2934b`.

Срез **F3p** — разбор логов кнопкой «Разобрать» вынесен из
`server/loganalysis.go` в `application/logs/analysis.go`: `Req`/`Analysis`
(с `FilesLabel`/`Key`), `BuildAnalysis` (сбор логов проекта + фильтры
поиска/уровня ДО группировки + дайджест), `PickFiles` (по имени либо свежайшие,
файл проекта первым), `FilesLabel`, `DigestPrompt` (контракт ответа модели),
`Cooldown` (окно 90 с — двойной клик не зовёт модель дважды), `Dirs` (каталоги
логов проекта). `server/loganalysis.go` стал тонкой обвязкой: алиасы
`logAnalysisReq`/`logAnalysis`, `buildLogAnalysis` (проект из реестра + имя
своего лога + `applogs.BuildAnalysis`), `markAnalyzed` (делегирует
`logCooldown.Mark`), константа `logAnalysisCooldown`, обёртка `LogDigestPrompt`.
`Session` держит `logCooldown *applogs.Cooldown` вместо пары `logLastKey`/
`logLastAt`; `server/actions_logs.go` — `pickLogFiles` как обёртка над
`applogs.PickFiles`. Неэкспортируемый метод `key()` стал `Analysis.Key()`
(правка `server/loganalysis_test.go`/`server/server.go`). Тесты: новые
герметичные `application/logs/analysis_test.go` (фильтры+склейка, отсутствующий
файл, `PickFiles`, `FilesLabel`, контракт `DigestPrompt`, окно `Cooldown`,
`Dirs`); серверные `loganalysis_test.go`/`actions_logs_test.go` не менялись по
существу. Верификация: канонический набор зелёный (28 пакетов), `-race` по
`application/logs` чист, `gofmt` по правкам чист. Документация: план (шапка +
таблица F3p + narrative), `docs/00-overview/project-map.md` (строка
`application/logs/` расширена `BuildAnalysis`/`analysis.go`),
`docs/20-features/chat-assistant.md` (разбор логов → `application/logs`).
Остаток Этапа 6 — F3+: `server/session.go` (оркестрация/HITL/событийность),
git-flow хуки, `chatassist`/`actions`/`diff`/`server`. Коммит F3p `856f451`.

Срез **F3q** — разбор unified-диффа git вынесен из `server/diff.go` в новый
stdlib-only пакет **`domain/diff`** (`diff.go`): `Parse` (разбиение на файлы),
`Split` (блоки по `diff --git`/`diff --cc`), `ParseBlock` (метаданные + патч,
статус по `---`/`+++` и `rename to`, подсчёт `+`/`-`), `PathOf` (срез `a/`/`b/`,
git-кавычки), `PrefixPath` (префикс сабмодуля), тип `Status` с константами
`Added`/`Modified`/`Removed`/`Renamed`/`Submodule`, типы `FileEntry`/`Diff`.
В `server/diff.go` остались git-вызовы и кэш: `branchDiff` (worktree vs
merge-base), `gitProjectDiff` (кэш по `project|ref|base`, обход сабмодулей),
`invalidateDiffs`; `cachedDiff.Files` теперь `[]diff.FileEntry`, алиасы
`diffFileEntry`/`diffModified` сохранены для хендлеров `server/git.go`. Тесты
`server/diff_test.go` переехали в `domain/diff/diff_test.go` (`git mv`) и
дополнены (статусы/счётчики added/removed/modified, rename и кавычки, `Split`).
Верификация: канонический набор + `./domain/...` зелёные, `-race` по
`domain/diff` чист, `gofmt` по правкам чист. Документация: план (шапка +
таблица F3q + narrative), `docs/00-overview/project-map.md` (новая строка
`domain/diff/`), `docs/20-features/{contextual-diff,gitops-workflow,web-ui,
submodules-multirepo}.md` (ссылки на разбор → `domain/diff`). Остаток Этапа 6 —
F3+: `server/session.go` (оркестрация/HITL/событийность), git-flow хуки,
`chatassist`/`actions`/`server`. Коммит F3q `9bb6b6a`.

Срез **F3r** — жизненный цикл worktree задачи/эпика вынесен из
`server/gitflow_auto.go` в `application/gitflow` (`lifecycle.go`):
`WorktreeManager` (порт `WorktreeHost`) — `TaskWorktree` (Ф-3: синхрон релизной
ветки эпика с main, rebase ветки задачи на эпик, создание worktree
`ai/task/<id>`, подъём worktree сабмодулей + их регистрация, checkpoint base_sha,
индекс изменений ветки задачи), `EpicWorktree` (4.1: ленивый worktree релизной
ветки эпика `ai/epic/<id>` под пер-проектным локом) и
`DropEpicWorktreeLocked` (снятие worktree эпика с авто-коммитом правок
архитектора). `server/gitflow_auto.go` оставил адаптер `worktreeHost` (поверх
`reg`/`repoOf`/`gitExec`/`mergeLock`/RAG-хуков/`srvEmitBoard`/
`recordTaskBaseSHA`) и тонкие обёртки `s.taskWorktree`/`s.epicWorktree`/
`s.dropEpicWorktreeLocked` (потребители `server/gitflow.go`, `gitflow_ar.go`,
`gitflow_sync.go`, `session.go` не менялись). Тесты: новые герметичные
`application/gitflow/lifecycle_test.go` (пропуски не-git/пустого ID/готового
worktree, полный цикл задачи, отсутствие ветки эпика, сбой `git worktree add`,
worktree эпика — существующий/создание, снятие); серверные
`gitflow_epic_worktree_test.go`/`gitflow_submodule_test.go`/
`gitflow_done_guard_test.go`/`gitflow_auto_e2e_test.go` не менялись. Верификация:
канонический набор зелёный (28 пакетов), `-race` по `application/gitflow` чист,
`gofmt` по правкам чист. Документация: план (шапка + таблица F3r + narrative),
`docs/00-overview/project-map.md` (строка `application/gitflow/` расширена
`WorktreeManager`/`lifecycle.go`). Остаток Этапа 6 — F3+: RAG-хуки
(`server/ragref.go`), `server/session.go` (оркестрация/HITL/событийность),
`chatassist`/`actions`/`server`. Коммит F3r `8346cfa`.

Срез **F3s** — политика синхронизации рабочей копии агентов с main вынесена из
`server/ragref.go` в `application/rag/sync.go`: `WorkingCopySyncer` (порт
`SyncSink` с `Detail`/`Warn`/`Info`) с методом `Sync` — подтягивание `main` в
ветку рабочей копии: пропуск при не-git/совпадении веток, при занятом checkout
(`repo.Branch != agentBranch`) и грязном дереве (`git status --porcelain`);
конфликтный merge откатывается (`git merge --abort`); признак реального
обновления — сдвиг HEAD; helpers `headSHA`/`abortMerge` над `git.Executor`.
`server/ragref.go` оставил адаптер `syncSink` (лог проекта) и тонкую обёртку
`s.syncAgentBranchWithMain` — сигнатура сохранена, `server/ragref_test.go` не
менялся; `syncAgentBranchAsync`/`afterMainChanged`/`refreshRagIndex*` остались в
`server` (завязаны на `Session`). Тесты: новые герметичные
`application/rag/sync_test.go` (`syncExec`/`syncSinkRec`: пропуски пустой
базы/ветки/nil repo, занятый checkout, грязное дерево, откат конфликта, сдвиг
HEAD → true, без сдвига → false, текст merge). Верификация: канонический набор
зелёный (28 пакетов; один прогон упал на пред-существующем флаке
`TestChatAssistantRejectBranchAfterConfirm` — в изоляции и на повторе `./server/`
зелёный), `-race` по `application/rag` чист, `gofmt` по правкам чист.
Документация: план (шапка + таблица F3s + narrative),
`docs/00-overview/project-map.md` (строка `application/rag/` расширена
`WorkingCopySyncer`/`sync.go`). Остаток Этапа 6 — F3+: RAG-хуки
(`refreshRagIndex*`/`syncAgentBranchAsync`/`afterMainChanged`), `server/
session.go` (оркестрация/HITL/событийность), `chatassist`/`actions`/`server`.
Коммит F3s `0ede388`.

Срез **F3t** — конкретные фоновые прогоны индексации RAG вынесены из
`server/ragref.go`/`server/ragindex.go` в `application/rag/jobs.go`: `RefJob`
(содержимое ветки по ref: загрузка снапшота, пропуск актуального индекса
`BranchIndexUpToDate`, `EnsureCollection` + `IndexProject`), `RefDiffJob`
(изменения ветки относительно базы) и `DirJob` (каталог под заранее
определённой веткой/коммитом, `IndexProjectRAG`). Порты — `RefLoader`/
`DiffLoader` (`func`-типы) и `IndexSink` для отчётов; `orDash` вместо серверного
`orEmpty`. `server/ragref.go` — `loadRefItems`/`loadRefDiff` стали значениями
`apprag.RefLoader`/`apprag.DiffLoader`, `ragIndexSink` (лог проекта + чат);
`IndexRefBackground`/`IndexRefDiffBackground`/`IndexDirBackground` — тонкие
обёртки, собирающие job и запускающие через `startIndexBackground` (single-flight
`BackgroundIndexer`). Тесты: новые герметичные `application/rag/jobs_test.go`
(`RefJob` — полный прогон/пропуск актуального индекса/ошибка загрузчика,
`RefDiffJob`, `DirJob` на реальном temp-каталоге); серверные
`ragref_test.go`/`ragindex_test.go` не менялись (переопределяют
`buildProjectIndexer`/`loadRefItems`/`loadRefDiff`). Верификация: канонический
набор зелёный (28 пакетов), `-race` по `application/rag` чист, `gofmt` по
правкам чист. Документация: план (шапка + таблица F3t + narrative),
`docs/00-overview/project-map.md` (строка `application/rag/` расширена
`RefJob`/`RefDiffJob`/`DirJob`/`jobs.go`). Остаток Этапа 6 — F3+: RAG-хуки
(`refreshRagIndex*`/`syncAgentBranchAsync`/`afterMainChanged`), `server/
session.go` (оркестрация/HITL/событийность), `chatassist`/`actions`/`server`.
Коммит F3t `eeea0a5`.

Срез **F3u** — набор сессионных промпт-инъекций вынесен из `server/session.go` в
`domain` (`injectionset.go`): `InjectionSet` (потокобезопасный, под собственным
mutex) с `List` (копия наружу), `Add`, `Upsert` (перезапись записи с тем же ID —
повторная запись правит, а не плодит дубли; пустой ID → ошибка) и `Remove`
(честный признак наличия). `Session` делегирует
`SessionInjections`/`AddSessionInjection`/`UpsertSessionInjection`/
`RemoveSessionInjection` через ленивый `injections()` (набор создаётся при первом
обращении) — нулевая `Session` (её собирают тесты) остаётся работоспособной.
Поле `sessionInjections` сменило тип `[]board.Injection` → `*domain.InjectionSet`.
Тесты: новые герметичные `domain/injectionset_test.go` (пустой набор → nil, копия
не мутирует набор, upsert-добавление/перезапись без дубля, удаление/отсутствие);
серверный `TestSessionInjectionHotApply` (идёт через `Session`) не менялся.
Верификация: канонический набор зелёный (28 пакетов; один прогон упал на
пред-существующем флаке `TestChatAssistantRejectBranchAfterConfirm` — в изоляции
и на повторе `./server/` зелёный), `-race` по `domain` чист, `gofmt` по правкам
чист. Документация: план (шапка + таблица F3u + narrative),
`docs/00-overview/project-map.md` (строка `domain/` расширена `injectionset.go`).
Остаток Этапа 6 — F3+: HITL-гейт и read-model доски в `server/session.go`,
`chatassist`/`actions`/`server`. Коммит F3u `ddae990`.

Срез **F3v** — правка и презентация конфликтных файлов модели вынесены из
`server/actions_resolve.go` в `application/gitflow` (`conflictfiles.go`):
`NewConflictView` (отбор файлов с остаточными маркерами + фрагмент вокруг
маркеров для больших файлов), `ConflictRegion`/`IsConflictMarkerLine`,
`ApplyConflictFiles` (запись `files` — полное содержимое, затем `edits` —
точечные замены с проверкой уникальности `old`), `ConflictSafeRel`/`WithinDir`
(безопасность пути), типы `ConflictEdit`/`ConflictView`, константы
`MaxConflictFileBytes`/`MaxConflictFiles`. `server/actions_resolve.go` оставил
алиасы (`type ConflictEdit = gitflow.ConflictEdit`,
`type conflictView = gitflow.ConflictView`) и тонкие обёртки
`newConflictView`/`conflictSafeRel`/`withinDir` (вызовы ассистента и тестов не
менялись), а `conflictApply` зовёт `gitflow.ApplyConflictFiles`. Удалены
переехавшие локальные функции/константы и импорт `path/filepath`. Тесты: новые
герметичные `application/gitflow/conflictfiles_test.go` (safe-rel/within-dir,
truncate больших файлов, чистые файлы без маркеров, apply files+edits, отказы:
не конфликтный файл, выход за worktree, неуникальный/пустой `old`, маркеры/
регион); серверный `actions_resolve_test.go` не менялся (идёт через обёртки).
Верификация: канонический набор зелёный (28 пакетов), `-race` по
`application/gitflow` чист, `gofmt` по правкам чист. Документация: план (шапка +
таблица F3v + narrative), `docs/00-overview/project-map.md` (строка
`application/gitflow/` расширена `conflictfiles.go`). Остаток Этапа 6 — F3+:
HITL-гейт и read-model доски в `server/session.go`, `chatassist`/`actions`/
`server`. Коммит F3v `00d29e1`.

Срез **F3w** — детерминированная политика диалога чат-ассистента вынесена из
`server/chatassist.go`/`server/actions.go` в новый пакет **`application/chatassist`**
(`policy.go`): распознавание согласия (`ConfirmAffirmative`/`BareConfirm` +
`ConfirmTokens`), вопроса (`AsksSomething`), смены темы (`PrevUserDiffers`),
«отчёта о записи на доске» (`LooksLikeBoardWriteClaim` + узкий список фраз),
сводка structured-вопроса (`AskSummary(*chat.Ask)`) и антизацикливающая подсказка
`ActionAnchor(hist []chat.Message, question string)` (вся логика бывшего
`chatActionAnchor`, кроме чтения истории). `server/chatassist.go` оставил
`chatActionAnchor` (читает `chat.Store.History` и делегирует) и тонкие обёртки
`looksLikeBoardWriteClaim`/`askSummary`; `server/actions.go` — обёртку
`confirmAffirmative`, `ActionConfirmed` без изменений. Удалены переехавшие
`asksSomething`/`prevUserDiffers`/`bareConfirm`/`boardWriteClaimPhrases`. Тесты:
новые герметичные `application/chatassist/policy_test.go` (согласие/не-согласие,
bare-согласие, отчёт о записи, вопрос, смена темы, `AskSummary`, якорь:
подтверждение/lf/новая тема/два вопроса/пусто/role=ask); серверные
`actions_test.go`/`chatassist_test.go` не менялись (идут через обёртки).
Верификация: канонический набор зелёный (29 пакетов), `-race` по
`application/chatassist` чист, `gofmt` по правкам чист. Документация: план
(шапка + таблица F3w + narrative), `docs/00-overview/project-map.md` (новая
строка `application/chatassist/`). Остаток Этапа 6 — F3+: HITL-гейт и read-model
доски в `server/session.go`, `server`-хендлеры. Коммит F3w `de5642b`.

Срез **F3x** — HITL-затвор (подтверждение человеком публикации эпиков/задач)
вынесен из `server/session.go` в новый пакет **`application/hitl`**
(`gate.go`): `Gate.Wait` (публикует событие затвора + статус `waiting`,
блокирует до решения или отмены контекста, `Reset` по завершении),
`Gate.Approve` (проверка «идёт оркестрация» + совпадение типа затвора, решение
в канал буфера 1), `Gate.State` (консистентные `gating`/`typ`/`msg`) и тип
`GateEvent`. Эффекты сессии инвертированы портом `GateHost`
(`Stopped`/`Running`/`BroadcastStatus`/`PublishGate`). `server/session.go`
оставил поле `gate *hitl.Gate` (ленивый `gateOf()` — нулевая сессия
работоспособна), методы-обёртки `Epics`/`Tasks` (вычисляют ids → `Wait`),
`approve` (префикс проекта в ошибке) и реализацию `GateHost`;
`broadcastStatus`/`broadcastSnapshot`/`setStandby`/`server.sessionFlags`/
`server/chatassist.go` читают состояние через `gate.State()`; `gateEvent` —
алиас `hitl.GateEvent`. Порядок блокировок: `gate.mu` берётся только на время
смены состояния, хостовые методы зовутся БЕЗ удержания `gate.mu`. Тесты: новые
герметичные `application/hitl/gate_test.go` (`fakeHost`: публикация+блокировка+
approve, отказ, отмена контекста → `Reset`, остановленная сессия → мгновенный
`context.Canceled` без события, `Approve` без затвора/без оркестрации/чужой тип,
снятие «залежавшегося» решения, `Reset`); серверный
`TestSessionSnapshotReplaysActiveGate` поправлен на `gateOf().State()`
(был доступ к полям `sess.gating`/`sess.gateMsg`). Верификация: канонический
набор зелёный (30 пакетов), `-race` по `application/hitl` чист, `gofmt` по
правкам чист. Документация: план (шапка + таблица F3x + narrative),
`docs/00-overview/project-map.md` (новая строка `application/hitl/`). Остаток
Этапа 6 — F3+: read-model доски в `server/session.go`, `server`-хендлеры.
Коммит F3x `a0c9eb3`.

Срез **F3y** — read-model доски вынесен из `server/session.go` в новый пакет
**`application/board`** (`view.go`): `View` (сборка снимка — эпики в эффективном
порядке очереди через `infrastructure/board.SortEpics`, задачи, баги, meta),
`ViewPage` (пагинация + полные `Total`-счётчики), `SlicePage[T]`,
`TruncateText`, типы `Snapshot`/`Total`, порт `Store`
(`ListEpics`/`ListTasks`/`ListBugReports`/`GetMeta`). Git-статус проекта в снимок
НЕ входит (презентация git-flow завязана на реестр — остаётся на сервере).
`server/session.go`: `boardSnapshot` теперь встраивает `appboard.Snapshot` и
добавляет поле `Git *gitView`; `boardView`/`boardViewPage` — тонкие обёртки,
`truncateText` делегирует `appboard.TruncateText`; серверные типы
`boardTotal`/`slicePage` удалены (переехали). Тесты: новые герметичные
`application/board/view_test.go` (`fakeStore`: порядок эпиков по приоритету,
проброс ошибки, полный снимок + пагинация/offset/beyond, `SlicePage`,
`TruncateText`); серверные `boardpage_test.go` (зовёт
`boardView`/`boardViewPage`) / `chatassist_test.go` / `gitflow_mr_test.go` /
`injections_api_test.go` / `server_test.go` (тип `boardSnapshot`) не менялись.
Верификация: канонический набор зелёный (31 пакет; один прогон `./server/`
упал на пред-существующем флаке — на повторе зелёный), `-race` по
`application/board` чист, `gofmt` по правкам чист. Документация: план (шапка +
таблица F3y + narrative), `docs/00-overview/project-map.md` (новая строка
`application/board/`). Остаток Этапа 6 — F3+: `server`-хендлеры. Коммит F3y `991da8c`.

Срез **F3z** — детерминированная сборка сводки доски для промпта чат-ассистента
вынесена из `server/chatassist.go` (`chatPromptFor`) в
**`application/chatassist`** (`boardprompt.go`): `BoardSummary(BoardView)`
(исходная задача, эпики, задачи по статусам + список в работе, конфликты мёрджа,
баги), `OrchestrationStatus(running, gating)` (затвор важнее запуска),
`BoardRevisionLine(rev uint64)`, тип `BoardView`. `server/chatassist.go` оставил
в `chatPromptFor` только чтение рантайма сессии (`running`/`gate.State()`/
`boardRev`), вызов `boardView` и fallback «Kanban-доска недоступна» + дайджест
логов; текст сводки делегируется `appchatassist`. Тесты: новые герметичные
`application/chatassist/boardprompt_test.go` (статус/ревизия, пустая доска,
полная сводка: эпики/задачи в работе/конфликты/баги); серверные
`chatassist_test.go` (`TestChatAssistPromptHasBoardContext`/
`TestChatAssistPromptShowsMergeConflict`) не менялись (текст воспроизведён
дословно). Верификация: канонический набор зелёный (31 пакет), `-race` по
`application/chatassist` чист, `gofmt` по правкам чист. Документация: план (шапка
+ таблица F3z + narrative), `docs/00-overview/project-map.md` (строка
`application/chatassist/` расширена). Остаток Этапа 6 — F3+: `server`-хендлеры.
Коммит F3z `eb9935e`.

Срез **F3aa** — кольцевой буфер логов рантайма вынесен из `server/applog.go` в
`application/logs/applog.go`: `Line`/`RingBuffer` (`Push`/`Tail`/`TakeDirty`) +
константы `BufferLines`/`DefaultTail`/`MaxTail`. Серверный `appLogBuffer` стал
адаптером с прежними именами методов (`push`/`tail`/`takeDirty`), поэтому код
сессии и REST-хендлера не менялся. Тесты: новые
`application/logs/applog_test.go`; серверные `applog_test.go` не менялись (идут
через адаптер). Верификация: канонический набор зелёный (на живом прогоне 6 тестов
`TestChatAssistant*` упали на предсуществующем флаке окружения — подтверждено на
`ace1ba7`, до Этапа 6; в изоляции и на повторе стабильно падают),
`-race` по `application/logs` чист, `gofmt` по правкам чист. Документация: план
(шапка + таблица F3aa + narrative), `docs/00-overview/project-map.md` (строка
`application/logs/` расширена `applog.go`). Остаток Этапа 6 — F3+:
`server`-хендлеры. Коммит F3aa `067c46d`.

Срез **F3ab** — инкрементальный хвост лог-файлов вынесен из `server/logbroker.go`
в `application/logtail/tail.go`: `Broker` (`Run`/`Subscribe`/`Broadcast`/`Rescan`),
`SubFile` (`OpenAtEnd`/`OpenFromStart`/`Rotated`/`ReadNew`/`Close`), `Line`,
`ListLogFiles`, константы `ScanEvery`/`HeadLen`/`TickInterval`; порт —
`Publish func(project, line Line)`. Серверный `logBroker` стал тонким адаптером,
публикующим строки в хаб (`logMessage`). Тесты: перенесены
`server/logbroker_test.go` → `application/logtail/tail_test.go` (package
`logtail`, экспортированные имена, `recordingBroker`; добавлен
`TestBrokerRunPublishesAndStops`); серверный `TestLogBrokerBroadcastPublishesNewLines`
(WS-хаб) не менялся. Верификация: канонический набор зелёный (на живом прогоне
чат-ассистентские флаки ушли — окружение вернулось), `-race` по
`application/logtail` чист, `gofmt` по правкам чист. Документация: план (шапка +
таблица F3ab + narrative), `docs/00-overview/project-map.md` (новая строка
`application/logtail/`). Остаток Этапа 6 — F3+: `server`-хендлеры. Коммит F3ab `1cff506`.

Срез **F3ac** — чтение хвоста лог-файла с grep/limit/truncation вынесено из
`server/actions_logs.go` в `application/logs/readslice.go`: `ReadLogSlice`
(хвост файла с `readMaxBytes`, фильтр grep, лимит строк, обрезка содержимого
по `contentMaxBytes`, тип `Slice`). Серверный `readLogSlice` стал тонкой
обёрткой, маппингом `Slice` → `logToolFile`. Тесты: новые
`application/logs/readslice_test.go` (базовое чтение, grep, лимит,
отсутствующий файл, обрезка по байтам); серверные `actions_logs_test.go` не
менялись. Верификация: канонический набор зелёный (предсуществующий флак
`TestChatAssistant*` на живом прогоне — в изоляции зелёный), `-race` по
`application/logs` чист, `gofmt` по правкам чист. Документация: план (шапка +
таблица F3ac + narrative), `docs/00-overview/project-map.md` (строка
`application/logs/` расширена `readslice.go`). Остаток Этапа 6 — F3+:
`server`-хендлеры. Коммит F3ac `6bef367`.

## Состояние последней сессии (Clean Architecture Этап 4, срезы E1–E5 + E6a/E6b/E6e1 — каркас `infrastructure/`)

Начат Этап 4 (`PLAN-2026-10-12-todo-clean-architecture.md`), разбит на срезы
E1–E6 (таблица в разделе «Этап 4» плана). Срез **E1** закрыт: создан каталог
`infrastructure/`, пакет `models` переехал в `infrastructure/llm`
(`git mv models infrastructure/llm`) и переименован `models` → `llm`; импортёры
(`main.go`, `repositories/Repository.go`, `server/{resolve,server,session,chatassist,gitflow_ar}.go`
+ тесты) переведены на `ai/infrastructure/llm` и квалификатор `llm.`. Новый
`infrastructure/llm/ports.go` фиксирует реализацию порта
`domain/services.LLMClient` для `*OllamaProvider`/`*LayeredProvider`
(`*OpenAIProvider` пока без `ChatStream` — покроется при переводе OpenAI-пути
на порт). Правка чисто механическая (перенос+переименование), поведение не
менялось.

Срез **E2** закрыт: `git mv rag infrastructure/rag` (пакет `rag` сохранён), 25
импортёров переведены на `ai/infrastructure/rag`. Порт `services.RAGClient` не
совпадал с богатым API `rag.Client`, поэтому добавлен адаптер
`rag.ProjectAdapter` (`infrastructure/rag/port.go`, `NewProjectAdapter(client,
project, dir)`) + compile-assertion; тесты `infrastructure/rag/port_test.go`
(4 шт., hermetic). Замечена инфра→инфра-зависимость `rag` → `ai/forges`
(`forges.IsIgnoredDir` в `walk.go`) — кандидат на устранение при ревизии
`forges`.

Срез **E3** закрыт: `git mv gitops infrastructure/git`, пакет `gitops`
переименован в `git`; 28 импортёров переведены на `ai/infrastructure/git` и
квалификатор `git.` (в 2 тестах `server/ragref_test.go` локальный fake-исполнитель
переименован `git` → `fakeExec`, чтобы не затенять пакет). Старая «эскизная»
форма порта `services.GitOperations` не совпадала с реальным API gitops —
переписана в набор методов рабочей копии с подписями `*git.Repo`, поэтому порт
реализуется **напрямую без адаптера** (assertion в `infrastructure/git/port.go`
+ исполняемый `infrastructure/git/port_test.go`). Операции с инфраструктурными
типами (`MergeResult`/`WorktreeInfo`/`Submodule`) и создание копии (нужен
`Executor`) в порт не вошли — остаются у `*Repo`.

Срез **E4** закрыт: `git mv sandbox infrastructure/sandbox` (пакет `sandbox`
сохранён), 6 импортёров переведены на `ai/infrastructure/sandbox`. Порт
`services.SandboxEngine` (старая «эскизная» форма Start/Stop/Exec/CopyIn/CopyOut)
противоречил дизайну пакета (файловые операции — на хосте, bind-mount) —
переписан в фактическую абстракцию `services.SandboxWorkspace{Run; Close}` с
`services.SandboxResult`. Чтобы обойтись без адаптера, `Result` и `Workspace` в
пакете стали **alias'ами** типов домена, поэтому `*LocalWorkspace`/
`*DockerWorkspace` удовлетворяют порту напрямую (assertion в
`infrastructure/sandbox/port.go`); прежние `sandbox.Result`/`sandbox.Workspace`
работают как раньше. Обновлён read-путь теста `tools/sandbox_test.go`
(`../infrastructure/sandbox/{compose.yaml,Dockerfile}`) и комментарии-пути.

Срез **E5** закрыт: `git mv board infrastructure/board` (пакет `board` сохранён),
все 75 импортёров переведены на `ai/infrastructure/board` (agents/{developer,
chatassist,architect}, application/planner, injections, runner/adapters, server/*,
tokens, tools/*, main.go и харнессы). Правка чисто механическая — путь импорта;
поведение и `board.*`-квалификатор не менялись. Из канонического набора убран
`./board/` (теперь входит в `./infrastructure/...`); в живых доках каталог
`board/` → `infrastructure/board/` (`architecture.md`, `glossary.md`,
`project-map.md`, `installation.md`, `quickstart.md`, `kanban-board.md`,
`tokens-and-metrics.md`, `adding-a-feature.md`, `development.md`, `testing.md`,
`troubleshooting.md`).

Остался срез Этапа 4 — E6 (перевести `tools/` на `domain/services` и разбить
пакет по доменам); он разбит на под-срезы E6a–E6e. **E6a** закрыт: `tools`
(non-test) больше не импортирует `ai/infrastructure/board` — `DecodeComments`
перенесён в `domain` (`domain/comment.go`), `board.DecodeComments` делегирует,
`tools/board_tools.go` берёт `domain.{ErrExists,DecodeInjections,ValidateInjections,
DecodeComments}`. **E6e1** закрыт (пользователь выбрал направление «разбиение
`tools/` на под-пакеты по доменам, под-пакеты вправе импортировать
`infrastructure/*`»; «без инфра-импортов» для rich-API rag/git/sandbox
недостижимо без загрязнения `domain`): создан `tools/git` (`package git`) —
вся логика `ResolveGitConflicts` перенесена из `tools/gitresolve.go`, инфра-git
импортируется как `gitops`, зависимость от `FileOps` инвертирована интерфейсом
`git.Ops{TakeTouched;LspCheckFiles}` + `Dir string`; в `tools` остался алиас
`const ResolveGitConflicts = git.ResolveToolName`, `registry.go` собирает
`&git.ResolveTool{…}`. Ключ к отсутствию цикла: `Tool`/`ToolDefinition` уже в
`domain`/`domain/services`, под-пакет реализует `services.Tool` напрямую.
**E6b** закрыт: RAG-инструменты (`CodeSearch`/`RagIndexStatus`) вынесены в
`ai/tools/rag` (package `rag`), инфра-RAG импортируется как `infrarag`; обратная
зависимость от `FileOps` инвертирована интерфейсом
`rag.Context{ProjectName; WorkingDir}` — обе величины читаются ЛЕНИВО
(`SetProjectName`/`SetOutputDir` зовутся после сборки набора); в `tools` —
алиасы `RAGSearcher`/`CodeSearch`/`RagIndexStatus`, `registry.go` собирает
`&rag.SearchTool`/`&rag.StatusTool`; в `FileOps` добавлен `WorkingDir()`,
nil-проверка `ProjectName` поправлена; тесты переехали в `tools/rag`, фолбэк
имени проекта покрыт `tools/fileops_projectname_test.go`.
Осталось: E6e-остаток (`tools/lsp` и др.). E6d выполнен в E6e1 (отдельного
git-инструмента больше нет); E6c (`tools/sandbox`) отложен: `sandbox`-исполнение
завязано на `tools` (`runCommand` остаётся в `tools`, сессионная песочница
`Activate`/`Deactivate`/`SandboxNetwork` и её тесты `sandbox_session_test.go`
живут в `tools` и вызывают `runCommand`) — чистого листа для выноса нет.
Верификация: канонический
набор (в него добавлены `./tools/...`, `./infrastructure/...`; убраны `./sandbox/`
и `./board/`) зелёный, `-race` по `infrastructure/{llm,rag,git,sandbox}` чист,
`gofmt -l` по своим файлам чист.
Документация: раздел «Этап 4» + шапка плана, пути `models/` →
`infrastructure/llm/`, `rag/` → `infrastructure/rag/`, `gitops/` →
`infrastructure/git/` (и квалификатор `gitops.` → `git.`), `sandbox/` →
`infrastructure/sandbox/`, `board/` → `infrastructure/board/` в живых доках
(`architecture.md`, `project-map.md`, `providers.md`, `context-compression.md`,
`agents-and-roles.md`, `glossary.md`, `rag.md`, `submodules-multirepo.md`,
`gitops-workflow.md`, `sandbox.md`, `kanban-board.md`, `tokens-and-metrics.md`,
`configuration.md`, `installation.md`, `quickstart.md`, `infrastructure.md`,
`makefile-contract.md`, `troubleshooting.md`, `adding-a-feature.md`,
`development.md`, `testing.md`), канонический набор и примечание в этом файле.
Коммиты E1 (`4279c9f`), E2 (`2e71139`), E3 (`3e01f8f`), E4 (`ab39ad6`),
E5 (`91ca525`), E6a (`1f87352`), E6b (`a5444c5`), E6e1 (`c78f039`) сделаны.

## Состояние последней сессии (Clean Architecture Этап 3, срез C6 — полная миграция planner)

Срез C6 завершён: модели доски сведены с доменом (`domain.TaskSpec`/`Task`/`Epic`
получили `FlexInt`/`FlexBool` и string-таймстампы, как `board.*`), конвертеры
сводятся к identity, `EpicLess`/`EpicPhase`/`SortEpics`/`UnmarshalTasks`
перенесены в `domain` (`domain/order.go`, `domain/unmarshal.go`).
`KanbanRunner.store` переведён на `services.BoardStore`, все `board.*`-обращения
planner мигрированы на `domain.*`/`services.*`. Стор-порт агентов (`developer`,
`backendlead`, `frontendlead`, `devopslead`, `devops`, `qaengineer`, `chatassist`,
`architect`) переведён на `services.BoardStore`. Вызовы `NewKanbanRunner` в
`server/session.go` и `main.go` обёрнуты в `board.NewDomainStore`. Верификация:
`go build`/`go vet`/`go test` по каноническому набору зелёные, кроме ровно 17
базовых падений `application/planner` (состав не менялся — не регресс).
Models-порт вынесен из `models` (порт `planner.LLMProvider`,
`domain.WithHeavyModel`/`domain.HeavyModelFor`, `models/force.go` удалён);
Acceptor-порт вынесен из `acceptor`: отчёт приёмки — `domain.Report`
(`domain/acceptance.go`), порт `planner.Acceptor` (`MaxRounds`/`Accept`),
адаптер `acceptor.PlannerAdapter` подключается в `main.go`; `executor.go`
больше не импортирует `acceptor`. Остаток пункта 3: Tools-порт (planner
по-прежнему импортирует `tools`); `runner` в non-test коде planner
отсутствует.

## `temp/` — хранилище проектов (не трогать)

Каталог `temp/` — рабочее хранилище проектов, которые генерируют и дорабатывают
автоматические агенты-разработчики (backend/frontend и другие). Это **не исходный код
проекта**, а артефакты выполнения.

Правила для любого агента (включая opencode):

- **НЕ удаляй `temp/` целиком** и его содержимое по команде «почисти/очисти» —
  там живут пользовательские проекты.
- **НЕ редактируй** файлы внутри `temp/` напрямую, если задача не касается
  конкретного проекта: этот каталог — выходная директория агентов, а не
  редактируемый исходник.
- **НЕ коммить** содержимое `temp/` в git: оно игнорируется (см. `temp/.gitignore`),
  но не добавляй его в индексацию вручную.
- Агенты, работающие с проектом, используют единый путь
  `temp/<имя_проекта>/` (см. `projects.ProjectDir`) и не выходят за
  его пределы.
- Если задача требует изменить код внутри уже созданного проекта — работай через
  агента-разработчика (`backend`/`frontend`) соответствующей специализации, а не правь
  файлы в `temp/` как исходник.

## Сборка и проверка

Рабочая сборка (обходит известный блокер в `temp/`):

```
go build . ./agents/... ./application/... ./tools/... ./cmd/... ./server/ ./runner/ ./infrastructure/... ./domain/...
go vet . ./agents/... ./application/... ./tools/... ./cmd/... ./server/ ./runner/ ./infrastructure/... ./domain/...
go test . ./agents/... ./application/... ./tools/... ./cmd/... ./server/ ./runner/ ./infrastructure/... ./domain/...
```

Примечание: `./application/planner` — бывший `agents/planner` (перенос по
PLAN-2026-10-12, этап 3); предсуществовавшие падения его тестов разобраны —
набор зелёный. `./infrastructure/llm` — бывший `models/` (Этап 4, срез E1);
`./infrastructure/rag` — бывший `rag/` (срез E2); `./infrastructure/git` —
бывший `gitops/` (срез E3); `./infrastructure/sandbox` — бывший `sandbox/`
(срез E4); `./infrastructure/board` — бывший `board/` (срез E5, `./board/`
убран из перечня). `./tools/...` — с под-пакетами `tools/git` (E6e1),
`tools/lspclient`, `tools/binpath`. `./domain/...` — доменные пакеты с тестами
(`domain/log`, `domain/diff`), добавлен на Этапе 6.

Живая проверка сессионной песочницы (нужен Docker; вне рабочего набора,
по образцу `E2E_LIVE`):

```
SANDBOX_LIVE=1 go test ./infrastructure/sandbox/ -run TestLiveSession -v
```

Известные нюансы:
- `go build ./...` падает на `temp/moon-distance/internal/middleware/cors.go`
  (артефакт сгенерированного проекта) — используй перечень выше.
- `gofmt -l .` показывает 29 файлов с предсуществующими отклонениями
  форматирования (часто — отсутствующая завершающая строка). Это не гейт:
  запускай `gofmt -l`/`gofmt -w` только на своих файлах, иначе в коммит
  попадёт форматирование чужого кода.
- На macOS (GNU Make 3.81) падают два теста `agents/acceptor`:
  `TestAcceptBuildFallsBackToInfraMirror` и
  `TestAcceptBuildToolMissingSkips` — они ждут make-маркер
  (`Ошибка`/`Error 127`), которого нет в выводе локального GNU Make
  (`make: *** [build] Error 1`). Зависит от хоста, не регрессия.

## Токено-эффективные Bash-команды

Контекстное окно модели — конечный ресурс: `cat` целиком, `grep -r` без
исключений и полный вывод сборки съедают его быстрее, чем задача продвигается.

**Приоритет инструментов (что первично):**
1. Структурированные инструменты и LSP — поиск символов
   (`LspDefinition`/`LspReferences`/`LspHover`) и чтение кода
   (`List`/`ReadMap`/`ReadFiles` с `lines`).
2. Bash — только там, где аналога нет: git-контекст, компактный вывод
   проверок, точечный поиск по тексту.

**Разведка, поиск, чтение:**

| Задача | Команда | Приоритет |
|---|---|---|
| Структура (быстро) | `find . -maxdepth 3 -not -path '*/.*' -not -path '*node_modules*'` | фолбэк (дублирует `List`) |
| Поиск по тексту | `grep -rnw --exclude-dir={node_modules,.git,dist} "символ" .` | фолбэк (дублирует LSP/`CodeSearch`) |
| Импорты и заголовок | `sed -n '1,50p' путь` (или `head -n 50 путь`) | фолбэк (дублирует `ReadMap`) |
| Список функций (py) | `python3 -c "import ast; print([n.name for n in ast.parse(open('f.py').read()).body if isinstance(n, ast.FunctionDef)])"` | фолбэк |
| Git-контекст | `git log -n 3 --oneline -- путь`, `git blame -L 10,20 путь` | основной (аналога нет) |
| Компактные проверки | `pytest -q`, `npm test -- --silent`, `go test ./...` (без `-v`) | основной |
| Форматирование | `gofmt -l .` (без `-d`) | основной |
| Счёт вхождений | `grep -c`, `rg --count` | основной |

**Запрещено:**
- `cat`/`head`/`tail` файлов «в лоб» вместо инструментов чтения;
- `grep -r`/`rg` без `--exclude-dir` и без точечного шаблона;
- полный нефильтрованный вывод сборок и тестов (без `-v`, `--silent`, `-q`);
- «первые N строк файла» вместо карты кода/сигнатур;
- перебор окружения при отсутствии инструмента (`which`/`find` по всей ФС) —
  читай вывод команды и следуй подсказке инструмента.

Фильтровать вывод пайпом допустимо и нужно: `... 2>&1 | tail -40`,
`| grep -E "FAIL|error" -A 3`, `| head -60`, `> /dev/null && echo OK`.
Флаги из первых четырёх строк таблицы — именно **фолбэк**: при доступных
LSP/структурированных инструментах они дублируют их и тратят контекст.

**Внутренние агенты (`agents/*`)** — у них Bash-инструмента нет, есть только
`Run` (`tools/fileops.go`), который уже ограничивает объём вывода. Правила те
же: компактные флаги проверок (`make test`/`make lint`, `pytest -q`,
`npm test -- --silent`), git-контекст через `Run`, точечный `grep` через `Run`
только при `status skipped` у LSP/`CodeSearch`.

Состояние последней сессии (петля QAL-01: «пустая» работа съедает бюджет
автономии, специалист может честно поставить paused; верификация зелёная,
коммита нет — правки в рабочем дереве вместе с песочницей): диагноз — ветка
«fallback в done отклонён гардом» (`agents/planner/kanban.go`, сейчас ~:1457)
оставляла задачу `in_progress` без heartbeat, `recoverStuckTasks` (~:1115)
возвращал её в очередь через ~40 мс, и тот же цикл гонял прогон заново: у
QAL-01 набежало `attempts=24`, токены росли с каждым кругом, а комментарий
обещал эскалацию, которую давал только детектор петли (`resp.Looped` — QA его
не триггерит). Фикс: общий `escalate(ctx, t, escalationDiag{event, detail,
action})` — `escalateLoop` теперь тонкая обёртка («зациклилась»), гард-ветка
зовёт его с `event: "работа не сделана"` и action «внеси правки + коммит; если
задача невыполнима — human_help с причиной»; исчерпание `KANBAN_MAX_ESCALATIONS`
(общий счётчик с петлями) → `StatusHumanHelp` + `setStopReason` «бюджет автономии
исчерпан … нужен человек», запуск падает с этой причиной, а не с «нет
прогресса». Оркестратор теперь УВАЖАЕТ `human_help`/`cancelled` от специалиста до
fallback-а (раньше `cancelled→done` ронял Run: переход запрещён FSM, а эскалация
пыталась сделать `ready` из `cancelled`), причина уходит в чат статусным
сообщением. Инструмент: `BoardSetTaskStatus` принимает `human_help` с
обязательным `reason` (`tools/board_tools.go`) → новое поле
`board.Task.PauseReason` (`json:"pause_reason"`, очищается при выходе из
`human_help`; статус `paused` заменён на `human_help` мержем harness-rework); промпты: `developer.kanbanStep` и новый `kanbanRule` у QA
(`agents/qaengineer/agent.go` — добавляется только при `Store != nil`,
по образцу разработчика) объясняют done/human_help. Web UI: `pause_reason` в
`web/src/Types/Types.ts` + блок «Причина остановки» в `TaskModal.tsx`,
`npm run typecheck`/`npm run build` (dist пересобран: `index-qIdXu1th.js`).
Тесты: `TestGuardRejectionEscalatesWithinBudget` (бюджет 1 → `human_help`, ошибка
«бюджет автономии исчерпан»/«нужен человек», инъекция с причиной),
`TestAgentHumanHelpRespectedByFallback` (provider сам ставит human_help+reason; гард
на месте доказывает, что fallback не затирает остановку; `Escalations=0`),
`TestBoardSetTaskStatusHumanHelpReason` (reason обязателен, оседает в задаче,
чистится на `ready`); существующие `TestPhantomDoneNotClosedByFallback`
(теперь останавливается по бюджету, а не на 100-м раунде),
`TestKanbanTaskLoopEscalatesThenAsksHuman`, `TestEscalationBudgetStopsAndAsksHuman`
— зелёные. Верификация: `go build`/`go vet`/`go test` по перечню выше зелёные
(в т.ч. `agents/acceptor`), `-race` на новых тестах, `gofmt -l` по своим файлам
чист. Документация: секция «Задача без результата крутится заново: «fallback в
done отклонён гардом»» в `docs/40-operations/troubleshooting.md` (симптом из
лога QAL-01, три шага «что делать по существу»), правки `KANBAN_MAX_ESCALATIONS`
в `docs/30-reference/environment-variables.md`, пункты 6–7 в «Эскалация
зациклившейся задачи» `docs/20-features/agents-and-roles.md`. Пункты (б)/(в)
предыдущего разбора закрыты исследованием: QA-задачи ссылаются на
несуществующий `internal/service`/`internal/repo` (бэкенд собрал
`internal/api|config|domain`) — корневая причина за человеком (перепланировать
задачи или вынести модуль отдельной задачей), write-инструменты у QA были
(`qaBoardToolNames`), `RequiredToolFirstRound` не виноват. НЕ сделано:
коммит (спросить пользователя) и перезапуск живого сервера (`./ai serve`,
порт 8090 — прервёт текущую оркестрацию mytrip).

Состояние последней сессии (эфемерная песочница чинит `cd`/git в worktree,
жалоба QAL-01): при `CODEGEN_SANDBOX=container` команда `cd <хостовый путь
worktree>` падала с `can't cd`, а `git status` — `fatal: not a git repository:
…/temp/mytrip/.git/worktrees/…`, и агент уходил в петлю «сделай git init».
Причина: контейнер монтировал проект только в `/workspace` (хостового пути не
было), а `.git` worktree — это файл `gitdir:` на главный клон, которого в
контейнере тоже не было. `tools/sandbox.go`: `sandboxSpec.Mount` →
`Mounts []string`, рабочий каталог монтируется по ХОСТОВОМУ пути и workdir =
dir (теперь `result["workdir"]` совпадает с путём контейнера), плюс
`sandboxGitMounts(dir, readOnly)` — если `.git` — файл, отдельным томом идёт
общий каталог git'а (через файл `commondir`, обычно `../..` от gitdir) по тому
же пути; пропуски — gitdir уже внутри каталога, не существует на хосте (docker
создал бы пустой каталог) или путь с `:`/`,`. Родительский каталог в отличие от
сессионного контейнера НЕ монтируется: эфемерный режим зовётся и для
произвольных рабочих каталогов, а родитель мог бы оказаться домашним каталогом.
`sandbox/compose.yaml` и константа `sandboxWorkspace` остались на `/workspace`
(ручная точка входа для человека, не путь агента). Тесты hermetic:
`TestSandboxWorktreeMountsGitCommonDir` (раскладка «клон + worktree» без git:
абсолютный/относительный gitdir, `:ro`, обычный клон, `sep-inside`/`missing`/
`colon` без лишнего тома) + обновлены `TestSandboxMountsWorkdirReadWriteByDefault`,
`TestDockerArgsContainIsolationFlags`, `TestDockerArgsReadOnlyMode`; живой
`TestSandboxRealContainerWorktree` (git status + коммит из контейнера виден на
хосте; `pwd` = хостовому пути в `TestSandboxRealContainer`). Верификация
зелёная: `go build`/`go vet`/`go test` по перечню выше, `-race` на новых
тестах, плюс живой прогон на реальном `temp/.wt-task-mytrip-QAL-01`
(`ai-sandbox:latest`, оба тома): `pwd` = хостовый путь, `git status` →
`## ai/task/QAL-01`. Документация: раздел «Монтирование» в
`docs/20-features/sandbox.md` (таблица томов + почему хостовый путь и отдельный
git-том), запись «`cd: can't cd to …` / `fatal: not a git repository`» в
`docs/40-operations/troubleshooting.md`, обновлены line-ссылки на
`tools/sandbox.go` и `sandbox/destructive.go`. ОТЛОЖЕНО (пункты б/в из
предыдущего разбора): зависимость задач от существования `internal/service`
(QAL-01 стартовала раньше, чем модуль появился) и включение
`RequiredToolFirstRound` для QA — write-инструменты у QA уже есть
(`agents/qaengineer/agent.go:20-23`), поэтому чинить надо сам тайминг задачи.

Состояние последней сессии (петля FEL-02 на живом прогоне, отпечаток состояния
по проверке): агент 20+ раундов гонял одну и ту же падающую проверку, меняя
ТОЛЬКО фильтр вывода (`npm test src/pages/Login.test.tsx 2>&1 | grep -A5` →
`| grep -B3 -A20` → `| grep -i "console|warn"`). Отпечаток состояния по
нормализованному ВЫВОДУ проверки такую петлю пропускал: grep печатает разные
куски одного и того же падения, вывод менялся каждый раунд. Ключ отпечатка —
теперь нормализованная САМА проверка (`runner/loopstate.go:verifyIdentity`:
отбрасывается конвейер после `|` с флагами, перенаправления и каталог в
`cd X &&`); счётчики ведутся ПО ПРОВЕРКЕ в map (`stateRuns`), а не по
«последней проверке раунда», иначе чередование двух падающих проверок обнуляло
бы отсчёт. Нормализованный вывод остался в тексте вмешательства (диагноз модели).
Вторая половина правки — доставка вмешательства: пороги конфликтовали (разрыв на
3-м падении, вмешательство на 5-м — недостижимо), а сообщение дописывалось в
конец раунда, т. е. доезжало на 1–2 раунда позже и пропадало вместе с
схлопнутой историей. Теперь `LOOP_STATE_WARN` (2, зажат ниже
`LOOP_STATE_REPEATS`) готовит предупреждение, а `runner.go` внедряет его в
СЛЕДУЮЩИЙ запрос (после сжатия истории, до вызова LLM). Блокировка проверки
переехала с точной сигнатуры на нормализованную проверку (иначе смена флагов grep
её обходила) и больше НЕ обнуляет отсчёт петли — иначе блокировка вечно
обнуляла бы счётчик и разрыв не наступал бы никогда. FEL-02 освобождена через
штатный хук доски: `PUT …/tasks/FEL-02 {"status":"done"}` → авто-коммит `5943050`
(8 файлов, +382) → авто-MR PR #12 → авто-мёрдж в релиз эпика `ARCH-04` (`e307000`);
её тесты всё ещё падают — `window.location.href = '/'` в JSDOM не поддерживается
(ровно этот случай теперь описан во вмешательстве). Сервер перезапущен на новом
бинаре (`./ai serve`, порт 8090), оркестрация `mytrip` продолжена, пошла FEL-03.
Тесты: `TestVerifyIdentityIgnoresPipelineAndFlags`,
`TestLoopDetectorWarnsOneRoundBeforeBreak`, `TestLoopBlockedVerifySurvivesArgumentChange`,
`TestLoopBlockedRoundKeepsStateSeries` + доработан сквозной
`TestGenerateStateHashBreaksLoopWithVaryingArgs` (предупреждение в запросе раньше
разрыва, через `recordingProvider`). Документация:
`docs/plans/PLAN-2026-10-03-done-loop-breaker.md` (разделы про отпечаток и
вмешательство + живая проверка на FEL-02), `LOOP_STATE_WARN` в
`docs/30-reference/environment-variables.md` и `.env.example`, разделы
«Отпечаток состояния»/«Verify guard» в `docs/20-features/agents-and-roles.md`,
новая запись в `docs/40-operations/troubleshooting.md`.

Состояние последней сессии (пустой удалённый репозиторий, E2E закрыт на живом
GitHub): `gitops.Clone` больше не падает на репозитории без коммитов.
`git clone` пустого репозитория проходит (exit 0 + «cloned an empty
repository»), но HEAD нерождённый, поэтому `rev-parse --abbrev-ref HEAD` давал
128 и Web UI отвечал 502 «ошибка клонирования: … ambiguous argument 'HEAD'».
`gitops/gitops.go`: `detectBaseBranch` (rev-parse → `symbolic-ref --short HEAD`
unborn-ветка → `origin/HEAD` для detached HEAD → константа
`DefaultBaseBranch`="main", ошибок не возвращает), `HasCommit` и
`seedBaseBranch` — базовая ветка создаётся и публикуется (README.md `# <имя>` →
`chore: initial commit (<base>)` → `git push -u origin <base>`), коммит сначала
от identity хоста, при отказе повтор с `-c user.name=AI -c
user.email=ai@localhost`. `server/git.go`: при ошибке клона полу-клон
`temp/<имя>` удаляется (иначе повтор упирается в 409) и `discardHalfClone`
убирает каталог от неудачного клона ТОГО ЖЕ origin при нуле коммитов и чистом
состоянии; чужой клон/наши коммиты/незакоммиченные правки не трогаются (409,
`sameGitRemote` нормализует только `.git` и слеш). Тесты: `gitops/gitops_test.go`
— `TestCloneSeedsEmptyRemoteBase` (посев), `TestCloneEmptyRemotePushFailure`,
`TestCloneDetachedHeadUsesOriginHead`, `TestCloneNoBranchSignalFallsBackToDefault`
(+ переписан `TestCloneDryRun` — добавилась проверка `rev-parse --verify`);
`gitops/cli_test.go` — `TestGitCLICloneEmptyRemoteSeedsBase` (реальный git на
пустом bare-origin: пуш базы, `git show refs/heads/main:README.md`,
merge-base, diff); `server/git_test.go` — `TestOpenGitProjectEmptyRemoteSeedsBase`,
`TestOpenGitProjectRemovesFailedCloneDir`, `TestOpenGitProjectDiscardsHalfClone`,
`TestOpenGitProjectKeepsForeignDir` (4 негативных случая в подтестах).
Верификация: `go build`/`go vet`/`go test` по перечню выше (зелёные, кроме двух
пред-существующих macOS-флаков `agents/acceptor`), `-race` на новых тестах.
Живой E2E на хосте: `go run . serve` (Redis 56379, Qdrant 6333, Ollama —
в Docker/локально) + `POST /api/projects {"path_or_git":
"git@github.com:uraankhayayaal/mytrip.git"}` → 200, в GitHub появились
`refs/heads/main` и `HEAD` на коммите `chore: initial commit (main)`, локально
ветки `main` (с upstream origin/main) и `ai/mytrip`, `merge-base main ai/mytrip`
разрешается, `GET /api/projects/mytrip/diff` → 200 (`files: null` — проект ещё
пуст). Документация: раздел «Пустой удалённый репозиторий» в
`docs/20-features/gitops-workflow.md`, три записи в
`docs/40-operations/troubleshooting.md` («ambiguous argument 'HEAD'», «каталог
уже существует и не пуст», «плохой origin»).

Состояние последней сессии (PLAN-2026-09-28-done-rag-main-freshness,
жалоба «переключаю эпик — агент в analysis не видит изменений предыдущего»,
остался ручной E2E): пользователь одобрил все три причины. Ф-1 явное имя
RAG-проекта: `tools/fileops.go` `FileOps.Project` + `SetProjectName` +
`ProjectName`, `tools/codesearch.go`/`ragstatus.go` на `tools.projectNameOf`
(было `basename(OutputDir)` — у агента задачи это `.wt-task-…`, проект не
находился), `agents/developer/developer.go:ReindexTouched`,
`agents/architect|chatassist` (Project при создании),
`agents/planner/kanban.go:specialistFor` (знает и проект, и worktree).
Ф-2 `server/ragref.go` (новый): ветка читается из git-объектов —
`collectRefItems` (`git ls-tree -r -z --long` + `git show ref:<путь>`,
`parseLsTreeEntry`, `indexablePath/Content`, `maxRefFileBytes`),
`Session.IndexRefBackground` (индекс ветки) и `IndexRefDiffBackground` +
`collectRefDiff` (`git diff base...ref`, `git cat-file -s`) — индекс ИЗМЕНЕНИЙ
ветки задачи относительно main (остальное покрывает индекс main; полная
индексация worktree стоила бы эмбеддингов всего проекта на каждый старт
задачи). Явный `branch` больше не переименовывает снимок checkout:
`IndexBackground(branch)` = ref, `IndexBackground("")` = каталог. Ф-3
`rag.Client.BranchIndexed` (`rag/status.go`) + пропуск прогона с отдельным
статусом «RAG-индекс проекта актуален» (не путать с «обновлено 0 файлов»);
single-flight стал ПО ВЕТКЕ (`sess.indexing map[string]bool`, `indexSlotKey`) —
иначе переиндексация main съедала бы индексацию ветки задачи. Хуки:
`server/gitflow.go` (после реального merge эпика `afterMainChanged`; при
`already=true` только проверка индекса main — сохранён тест «уже слитая ветка
не должна мутировать git»), `server/gitflow_auto.go:66` (синхронизация эпика)
и `:164` (старт задачи — дифф ветки), `server/session.go:285` (фоновый merge
main в ai/<имя> до первого агента). Merge в рабочую копию: только чистое
дерево, `git merge --abort` при конфликте (`abortMerge`), пропуск при
отсутствии сдвига HEAD (`headSHA`) — правки агентов не теряются. Тесты
`server/ragref_test.go` (новый, 10 шт.: разбор ls-tree/отсевы/бинарник/ошибки,
дифф ветки, релиз→индекс main, грязное дерево, конфликт+abort, без сдвига
HEAD, актуальный индекс, хук диффа, хуки без сессии). Верификация зелёная:
`go build`/`go vet`/`go test` по перечню выше + `./rag/ ./server/ ./runner/
./gitops/`. Документация:
`docs/plans/PLAN-2026-09-28-done-rag-main-freshness.md` (+ строка в
`docs/plans/README.md`), раздел «Хуки git-flow (без env)» и правки
CodeSearch/IndexBackground в `docs/20-features/rag.md`, хуки RAG в
`docs/20-features/gitops-workflow.md`, две записи в
`docs/40-operations/troubleshooting.md`.

Состояние последней сессии (E2E выбора модели закрыт): ручной E2E из предыдущей
сессии заменён постоянным живым тестом `server/e2e_model_selection_test.go`
(build-тег `e2e`, вне рабочего набора: `E2E_LIVE=1 go test -tags e2e -run
TestE2EModelSelection ./server/ -timeout 10m`). Тест поднимает настоящий сервер
(`httptest` + `newTestServer` с miniredis — живая нужна только Ollama), сам
подбирает две модели с tool-calls из `/api/tags` (`E2E_MODEL_SMALL`/
`E2E_MODEL_SECOND`/`E2E_MODEL_LARGE`, адрес `OLLAMA_BASE_URL`) и проверяет:
стартовый выбор из окружения, сортировку провайдеров, переключение через
`POST /api/providers/select` → подтверждение в `GET` (регресс кэша провайдера),
**реальный** чат `/api/projects/{id}/chat` с доказательством через `/api/ps`
(до запроса модели выгружены — загружена ровно выбранная), логи `server: LLM:`
в `server.log` и `[llm] модель:` в логе проекта, отказы (400 на неизвестную
модель/провайдер, битый `base_url` → 400 + ошибка в `GET`, `reset` →
`override=false`). Прогон зелёный (26s), `go build`/`go vet`/`go test` по
перечню AGENTS.md зелёные. Документация: новая секция «Живой E2E (опционально,
нужна Ollama)» в `docs/40-operations/testing.md`.

Состояние последней сессии (Clean Architecture Этап 3, пункт 3 — порт-ориентированный
перенос planner): коммиты `08b8273` (domain.AgentType/Role — канонические значения,
устранено расхождение `backend_dev`/`qa_engineer` с фактическими `backend`/`qa`;
planner.plan.go алиасит), `20f1d81` (domain.Plan/Step/ComputeWaves/readySteps;
`Step.Tasks` сменил `[]board.TaskSpec` на `[]domain.TaskSpec`, executor конвертирует
`toDomainTaskSpecs`; сериализация не изменилась), `a8682c3` (под-шаг C1: `domain.Status`
— авторитетный FSM доски — `ValidateTransition`/`StatusError`/`Terminal` (без
human_help)/`Valid`/`Label`; `board.Status`/`StatusError` — алиасы, `board.ValidateTransition`
делегирует; `board.Meta`/`board.StatusCounts` алиасированы на domain; тест
`domain/status_test.go`), `87c27d3`/последний docs (план: срез D закрыт, срез C разбит
на C1–C6). Верификация: `go build`/`go vet`/`go test` по перечню выше зелёные, кроме
ровно базовых 17 падений `application/planner` (состав не менялся — не регресс); board/
server/tools/runner зелёные. Срез C НЕ закрыт: C2 (алиасы Comment/Injection/BugReport/
BugStatus — требуют переноса методов Normalize/Validate в domain), C3 (domain.EpicLess/
UnmarshalTasks), C4 (каскад: контекст-плумбинг инъекций/комментариев
NewInjectionContext/WithInjection*/NewCommentContext читается `runner/injections.go` и
`server/chatassist.go` — переносить в domain или через порт), C5 (расширить
`services.BoardStore` методами GetMeta/SaveMeta/EpicCounts/TaskCounts/Finalize*Tokens/
AllDone, `KanbanRunner.store` — на интерфейс), C6 (миграция ~183 `board.*`-обращений
planner в non-test коде + тесты). Остаток портов Этапа 3: `agents/acceptor` в
executor.go, порты Runner/Models/Tools. Коммит пользователю: не запрашивался явно.

Состояние последней сессии (Clean Architecture Этап 3, срез C2 — алиасы типов
домена, коммит `0750db5`): `board.{Comment,Comments,Injection,BugReport,
BugStatus,BugStatusError}` и их константы стали алиасами `domain.*`; в домен
перенесены `Comment.Normalize/Validate` (+`newCommentID`, id `cmt-`),
`Injection.Validate`/`NewInjectionID` (16 байт hex, было `inj-`+6 байт —
расхождение устранено), полная `ValidateInjections` (проверка дублей id) и
строгий `DecodeInjections` (unknown-поля — ошибка), а также
`BugStatusError`/`ValidateBugTransition`. Контекст-плумбинг инъекций/комментариев
оставлен в `board` (срез C4). Верификация: `go build`/`go vet`/`go test` по
каноническому набору — зелёно, кроме ровно базовых 17 падений `application/
planner` (состав не менялся); `domain`/`board`/`injections`/`runner`/
`application/usecases`/`tools` зелёные, gofmt по правкам чист. ПЕРЕОЦЕНКА C3:
`domain.EpicLess`/`UnmarshalTasks` нельзя перенести в домен без унификации
моделей доски — `board.Epic`/`Task` встраивают `board.TaskSpec` (`SequenceOrder
FlexInt`, метки `string`), а `domain.Epic`/`Task` — `domain.TaskSpec` (`int`,
`time.Time`); поэтому C3 слит с C6 (унификация `board.TaskSpec/Task/Epic` ↔
`domain.*`, затем перенос компаратора/разбора задач и миграция ~183
`board.*`-обращений planner). Остаются C4 (перенос контекст-плумбинга),
C5 (порт BoardStore), C6+C3.
