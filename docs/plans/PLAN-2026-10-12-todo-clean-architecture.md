# PLAN-2026-10-12 — Рефакторинг по Clean Architecture

**Статус:** Этапы 1–2 завершены. Этап 3: use-cases `RunTask`,
`CompressHistory`, `BreakLoop`, `Escalate` перенесены в
`application/usecases` и покрыты unit-тестами; пункт 3 (перенос
`agents/planner/`) выполнен **частично** — пакет перемещён в
`application/planner`, зависимости от конкретных агентов инвертированы через
порты `AgentFactory`/архитектурные, чистые типы плана (`AgentType`/`Role`/
`Step`/`Plan`/`ComputeWaves`) вынесены в `domain/`, `domain.Status` стал
авторитетным FSM доски (под-шаг C1). Срез C завершён: C2 (алиасы типов),
C4 (контекст-плумбинг), C5 (порт `services.BoardStore`), C6 (унификация
моделей `board.*` ↔ `domain.*`, перенос компараторов/разбора в `domain`,
`KanbanRunner.store` → `services.BoardStore`, миграция всех `board.*`-
обращений planner на `domain.*`/`services.*`). Models-порт вынесен из
`models` (порт `planner.LLMProvider`, `domain.WithHeavyModel`/
`domain.HeavyModelFor`, `models/force.go` удалён); Acceptor-порт вынесен из
`acceptor` (`domain.Report`, `planner.Acceptor`, `acceptor.PlannerAdapter`);
Tools-порт вынесен в `domain/services/tools.go` (`Tool`/`ToolSet`/
`ToolSetBuilder`/`LSPDiagnoser`/`Snapshot`/`SnapshotFactory`/
`PublicAPIAuditor`), адаптеры — `tools/plannerports.go`
(`PlannerToolSet`/`LSPDiagnoserAdapter`/`SnapshotAdapterFactory`/
`PublicAPIAuditorAdapter`), подключение — `main.go`;
`application/planner` в non-test коде больше не импортирует `tools`/`models`/
`board`/`runner`/`agents/acceptor` (проверено `go list -f .Imports`). Этап 3
закрыт полностью: `runner` стал тонким адаптером (адаптеры портов вынесены в
`runner/adapters/`), ссылки на `agents/planner` в живых документах обновлены,
предсуществующие падения тестов `application/planner` разобраны — см. «Остаток
работ» в конце.

**Этап 4 (в работе):** срез **E1 ✅** — создан каркас `infrastructure/`,
пакет `models` переехал в `infrastructure/llm` (пакет `models` → `llm`) и
привязан к порту `domain/services.LLMClient` compile-assertions
(`infrastructure/llm/ports.go`). Срез **E2 ✅** — `rag` → `infrastructure/rag`
(пакет `rag`), адаптер `rag.ProjectAdapter` реализует
`domain/services.RAGClient` (`infrastructure/rag/port.go`). Срез **E3 ✅** —
`gitops` → `infrastructure/git` (пакет `gitops` → `git`), порт
`domain/services.GitOperations` совпадает по подписям с методами `*git.Repo` и
реализуется напрямую (assertion в `infrastructure/git/port.go`). Срез **E4 ✅** —
`sandbox` → `infrastructure/sandbox` (пакет `sandbox`), порт
`domain/services.SandboxWorkspace`: `Result`/`Workspace` стали alias'ами
канонических типов домена, поэтому `*LocalWorkspace`/`*DockerWorkspace`
реализуют порт напрямую (assertion в `infrastructure/sandbox/port.go`). Срез
**E5 ✅** — `board` → `infrastructure/board` (пакет `board`), 75 импортёров на
`ai/infrastructure/board`. Срез **E6** разбит на под-срезы (E6a…E6e, таблица
ниже); **E6a ✅** — `tools/` больше не импортирует `ai/infrastructure/board`
(переведён на `domain`); **E6b ✅** — RAG-инструменты вынесены в подпакет
`ai/tools/rag`.
Разбивка этапа на срезы E1–E6 — в разделе «Этап 4» ниже.

**Этап 6 (в работе):** срез **F1 ✅** — ручной откат задачи (`RollbackTask`) и
прогноз токенов (`NewTokenEstimator`) вынесены из `server/` в
`application/usecases`; срез **F2 ✅** — детерминированная обработка логов
вынесена в новый пакет `domain/log` (stdlib-only), `server/loganalysis.go`
оставляет только чтение файлов и сборку промпта; срез **F3a ✅** — гард
«phantom done» (`GuardTaskDone`) вынесен в `application/usecases` с портом
`DoneGuardSource`; срез **F3b ✅** — цикл LLM-разрешения конфликтов
(`ResolveWithLLM`) вынесен в новый пакет `application/gitflow` (порты
`Store`/`Developer`/`Acceptor`/`Markers`), попутно удалён мёртвый код; срез
**F3c ✅** — политика создания MR (`MRCreator`) вынесена в `application/gitflow`
(порты `MRGateway`/`Forge`/`MRRegistry`); срез **F3d ✅** — раскладка worktree,
артефактная политика `.gitignore` и презентация git-ошибки вынесены в
`application/gitflow`; срез **F3e ✅** — авто-шаги завершения задачи
(`TaskFinalizer`, порт `TaskFinalizerHost`) вынесены в `application/gitflow`;
срез **F3f ✅** — авто-синхрон релизной ветки эпика с main (`EpicMainSyncer`,
порт `EpicMainHost`) вынесен в `application/gitflow`; срез **F3g ✅** — политика
ветвления (`Brancher`: создание/идемпотентный `EnsureBranch`, авто-ветки эпика и
задачи, снятие веток+MR при удалении эпика) вынесена в `application/gitflow`
(порты `BranchRepo`/`BranchRegistry`); срез **F3h ✅** — политика вливания ветки
задачи в релиз эпика (`TaskMerger.MergeTaskBranch`) вынесена в
`application/gitflow` (порт `MergeTaskHost`); срез **F3i ✅** — политика релиза
эпика в main (`Releaser.ReleaseEpic`) вынесена в `application/gitflow` (порт
`ReleaseHost`, доменные sentinel-ошибки вместо `apiError`); срез **F3j ✅** —
конфликтный резолв «main ↔ релизная ветка эпика» (`ConflictResolver`
`StartRebase`/`FinishResolve`, порты `ConflictHost`/`ConflictRepo`/
`ConflictWorktree`) вынесен из `server/gitflow_resolve.go` в
`application/gitflow` (сентинелы `ErrResolveBusy`/`ErrNoResolve`/
`ErrResolveStateSave`); срез **F3k ✅** — RAG-хуки: чтение содержимого ветки из
git (`LoadRefItems`/`LoadRefDiff`), сбор файлов каталога (`CollectIndexItems`) и
полная индексация проекта (`IndexProjectRAG`), проверка актуальности
(`BranchIndexUpToDate`) вынесены из `server/ragref.go`/`server/ragindex.go` в
новый пакет `application/rag` (порт `Indexer`); срез **F3l ✅** — политика
открытия git-проекта (`ProjectOpener.Open`: имя из URL, клон, полу-клон,
регистрация проекта/сабмодулей) вынесена из `server/git.go` в
`application/gitflow` (порт `OpenRegistry`); срез **F3m ✅** — оркестрация
фоновой индексации RAG (`BackgroundIndexer`: single-flight по ветке, горутина,
отчёты в лог/чат) вынесена из `server/ragindex.go` в `application/rag`
(порты `IndexSink`/`IndexerFactory`); срез **F3n ✅** — структурированный
вопрос ассистента (`AskUser`: разбор пачки вопросов `ParseQuestions`,
блокирующий цикл `AskUser`↔`AnswerAsk` в `Pending`) вынесен из `server/ask.go`
в новый пакет `application/ask` (порт `Announcer`); срез **F3o ✅** — сборка и
отбор лог-файлов проекта (`CollectProject`/`CollectFiles`/`ReadTail`) вынесены
из `server/logs.go` в новый пакет `application/logs`; срез **F3p ✅** — разбор
логов кнопкой «Разобрать» (`BuildAnalysis`/`PickFiles`/`DigestPrompt`/
`Cooldown`) вынесен из `server/loganalysis.go` в `application/logs`; срез
**F3q ✅** — разбор unified-диффа git (`Parse`/`ParseBlock`/`PrefixPath`)
вынесен из `server/diff.go` в новый пакет `domain/diff`; срез **F3r ✅** —
жизненный цикл worktree задачи/эпика (`TaskWorktree`/`EpicWorktree`/
`DropEpicWorktreeLocked`) вынесен из `server/gitflow_auto.go` в
`application/gitflow` (порт `WorktreeHost`); срез **F3s ✅** — политика
синхронизации рабочей копии агентов с main (`syncAgentBranchWithMain`) вынесена
из `server/ragref.go` в `application/rag` (порт `SyncSink`); срез **F3t ✅** —
конкретные фоновые прогоны индексации (`RefJob`/`RefDiffJob`/`DirJob`) вынесены
из `server/ragref.go`/`server/ragindex.go` в `application/rag` (`jobs.go`);
срез **F3u ✅** — набор сессионных промпт-инъекций (`InjectionSet`) вынесен из
`server/session.go` в `domain`; срез **F3v ✅** — правка/презентация конфликтных
файлов модели (`NewConflictView`/`ApplyConflictFiles`/`ConflictSafeRel`) вынесена
из `server/actions_resolve.go` в `application/gitflow` (`conflictfiles.go`); срез
**F3w ✅** — детерминированная политика диалога чат-ассистента
(`ConfirmAffirmative`/`AskSummary`/`ActionAnchor`) вынесена в новый пакет
`application/chatassist`; срез **F3x ✅** — HITL-затвор
(`Epics`/`Tasks`/`Wait`/`Approve`/`Reset`) вынесен из `server/session.go` в новый
пакет `application/hitl` (порт `GateHost`); срез **F3y ✅** — read-model доски
(сборка упорядоченного снимка + пагинация) вынесен из `server/session.go` в новый
пакет `application/board` (порт `Store`).
Остальное (git-flow хуки, остальная оркестрация, RAG-хуки) — в
плане; разбивка в разделе «Этап 6» ниже.

## Проблема

Текущая архитектура проекта — слоистая, но не соответствует принципам
Clean Architecture. Основные нарушения:

1. **Циклические зависимости между пакетами.** `server` не может импортировать
   `tools` (циклическая зависимость). Обходной путь — мосты через `Set.Add`,
   что размывает границы и делает зависимости неявными.

2. **Направленность зависимостей нарушена.** `runner` импортирует `tools`, но
   `tools` не может импортировать `runner`. Обходной путь — `tools/sanitize.go`,
   дублирующий логику из `runner`. Это нарушает правило «внутренние слои не
   должны знать о внешних».

3. **Отсутствие явных границ слоёв.** Пакеты организованы по техническому
   признаку (`server`, `tools`, `runner`, `agents`), а не по бизнес-доменам.
   Нет разделения на domain, application, interfaces, infrastructure.

4. **Плоская структура внутри пакетов.** `agents/planner/` содержит 16+ файлов
   с разными ответственностями (kanban, plan, tokens, ragcontext, lspgate,
   project_map) без чёткого разделения. Пакет `tools/` — 40+ файлов.

5. **Деградация через nil-зависимости.** `tools.Deps` с `Board == nil`
   переключает агента в «file-only» режим. Это скрытый контракт, не выраженный
   через типы.

6. **Непрозрачные данные между слоями.** `checkpoint` хранит `PlanJSON` как
   байты, чтобы позволить менять структуру плана. Это следствие отсутствия
   явной модели предметной области.

7. **Специализации агентов — копирование, а не композиция.** Пакеты
   `agents/architect/`, `agents/backendlead/`, `agents/frontendlead/` и др.
   имеют схожую структуру (agent.go + config.go + ragcontext.go) с дублированием.

## Решение

Перевести проект на **Clean Architecture** с четырьмя слоями:

```
┌─────────────────────────────────────────────────────┐
│  Interfaces (server, web, CLI, runner/loop)         │
├─────────────────────────────────────────────────────┤
│  Application (use-cases: запуск задачи, эскалация)  │
├─────────────────────────────────────────────────────┤
│  Domain (entities, интерфейсы сервисов, политики)   │
├─────────────────────────────────────────────────────┤
│  Infrastructure (LLM, RAG, git, sandbox, storage)   │
└─────────────────────────────────────────────────────┘
```

