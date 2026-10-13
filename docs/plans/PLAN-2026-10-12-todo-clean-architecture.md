# PLAN-2026-10-12 — Рефакторинг по Clean Architecture

**Статус:** Этапы 1–2 завершены. Этап 3: use-cases `RunTask`,
`CompressHistory`, `BreakLoop`, `Escalate` перенесены в
`application/usecases` и покрыты unit-тестами; пункт 3 (перенос
`agents/planner/`) выполнен **частично** — пакет перемещён в
`application/planner`, зависимости от конкретных агентов инвертированы через
порты `AgentFactory`/архитектурные. Остались срезы C (порт `BoardStore`),
D (типы плана в `domain/`) и `acceptor` — см. «Остаток работ» в конце.

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
   `application`). ⏳ `runner.Generate` делегирует в `usecases.RunTask`;
   `runner/compression.go` — адаптер сжатия; оставшиеся файлы (`autofix.go`,
   `applog.go`, `injections.go`, `taskstate.go`, `wipcommit.go`, `reindex.go`)
   реализуют порты-функции (`AutoFixer`, `RuntimeLogger`, `InjectionApplier`,
   `RoundStateReporter`, `WIPCommitter`, `Reindexer`) для инфраструктуры —
   бизнес-логика вынесена, но пакет ещё не «тонкий» по объёму.
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
   - **Осталось (срезы C/D) ❌**:
     - **C** — порт `BoardStore` поверх `domain` вместо `*board.Store`
       (самый крупный: оркестратор работает с `board.Task`/`board.Epic` по
       всему коду; `board.DomainStore` уже реализует `services.BoardStore`).
     - **D** — перенос чистых типов `Plan`/`Step`/`AgentType`/`Role` в `domain/`.
       Заблокировано расхождением: `domain/plan.go` уже определяет эти типы, но
       с ДРУГИМИ значениями `AgentType` (`backend_dev` против `backend`) и без
       поля `Tasks []board.TaskSpec` — нужна аккуратная сверка (влияет на JSON
       чекпоинтов/PLAN.md), отдельная задача.
     - `executor.go` всё ещё импортирует `agents/acceptor` (приёмка — не только
       конструкция агента, а цикл с `acceptor.Report`).

**Критерий:** `runner.Runner` делегирует в `application/usecases/RunTask` ✅
(`runner.Generate` → `usecases.RunTask`); логика из `agents/planner/`
распределена по `application/` и `domain/` — ⏳ `application/planner` создан и
зависимости от конкретных агентов инвертированы через порты; остаточные срезы
C (BoardStore) и D (типы плана в domain) не закрыты.

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
go build . ./agents/... ./tools/ ./board/ ./sandbox/ ./cmd/... ./server/ ./runner/ ./gitops/
go vet . ./agents/... ./tools/ ./board/ ./sandbox/ ./cmd/... ./server/ ./runner/ ./gitops/
go test . ./agents/... ./tools/ ./board/ ./sandbox/ ./cmd/... ./server/ ./runner/ ./gitops/
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
  в `application/planner` (срез A), порты ролевых агентов (срез B) и
  архитектора (срез B2). Осталось:
  - **Срез C — порт `BoardStore`** (`domain`-типы вместо `*board.Store`):
    оркестратор работает с `board.Task`/`board.Epic` по всему коду;
    `board.DomainStore` уже реализует `services.BoardStore`. Самый крупный срез.
  - **Срез D — `Plan`/`Step`/`AgentType`/`Role` в `domain/`**: тип `domain.Plan`
    уже существует, но значения `AgentType` расходятся (`backend_dev` против
    `backend`) и нет поля `Tasks []board.TaskSpec` — нужна сверка (влияет на
    JSON чекпоинтов/PLAN.md).
  - **`agents/acceptor`** в `executor.go`: приёмка — цикл с `acceptor.Report`,
    а не только конструкция агента.
  - **Порты `Runner`/`Models`/`Tools`**: planner по-прежнему зависит от
    `runner` и `models`/`tools` напрямую (LLM-исполнение, учёт токенов,
    project-map/RAG).
- **`runner` не «тонкий» по объёму.** Делегирование есть, но адаптеры портов
  (`autofix.go`, `applog.go`, `injections.go`, `taskstate.go`, `wipcommit.go`,
  `reindex.go`) остаются в пакете.
- **Предсуществующие падения тестов `application/planner`** (17 шт., включая
  `TestGuardRejectionEscalatesWithinBudget`, `TestPhantomDoneNotClosedByFallback`,
  `TestTokenAccountingEndToEnd`): воспроизводятся на коммите `cb8fae2` до
  Этапа 3 и не изменились после срезов A/B/B2 (проверено по счётчику) — это не
  регрессия переноса. Разобрать отдельно; эскалация и `RunTask` покрыты
  unit-тестами в `application/usecases` (`escalate_test.go`, `run_test.go`),
  выбор агента — `application/planner/agentfactory_test.go`, все зелёные.
- **Обновить ссылки на путь** `agents/planner/...` в живых документах
  (`docs/20-features/*`, `docs/00-overview/*`) — они указывают на
  `application/planner/...`; исторические `PLAN-*-done-*.md` не переписываются.

## Что осталось пользователю

- Решение о сроках и приоритетах этапов.
- Возможность выполнить рефакторинг поэтапно или выбрать только отдельные
  этапы.
- Live E2E после завершения каждого этапа.
