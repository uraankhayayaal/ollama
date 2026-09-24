# AGENTS.md — правила для агентов (opencode / помощники)

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
go build . ./agents/... ./tools/ ./board/
go vet . ./agents/... ./tools/ ./board/
go test . ./agents/... ./tools/ ./board/
```

Известные нюансы:
- `go build ./...` падает на `temp/moon-distance/internal/middleware/cors.go`
  (артефакт сгенерированного проекта) — используй перечень выше.
- Предсуществующие неформатированные файлы (`agents/acceptor/checks.go`,
  `agents/acceptor/run.go`) не трогать.

Состояние последней сессии (PLAN-2026-09-24-done-architect-intelligence, Ф-1..Ф-8, остался ручной E2E):
Ф-4 «Корректность задачи, паттерны, AskUser» — архитектор получил
`KanbanRunner.SetRAG` (Р-6) и `KanbanRunner.SetArchitectExtras` (Р-5),
применяемые в `phaseArchitect`/`phaseArchitectReview`/`phaseBugs` через
`prepareArchitect` (kanban.go); `sess.start` передаёт
`SetRAG(ragClient)` + `SetArchitectExtras(&askTool{b: sess}, newIndexBackgroundTool(sess))`
(server/session.go:235); CLI-канбан (main.go) — только `SetRAG(ragClient)`
(автономно, без AskUser/IndexBackground — degrade). Промпт архитектора —
секции «КОРРЕКТНОСТЬ ЗАДАЧИ (спрашивать, а не угадывать)» (AskUser с
`recommended=true` до публикации бэклога, иначе явные допущения в
`architecture_summary`), «ПАТТЕРНЫ ПРОЕКТИРОВАНИЯ» (REST/12-factor/KISS,
анти-GraphQL/devcontainer), «RAG-ИНДЕКС (предложи построить в фоне)»
(RagIndexStatus → AskUser → IndexBackground, продолжать проектирование).
Runner: `RequiredToolFirstRound` трактуется как группа обязательных
инструментов (предварительные чтения/AskUser разрешены) — правка только
doc-комментариев.

Ф-5 «Фоновая индексация RAG» — `server/ragindex.go`: мост
`IndexBackground` (безопасный, без подтверждения) в `ActionsBackend` +
`Session.IndexBackground` — горутина (walk + `IndexProject`, single-flight
через `sess.indexing`, отчёт в `chat.RoleStatus`/лог проекта), идемпотентный
прогон (`IndexProject` сперва очищает точки проекта). Фоновая индексация не
блокирует агентский цикл; клиент RAG создаётся фабрикой `buildProjectIndexer`
(замена для hermetic-тестов). `npm run build` web/ зелёный; `go test
./server/` — неизвестно 2 флаки чат-ассистента
(`TestChatAssistantCreatesBugAndTask`, `TestChatAssistantDeleteTaskAfterConfirm`),
падают и на чистой базе (не связаны с Ф-4/Ф-5). Опциональный нюанс Ф-5
сделан: REST `POST /api/projects/{id}/index` (`handleProjectIndex`,
server/server.go — 409 при идущей, 503 при недоступном RAG) + кнопка «Индекс
RAG» в `web/src/App.tsx` (head-actions); тесты `TestRESTProjectIndex` /
`TestRESTProjectIndexRAGUnavailable`.

Ф-6 «Кросс-функциональные инсайты» — `board/entity.go`: тип
`Opportunity{TargetRole, Suggestion}` + `Backlog.Opportunities []Opportunity`
(`json:"opportunities,omitempty"`). `agents/architect/agent.go`: опциональное
поле `opportunities` в схеме `submit_architecture_backlog`; `submitBacklog`
валидирует записи (непустые target_role+suggestion) — грязные деградируют в
`skipped_opportunities`, валидные складываются в `Summary` эпиков секцией
«КРОСС-ФУНКЦИОНАЛЬНЫЕ ВОЗМОЖНОСТИ (рекомендации смежным направлениям)»;
промпт — секция «КРОСС-ФУНКЦИОНАЛЬНЫЕ ВОЗМОЖНОСТИ (opportunities)» (в
основной фазе) и пометка «opportunities: <роль> — <предложение>» в описании
эпика исправления (экспертиза багов). Тесты: парсинг + round-trip в Summary
(`agents/architect/agent_test.go`, `board/entity_test.go`), опциональность
(старые вызовы без поля валидны), скип грязных записей.

Ф-7 «Верификация и полировка»: `go build/vet` по всем пакетам
(./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/) — зелёные;
`go test` — зелёные, кроме 2 пред-существующих флаков aссистента
(`TestChatAssistantCreatesBugAndTask`, `TestChatAssistantDeleteTaskAfterConfirm`,
падают и на чистой базе); `npm run build` web/ зелёный. Остался ручной E2E
на реальном проекте (Web UI, инфраструктура Redis/Qdrant/модель у
пользователя): новая задача → архитектор поднимает RAG-индекс в фоне по
согласию, декомпозирует с учётом стека и ролей; консольный проект — без
Frontend Lead. Далее по плану — Ф-8 и следующие фазы уже реализованы
(Ф-8 в списке: эпики из чата — ревизия архитектора).

Состояние последней сессии (PLAN-2026-09-24-done-makefile, Ф-1..Ф-5 готовы,
остался ручной E2E): корневой Makefile проекта (temp/<проект>/Makefile) —
единая точка входа команд субагентов и приёмки. Ф-1: `tools/stacktool.go`
`StackInfo.Makefile (json:"makefile")` + маркер «Makefile»; секция «MAKEFILE
ПРОЕКТА» в `architectureSystemPrompt` (обязательный эпик «Makefile проекта»,
эталонный контракт целей, assigned_role Backend Lead/DevOps Lead) + п.8
`bugExpertSystemPrompt`/п.7 `epicReviewSystemPrompt`. Ф-2: `developer.go:264`
п.5 (сначала ReadFiles Makefile → `make backend-*`/`make frontend-*`), QA
(`make test`/`make e2e`, приёмка `make build`/`make lint`), лиды backend/
frontend/devops/qa — «ЦЕЛЬ ПРОВЕРКИ ИЗ MAKEFILE» (devopslead — «ИНФРА-БЛОК
ЧЕРЕЗ MAKEFILE»). Ф-3: `acceptor/detect.go` `makefileLocate(root,dir)` (поиск
от подпроекта до корня приёмки) + `makefileTargets` (парсер целей, multi-target
`build test:`, пропуск `:=`, `.PHONY`, `%`, с переменными) + `makeCommand`/
`makeAnalyzeCommand` (test→lint)/`makeInfraMirror`; приоритет env ACCEPT_* →
make-цель → автодетект kind; make-команды исполняются в mkDir (Makefile);
фолбэк `make infra.<цель>` при недоступном инструменте хоста (make-специфичный
маркер «Ошибка/Error 127» в `toolMissing`, accept.go — в checks.go/run.go
правок нет); для run инфра-зеркало НЕ применяется. Ф-4: `devops/agent.go` п.5-6
(инфра-блок: up/down/logs/ps, зеркальные infra.<цель>, самозавершающийся e2e),
`fileops.go:991` missingToolHint → «make infra.<цель>». Верификация зелёная
(плюс 3 пред-существующих флака чат-ассистента на чистой HEAD:
`TestChatAssistantCreatesBugAndTask`, `TestChatAssistantDeleteTaskAfterConfirm`,
`TestChatAssistantPublishesBoardWhenIdle`).

Состояние после PLAN-2026-09-24-done-merge-conflict-board (Ф-1..Ф-5, остался
ручной E2E): конфликты мёрджа стали видимы на доске. Ф-1: `board/entity.go` —
`MergeConflictFiles []string` у `Epic`/`Task` (`json:"merge_conflict_files,
omitempty"`); запись/очистка в `autoCommitAndMergeTask` (done→релиз), `Session.
TaskMerge` (server/actions.go), REST `handleMergeTask`/`handleReleaseEpic`
(server/gitflow.go), авто-синхрон эпика `autoResolveMainSync` +
`clearEpicMergeConflict` (server/gitflow_auto.go:471), `handleEpicRebase`/
`handleEpicResolve` (server/gitflow_resolve.go); `RoleStatus`-уведомления.
Ф-2: блок «Конфликты мёрджа» в `chatAssistantPrompt` (server/chatassist.go) +
правило «не повторять TaskMerge» в `agents/chatassist/agent.go`. Ф-3:
`web/src/Types/Types.ts` `merge_conflict_files` + бейдж в модалках
`TaskModal`/`EpicModal` (TaskModal/styles.scss, EpicModal/styles.scss). Ф-4:
`TaskMerge` возвращает детерминированную конфликт-строку с файлами и точкой
резолва, состояние не меняется. Тесты: `TestTaskDoneAutoMergeConflict` (поле
f.txt), `TestMergeTaskConflict409` (409+поле), `TestMergeTaskConflictIdempotent`,
`TestMergeTaskClearsConflictField`, `TestMergeConflictFilesJSON` (board),
`TestChatAssistPromptShowsMergeConflict`; `go build/vet` по перечню AGENTS.md
зелёные, `go test` зелёные кроме 3 пред-существующих флаков чат-ассистента.