### Целевая структура пакетов

```
domain/           — предметная область (Task, Plan, Epic, Agent, Run)
domain/services/  — интерфейсы сервисов (LLM, RAG, Git, Sandbox, Board)
application/      — use-cases (RunTask, Escalate, Compress, LoopBreak)
interfaces/       — адаптеры (server, web, runner/loop, cli)
infrastructure/   — реализации (llm, rag, git, sandbox, board)
```

### Ключевые принципы

- **Зависимости направлены внутрь.** Внешние слои зависят от внутренних, а не
  наоборот. `domain` не знает о `application`, `interfaces`, `infrastructure`.
- **Интерфейсы принадлежат внутренним слоям.** `domain/services` объявляет
  интерфейсы; `infrastructure` их реализует; `application` зависит от
  интерфейсов, а не от конкретных реализаций.
- **Use-cases оркестрируют, не реализуют.** `application` содержит координацию
  между доменными сервисами, но не бизнес-логику инфраструктуры.

## Этапы

### Этап 1: Извлечение domain-модели

**Цель:** Создать слой `domain/` с ключевыми сущностями и интерфейсами
сервисов. Не перемещать существующий код — только объявить типы и
интерфейсы, которые будут реализованы позже.

1. Создать `domain/` с сущностями: `Task`, `Plan`, `Epic`, `Agent`, `Run`,
   `AgentDefinition`, `AgentRole`.
2. Создать `domain/services/` с интерфейсами:
   - `LLMClient` (генерация, tool-calls, стриминг)
   - `RAGClient` (индексация, поиск, статус)
   - `GitOperations` (clone, commit, merge, branch, worktree)
   - `SandboxEngine` (запуск, остановка, файловые операции)
   - `BoardStore` (CRUD задач, комментариев, статусов)
3. Убедиться, что `domain/` компилируется без зависимостей от других пакетов
   (только стандартная библиотека).

**Критерий:** `go build ./domain/...` проходит; в `domain/` нет импортов
проектных пакетов.

### Этап 2: Разрыв циклических зависимостей

**Цель:** Устранить циклические зависимости `server` ↔ `tools` и
одностороннюю `runner` → `tools`.

1. **server ↔ tools:**
   - Выделить общие контракты в `domain/services/`.
   - `server` зависит от интерфейсов, а не от конкретных реализаций `tools`.
   - Заменить мосты `Set.Add` на явную внедрение зависимостей через
     конструкторы.
2. **runner ↔ tools:**
   - Переместить логику из `tools/sanitize.go` в `domain/` или
     `application/`.
   - `tools` вызывает доменную логику напрямую, без обходных путей.
3. **tools.Deps:**
   - Заменить `Board == nil` на явный интерфейс `BoardStore` с реализациями
     `RealBoard` и `NullBoard` (или аналогично).

**Критерий:** `go build ./...` проходит без циклических зависимостей;
`tools/sanitize.go` удалён или перемещён.

### Этап 3: Выделение application-слоя

**Цель:** Перенести оркестрацию из `runner` и `agents/planner` в
`application/use-cases/`.

1. Создать `application/usecases/`:
   - `RunTask` — запуск агента по задаче (текущий `runner.Runner`). ✅
     `usecases/run.go` (`RunTask`/`RunPorts`/`RunConfig`, 657 строк) содержит
     цикл целиком; `runner.Generate` (`runner/runner.go:233`) — тонкий адаптер,
     собирающий порты (провайдер, агент, инъекции, компрессор, конфиг) из
     окружения и делегирующий в `usecases.RunTask`. Нейтральный LLM-порт:
     `domain.ChatRequest`/`domain.ToolDefinition` (`domain/chat.go`),
     `services.LLMClient` на `ChatRequest`; `tools.ToolDefinition` — алиас
     домена. Покрыт `usecases/run_test.go` (завершение, вызов инструмента и
     возврат результата, лимит раундов, инъекции, короткая история).
   - `CompressHistory` — сжатие контекста (`runner/compress.go`,
     `runner/compression.go`). ✅ Базовый движок (`compress.go`:
     позиционное ядро + Ф-6..Ф-10 пайплайн, `CompressContext`/`CompressOptions`/
     `CompressionReport`/`EvictionItem`) в `usecases/compress.go`; оркестрация
     `compression.go` делегирует в `usecases` (бюджет из окружения/usage,
     страховка по окну провайдера, компакция через `providerCompactor`).
   - `BreakLoop` — обнаружение и вмешательство в петли (`runner/loop.go`,
     `runner/loopstate.go`). ✅ `loop.go` перенесён; `policy.go` содержит
     чистые политики цикла (подсказки модели, разбор NEED_*, классификация
     провала инструмента, пороговые константы), `domain/tokens.go` — оценку
     токенов; `runner` делегирует в них тонкими алиасами.
   - `Escalate` — эскалация при исчерпании бюджета (`agents/planner/kanban*.go`).
     ✅ `usecases/escalate.go`: `Escalate`/`EscalationDiag`/`EscalationPorts`
     поверх узкого порта `EscalationStore` (`domain.Task`, `domain.Injection`,
     `domain.Status`); `KanbanRunner.escalate`/`escalateLoop`
     (`agents/planner/kanban_execute.go`) делегируют туда, оставляя в
     инфраструктуре доску (`board.DomainStore`), бюджет
     (`KANBAN_MAX_ESCALATIONS`) и вывод в чат/лог. Покрыт
     `usecases/escalate_test.go` (эскалация в бюджете, остановка при
     исчерпании, ошибки). В `domain/comment.go` добавлены константы
     `CommentTypeSystem/User/QA` (нужны use-case для авто-комментария);
     `domain.ModelTierLarge` уже существовал.
2. `runner` становится тонким адаптером к `application` (как `server` к
   `application`). ✅ `runner.Generate` делегирует в `usecases.RunTask`;
   `runner/compression.go` — адаптер сжатия. Адаптеры портов-функций
   (`AutoFixer`, `RuntimeLogger`, `InjectionApplier`, `RoundStateReporter`,
   `WIPCommitter`, `Reindexer`) вынесены из пакета в `runner/adapters/`
   (`autofix.go`, `applog.go`, `injections.go`, `taskstate.go`, `wipcommit.go`,
   `reindex.go`): там же конфигурация из окружения и формат сообщений, а
   политика цикла остаётся в `application/usecases`. В `runner` остались
   только `Generate` (сборка портов), `compression.go`/`compress_delegate.go`
   (сжатие), `debug.go` и `agent_runner.go` — пакет стал «тонким» по объёму
   (~590 строк non-test против ~1260): агент-разработчик (`agents/developer`)
   больше не зависит от `runner`, а берёт адаптеры напрямую из
   `runner/adapters`.
3. Перенести логику из `agents/planner/` (16+ файлов) в
   `application/` и `domain/`. ⏳ **ЧАСТИЧНО (порт-ориентированный перенос)**:
   - **Срез A ✅** — весь пакет перемещён `agents/planner` → `application/planner`
     (`git mv`, имя пакета `planner` сохранено; обновлены 3 импортёра —
     `server/session.go`, `server/gitflow_ar.go`, `main.go`). Sibling-пакет, а не
     `usecases`, потому что `usecases` импортируется `runner`, а planner зависит
     от `runner`/`agents/*` — переезд в `usecases` дал бы цикл
     (`usecases → runner → usecases`).
   - **Срез B ✅** — `AgentFactory` (`application/planner/agentfactory.go`):
     порт конструирования ролевых агентов (`Lead`/`Specialist`/`Step`);
     конкретные `agents/backendlead|developer|devops|devopslead|frontendlead|
     qaengineer` сведены в одну реализацию `roleAgentFactory` + `SetAgentFactory`.
     Оркестратор (`kanban_prompts.go`, `kanban_testing.go`, `executor.go`)
     зависит только от порта. Тест `agentfactory_test.go`.
   - **Срез B2 ✅** — `AgentFactory` расширен архитектурными методами
     (`Architect`/`ArchitectReviewer`/`ArchitectBugExpert`/`ConfigureArchitect`/
     `SetArchitectEpicWorkdir`, реализация — `architectfactory.go`); `agents/architect`
     больше не импортируется оркестратором (`kanban.go`/`kanban_planning.go`/
     `kanban_testing.go`). Реализация возвращает конкретный `*architect.Architect`,
     поэтому динамический тип агента сохранён (тестовые двойники работают).
   - **Срез D ✅** — чистые типы плана перенесены в `domain/`:
     - `domain/plan.go` теперь канонически объявляет `AgentType` (значения
       приведены к фактическим: `backend`/`frontend`/`devops`/`devopslead`/
       `qa`/`frontendlead`/`backendlead`; устранено расхождение с прежними
       `backend_dev`/`qa_engineer`/...), `Role`, `Step` (с полем
       `Tasks []domain.TaskSpec`) и `Plan`, а также чистую логику волн
       `ComputeWaves`/`readySteps`. `planner.plan.go` алиасит
       `domain.AgentType`/`domain.Role`/`domain.Step`/`domain.Plan` и оставляет
       себе только разбор `ParsePlan` и санитайз JSON.
     - `Step.Tasks` сменил тип `[]board.TaskSpec` (FlexInt/FlexBool) на
       `[]domain.TaskSpec`; `executor.go` переводит декомпозицию на границе
       (`toDomainTaskSpecs`). Сериализация не изменилась (FlexInt -> int).
     - Срез разделён на два коммита: `AgentType`/`Role` (алиасы) и `Plan`/`Step`/
       `ComputeWaves`. Обновлены `plan_doc.go` и `plan_doc_test.go`.
   - **Срез C (порт `BoardStore`/модели) ✅**:
     - **C1 ✅** — `domain.Status` — авторитетный FSM доски
       (`ValidateTransition`/`StatusError`/`Terminal`/`Valid`/`Label`),
       `board.Status`/`StatusError`/`Meta`/`StatusCounts` — алиасы.
     - **C2 ✅** — алиасы `Comment`/`Injection`/`BugReport`/`BugStatus` на
       `domain` (методы `Normalize`/`Validate`/`NewInjectionID`/строгий
       `DecodeInjections` перенесены в домен).
     - **C3 (переоценён → C6)** — `EpicLess`/`UnmarshalTasks` упираются в
       расхождение моделей `board.TaskSpec` (`FlexInt`/`string`) и
       `domain.TaskSpec` (`int`/`time.Time`); переносятся вместе с унификацией
       моделей в C6.
     - **C4 ✅** — контекст-плумбинг инъекций/комментариев перенесён в `domain`.
     - **C5 (частично ✅)** — порт `services.BoardStore` расширен; перевод
       `KanbanRunner.store` на интерфейс — вместе с C6.
      - **C6 ✅** — унификация моделей `board.Task/Epic` ↔ `domain.*` и миграция
        типов planner. `domain.TaskSpec`/`Task`/`Epic` получили `FlexInt`/`FlexBool`
        и string-таймстампы (как `board.*`); конвертеры сводятся к identity;
        `EpicLess`/`EpicPhase`/`SortEpics`/`UnmarshalTasks` перенесены в `domain`;
        `KanbanRunner.store` → `services.BoardStore`; все `board.*`-обращения
        planner мигрированы на `domain.*`/`services.*`.
      - `executor.go` больше не импортирует `agents/acceptor`: приёмка идёт
        через порт `planner.Acceptor` и отчёт `domain.Report` (адаптер
        `acceptor.PlannerAdapter` подключается в `main.go` и в тестах).
   - **Порт `Tools` ✅ (коммит `c589be4`)** — planner в non-test коде больше не
     импортирует `tools`. Интерфейсы объявлены в `domain/services/tools.go`
     (`Tool`/`ToolSet`/`ToolSetBuilder`, `LSPDiagnoser`, `Snapshot`/
     `SnapshotFactory`, `PublicAPIAuditor`); адаптеры к `tools` —
     `tools/plannerports.go` (`PlannerToolSet`, `LSPDiagnoserAdapter`,
     `SnapshotAdapterFactory`, `PublicAPIAuditorAdapter`); подключаются в
     `main.go` (`SetPlannerToolBuilder`/`SetLSPDiagnoser`/`SetSnapshotFactory`/
     `SetPublicAPIAuditor`) и в `application/planner/testmain_test.go` (адаптеры
     выставляются один раз через `TestMain`).

