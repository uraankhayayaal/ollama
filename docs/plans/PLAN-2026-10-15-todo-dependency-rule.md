# PLAN-2026-10-15: Dependency Rule (Этап 8)

Продолжение `PLAN-2026-10-12-todo-clean-architecture.md` после Этапов 1–7.
Кодовая база разложена по пакетам (domain/application/infrastructure), крупные
файлы разрезаны, но **направления зависимостей** местами не совпадают с
Dependency Rule: внутренние слои импортируют внешние, а инфраструктура —
высокоуровневый код. Этап 8 устраняет эти связи, не меняя поведения.

## Правила слоёв (целевая модель)

| Слой | Может импортировать |
|---|---|
| `domain` (+ `domain/services` — порты) | только stdlib и сторонние либы |
| `application` | только `domain` |
| `infrastructure` | только `domain` (реализация портов) |
| Внешние: `server`, `main`, `runner`, `agents`, `tools`, `runctx` | всё ниже; outer→outer между собой допустим (решение E6e1) |
| Shared-утилиты: `logging`, `projects`, `tokens`, `langdetect` | допустимы для всех слоёв (фиксируется в доках) |

## Проблема (нарушения, найденные разведкой)

| # | Направление | Где | Тяжесть |
|---|---|---|---|
| 1 | `infrastructure/llm` → `application/usecases`, `ai/agents`, `ai/tools` | `Generate` = полный агентский цикл через `usecases.RunAgent`; `tools.ToOllama`/`ParseArguments`; `agents.Agent` в сигнатуре | ❌ обратная зависимость — единственное место, где infrastructure импортирует outer |
| 2 | `application` → `infrastructure` | `gitflow`/`rag` → `git.*` (13+ файлов), `board.*` (алиасы) в `application/board`, `chatassist`, `tokens`, `injections`, `usecases`; `planner` → `rag.NewClientSafe` | ❌ |
| 3 | `application/planner` → конкретные `agents/*` | `agentfactory.go`/`architectfactory.go` (фабрики внутри use-case-слоя) | ❌ |
| 4 | `agents/*` → `infrastructure` | `architect`/`chatassist`/`developer` → `board`/`rag`/`git` | ❌ (решение: чинить) |
| 5 | `application` → внешние пакеты | `ask`→`chat`, `planner`→`checkpoint`/`runevents`/`forges`, `gitflow`→`workspace`, `usecases`→`tokens` | ❌ вторичные |
| 6 | `repositories/` | мёртвый пакет, 0 импортёров | удалить |

## Критерий

Поведение и публичный API не меняются (кроме удаления мёртвого
`GetToolsForOllama`). Канонический набор (`go build`/`go vet`/`go test
-count=1`) зелёный, `gofmt` по правкам чист. Импортный гейт (скрипт на
`go list`): `domain` чист от проектных пакетов; `application` — без
`infrastructure`/`agents`/`tools`; `infrastructure` — без
`application`/`agents`/`tools`.

## Решения (согласованы с пользователем)

- **(a)** `Generate` убирается из провайдеров: провайдеры = чистые
  `ChatOnce`/`ChatStream`; полный цикл — `usecases.RunAgent`; эскалация
  small→large переезжает в application-декоратор.
- **Объём максимум**: всё, включая `agents`→infrastructure и вторичные
  application→chat/checkpoint/runevents/workspace.
- `repositories/` удалить.

## Срезы (порядок выполнения)

