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
(`infrastructure/llm/ports.go`). Разбивка этапа на срезы E1–E6 — в разделе
«Этап 4» ниже.

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
infrastructure/   — реализации (models, rag, gitops, sandbox, board)
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
| **E2** | `rag` → `infrastructure/rag` + assertion `services.RAGClient` | ⏳ |
| **E3** | `gitops` → `infrastructure/git` + assertion `services.GitOperations` | ⏳ |
| **E4** | `sandbox` → `infrastructure/sandbox` + assertion `services.SandboxEngine` | ⏳ |
| **E5** | `board` → `infrastructure/board` (самый крупный: ~75 импортёров) | ⏳ |
| **E6** | Разбить `tools/` по доменам (`tools/lsp`, fileops/git/rag/board-подпакеты) и перевести их на `domain/services` вместо прямых импортов `ai/board`/`ai/rag`/`ai/sandbox`/`ai/gitops` | ⏳ |

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
go build . ./agents/... ./application/... ./tools/ ./board/ ./sandbox/ ./cmd/... ./server/ ./runner/ ./gitops/ ./infrastructure/...
go vet . ./agents/... ./application/... ./tools/ ./board/ ./sandbox/ ./cmd/... ./server/ ./runner/ ./gitops/ ./infrastructure/...
go test . ./agents/... ./application/... ./tools/ ./board/ ./sandbox/ ./cmd/... ./server/ ./runner/ ./gitops/ ./infrastructure/...
```

Дополнительно:

- `go list -deps ./domain/...` — проверка отсутствия зависимостей от
  проектных пакетов.
- Новые unit-тесты для `domain/` (сущности, интерфейсы).
- Новые unit-тесты для `application/usecases/` (оркестрация).
- Live E2E: `SANDBOX_LIVE=1 go test ./sandbox/ -run TestLiveSession -v`.

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