**Критерий:** `runner` делегирует в `application/usecases/RunTask` ✅
(`runner.Generate` → `usecases.RunTask`); логика из `agents/planner/`
распределена по `application/` и `domain/` ✅ — `application/planner` создан,
зависимости от конкретных агентов инвертированы через порты, чистые типы плана
и `AgentType`/`Role` вынесены в `domain/`, `domain.Status` — авторитетный FSM
доски, срез C закрыт (C1–C6).

### Этап 4: Рефакторинг инструментов

**Цель:** Упростить пакет `tools/` и привести его к модели
`infrastructure/`.

1. Разбить `tools/` на подпакеты по доменам:
   - `infrastructure/llm/` — интеграция с Ollama, OpenAI.
   - `infrastructure/rag/` — индексация и поиск.
   - `infrastructure/git/` — git-операции (из `gitops/`).
   - `infrastructure/sandbox/` — контейнеры и изоляция.
   - `infrastructure/board/` — доска задач.
2. Каждый подпакет реализует интерфейсы из `domain/services/`.
3. Инструменты агентов (`tools/fileops.go`, `tools/codesearch.go` и др.)
   переходят на использование `domain/services/` вместо прямых вызовов.

**Критерий:** `tools/` разбит на подпакеты; каждый подпакет зависит от
`domain/`, а не от других инфраструктурных пакетов.

#### Срезы Этапа 4

Этап разбит на срезы (по образцу Этапа 3): сначала под `infrastructure/`
переезжают уже существующие инфраструктурные пакеты, затем режется `tools/`.

| Срез | Что | Статус |
|---|---|---|
| **E1** | Каркас `infrastructure/`; `models` → `infrastructure/llm` (пакет `models` → `llm`), compile-assertions `services.LLMClient` (`ports.go`) | ✅ |
| **E2** | `rag` → `infrastructure/rag` (пакет `rag`), адаптер `rag.ProjectAdapter` к порту `services.RAGClient` | ✅ |
| **E3** | `gitops` → `infrastructure/git` (пакет `git`), порт `services.GitOperations` реализуется `*git.Repo` напрямую | ✅ |
| **E4** | `sandbox` → `infrastructure/sandbox` (пакет `sandbox`), порт `services.SandboxWorkspace` реализуется `*LocalWorkspace`/`*DockerWorkspace` напрямую через alias типов домена | ✅ |
| **E5** | `board` → `infrastructure/board` (самый крупный: ~75 импортёров) | ✅ |
| **E6** | Разбить `tools/` по доменам (`tools/lsp`, fileops/git/rag/board-подпакеты) и перевести их на `domain/services` вместо прямых импортов `ai/board`/`ai/rag`/`ai/sandbox`/`ai/infrastructure/git` — разбит на под-срезы E6a…E6e (ниже) | 🔄 (E6a ✅) |

Под-срезы E6 (tools тесно связан сам с собой: `FileOps` несёт методы LSP,
`sandbox`, RAG, поэтому «разбить одним куском» нельзя):

| Под-срез | Что | Статус |
|---|---|---|
| **E6a** | `tools/` → `domain` вместо `ai/infrastructure/board`: `ErrExists`/`DecodeInjections`/`ValidateInjections` — из `domain` (уже были алиасами), `DecodeComments` перенесён в `domain` (`domain/comment.go`), `board.DecodeComments` делегирует. `tools` (non-test) больше не импортирует `infrastructure/board` | ✅ |
| **E6b** | `tools/rag` — CodeSearch/RagIndexStatus вынесены в подпакет `ai/tools/rag` (package `rag`), инфра-RAG импортируется как `infrarag`; зависимость от `FileOps` инвертирована интерфейсом `rag.Context{ProjectName; WorkingDir}` (лениво — `SetProjectName`/`SetOutputDir` зовутся уже после сборки набора) | ✅ |
| **E6c** | `tools/sandbox` не выделен: `sandbox`-исполнение завязано на `tools` (`runCommand` остаётся в `tools`; сессионная песочница `Activate`/`Deactivate`/`SandboxNetwork` и её тесты `sandbox_session_test.go` живут в `tools` и вызывают `runCommand`, поэтому чистого листа для выноса нет) | ⏳ (отложен) |
| **E6d** | `tools/gitresolve`/`tool.go` → `tools/git` — выполнено в **E6e1** (отдельного git-инструмента больше нет) | ✅ (в E6e1) |
| **E6e** | Собственно разбиение `tools/` на под-пакеты (`tools/lsp`, `tools/rag`, `tools/sandbox`, `tools/git`, …) с инверсией зависимости от `FileOps` (интерфейс `Runner`/`Project`/`Ops` в под-пакете, методы/поля `FileOps` делегируют). Под-пакеты реализуют `services.Tool` напрямую (`Definition() domain.ToolDefinition`) и вправе импортировать `infrastructure/*`. Выполняется по одному под-пакету (E6e1…); **E6e1 ✅** — `tools/git` | 🔄 (E6e1 ✅) |

**E1 ✅ (каркас `infrastructure/`, `models` → `infrastructure/llm`).**
`git mv models infrastructure/llm`, пакет переименован `models` → `llm`;
импортёры (`main.go`, `repositories/Repository.go`, `server/resolve.go`,
`server/server.go`, `server/session.go`, `server/chatassist.go`,
`server/gitflow_ar.go` + тесты) переведены на `ai/infrastructure/llm` /
квалификатор `llm.`. Новый `infrastructure/llm/ports.go` фиксирует
соответствие порту `domain/services.LLMClient`: `*OllamaProvider` и
`*LayeredProvider` (у `OpenAIProvider` пока нет `ChatStream` — покроется при
переводе OpenAI-пути на порт). Поведение не менялось: чистое перемещение +
переименование. Верификация: канонический набор (добавлен
`./infrastructure/...`) зелёный, `-race` по `infrastructure/llm` чист,
`gofmt -l` по своим файлам чист.

**E2 ✅ (`rag` → `infrastructure/rag` + адаптер порта).** `git mv rag
infrastructure/rag`, имя пакета сохранено (`rag`); 25 импортёров переведены на
`ai/infrastructure/rag` (`main.go`, `runctx`, `server/{session,ragindex,ragref,chatassist}`,
`runner/adapters/reindex.go`, `application/planner`, `agents/{architect,developer,chatassist}`,
`tools/{codesearch,ragstatus}` + тесты). Порт `services.RAGClient` — узкий и
не совпадает с богатым API `rag.Client` (явный `projectName`, `SearchParams`,
`IndexProject`), поэтому добавлен адаптер `rag.ProjectAdapter`
(`infrastructure/rag/port.go`, `NewProjectAdapter(client, project, dir)`):
проект и рабочий каталог фиксируются в конструкторе, `Search` переводит
`SearchResult` → `services.RAGResult` (версионные маркеры в `Metadata`),
`Index` индексирует дерево каталога (обход `WalkProject` + `IndexProject`,
ветка/коммит по `DetectIndexOptions`), `BranchIndexed` сравнивает с коммитом
каталога. Compile-assertion `var _ services.RAGClient = (*ProjectAdapter)(nil)`.
Тесты `infrastructure/rag/port_test.go` (4 шт., hermetic на `fakeStore`/
`fakeEmbedder`/`withGit`): маппинг поиска, `BranchIndexed` (git/вне git),
индексация каталога (Upsert), пустой каталог → ошибка. Верификация: канонический
набор зелёный, `-race` по `infrastructure/rag` чист. **Осталось на потом:**
`rag` напрямую импортирует `ai/forges` (`forges.IsIgnoredDir` в `walk.go`) —
инфра→инфра-зависимость, которую нужно убрать при ревизии `forges`
(кандидат: перенести `IsIgnoredDir` в `domain` или `tools`).

**E3 ✅ (`gitops` → `infrastructure/git` + порт `services.GitOperations`).**
`git mv gitops infrastructure/git`, пакет `gitops` переименован в `git`; 28
импортёров переведены на `ai/infrastructure/git` и квалификатор `git.`
(`server/{git,gitflow*,diff,actions*,ragref,taskrollback,server}.go`,
`agents/developer`, `runner/adapters/wipcommit.go`, `tools/{tool,gitresolve}.go`
+ тесты). Порт `services.GitOperations` переписан: старая «эскизная» форма
(`Clone(remote, dir)`, `Merge(dir, branch)`, `[]FileDiff`, `GitStatus`) не
совпадала с реальным API gitops (последний оперирует `*Repo` с полями
root/remote/branch/base, а создание копии требует `Executor`). Новая форма —
набор методов рабочей копии с подписями, **совпадающими** с `*git.Repo`, поэтому
репозиторий реализует порт напрямую (без адаптера): состояние
(`CurrentBranch`/`HeadSHA`/`InTaskBranch`/`Dirty`/`DirtyPaths`/`BranchExists`),
история (`MergeBase`/`MergedInto`/`CountCommits`/`Diff`), изменения
(`Commit`/`CommitIfDirty`/`Stage`/`Push`), ветки
(`CreateBranch`/`Checkout`/`RejectBranch`), конфликты
(`ConflictingFiles`/`UnmergedFiles`/`ConflictDiff`). Операции, возвращающие
инфраструктурные типы (`MergeResult`, `WorktreeInfo`, `Submodule`) или
требующие `Executor`, в порт не вошли — остаются у `*Repo`. Compile-assertion
`var _ services.GitOperations = (*Repo)(nil)` и исполняемый тест
`infrastructure/git/port_test.go` (репозиторий отдаётся через интерфейс,
`CurrentBranch`/`Dirty` работают). Верификация: канонический набор зелёный,
`-race` по `infrastructure/git` чист, `gofmt` по своим файлам чист.

**E4 ✅ (`sandbox` → `infrastructure/sandbox` + порт `services.SandboxWorkspace`).**
`git mv sandbox infrastructure/sandbox`, имя пакета сохранено (`sandbox`); 6
импортёров переведены на `ai/infrastructure/sandbox` (`acceptor/run.go`,
`cmd/sandbox-proxy/main.go`, `server/sandbox.go`, `tools/{fileops,sandbox,destructive}.go`
+ тесты). Порт `services.SandboxEngine` (старая «эскизная» форма
`Start`/`Stop`/`Exec`/`CopyIn`/`CopyOut` с `SandboxConfig`/`SandboxMount`/
`SandboxInstance`) противоречил осознанному дизайну пакета: файловые операции
остаются на хосте (bind-mount), CopyIn/CopyOut не нужны. Порт переписан в
фактическую потребительскую абстракцию — `SandboxWorkspace{Run(ctx, workdir,
command) (SandboxResult, error); Close(ctx) error}` с `SandboxResult{Stdout,
Stderr, ExitCode, TimedOut, ExitErr}`. Чтобы обойтись без адаптера, `Result` и
`Workspace` в пакете стали **alias'ами** канонических типов домена
(`infrastructure/sandbox/workspace.go`), поэтому `*LocalWorkspace` и
`*DockerWorkspace` удовлетворяют порту напрямую — compile-assertions в
`infrastructure/sandbox/port.go`; все прежние обращения `sandbox.Result`/
`sandbox.Workspace` продолжают работать. Жизненный цикл сессии
(`SessionOptions`/`StopSession`) и клиент Docker Engine (`Engine`) в порт не
вошли. Правка теста `tools/sandbox_test.go` (читает `../infrastructure/sandbox/
{compose.yaml,Dockerfile}`) и комментариев-путей в `tools/sandbox.go`,
`infrastructure/sandbox/{ensureimage,stack,workspace}.go`. Верификация:
канонический набор (`./sandbox/` заменён на `./infrastructure/...`) зелёный,
`-race` по `infrastructure/sandbox` чист, `gofmt` по своим файлам чист.

**E5 ✅ (`board` → `infrastructure/board`).** `git mv board infrastructure/board`,
имя пакета сохранено (`board`); все 75 импортёров переведены на
`ai/infrastructure/board` (`agents/{developer,chatassist,architect}`,
`application/planner`, `injections`, `runner/adapters`, `server/*`, `tokens`,
`tools/*`, `main.go`, `e2e_harness*`). Правка чисто механическая — меняется
только путь импорта; `board.*`-квалификатор и поведение не тронуты (внутри
пакета — прежние импорты `ai/domain`/`ai/domain/services`). Из канонического
набора убран `./board/` (каталог входит в `./infrastructure/...`); в живых доках
`board/` → `infrastructure/board/`. Верификация: канонический набор зелёный
(`./application/planner` и `./infrastructure/board` — ok), `gofmt` по своим
файлам чист.