| Срез | Содержимое | Статус |
|---|---|---|
| H2 | Алиасы `board`→`domain` в `application/board`, `application/chatassist`, `tokens`, `injections`, `usecases` | ✅ |
| H1 | `infrastructure/llm` чистый: порты→`domain/services`, убрать `Generate`, декоратор эскалации, `ToOllama`→llm, удалить `GetToolsForOllama` | ✅ |
| H4 | `planner`→конкретные agents: фабрики выносятся в `agents/factory`, подключение через `SetAgentFactory` в `main` | ✅ |
| H3 | `gitflow`/`rag`→домен: данные git-слияния и rag-типы→domain, `git.Executor`→порт, `NewClientSafe`→инъекция | разбит на H3a–H3c |
| H3a | git-данные: `MergeResult`/`MergeFeatureOptions`/`MergeConflictError`/`SanitizeBranchName`/`TaskBranchPrefix`/`RedactSecrets`/`ErrWorkflowScope`→`domain` (+alias в `git`) | ✅ |
| H3b | `rag`→домен: `Index*`/`SearchParams`/`SearchResult`/`IsIgnoredDir`→domain, `git.Executor`→порт, `NewClientSafe`→инъекция | ✅ |
| H3c | `gitflow` прямые git-операции (`Clone`/`HasCommit`/`RepoFromState`/`Worktree`/`EnsureSubmodules`/`HasConflictMarkers`/`*git.Repo`)→ хост-порты | ✅ |
| H6 | `agents`→infrastructure: `board.Store`→`services.BoardStore`, rag/git-типы→домен | разбит на H6a–H6b |
| H6a | `architect`/`chatassist`: `board.*`/rag-типы→`domain`, `Config`/`LoadConfig`→`infrastructure/board/storeconfig.go` | ✅ |
| H6b | `developer`/`factory`: `board.*`→`domain`, git-WIP и RAG-переиндексация→`runner/adapters`, `rag.Searcher`→`services.Searcher` | ✅ |
| H5 | Вторичные: `chat.Ask*`→domain, `checkpoint`-порт, `runevents.Reporter`-порт, `workspace`-типы→domain | разбит на H5a–H5e |
| H5a | `agents.Agent`→`services.Agent` (алиас) + planner без импорта `ai/agents` | ✅ |
| H5b | `chat.Ask*`/`chat.Message`/`chat.Role*`→domain (`chat` алиасит) | ✅ |
| H5c | `checkpoint`-порт (`planner` без `ai/checkpoint`) | ✅ |
| H5d | `runevents.Reporter`-порт (`planner` без `ai/runevents`) | ✅ |
| H5e | `workspace.Info`/`AddParams`/`Kind`→domain (`gitflow` без `ai/workspace`) | Ожидает |
| H7 | Удалить `repositories/`, импортный гейт, финальная верификация, доки | Ожидает |

### H2: алиасы board→domain

`iboard.*` и большая часть `board.*` в application-пакетах — это алиасы
`domain` (Epic/Task/TaskSpec/Meta/BugReport/Status*/SortEpics/Injection*/…).
Срез чисто переключает импорты на `ai/domain`; поведение не меняется.

### H1: infrastructure/llm чистый

1. `ChatProvider`/`StreamChatProvider`/`ModelLimitsProvider`, `ResumeState`
   + контекст-хелперы → `domain/services` (в `usecases` — алиасы).
2. Удалить `Generate` у `*OllamaProvider`/`*OpenAIProvider`/`*LayeredProvider`
   и интерфейс `llm.LLMProvider`; `ResolveProvider` возвращает small/large
   чат-провайдеров.
3. Эскалация Layered (small→large, retry с `WithoutResumeState`) →
   `usecases.Escalating` (полный цикл через `RunAgent`).
4. Потребители подключаются в composition root: `main.go` (3),
   `server` (8 мест типа + 4 вызова), `application/planner` (порт
   `LLMProvider`), `application/gitflow/resolve.go`.
5. `tools.ToOllama` → `infrastructure/llm` (конвертация в ollama api —
   инфраструктурная деталь); удалить мёртвый `GetToolsForOllama`
   (0 вызовов) из `agents.Agent` и реализаций; `tools.ParseArguments` →
   `domain`; `tools.ToolCall` → `domain.ToolCall`.
6. `agents.Agent` в сигнатурах портов → доменный порт `services.Agent`
   (структурно удовлетворяет `agents.Agent`).

