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
| H3 | `gitflow`/`rag`→домен: `MergeResult`/`MergeConflictError`/`Index*`/`SearchParams`→domain, `git.Executor`→порт, `NewClientSafe`→инъекция | Ожидает |
| H6 | `agents`→infrastructure: `board.Store`→`services.BoardStore`, rag/git-типы→домен | Ожидает |
| H5 | Вторичные: `chat.Ask*`→domain, `checkpoint`-порт, `runevents.Reporter`-порт, `workspace`-типы→domain | Ожидает |
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

### H3: gitflow/rag→домен

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

### H6: agents→infrastructure

`architect`/`chatassist`/`developer`: `board.Store` → `services.BoardStore`,
`board.*`-типы → `domain`, rag/git-типы → доменные (появляются в H3).
`agents`→`tools`/`projects`/`logging` остаются (outer→outer / утилиты).

### H5: вторичные application→внешние

- `chat.Ask`/`AskQuestion`/`AskAnswer` → domain (`chat` алиасит).
- `checkpoint.Snapshot`/`Status*` → domain + порт-интерфейс, инъекция из main.
- `runevents.Reporter`-порт → `domain/services` (контекст-хелперы + интерфейс).
- `workspace.Info`/`AddParams`/`Kind` → domain-данные (реестр остаётся за
  портом `OpenRegistry`).
- `usecases`→`tokens`/`agents` закрывается портами из H1/H2.

### H7: уборка и верификация

Удаление `repositories/`; скрипт импортного гейта; канонический набор +
`-race` по новым тестам + `gofmt` по правкам; обновление
`docs/00-overview/project-map.md` и `docs/10-architecture/architecture.md`
(модель слоёв + список допустимых shared-утилит).

## Риски

- Правка тестов `server/` под новую сборку провайдеров (после H1).
- Предсуществующие флаки `TestChatAssistant*` — не путать с регрессами.
- H3 — самый тяжёлый срез (~20 файлов `gitflow`/`rag`).