**E6a ✅ (`tools/` → `domain` вместо `ai/infrastructure/board`).** `tools` уже
работал с доской через порт `services.BoardStore`, а единственной связью с
`infrastructure/board` были `ErrExists`/`DecodeInjections`/`ValidateInjections`
(уже алиасы/обёртки `domain`) и `DecodeComments`. Перенесён `DecodeComments` в
`domain` (`domain/comment.go` — `func DecodeComments([]byte) (Comments, error)`),
`board.DecodeComments` стал делегатом (`infrastructure/board/comment.go`,
удалён неиспользуемый импорт `encoding/json`), а `tools/board_tools.go` вызывает
`domain.{ErrExists,DecodeInjections,ValidateInjections,DecodeComments}` и больше
не импортирует `ai/infrastructure/board` (тесты `tools/board_*_test.go` по-прежнему
поднимают реальный store — это только тестовый бинарник). Верификация:
`./tools`, `./domain`, `./infrastructure/board`, `./server` зелёные.

**E6e1 ✅ (`tools/git` — ResolveGitConflicts).** Направление выбрано по запросу:
E6 читается как **разбиение `tools/` на под-пакеты по доменам** (под-пакеты
вправе импортировать `infrastructure/*`; «без прямых импортов инфраструктуры»
для rich-API — rag/git/sandbox — недостижимо без загрязнения `domain`). Ключ к
отсутствию цикла: `Tool`/`ToolDefinition` уже живут в
`domain`/`domain/services` (`services.Tool`, `domain.ToolDefinition`), поэтому
под-пакет реализует `services.Tool` напрямую и не импортирует `ai/tools`.
Первый под-пакет — `tools/git` (`package git`): из `tools/gitresolve.go`
перенесена вся логика резолва конфликтов (`Execute`/`actionConflicts`/
`actionResolve`, `markeredBody`, `resolveJSON`, `sortDedup`/`union`/
`sliceContains`), инфраструктурный git импортируется как
`gitops "ai/infrastructure/git"`. Обратная зависимость от `FileOps` инвертирована
интерфейсом `git.Ops{TakeTouched; LspCheckFiles}` + полем `Dir string`
(вместо `ops.OutputDir`). Экспортированы `git.ResolveTool` и
`git.ResolveToolName`; в `tools` остался тонкий алиас
`const ResolveGitConflicts = git.ResolveToolName`, а `registry.go` собирает
`&git.ResolveTool{Dir:…, Ops: deps.FileOps, Ex: deps.GitExec}`. Тест
`tools/gitresolve_test.go` переехал в `tools/git` (`package git`, `fakeGitExec`
без изменений). Верификация: `./tools/...` (в т.ч. новый `ai/tools/git`),
`./server`, канонический набор — зелёные, `gofmt` чист.

**E6b ✅ (`tools/rag` — CodeSearch/RagIndexStatus).** `tools/codesearch.go` и
`tools/ragstatus.go` вынесены в `ai/tools/rag` (`package rag`); инфра-RAG
импортируется как `infrarag "ai/infrastructure/rag"` (RAG-контракт богаче порта
`services.RAGClient` — `Searcher`/`ProjectInfo`/`UnavailableError`/`DetectBranch`,
переносить его в `domain` нельзя: `DetectBranch` ходит в git). Обратная
зависимость от `FileOps` инвертирована интерфейсом `rag.Context{ProjectName()
string; WorkingDir() string}`: обе величины читаются ЛЕНИВО (оркестрация зовёт
`SetProjectName`/`SetOutputDir` уже после сборки набора инструментов —
`kanban_execute.go`), поэтому не запоминаются при `Select`. В `tools/fileops.go`
добавлен nil-безопасный аксессор `WorkingDir()` (и поправлен порядок nil-проверки
в `ProjectName`). Экспортированы `rag.SearchTool`/`rag.StatusTool`/
`rag.CodeSearch`/`rag.IndexStatus`; в `tools` остались алиасы `RAGSearcher =
rag.Searcher` (`tool.go`), `CodeSearch`/`RagIndexStatus` (`registry.go`), а
`registry.go` собирает `&rag.SearchTool{Ctx, Searcher}`/`&rag.StatusTool{…}` через
nil-безопасный `ragCtx(deps.FileOps)`. Тест `tools/codesearch_test.go` переехал в
`tools/rag` (фейковый `fakeCtx` повторяет фолбэк имени проекта; сам фолбэк теперь
покрыт `tools/fileops_projectname_test.go` вместе с `WorkingDir`). Верификация:
`./tools/...` (в т.ч. новый `ai/tools/rag`), канонический набор — зелёные,
`gofmt` по своим файлам чист.

### Этап 5: Рефакторинг агентов

**Цель:** Устранить дублирование между специализациями агентов.

1. Вынести общую логику в `domain/agent/`:
   - Базовый цикл агента (получение инструментов, отправка в LLM, обработка
     ответа).
   - Роль как конфигурация, а не как отдельный пакет.
2. Специализации (`architect`, `backendlead`, `frontendlead`, `qaengineer`
   и др.) становятся наборами конфигураций (промпты, инструменты, лимиты),
   а не отдельными пакетами с копированием кода.
3. `AgentDefinition` из `agents/AgentDefinition.go` переносится в
   `domain/agent/`.

**Критерий:** Новая специализация агента создаётся конфигурацией, а новым
пакетом; дублирование между `agents/*/` устранено.

### Этап 6: Интерфейсы и адаптеры

**Цель:** Привести `server`, `web`, `cli` к единому стилю адаптеров.

1. `server/` становится REST/WebSocket адаптером к `application/usecases/`.
2. `web/` (React) общается только через HTTP API, без обходных путей.
3. CLI (`main.go`) — тонкая обёртка над `application/usecases/`.
4. Хуки git-flow (`server/gitflow*.go`) переносятся в `application/` или
   `domain/services/`.

**Критерий:** `server/` не содержит бизнес-логики; только маршрутизация,
сериализация, авторизация.

**Срезы (статус):**

| Срез | Что | Статус |
|---|---|---|
| F1 | Ручной откат задачи и прогноз токенов вынесены из `server/` в `application/usecases` (`rollback.go`, `tokenestimate.go`); `server/taskrollback.go`/`tokenestimate.go` стали тонкими обёртками | ✅ |
| F2 | Детерминированная обработка логов вынесена из `server/` в `domain/log` (разбор строк/уровней/стека, шаблонизация, `BuildLogDigest`, `MergeLogFindings`, фильтры, `TruncateLogText`); `server/loganalysis.go` читает файлы и собирает промпт | ✅ |
| F3a | Гард «phantom done» (`board.Store.TaskDoneGuard`) вынесен из `server/` в `application/usecases` (порт `DoneGuardSource`); `server/gitflow_done_guard.go` — адаптер | ✅ |
| F3b | Цикл LLM-разрешения конфликтов (`Resolver.ResolveWithLLM`, промпт, `verify`, `clearConflictField`) вынесен из `server/` в `application/gitflow` (порты `Store`/`Developer`/`Acceptor`/`Markers`); удалён мёртвый код (`buildPrompt`/`collectFiles`/`writeResolved`) | ✅ |
| F3c | Политика создания MR (`MRCreator.CreateTaskMR`/`CreateEpicMR`, `ErrEmptyBranch`) вынесена из `server/` в `application/gitflow` (порты `MRGateway`/`Forge`/`MRRegistry`) | ✅ |
| F3d | Раскладка worktree (`TaskWorktreePath`/`EpicWorktreePath`/`ChildTaskKey`), артефактная политика `.gitignore` (`TaskGitignore`/`EnsureTaskGitignore`) и презентация git-ошибки (`GitflowErrBoard`) вынесены в `application/gitflow` | ✅ |
| F3e | Авто-шаги завершения задачи (`TaskFinalizer.Finalize`: авто-коммит worktree → авто-MR → мёрдж в релиз эпика → снятие worktree) вынесены в `application/gitflow` (порт `TaskFinalizerHost`) | ✅ |
| F3f | Авто-синхрон релизной ветки эпика с main (`EpicMainSyncer`: быстрый путь MergeFeature / авто-резолв тривиальных конфликтов / LLM-резолв сложных) вынесен в `application/gitflow` (порт `EpicMainHost`) | ✅ |
| F3g | Политика ветвления (`Brancher`: `EnsureBranch`/`CreateEpicBranch`/`CreateTaskBranch`/`AutoCreateEpicBranch`/`AutoCreateTaskBranch`/`RemoveEpicBranches`, `ErrEpicBranchMissing`/`ErrBranchCreate`) вынесена в `application/gitflow` (порты `BranchRepo`/`BranchRegistry`) | ✅ |
| F3h | Политика вливания ветки задачи в релиз эпика (`TaskMerger.MergeTaskBranch`: проверка проекта/веток, снятие worktree эпика, `MergeFeature` под локом, инвалидация диффов) вынесена в `application/gitflow` (порт `MergeTaskHost`) | ✅ |
| F3i | Политика релиза эпика в main (`Releaser.ReleaseEpic`: проверка git/базы/статуса done/ветки эпика, `MergeFeature` под локом, инвалидация диффов + хук «main изменился») вынесена в `application/gitflow` (порт `ReleaseHost`, доменные sentinel-ошибки вместо `apiError`); `apiError` удалён | ✅ |
| F3j | Конфликтный резолв «main ↔ релизная ветка эпика» (`ConflictResolver.StartRebase`/`FinishResolve`: прогноз merge-tree, постоянный конфликтный worktree, авто-резолв тривиальных блоков, обязательная приёмка, коммит резолва, финальный merge+push) вынесен из `server/gitflow_resolve.go` в `application/gitflow` (порты `ConflictHost`/`ConflictRepo`/`ConflictWorktree`); `conflictHost`/`serverConflictRepo` — серверные адаптеры | ✅ |
| F3k | RAG-хуки: чтение файлов ветки из git (`LoadRefItems`/`LoadRefDiff`/`ParseLsTreeEntry`/`IndexablePath`/`IndexableContent`/`RefFileSize`), сбор файлов каталога (`CollectIndexItems`) и полная индексация проекта (`IndexProjectRAG`), проверка актуальности (`BranchIndexUpToDate`) вынесены из `server/ragref.go`/`server/ragindex.go` в новый пакет `application/rag` (порт `Indexer`, `RefSnapshot`, `MaxRefFileBytes`); оркестрация фоновых прогонов и хуки остаются в `server` | ✅ |
| F3l | Политика открытия git-проекта (`ProjectOpener.Open`: имя из URL `GitProjectName`, клон, распознавание полу-клона `discardHalfClone`, регистрация проекта и сабмодулей; `SubmoduleProjectName`/`SameGitRemote`) вынесена из `server/git.go` в `application/gitflow` (порт `OpenRegistry`, сентинелы `ErrGitURLName`/`ErrDirNotEmpty`(+`DirNotEmptyError`)/`ErrCloneFailed`/`ErrRegisterFailed`); `handleOpenGitProject` — тонкая обёртка | ✅ |
| F3m | Оркестрация фоновой индексации RAG (`BackgroundIndexer`: single-flight по ветке/источнику, запуск в горутине, отчёты в лог и чат) вынесена из `server/ragindex.go` в `application/rag` (порты `IndexSink`/`IndexerFactory`, тип `IndexRun`); `server` строит конкретный прогон (каталог/ветка/дифф) | ✅ |
| F3n | Структурированный вопрос ассистента (`AskUser`): разбор/валидация пачки вопросов (`ParseQuestions`), состояние одного активного вопроса (блокирующий цикл `AskUser`↔`AnswerAsk`, `Pending`), протокол ответов (`Summarize`) вынесены из `server/ask.go` в `application/ask` (порт `Announcer`); `Session` делегирует, адаптер `askAnnouncer` публикует карточку/статус/лог | ✅ |
| F3o | Панель логов (Logboard): сбор `*.log`, отбор «своих» из общего каталога и дедупликация (`CollectProject`), чтение хвоста с `MaxBytes` (`ReadTail`), типы `Entry`/`Response` вынесены из `server/logs.go` в `application/logs`; в `server` остались HTTP-хендлеры, `logDirs`/`logsDir`, брокер и алиасы типов | ✅ |
| F3p | Разбор логов кнопкой «Разобрать»: выборка файлов (`PickFiles`), фильтры поиска/уровня и дайджест (`BuildAnalysis`), контракт ответа (`DigestPrompt`), cooldown повторов (`Cooldown`) вынесены из `server/loganalysis.go` в `application/logs`; `server` оставил алиасы типов, обвязку проекта и адаптер cooldown | ✅ |
| F3q | Разбор unified-диффа git (`Parse`/`Split`/`ParseBlock`/`PathOf`/`PrefixPath`, статусы `Added`/`Modified`/`Removed`/`Renamed`/`Submodule`, типы `FileEntry`/`Diff`) вынесен из `server/diff.go` в новый stdlib-only пакет `domain/diff`; `server` оставил git-вызовы (`branchDiff`/`gitProjectDiff`), кэш `cachedDiff` и алиасы | ✅ |
| F3r | Жизненный цикл worktree задачи/эпика (`TaskWorktree` — создание worktree задачи + rebase на эпик + сабмодули, `EpicWorktree` — ленивый worktree релизной ветки эпика, `DropEpicWorktreeLocked` — снятие worktree эпика) вынесен из `server/gitflow_auto.go` в `application/gitflow` (`WorktreeManager`, порт `WorktreeHost`); `server` оставил адаптер `worktreeHost` и тонкие обёртки | ✅ |
| F3s | Политика синхронизации рабочей копии агентов с main (`syncAgentBranchWithMain`: пропуск при грязном дереве/занятом checkout, откат конфликтного merge, признак сдвига HEAD) вынесена из `server/ragref.go` в `application/rag` (`WorkingCopySyncer`, порт `SyncSink`, `sync.go`); `server` оставил адаптер `syncSink` и тонкую обёртку | ✅ |
| F3t | Конкретные фоновые прогоны индексации RAG (`RefJob` — содержимое ветки по ref, `RefDiffJob` — изменения ветки относительно базы, `DirJob` — каталог под его веткой; загрузка снапшота + пропуск актуального индекса + отчёт) вынесены из `server/ragref.go`/`server/ragindex.go` в `application/rag` (`jobs.go`); `server` оставил адаптеры загрузчиков/фабрики и тонкие обёртки `Index*Background` | ✅ |
| F3u | Набор сессионных промпт-инъекций (`SessionInjections`/`Add`/`Upsert`/`Remove`: адресация по ID, честный признак удаления, потокобезопасная копия) вынесен из `server/session.go` в `domain` (`InjectionSet`, `injectionset.go`); `Session` делегирует и лениво создаёт набор (нулевая сессия работоспособна) | ✅ |
| F3v | Правка и презентация конфликтных файлов модели (`NewConflictView` — отбор остаточных маркеров + фрагмент больших файлов, `ApplyConflictFiles` — запись files/edits с проверкой уникальности `old`, `ConflictSafeRel`/`WithinDir` — безопасность пути, `IsConflictMarkerLine`/`ConflictRegion`) вынесены из `server/actions_resolve.go` в `application/gitflow` (`conflictfiles.go`); `server` оставил алиасы `ConflictEdit`/`conflictView` и тонкие обёртки | ✅ |
| F3w | Детерминированная политика диалога чат-ассистента (`ConfirmAffirmative`/`BareConfirm`, `AsksSomething`, `PrevUserDiffers`, `LooksLikeBoardWriteClaim`, `AskSummary`, `ActionAnchor`) вынесена из `server/chatassist.go`/`server/actions.go` в новый пакет `application/chatassist` (`policy.go`); `server` оставил тонкие обёртки (`chatActionAnchor` читает историю и делегирует, `confirmAffirmative`/`looksLikeBoardWriteClaim`/`askSummary`) | ✅ |
| F3x | HITL-затвор (`Epics`/`Tasks` — `Wait` c публикацией события/статуса и блокировкой до решения, `Approve` — разрешение/отказ, `Reset`, состояние `State`) вынесен из `server/session.go` в новый пакет `application/hitl` (`gate.go`, порт `GateHost`: `Stopped`/`Running`/`BroadcastStatus`/`PublishGate`); `Session` оставил адаптер эффектов и тонкие обёртки, `gateEvent` — алиас `hitl.GateEvent` | ✅ |
| F3y | Read-model доски: сборка упорядоченного снимка (`View` — эпики в эффективном порядке очереди через `infrastructure/board.SortEpics`, задачи, баги, meta), пагинация со счётчиками (`ViewPage`/`SlicePage`), `TruncateText`, типы `Snapshot`/`Total` вынесены из `server/session.go` в новый пакет `application/board` (`view.go`, порт `Store`); `server` оставил `boardSnapshot` (встраивает `appboard.Snapshot` + `Git *gitView`) и тонкие обёртки | ✅ |
| F3+ | Остальная бизнес-логика `server/` (git-flow хуки, сессии/оркестрация, остальные RAG-хуки) → `application`/`domain/services` | в плане |