**Выполнено (H1):** `domain/services/llm.go` получил порты
`ChatProvider`/`StreamChatProvider`/`ModelLimitsProvider`; новый
`domain/services/agent.go` — `Agent`/`AgentRunner`/`AgentCycle`. `usecases/ports.go`
переведён на алиасы (`ChatProvider`/`StreamChatProvider`/`ModelLimitsProvider`/
`Agent`/`AgentRunner`) без импорта `ai/agents`; новый
`application/usecases/provider.go` — `FullCycle = services.AgentCycle`,
`NewCycle`/`NewEscalating` + перенесённая heavy-логика
(`NeedsHeavyModel`/`agentName`/`llmAlwaysHeavy`/`heavyForContext`), эскалация
повторяет слой large с `WithoutResumeState`. `infrastructure/llm`: удалены
`LLMProvider.go`/`layered.go` (и `layered_test.go` → `usecases/provider_test.go`
на фейковом `SetAgentRunner`), `Generate` у `*OllamaProvider`/`*OpenAIProvider`,
`Resolved{Small,Large services.ChatProvider}`, `ResolveProvider() (*Resolved,…)`;
`ToOllama` переехал в `infrastructure/llm/toollama.go`, `ParseArguments` — в
`domain/toolargs.go` (в `tools` — тонкая обёртка + `unmarshalLikeModel` на
`domain.RelaxJSON`); llm берёт `domain.ToolCall`/`domain.ParseArguments`. Удалён
мёртвый `GetToolsForOllama` из `agents.Agent` и всех реализаций (+ планистские
`SetOllamaToolConverter`/`ollamaToolConverter`). Потребители: `server/resolve.go`
(`get()`→`usecases.FullCycle`, кэш `cycle`), `server/providers.go`, `session.go`,
`chatassist.go`, `gitflow_ar.go`, `main.go`, порт `planner.LLMProvider` и
`gitflow.Developer` → `services.AgentCycle`/`services.Agent`; `runner`-адаптер —
на `services.AgentRunner`. `repositories/` (мёртвый) удалён досрочно (ломало
`go build ./...`). Верификация: канонический набор зелёный, `infrastructure/llm`
тянет только `domain`/`domain/services`/`logging`. Коммит — по запросу.

### H4: planner→конкретные agents

`roleAgentFactory`/`infraAgentFactory`/`SpecialistForRole` выносятся во
внешний пакет `agents/factory`; `planner` оставляет только порт
`AgentFactory` и подключение через существующий `SetAgentFactory`
(сборка — в `main.go`; `server/gitflow_ar.go` зовёт фабрику напрямую).

**Выполнено (H4):** конкретные `roleAgentFactory`/`architectFactory`/
`infraAgentFactory` переехали в новый пакет `agents/factory` (`Factory`/`New`,
`roleFactory` + `architectFactory`); `agents/factory` не импортирует `planner`
(метод `Step` порта принимает доменный `domain.AgentType` вместо `*planner.Step`,
поэтому порт плана и реализация связаны только структурно). В `planner` остались
порт `AgentFactory`, `SetAgentFactory`/`agentFactory()` и noop-заглушка по
умолчанию (возвращает nil — запуск без установленной фабрики теперь явная ошибка
сборки, а не молчаливый дефолт). Установка — в composition root: `main.go`
(`planner.SetAgentFactory(factory.New())`), `application/planner/testmain_test.go`,
новый `server/testmain_test.go` (серверный gitflow-резолвер зовёт
`planner.SpecialistForRole`). Тесты фабрики переехали в
`agents/factory/factory_test.go`. Верификация: канонический набор зелёный;
`go list -deps ./application/planner/` больше не содержит `ai/agents/*`
(осталась только базовая `ai/agents` с типами `Agent`/`Message` — вне H4);
`agents/factory` не тянет `application/planner` (цикла нет).

### H3: gitflow/rag→домен (срезы H3a–H3c)

1. Типы данных → domain: `MergeResult`, `MergeConflictError`,
   `MergeFeatureOptions`, `IndexItem`/`IndexResult`/`IndexOptions`,
   `SearchParams`/`SearchResult`, `SanitizeBranchName`, `RedactSecrets`,
   `forges.IsIgnoredDir`.
2. `git.Executor` → порт `services.Executor` (`*git.Repo` реализует
   напрямую, по образцу E3).