**F1 ✅** — `application/usecases/rollback.go` (`RollbackTarget`/`RollbackTask` +
`WorktreeRollbacker`-порт, доменные ошибки `ErrTaskNotFound`/`ErrNoWorktree`/
`ErrBadTarget`/`ErrNoSuchCommit`) и `application/usecases/tokenestimate.go`
(`NewTokenEstimator`, TTL-обучение) покрыты unit-тестами (`rollback_test.go`);
`server/taskrollback.go`/`server/tokenestimate.go` только подключают
зависимости. **F2 ✅** — новый пакет `domain/log` (stdlib-only,
`go list -deps` чист): типы `LogLine`/`LogFinding`/`LogDigest` (+`Empty`/`Text`),
`ParseLogLine`/`LogHeuristicLevel`/`IsLogStackFrame`/`LevelRank`/
`TruncateLogText`, `BuildLogDigest`, `MergeLogFindings`,
`FilterLogLines`/`FilterLogByLevel`; тесты переехали из
`server/logdigest_test.go` в `domain/log/digest_test.go`. `server/loganalysis.go`
импортирует `logpkg "ai/domain/log"` и держит только чтение файлов, выборку,
промпт (`LogDigestPrompt`) и cooldown; временный shim `server/logdigest.go`
удалён. Канонический набор зелёный, `-race` по `domain/log` чист.
**F3a ✅** — правило гарда «phantom done» (`GuardTaskDone`/`IsOrchestratorArtifact`,
порт `DoneGuardSource`: `IsGitProject`/`TaskBranch`/`EpicBase`/`CommitsAhead`/
`DirtyPaths`/`Warn`) в `application/usecases/doneguard.go`, покрыто
unit-тестами `doneguard_test.go` (герметичная заглушка источника, без git);
`server/gitflow_done_guard.go` — адаптер `DoneGuardSource` поверх реестра и
`git.Repo` + env-флаг `KANBAN_DONE_REQUIRES_COMMIT` и внедрение в
`board.Store.TaskDoneGuard` (в `attachGitHooks`); серверные интеграционные
тесты гарда (`server/gitflow_done_guard_test.go`) ходят через адаптер и
проходят без правок. **F3b ✅** — цикл LLM-разрешения конфликтов
(`Resolver`/`ResolveWithLLM`, `buildDeveloperPrompt`, `verify`,
`clearConflictField`, `flattenOutputDir`) и порты `Store`/`Developer`/
`Acceptor`/`Markers` в `application/gitflow/resolve.go`; сразу удалён мёртвый
код прежнего JSON-пути (`buildPrompt`/`collectFiles`/`writeResolved` — не
вызывались ниоткуда). `server/gitflow_ar.go` стал HTTP-обвязкой: адаптеры
`resolveDeveloper`/`resolveAcceptor`, `attemptLLMResolve` (store/provider +
stage/commit/push) и хендлеры. Герметичные тесты `resolve_test.go` (успех,
прогон по бюджету с ошибкой приёмки в промпте, остаточные маркеры, провал
приёмки, ошибки LLM, перенос `OUTPUTDIR/`, порядок задач, частичное снятие
конфликтов). **F3c ✅** — политика открытия MR
(`MRCreator`/`CreateTaskMR`/`CreateEpicMR`, `ErrEmptyBranch`) вынесена в
`application/gitflow/mr.go` за портами `MRGateway`/`Forge`/`MRRegistry`;
`server/gitflow_mr.go` оставил адаптеры, разбор HTTP-ошибок и read-model
`gitStatus`, серверный `errMREmptyBranch` — алиас доменной ошибки. Герметичные
тесты `mr_test.go` (порядок вызовов, пустая ветка → `ErrEmptyBranch`, ошибка
подсчёта коммитов пропускает, сбои базы/форджа/реестра, MR эпика). **F3d ✅** —
раскладка worktree (`TaskWorktreePath`/`EpicWorktreePath`/`ChildTaskKey`),
артефактная политика `.gitignore` (`GitignoreHeader`/`GitignorePatterns`/
`TaskGitignore`/`EnsureTaskGitignore`) и презентация git-ошибки
(`GitflowErrBoard`) вынесены в `application/gitflow/worktree.go`;
`server/gitflow_auto.go` оставил тонкие обёртки (`taskWorktreePath`/
`epicWorktreePath`/`childTaskKey`/`taskGitignore`/`ensureTaskGitignore`/
`gitflowErrBoard`). Герметичные тесты `worktree_test.go`. **F3e ✅** — авто-шаги
завершения задачи (`TaskFinalizer.Finalize`: авто-коммит worktree → авто-MR →
мёрдж в релиз эпика → снятие worktree, с LLM-авторезолвом конфликта) вынесены в
`application/gitflow/finalize.go` за портом `TaskFinalizerHost`
(`Project`/`TaskBranch`/`CommitWorktree`/`CreateTaskMR`/`BranchAlreadyMerged`/
`MergeTaskBranch`/`ResolveTaskConflict`/`SaveTask`/`RemoveTaskWorktree`/
`EmitBoard`/`AppendStatus`); `server/gitflow_auto.go` оставил адаптер
`taskFinalizerHost` (git, реестр веток, доска, сессия, шина) — mergeLock и
разбор `git.MergeConflictError` живут в адаптере. Герметичные тесты
`finalize_test.go` (пропуск не-git/без ветки, полный цикл, «уже влита», ошибка
MR не мешает мёрджу, конфликт разрешён/не разрешён LLM, ошибка мёрджа, пустой
worktree, пустая задача). **F3f ✅** — авто-синхрон релизной ветки эпика с main
(`EpicMainSyncer`: быстрый путь `MergeFeature`, иначе настоящий merge main во
временном worktree + авто-резолв тривиальных блоков + push; сложные конфликты не
продвигают ветку) вынесен в `application/gitflow/sync.go` за портом
`EpicMainHost` (`ProjectInfo`/`EpicBranch`/`Repo`/`DropEpicWorktreeLocked`/
`Locked`/`AttemptLLMResolve`/`TrivialResolve`/`SetEpicMergeConflict`/`Notify`/
`Status`) и типами `EpicRepo`/`WorktreeOps`; `server/gitflow_sync.go` — адаптер
`epicMainHost` + обёртки `serverEpicRepo`/`serverWorktreeOps` над `*git.Repo`,
`server/gitflow_auto.go` оставил тонкие `syncEpicMainOnce`/`syncEpicMainForTask`
(`autoResolveMainSync` удалён — логика в use-case). Герметичные тесты
`sync_test.go` (пропуск плохого состояния, быстрый путь, ошибка мёрджа,
тривиальный резолв, сложный конфликт с LLM/без, `SyncMainForTask` без лока).
**F3g ✅** — политика ветвления вынесена из `server/gitflow.go` в
`application/gitflow/branches.go`: `Brancher` (`EnsureBranch` — идемпотентное
создание от базы, `CreateEpicBranch`/`CreateTaskBranch` — создание ветки от
релизной ветки эпика, запись `BranchRef` в side-реестр, проставление
`git_branch` на доске, `AutoCreateEpicBranch`/`AutoCreateTaskBranch` —
best-effort авто-ветки хуков, `RemoveEpicBranches` — снятие веток+MR эпика и
задач), доменные ошибки `ErrEpicBranchMissing`/`ErrBranchCreate`, префиксы
`EpicBranchPrefix`/`TaskBranchPrefix`; порты `BranchRepo`
(`BranchExists`/`CreateBranch`) и `BranchRegistry` (`SetEpicBranch`/`EpicBranch`/
`SetTaskBranch`/`DeleteEpicBranch`/`DeleteTaskBranch`/`DeleteEpicMR`/
`DeleteTaskMR`) + существующий `Store` (`SaveEpic`/`SaveTask`) и колбэк
`Changed`. `server/gitflow.go` оставил адаптер `branchRegistry` поверх
`*workspace.Registry` и хелпер `s.brancher(...)`; свободная `ensureBranch`
удалена, хендлеры/хуки переведены на use-case с сохранением кодов (404/400/502/
500). Герметичные тесты `branches_test.go` (создание/идемпотентность/пустая
база/сбой git/ветка задачи от эпика/нет ветки эпика → `ErrEpicBranchMissing`/
авто-хуки/снятие веток/`EnsureBranch`). **F3h ✅** — политика вливания ветки задачи в релизную ветку
эпика (`TaskMerger.MergeTaskBranch`) вынесена из `server/gitflow.go` в
`application/gitflow/release.go`: проверка `t`/эпика/проекта (`MergeProject`:
`IsGit`/`Kind`/`Base`/`PushURL`), недостающих веток эпика и задачи, снятие
worktree эпика (`DropEpicWorktreeLocked`), `MergeFeature` под пер-проектным
локом (`Locked`) и инвалидация диффов на успехе. Порт `MergeTaskHost` +
`MergeRepo`. `server/gitflow.go` оставил адаптер `mergeTaskHost` и тонкую
обёртку `s.mergeTaskBranch`; HTTP-хендлер/ассистент/finalizer (три потребителя)
не менялись. Герметичные тесты `release_test.go` (успех с проверкой аргументов
слияния, nil/без эпика, не-git/нет проекта/нет репозитория, нет веток,
конфликт не инвалидирует диффы). **F3i ✅** — политика релиза релизной ветки
эпика в main (`Releaser.ReleaseEpic`) вынесена из `server/gitflow.go` в
`application/gitflow/release.go`: проверка git-проекта/базы/статуса done/ветки
эпика, `MergeFeature` под пер-проектным локом, инвалидация диффов и хук
«main изменился» (`MainChanged`: при `already=true` — только проверка индекса,
иначе переиндексация + подтягивание рабочей копии). Порт `ReleaseHost`. Вместо
серверного `apiError` use-case возвращает доменные sentinel-ошибки
(`ErrProjectNotFound`/`ErrNotGitProject`/`ErrNoBaseBranch`/`ErrEpicNotFound`/
`ErrEpicNotDone`/`ErrRepoUnavailable`/`ErrBoardUnavailable`/`ErrMergeFailed`),
которые хендлер маппит в коды (404/400/503/502/409); `apiError` удалён, у
`mergeTaskHost` появились `EpicStatus`/`MainChanged`. Герметичные тесты
`release_test.go` (успех, already-merged, все sentinel'ы состояния, конфликт vs
`ErrMergeFailed`). **F3j ✅** — конфликтный резолв «main ↔ релизная ветка эпика»
(`ConflictResolver.StartRebase`/`FinishResolve`) вынесен из
`server/gitflow_resolve.go` в `application/gitflow/conflict.go`: прогноз
конфликтов `merge-tree`, постоянный конфликтный worktree
(`.conflict-<проект>-<эпик>`), авто-резолв тривиальных блоков
(`TrivialResolve`/`Markers`), обязательная приёмка (`Accept`), коммит резолва
(`Commit`/`CommitAllowEmpty`) и финальный `MergeFeature` + push под
пер-проектным локом. Порт `ConflictHost` + `ConflictRepo`/`ConflictWorktree`;
исходы несут строковый `Status` (`resolving`/`ok`/`busy` для rebase;
`ok`/`still_conflicts`/`acceptance_failed`/`no_resolve`/`busy`/`conflicts` для
resolve), сентинелы `ErrResolveBusy`/`ErrNoResolve`/`ErrResolveStateSave`.
`server/gitflow_resolve.go` оставил адаптер `conflictHost` (через `mergeTaskHost`
+ `s.reg`/`s.repoOf`/`git.RepoFromState`/`acceptor.Accept`) и `serverConflictRepo`
поверх `*git.Repo`, `writeConflictErr` маппит sentinel'ы в коды; `handleResolveStatus`
и `unionPaths` (переехал) — как раньше. `mergeTaskHost` вырос до `ConflictHost`
(встроен). Герметичные тесты `conflict_test.go` (16 шт.): no-conflicts/repeat/
busy/все sentinel'ы состояния rebase, сбой прогноза и сбой записи состояния с
cleanup; resolve ok (dirty/clean), no_resolve/нет worktree/busy/still_conflicts/
acceptance_failed/final conflict/merge fatal. **F3k ✅** — RAG-хуки вынесены из
`server/ragref.go`/`server/ragindex.go` в новый пакет `application/rag`:
`LoadRefItems`/`LoadRefDiff` (чтение содержимого ветки из `git ls-tree`/`git
show`/`git diff`), `ParseLsTreeEntry`/`IndexablePath`/`IndexableContent`/
`RefFileSize` (отбор путей/файлов), `CollectIndexItems` (обход каталога),
`IndexProjectRAG` (коллекция + `IndexProject`), `BranchIndexUpToDate` (пропуск
свежего индекса), типы `RefSnapshot`/`Indexer`/`MaxRefFileBytes`. Оркестрация
фоновых прогонов (`Session.startIndexBackground`, single-flight по ветке,
отчёты в чат), git-flow хуки (`refreshRagIndex*`/`refreshRagTaskBranch`/
`afterMainChanged`) и синхрон рабочей копии (`syncAgentBranchWithMain`) остаются
в `server` (срез F3m); `server`-адаптеры `loadRefItems`/`loadRefDiff` —
пакетные переменные поверх `apprag.*`; `projectIndexer` — алиас
`apprag.Indexer`. Тесты чистых функций переехали в `application/rag`
(`ref_test.go`, `index_test.go`), серверные хуки/REST/сессия — как раньше.