3. Прямые вызовы `git.Clone`/`RepoFromState`/`rag.NewClientSafe` →
   существующие хост-порты или func-инъекция, сборка в `main`.
4. Порты gitflow с git-типами в сигнатуре становятся доменными —
   вызывающий код (server) почти не меняется.

**Выполнено (H3b):** RAG-типы и политика обхода вынесены в домен. Новые
`domain/rag.go` (`MainBranch`, `DefaultMaxResults`/`DefaultSearchMaxTotal`,
`IndexOptions`(+`BranchOrDefault`/`Commit`), `IndexItem`, `IndexResult`,
`SearchResult`, `SearchParams`, `ProjectInfo`(+`Indexed`), `ScopeForPath`) и
`domain/ignore.go` (`IsIgnoredDir` — общий список служебных каталогов).
`domain/services`: добавлен порт `Searcher` (`Ping`/`Search`/`ProjectInfo`) и
узкий `Executor`. `infrastructure/rag/domain_alias.go` даёт алиасы
(`rag.IndexItem = domain.IndexItem`, `rag.Searcher = services.Searcher`,
`rag.ScopeForPath = domain.ScopeForPath` и т.п.), `searcher.go` удалён;
`forges.IsIgnoredDir` делегирует `domain.IsIgnoredDir`; `git.Executor` стал
алиасом `services.Executor`. `application/rag` больше не импортирует
infrastructure: `background`/`index`/`jobs`/`ref`/`sync` переведены на
`domain`/`services`; `Sync` принимает узкий `SyncRepo` + `SyncProject.Root/
Checkout`; обходчик проекта инъектируется (`SetProjectWalker`, по умолчанию —
фабрика из `main.go`, тесты — `TestMain`). `application/planner` переведён на
`domain`-типы поиска, `rag.Searcher`→`services.Searcher`, `forges.IsIgnoredDir`→
`domain.IsIgnoredDir`, а конструкция клиента — инъекцией
(`SetPlannerRAGSearcher` из `main.go`). Wiring: `main.go` ставит
`planner.SetPlannerRAGSearcher` и `apprag.SetProjectWalker(rag.WalkProject)`,
`server/testmain_test.go` — обходчик. Верификация: канонический набор +
`./domain/`/`./infrastructure/` зелёные (флак `TestChatAssistant*` — предсуществующий),
`-race` по `application/rag`/`domain` чист, `gofmt` по правкам чист,
`go list -deps ./application/rag/` без `ai/infrastructure*`/`ai/agents`/`ai/forges`.

**Выполнено (H3a):** git-данные вынесены в `domain/gitmerge.go`
(`MergeResult`, `MergeConflictError`+`Error()`, `MergeFeatureOptions`,
`TaskBranchPrefix`, `SanitizeBranchName`, `ErrWorkflowScope`, `RedactSecrets`
с приватными regexp-хелперами). `infrastructure/git/domain_alias.go` даёт
алиасы (`git.MergeResult = domain.MergeResult` и т.п.), поэтому реализация git
и outer-слой (server/tools) не менялись. `application/gitflow` переведён на
`domain.*`: исчез импорт `ai/infrastructure/git` в `branches.go`, `release.go`,
`sync.go`, `conflict.go`, `worktree.go` (и в тестах `conflict/release/sync/
worktree_test.go`); во всех этих файлах портовые сигнатуры типизированы
доменными типами. Остались импортировать git только `conflictfiles.go`
(`HasConflictMarkers`), `lifecycle.go` (`*git.Repo`/`EnsureSubmodules`/
`Worktree`/`RepoFromState`) и `open.go` (`Clone`/`HasCommit`/`Executor`) —
это H3c. Верификация: канонический набор + `./domain/`/`./infrastructure/git/`
зелёные, `gofmt` по правкам чист.

Остаток: **H3c** (gitflow прямые git-операции: `conflictfiles.go`/
`lifecycle.go`/`open.go`).

**Выполнено (H3c):** `application/gitflow` больше не импортирует
`ai/infrastructure/git` (`go list -deps` чист от `ai/infrastructure*`; прямой
импорт остался только `ai/workspace` — H5). Новый файл
`application/gitflow/worktreerepo.go` объявляет порты жизненного цикла worktree
(`WorktreeRepo` с `Root()`/`WorktreeList`/`RemoveWorktree`/`Rebase`/`Checkout`/
`AddWorktree`+`WorktreeEntry`, тип `Submodule`). `lifecycle.go`:
`WorktreeHost.Repo` возвращает `WorktreeRepo` (не `*git.Repo`), вместо `Exec()`
порт получил `EnsureSubmodules`/`SubmoduleBase`/`DeinitSubmodule`/
`SubmoduleWorktree`/`CommitWorktree`; `git.SanitizeBranchName`→
`domain.SanitizeBranchName`. `conflictfiles.go`: `git.HasConflictMarkers`
заменён локальным `pendingConflictFiles`+`isConflictMarkerLine` (точные маркеры
git без `|||||||`). `open.go`: новые типы `ClonedRepo`/`ClonedSubmodule` и порт
`GitOpener` (`Exec`/`Clone`/`HasCommit`); `ProjectOpener.Host` вместо
`Exec git.Executor`. Адаптеры на сервере: `serverWorktreeRepo` (в
`server/gitflow_auto.go`, обёртка `*git.Repo`; `worktreeHost` реализует новые
методы через `git.EnsureSubmodules`/`git.Worktree`/`git.RepoFromState`) и
`serverGitOpener` (в `server/git.go`, поверх `git.Clone`/`git.HasCommit`).
Тесты `application/gitflow/{lifecycle,open}_test.go` получили локальные
`fakeWTRepo`/`fakeGitOpener` (делегируют реальным git-функциям над
`fakeExec`), поведение и проверки сохранены. Верификация: канонический набор
зелёный (единственное падение — пред-существующий флак
`TestChatAssistantRejectBranchAfterConfirm`, в изоляции зелёный), `-race` по
`application/gitflow` чист, `gofmt` по правкам чист.

### H6: agents→infrastructure

`architect`/`chatassist`/`developer`: `board.Store` → `services.BoardStore`,
`board.*`-типы → `domain`, rag/git-типы → доменные (появляются в H3).
`agents`→`tools`/`projects`/`logging` остаются (outer→outer / утилиты).

Срез **H6a** (`9f72a9e`): `architect`/`chatassist` отвязаны от
`ai/infrastructure/{board,rag}`. `board.Config`/`LoadConfig`/`StoreConfig` и
env-хелперы перенесены из `agents/architect/config.go` в
`infrastructure/board/storeconfig.go` (`agents/architect/config.go` удалён);
`agents/architect` использует `domain.*` (в т.ч. `UnmarshalBacklog`/`Epic`/
`ErrExists`/`Opportunity`) и `services.Searcher`; мёртвый `NewArchitect`
(строил Redis-store) удалён. Callers `architect.LoadConfig()` →
`board.LoadConfig()` (`main.go`, `server/*.go`, тесты).

Срез **H6b**: `agents/developer` и `agents/factory` отвязаны от
`ai/infrastructure/*` (во всех агентах прямых infra-импортов не осталось).
`developer`: `board.Task`/`board.TaskCheckpoint`→`domain.*`; git-WIP-коммит
(`RepoFromState`/`CLIExecutor`/`ListSubmodules`/`InTaskBranch`/`CommitIfDirty`,
кэш сабмодулей) и RAG-переиндексация (`rag.ScopeForPath`→`domain.ScopeForPath`,
`rag.DetectIndexOptions`, сборка клиента) вынесены в `runner/adapters`
(`WipRepo.CommitWipRound`, `ReindexTouched`, `NewRAGClient`, а также порт
`RAGClient{Searcher;ReindexClient}`); агент только делегирует, поле
`RAG` имеет тип `adapters.RAGClient`, git-логика — в адаптере-пакете
(разрешено: `runner/adapters` — outer/адаптеры). `factory.ConfigureArchitect`
принимает `services.Searcher` (было `rag.Searcher`-алиас).

### H5: вторичные application→внешние