**F3l ✅** — политика открытия git-проекта вынесена из `server/git.go` в
`application/gitflow/open.go`: `ProjectOpener.Open` (идемпотентное открытие:
имя из URL через `GitProjectName`, клон в `temp/<имя>`, распознавание
«полу-клона» `discardHalfClone` — обычный `.git`, тот же origin, нет коммитов,
чистое дерево, регистрация проекта и сабмодулей). Порт `OpenRegistry`
(`Get`/`Add`) реализуется `*workspace.Registry` напрямую; сентинелы
`ErrGitURLName`/`ErrDirNotEmpty` (+типизированный `DirNotEmptyError` с `Is`)/
`ErrCloneFailed`/`ErrRegisterFailed`; чистые помощники `GitProjectName`/
`SubmoduleProjectName`/`SameGitRemote`/`dirIsEmpty`. `server/git.go` оставил
тонкий `handleOpenGitProject` (маппинг 400/409/502/400 + `writeProjectMetaFromInfo`)
и адаптер `s.projectOpener()`; обёртки `gitProjectName`/`submoduleProjectName`
сохранены для остальных файлов `server`, удалены локальные
`discardHalfClone`/`sameGitRemote`/`dirIsEmpty`. Тесты: новые герметичные
`application/gitflow/open_test.go` (`fakeExec`/`fakeOpenRegistry`: имя/сабмодуль/
адрес, плохой URL, идемпотентность, чужой каталог → `ErrDirNotEmpty` без
разрушения содержимого, авточистка полу-клона, `ErrCloneFailed` с удалением
каталога, `ErrRegisterFailed`); серверные `git_test.go`/`gitflow_submodule_test.go`
не менялись и зелёные.

**F3m ✅** — оркестрация фоновой индексации RAG вынесена из
`server/ragindex.go` в новый `application/rag/background.go`:
`BackgroundIndexer` (single-flight по слоту `indexSlotKey` — ветка или путь
источника; запуск прогона в горутине; сброс контекста на
`context.Background()` — индексация не наследует отменённый контекст
вызывающего; отчёт о старте/завершении/актуальности/ошибке), типы `IndexSink`
(Info/Warn/Status), `IndexerFactory` (`Indexer`+`io.Closer`), `IndexRun` и
`Wait()`. `server/ragindex.go` оставил `ragIndexSink` (лог проекта + чат),
`sess.newProjectIndexer` (поверх `buildProjectIndexer`) и прогоны
`IndexDirBackground`; `server/ragref.go` — прогоны `IndexRefBackground`/
`IndexRefDiffBackground`. Поле `Session.indexing` заменено на `bgIndex`
(`*apprag.BackgroundIndexer`), создаётся в `newSession`. Тесты: новые
герметичные `application/rag/background_test.go` (`recordingSink`, `fakeCloser`,
`bgBlockingIndexer`: слот, single-flight + закрытие отказавшего клиента,
пропуск/ошибка/сбой фабрики, параллельные ветки). Серверные
`ragindex_test.go`/`ragref_test.go` не менялись и зелёные (переопределяют
`buildProjectIndexer`/`loadRefItems`/`loadRefDiff`).

**F3n ✅** — серверная логика структурированного вопроса `AskUser` вынесена из
`server/ask.go` в новый пакет `application/ask`: `ParseQuestions` (разбор и
валидация пачки вопросов из аргументов инструмента), `Pending` (состояние
одного активного вопроса на сессию — блокирующий цикл `Ask`↔`Answer`),
`Summarize` (протокол ответов), `StrAny`/`BoolAny`; порт `Announcer`
(`Ask`/`Status`/`Info`). `server/ask.go` оставил инструмент-мост `askTool`
(`tools.Tool`), `AskBackend`, схему `Definition`, таймаут `askAnswerTimeout` и
адаптер `askAnnouncer` (карточка-вардинг в чат, статус-строка, лог); `Session`
держит `pending *appask.Pending` (создаётся в `newSession`) и делегирует
`AskUser`/`AnswerAsk`. Тесты: новые герметичные `application/ask/ask_test.go`
(`recAnnouncer`: разбор/дефолты/ошибки схемы, блокирующий цикл, охрана «один
активный вопрос» и валидация ответа, отмена по контексту с повторным вопросом,
`Summarize`); серверные `ask_test.go` не менялись и зелёные (идут через
`Session`/REST).

**F3o ✅** — логика панели логов вынесена из `server/logs.go` в новый пакет
`application/logs`: `CollectProject` (сбор `*.log` по каталогам, отбор «своих»
из общего каталога, дедупликация, сортировка), `CollectFiles`, `ReadTail`
(хвост с ограничением `MaxBytes`), `Filter`, `FileExists`, типы `Entry`/`Response`,
константы `MaxBytes`/`ClearMarker`. В `server/logs.go` остались HTTP-хендлеры
(`handleGetLogs`/`handleDeleteLogs`), `logDirs`/`logsDir`, подписка брокера и
алиасы типов (`logFileEntry`/`logsResponse`, `logMaxBytes`) — серверные
`logs_test.go`/`actions_logs_test.go` не менялись. Тесты: новые герметичные
`application/logs/logs_test.go` (отбор/дедупликация/приоритет общего каталога,
только `*.log` и пропуск каталогов, обрезка хвоста и пустой файл).

**F3p ✅** — разбор логов кнопкой «Разобрать» вынесен из `server/loganalysis.go`
в `application/logs` (`analysis.go`): `Req`/`Analysis` (с `FilesLabel`/`Key`),
`BuildAnalysis` (сбор логов проекта + фильтры поиска/уровня ДО группировки +
дайджест), `PickFiles` (выборка по имени или свежайших, файл проекта первым),
`FilesLabel`, `DigestPrompt` (контракт ответа модели), `Cooldown` (окно 90 с —
двойной клик не зовёт модель дважды) и `Dirs` (каталоги логов проекта).
`server/loganalysis.go` стал тонкой обвязкой: алиасы `logAnalysisReq`/
`logAnalysis`, `buildLogAnalysis` (проект из реестра + имя своего лога +
`applogs.BuildAnalysis`), `markAnalyzed` (делегирует `logCooldown.Mark`),
константа `logAnalysisCooldown`, обёртка `LogDigestPrompt`. `Session` держит
`logCooldown *applogs.Cooldown` вместо пары `logLastKey`/`logLastAt`;
`server/actions_logs.go` — `pickLogFiles` как обёртка над `applogs.PickFiles`.
У неэкспортируемого метода `key()` нет алиасной видимости, поэтому он стал
экспортируемым `Analysis.Key()` (правка в `server/loganalysis_test.go`/
`server/server.go`). Тесты: новые герметичные `application/logs/analysis_test.go`
(фильтры+склейка находок, отсутствующий файл, `PickFiles`, `FilesLabel`,
контракт `DigestPrompt`, окно `Cooldown`, `Dirs`); серверные
`loganalysis_test.go`/`actions_logs_test.go` не менялись по существу. Дока
`docs/20-features/chat-assistant.md` обновлена (разбор → `application/logs`).

**F3q ✅** — разбор unified-диффа git вынесен из `server/diff.go` в новый
stdlib-only пакет `domain/diff` (`diff.go`): `Parse` (разбиение на файлы),
`Split` (блоки по `diff --git`), `ParseBlock` (метаданные + патч, статус по
`---`/`+++` и `rename to`, подсчёт `+`/`-`), `PathOf` (срез `a/`/`b/`,
git-кавычки), `PrefixPath` (префикс сабмодуля), тип `Status` с константами
`Added`/`Modified`/`Removed`/`Renamed`/`Submodule`, типы `FileEntry`/`Diff`.
В `server/diff.go` остались git-вызовы и кэш: `branchDiff` (worktree vs
merge-base), `gitProjectDiff` (кэш по `project|ref|base`, обход сабмодулей),
`invalidateDiffs`; `cachedDiff.Files` теперь `[]diff.FileEntry`, алиасы
`diffFileEntry`/`diffModified` сохранены для хендлеров `server/git.go`.
Тесты `server/diff_test.go` переехали в `domain/diff/diff_test.go` (`git mv`)
и дополнены (статусы/счётчики added/removed/modified, rename и кавычки,
`Split`). Верификация: канонический набор + `./domain/...` зелёные, `-race` по
`domain/diff` чист, `gofmt` по правкам чист. Доки: `project-map.md`
(`domain/diff/`), `contextual-diff.md`, `gitops-workflow.md`, `web-ui.md`,
`submodules-multirepo.md` (ссылки на разбор → `domain/diff`).

**F3r ✅** — жизненный цикл worktree задачи и эпика вынесен из
`server/gitflow_auto.go` в `application/gitflow/lifecycle.go`:
`WorktreeManager` (порт `WorktreeHost`) с методами `TaskWorktree` (Ф-3:
синхрон релизной ветки эпика с main, rebase ветки задачи на эпик, создание
worktree `ai/task/<id>`, подъём worktree сабмодулей и их регистрация,
checkpoint base_sha, RAG-хуки), `EpicWorktree` (4.1: ленивый worktree релизной
ветки эпика `ai/epic/<id>` под локом) и `DropEpicWorktreeLocked` (снятие
worktree эпика с авто-коммитом правок архитектора). `server/gitflow_auto.go`
оставил адаптер `worktreeHost` поверх `reg`/`repoOf`/`gitExec`/`mergeLock`/
RAG-хуков/`srvEmitBoard`/`recordTaskBaseSHA` и тонкие обёртки `taskWorktree`/
`epicWorktree`/`dropEpicWorktreeLocked` (вызовы `server/gitflow.go`,
`gitflow_ar.go`, `gitflow_sync.go`, `session.go` не менялись). Тесты: новые
герметичные `application/gitflow/lifecycle_test.go` (пропуски не-git/пустого ID/
готового worktree, полный цикл задачи, отсутствие ветки эпика, сбой `git
worktree add`, worktree эпика — существующий/создание, снятие); серверные
`gitflow_epic_worktree_test.go`/`gitflow_submodule_test.go`/
`gitflow_done_guard_test.go`/`gitflow_auto_e2e_test.go` не менялись. Верификация:
канонический набор зелёный (28 пакетов), `-race` по `application/gitflow` чист,
`gofmt` по правкам чист. Доки: `project-map.md`.

**F3s ✅** — политика синхронизации рабочей копии агентов с main вынесена из
`server/ragref.go` в `application/rag/sync.go`: `WorkingCopySyncer` (порт
`SyncSink`) с методом `Sync` — подтягивание `main` в ветку рабочей копии
(пропуск при не-git/совпадении веток, при занятом checkout и грязном дереве;
откат конфликтного merge через `git merge --abort`; признак реального обновления
через сдвиг HEAD), плюс helpers `headSHA`/`abortMerge` над `git.Executor`.
`server/ragref.go` оставил адаптер `syncSink` (лог проекта) и тонкую обёртку
`s.syncAgentBranchWithMain` (сигнатура сохранена — `server/ragref_test.go` не
менялся); `syncAgentBranchAsync`/`afterMainChanged`/`refreshRagIndex*` — в
`server`. Тесты: новые герметичные `application/rag/sync_test.go` (пропуски
пустой базы/ветки/nil repo, занятый checkout, грязное дерево, откат конфликта,
сдвиг HEAD → true, без сдвига → false, сообщение merge). Верификация:
канонический набор зелёный (28 пакетов), `-race` по `application/rag` чист,
`gofmt` по правкам чист. Доки: `project-map.md`.

**F3t ✅** — конкретные фоновые прогоны индексации RAG вынесены из
`server/ragref.go`/`server/ragindex.go` в `application/rag/jobs.go`: `RefJob`
(содержимое ветки по ref: загрузка снапшота, пропуск актуального индекса,
`EnsureCollection` + `IndexProject`), `RefDiffJob` (изменения ветки относительно
базы) и `DirJob` (каталог под заранее определённой веткой/коммитом). Порты —
`RefLoader`/`DiffLoader` (`func`-типы) и `IndexSink` для отчётов. `server` оставил
адаптеры (`loadRefItems`/`loadRefDiff` стали значениями `apprag.RefLoader`/
`apprag.DiffLoader`, `ragIndexSink`) и тонкие обёртки `IndexRefBackground`/
`IndexRefDiffBackground`/`IndexDirBackground`; `orEmpty` удалён (переехал как
`orDash`). Тесты: новые герметичные `application/rag/jobs_test.go` (`RefJob` —
полный прогон/пропуск/ошибка загрузчика, `RefDiffJob`, `DirJob`). Верификация:
канонический набор зелёный (28 пакетов), `-race` по `application/rag` чист,
`gofmt` по правкам чист. Доки: `project-map.md`.

**F3u ✅** — набор сессионных промпт-инъекций вынесен из `server/session.go` в
`domain` (`injectionset.go`): `InjectionSet` (потокобезопасный, под mutex) с
методами `List` (копия), `Add`, `Upsert` (перезапись по ID, пустой ID — ошибка)
и `Remove` (честный признак наличия). `Session` делегирует
`SessionInjections`/`AddSessionInjection`/`UpsertSessionInjection`/
`RemoveSessionInjection` через ленивый `injections()` — нулевая сессия (тесты)
остаётся работоспособной. Тесты: новые герметичные `domain/injectionset_test.go`
(пустой набор, копия, upsert-добавление/перезапись, удаление/отсутствие); серверный
`TestSessionInjectionHotApply` (через `Session`) не менялся. Верификация:
канонический набор зелёный (28 пакетов; один прогон упал на пред-существующем
флаке `TestChatAssistantRejectBranchAfterConfirm` — в изоляции зелёный), `-race`
по `domain` чист, `gofmt` по правкам чист. Доки: `project-map.md`.

**F3v ✅** — правка и презентация конфликтных файлов вынесены из
`server/actions_resolve.go` в `application/gitflow` (`conflictfiles.go`):
`NewConflictView` (отбор файлов с остаточными маркерами + фрагмент для больших
файлов), `ConflictRegion`/`IsConflictMarkerLine`, `ApplyConflictFiles` (запись
`files` — полное содержимое, затем `edits` — точечные замены с проверкой
уникальности `old`), `ConflictSafeRel`/`WithinDir` (безопасность пути), типы
`ConflictEdit`/`ConflictView`, константы `MaxConflictFileBytes`/`MaxConflictFiles`.
`server/actions_resolve.go` оставил алиасы (`type ConflictEdit = gitflow.ConflictEdit`,
`type conflictView = gitflow.ConflictView`) и тонкие обёртки
`newConflictView`/`conflictSafeRel`/`withinDir`, поэтому серверные
`actions_resolve_test.go` не менялись; `conflictApply` зовёт
`gitflow.ApplyConflictFiles`. Тесты: новые герметичные
`application/gitflow/conflictfiles_test.go` (safe-rel/within-dir, truncate
больших файлов, чистые файлы, apply files+edits, отказы: не конфликтный файл,
выход за worktree, неуникальный/пустой `old`, маркеры/регион). Верификация:
канонический набор зелёный (28 пакетов), `-race` по `application/gitflow` чист,
`gofmt` по правкам чист. Доки: `project-map.md`.

**F3w ✅** — детерминированная политика диалога чат-ассистента вынесена из
`server/chatassist.go`/`server/actions.go` в новый пакет `application/chatassist`
(`policy.go`): распознавание согласия (`ConfirmAffirmative`/`BareConfirm` +
`ConfirmTokens`), вопроса (`AsksSomething`), смены темы (`PrevUserDiffers`),
«отчёта о записи на доске» (`LooksLikeBoardWriteClaim` + узкий список фраз),
сводка structured-вопроса (`AskSummary(*chat.Ask)`) и антизацикливающая подсказка
`ActionAnchor(hist []chat.Message, question string)` (вся логика бывшего
`chatActionAnchor`, кроме чтения истории). `server/chatassist.go` оставил
`chatActionAnchor` (читает `chat.Store.History` и делегирует) и тонкие обёртки
`looksLikeBoardWriteClaim`/`askSummary`; `server/actions.go` — обёртку
`confirmAffirmative`; `ActionConfirmed` без изменений. Удалены переехавшие
`asksSomething`/`prevUserDiffers`/`bareConfirm`/`boardWriteClaimPhrases`. Тесты:
новые герметичные `application/chatassist/policy_test.go` (согласие/не-согласие,
bare-согласие, отчёт о записи, вопрос, смена темы, `AskSummary`, якорь:
подтверждение/lf/новая тема/два вопроса/пусто/role=ask); серверные
`actions_test.go`/`chatassist_test.go` не менялись (идут через обёртки).
Верификация: канонический набор зелёный (29 пакетов), `-race` по
`application/chatassist` чист, `gofmt` по правкам чист. Доки: `project-map.md`.

**F3x ✅** — HITL-затвор (подтверждение человеком публикации эпиков/задач)
вынесен из `server/session.go` в новый пакет `application/hitl` (`gate.go`):
`Gate.Wait` (публикует событие затвора + статус `waiting`, блокирует до решения
или отмены контекста, `Reset` по завершении), `Gate.Approve`
(проверка «идёт оркестрация» + совпадение типа, решение в канал), `Gate.State`
(консистентные `gating`/`typ`/`msg`), тип `GateEvent`. Эффекты сессии
инвертированы портом `GateHost` (`Stopped`/`Running`/`BroadcastStatus`/
`PublishGate`). `Session` оставил поля `gate *hitl.Gate` (ленивый `gateOf()`,
нулевая сессия работоспособна), методы-обёртки `Epics`/`Tasks` (вычисляют ids),
`approve` (добавляет префикс проекта к ошибке) и реализацию `GateHost`;
`broadcastStatus`/`broadcastSnapshot`/`setStandby`/`sessionFlags`/
`server/chatassist.go` читают состояние через `gate.State()`; `gateEvent` —
алиас `hitl.GateEvent`. Порядок блокировок: `gate.mu` берётся только на время
смены состояния, хостовые методы зовутся без удержания `gate.mu`. Тесты: новые
герметичные `application/hitl/gate_test.go` (`fakeHost`: публикация+блокировка+
approve, отказ, отмена контекста → `Reset`, остановленная сессия → мгновенный
`context.Canceled` без события, `Approve` без затвора/без оркестрации/чужой тип,
снятие «залежавшегося» решения, `Reset`); серверный
`TestSessionSnapshotReplaysActiveGate` поправлен на `gateOf().State()`.
Верификация: канонический набор зелёный (30 пакетов), `-race` по `application/hitl`
чист, `gofmt` по правкам чист. Доки: `project-map.md`.