Срез **H5a**: `agents.Agent` стал алиасом доменного порта `services.Agent`
(методы структурно совпадают: `[]domain.AgentMessage`/`[]domain.ToolDefinition`,
т.к. `tools.ToolDefinition = domain.ToolDefinition`); `agents/AgentDefinition.go`
больше не импортирует `ai/tools`. `application/planner` переведён с
`agents.{Agent,Message,MessageType*}` на `services.Agent` и
`domain.{AgentMessage,MessageType*}` (включая `kanban_testing.go`); все
application-пакеты теперь без не-тестовых импортов `ai/{agents,tools,infrastructure}`.
Остаётся `checkpoint`/`runevents` (H5c/H5d) и `chat`/`workspace` (H5b/H5e).

Срез **H5b**: данные диалога Web UI вынесены в домен (`domain/dialog.go`):
`ChatRole` (+`ChatRole*`), `ChatMessage`, `AskKind` (+`AskSingle`/`AskMulti`),
`Ask`, `AskQuestion`, `AskOption`, `AskAnswer` (префикс `Chat` для ролей/
сообщений — чтобы не сталкиваться с `domain.Role`/`domain.Message`). `chat`
алиасит все эти типы и константы (`chat.Role`/`chat.Message`/`chat.Ask*`
сохранены), метод `Message.fields` стал свободной `messageFields` (метод на
алиас-типе определить нельзя). `application/{ask,chatassist}` и их тесты
переведены на `domain.*` — `application` больше не импортирует `ai/chat`.

Срез **H5c**: чекпоинт плана (снимок, статусы шагов) вынесен в домен
(`domain/checkpoint.go`): `CheckpointSnapshot`, нетипизированные
`Checkpoint{Pending,Running,Done,Failed,Skipped}` и
`ErrCheckpointNotFound` (статусы непроизводны, чтобы не конфликтовать с
существующим `domain.StatusDone` таска). `checkpoint` алиасит их
(`Snapshot`/`Status*`/`ErrNotFound`), `*checkpoint.Store` реализует новый
порт `planner.CheckpointStore` (`Save`/`Load`/`MarkStep`/`SaveRoundState`/
`ClearRoundState`/`TrackMetric`), который инжектится из `main` через
`SetCheckpoint(store, resume)`. `application/planner` больше не импортирует
`ai/checkpoint`; `main` остаётся composition root (строит `checkpoint.Store`
и передаёт в порт). Поведение не менялось — это чистый перенос типов и
подмена конкретного типа на порт.

Срез **H5d**: репортёр событий агентского цикла вынесен в доменный порт
(`domain/services/reporter.go`): интерфейс `Reporter` (`WithAgent`/`WithScope`/
`OnMessage`/`OnMessageDelta`/`OnToolStart`/`OnToolResult`/`OnTokens`/
`OnAppLog`) + контекст-хелперы `WithReporter`/`ReporterFromContext`/`WithScope`
(ключ контекста живёт в домене, чтобы сервер и планировщик делили его).
`runevents.Router` реализует порт: `WithAgent`/`WithScope` теперь возвращают
`services.Reporter`, а контекст-хелперы пакета делегируют в `domain/services`.
`application/{planner,tokens}` переведены на `services.*` — `planner` больше не
импортирует `ai/runevents`. Поведение не менялось (репортёр по-прежнему кладёт
в контекст сервер и читает оркестратор; `runner` читает `services.Reporter`
и присваивает в `usecases.Reporter`).

Осталось по H5:
- `workspace.Info`/`AddParams`/`Kind` → domain-данные (реестр остаётся за
  портом `OpenRegistry`).

### H7: уборка и верификация

Удаление `repositories/`; скрипт импортного гейта; канонический набор +
`-race` по новым тестам + `gofmt` по правкам; обновление
`docs/00-overview/project-map.md` и `docs/10-architecture/architecture.md`
(модель слоёв + список допустимых shared-утилит).

## Риски

- Правка тестов `server/` под новую сборку провайдеров (после H1).
- Предсуществующие флаки `TestChatAssistant*` — не путать с регрессами.
- H3 — самый тяжёлый срез (~20 файлов `gitflow`/`rag`).