**F3y ✅** — read-model доски вынесен из `server/session.go` в новый пакет
`application/board` (`view.go`): `View` (эпики в эффективном порядке очереди
через `infrastructure/board.SortEpics`, задачи, баги, meta), `ViewPage`
(пагинация + полные `Total`-счётчики), `SlicePage[T]`, `TruncateText`, типы
`Snapshot`/`Total`, порт `Store`. Git-статус проекта в снимок НЕ входит
(презентация git-flow завязана на реестр — остаётся на сервере): `boardSnapshot`
сервера встраивает `appboard.Snapshot` и добавляет `Git *gitView`. Тесты: новые
герметичные `application/board/view_test.go` (`fakeStore`); серверные
`boardpage_test.go`/`chatassist_test.go`/`gitflow_mr_test.go` не менялись.
Верификация: канонический набор зелёный (31 пакет; один прогон `./server/` упал
на пред-существующем флаке — на повторе зелёный), `-race` по `application/board`
чист, `gofmt` по правкам чист. Доки: `project-map.md`.

## Ключевые решения

| Решение | Обоснование |
|---|---|
| Создать `domain/` без зависимостей | Чёткая граница предметной области; тестируемость |
| Интерфейсы в `domain/services/` | Зависимости направлены внутрь; лёгкая замена реализаций |
| `runner` как адаптер к `application` | Устранение дублирования логики оркестрации |
| Разбить `tools/` на подпакеты | Уменьшение связанности; независимое тестирование |
| Агенты как конфигурации | Устранение копирования; быстрое добавление новых ролей |
| `checkpoint` хранит модель `Plan` | Устранение непрозрачных байтов; эволюция схемы через миграции |

## Проверка

После каждого этапа:

```bash
go build . ./agents/... ./application/... ./tools/... ./cmd/... ./server/ ./runner/ ./infrastructure/...
go vet . ./agents/... ./application/... ./tools/... ./cmd/... ./server/ ./runner/ ./infrastructure/...
go test . ./agents/... ./application/... ./tools/... ./cmd/... ./server/ ./runner/ ./infrastructure/...
```

Дополнительно:

- `go list -deps ./domain/...` — проверка отсутствия зависимостей от
  проектных пакетов.
- Новые unit-тесты для `domain/` (сущности, интерфейсы).
- Новые unit-тесты для `application/usecases/` (оркестрация).
- Live E2E: `SANDBOX_LIVE=1 go test -run TestLiveSession -v`.

## Риски

- **Объём изменений.** Рефакторинг затрагивает большую часть кодовой базы.
  Рекомендуется выполнять поэтапно, с возможностью отката через git.
- **Обратная совместимость API.** Изменения в `domain/` могут потребовать
  обновления клиентов. Необходимо версионирование API.
- **Производительность.** Введение дополнительных слоёв может добавить
  накладные расходы. Профилирование после каждого этапа.
- **Существующие планы в `wip`.** `PLAN-2026-10-02-wip-enterprise.md` и
  `PLAN-2026-10-07-wip-harness-rework.md` могут конфликтовать. Необходимо
  согласование.

## Остаток работ по Этапу 3

- **Пункт 3 — порт-ориентированный перенос `planner`.** Сделано: перенос пакета
  в `application/planner` (срез A), порты ролевых агентов (срез B), архитектора
  (срез B2) и чистые типы плана в `domain/` (срез D: `AgentType`/`Role`/`Step`/
  `Plan`/`ComputeWaves`, значения `AgentType` канонизированы). Осталось:
  - **Срез C — порт `BoardStore`** (`domain`-типы вместо `*board.Store`): самый
    крупный. Оркестратор делает ~183 обращения к `board.*` в non-test коде
    (11 файлов) плюс тесты. Разбит на под-шаги:
    - **C1 ✅** — `domain.Status` стал авторитетным конечным автоматом доски
      (метки, `Terminal`/`Valid`, `ValidateTransition`, `StatusError`);
      `board.Status`/`board.StatusError` — алиасы, `board.ValidateTransition`
      делегирует. `board.Meta`/`board.StatusCounts` тоже алиасированы на
      `domain`. Тест `domain/status_test.go`.
     - **C2 ✅** — `board.{Comment,Comments,Injection,BugReport,BugStatus,
       BugStatusError}` и константы — алиасы домена; `Comment.Normalize/
       Validate`/`newCommentID`, `Injection.Validate`/`NewInjectionID` (16 байт
       hex) и строгий `DecodeInjections`, `BugStatusError`/`ValidateBugTransition`
       перенесены в `domain`.
     - **C3 (переоценён: перенесён в C6)** — `domain.EpicLess` и
       `domain.UnmarshalTasks` нельзя перенести в `domain` без унификации
       моделей доски: `board.Epic`/`board.Task` встраивают `board.TaskSpec`
       (`SequenceOrder FlexInt`, метки `string`), а `domain.Epic`/`domain.Task`
       — `domain.TaskSpec` (`int`, `time.Time`). Компаратор завязан на
       `*board.Epic`, разбор задач — на `board.TaskSpec`; оба требуют сначала
       свести модели (см. C6).
     - **C4 ✅** — контекст-плумбинг инъекций/комментариев перенесён из `board`
       в `domain` (типы/`NewInjectionContext`/`WithInjectionSource`/
       `WithInjectionScope`/`NewCommentContext`/`WithCommentSource`/
       `AllInjections`/`AllComments`); в `board` — обёртки/алиасы, `runner`/
       `server`/planner продолжают работать без правок.
      - **C5 ✅** — `services.BoardStore` расширен недостающими
        методами (`GetMeta`/`SaveMeta`, `EpicCounts`/`TaskCounts`,
        `MarkBugsFixedForEpic`, `FinalizeTaskTokens`/`FinalizeEpicTokens`,
        `AllDone`); `board.DomainStore` (встраивает `*board.Store`) уже
        удовлетворяет им благодаря алиасам `Meta`/`StatusCounts` — compile-
        assertion `var _ services.BoardStore = (*DomainStore)(nil)` держится.
        `KanbanRunner.store` переведён на интерфейс (вместе с C6).
      - **C6 ✅** — модели доски сведены с доменом: `domain.TaskSpec`/
        `domain.Task`/`domain.Epic` получили `FlexInt`/`FlexBool` и
        string-таймстампы (как `board.*`); конвертеры сводятся к identity;
        `EpicLess`/`EpicPhase`/`SortEpics`/`UnmarshalTasks` перенесены в
        `domain` (`domain/order.go`, `domain/unmarshal.go`); `KanbanRunner.store`
        → `services.BoardStore`; все `board.*`-обращения planner мигрированы
        на `domain.*`/`services.*`; вызовы `NewKanbanRunner` в `server/session.go`
        и `main.go` обёрнуты в `board.NewDomainStore`.
    - **Acceptor-порт ✅**: `executor.go` больше не импортирует
      `agents/acceptor`; отчёт приёмки — `domain.Report`, порт —
      `planner.Acceptor` (`MaxRounds`/`Accept`), адаптер
      `acceptor.PlannerAdapter` подключается в `main.go` и в тестах.
   - **Порт `Models` ✅**: planner больше не импортирует `models`; LLM-порт
     объявлен как `planner.LLMProvider` (только `Generate`), «тяжёлая модель»
     через контекст — `domain.WithHeavyModel`/`domain.HeavyModelFor`
     (`models/force.go` удалён, `models.LLMProvider` структурно совместим).
    - **Порт `Tools` ✅**: planner в non-test коде больше не импортирует `tools`
      (проверено `go list -f '{{join .Imports "\n"}}'` — `ai/tools`/`ai/models`/
      `ai/board`/`ai/runner`/`ai/agents/acceptor` в прямых импортах
      `application/planner` отсутствуют). Интерфейсы —
      `domain/services/tools.go`; адаптеры — `tools/plannerports.go`; точками
      внедрения служат `SetPlannerToolBuilder`/`SetLSPDiagnoser`/
      `SetSnapshotFactory`/`SetPublicAPIAuditor` (`main.go`, тестовый `TestMain`).
      Оставшиеся прямые импорты агентов (`agentfactory.go`/`architectfactory.go`)
      — легальная реализация портов `AgentFactory`, они переедут в
      инфраструктурный пакет в рамках Этапа 4/5.
- **`runner` не «тонкий» по объёму — устранено ✅.** Делегирование было, но
  адаптеры портов (`autofix.go`, `applog.go`, `injections.go`, `taskstate.go`,
  `wipcommit.go`, `reindex.go`) лежали в пакете. Перенесены в
  `runner/adapters/` (см. пункт 2 Этапа 3).
- **Падения тестов `application/planner` (17 шт.) — разобраны ✅.** Тесты
  воспроизводились на коммите `cb8fae2` (до Этапа 3) и не были регрессией
  переноса. Корневая причина: коммит `792f367` (Этап 6) сменил фолбэк
  специалиста `done` → `testing` и добавил `phaseTesting`, но тестовые
  провайдеры эмулируют боевой цикл неявно — они возвращали `tool-call`
  (`BoardSetTaskStatus`), а фейковый провайдер не исполняет вызовы (в бою это
  делает `models.OllamaProvider.Generate` → `usecases.RunAgent` →
  `runner.Generate`). Задача зависала в `testing`, и петля крутилась до 100
  раундов. Что сделано:
  - тестовый хелпер `executeBoardToolCalls` (`application/planner/kanban_test.go`)
    исполняет `Board*`-вызовы фейкового провайдера, подставляя `task_id` из
    промпта задачи, — как боевой цикл; фейковые провайдеры (`kanbanProvider`,
    `queueProvider`, `bugFlowProvider`, `stackProvider`, `askFirstProvider`,
    `autonomyProvider`, `heavyProbeProvider`, `idleProvider`, `tokenEchoProvider`)
    приведены к единой семантике «разработчик → testing, тестировщик → done»;
  - восстановлен пропавший при удалении `agents/qalead` (`6968e2a`) вызов
    `BoardSetBugStatus` в тестовом провайдере триажа и добавлены инструменты
    `BoardSetBugStatus`/`BoardGetBug` в `qaBoardToolNames`
    (`agents/qaengineer/agent.go`) — иначе QA-лид физически не мог подтвердить
    багрепорт (боевой дефект: `bugTriagePrompt` требует `BoardSetBugStatus`, а у
    объединённого QA-агента его не было);
  - закрыт боевой дефект «задача осталась в `testing` после прогона QA»:
    `phaseTesting` больше не считает это «прогрессом» (была бесконечная петля
    QA↔testing до 100 раундов, инцидент FEL-04), а эскалирует
    (`kanban_testing.go` → `escalate` «работа не сделана»); FSM доски расширен
    переходом `testing → ready` (`domain/status.go`) — эскалация возвращает
    задачу в очередь.
  Верификация: весь канонический набор зелёный (в т.ч. `application/planner`),
  `-race` по `application/planner` чист.
- **Ссылки на путь `agents/planner/...` в живых документах — обновлены ✅**
  (`docs/20-features/planner.md`, `kanban-board.md`, `agents-and-roles.md`,
  `architect.md`, `lsp.md`, `docs/00-overview/architecture.md`,
  `how-it-works.md`, `glossary.md`, `docs/30-reference/agent-tools.md`,
  `docs/40-operations/testing.md`, `troubleshooting.md`) — теперь
  `application/planner/...`; исторические `PLAN-*-done-*.md` не переписываются.
  Обнаружена смежная устаревшая ссылка: `acceptor` переехал из
  `agents/acceptor/` в корневой `acceptor/`, а живые документы
  (`docs/20-features/acceptor.md`, `makefile-contract.md`, `lsp.md`,
  `docs/40-operations/testing.md`, `agents-and-roles.md`) всё ещё указывали
  `agents/acceptor/...` — обновлены ✅ (путь заменён на `acceptor/...`).

## Что осталось пользователю

- Решение о сроках и приоритетах этапов.
- Возможность выполнить рефакторинг поэтапно или выбрать только отдельные
  этапы.
- Live E2E после завершения каждого этапа.
