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

Состояние последней сессии (PLAN-wip-architect-intelligence, Ф-4/Ф-5):
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
падают и на чистой базе (не связаны с Ф-4/Ф-5). Отложено: опциональный
REST `POST /api/projects/{id}/index` + кнопка в Web UI (необязательный нюанс
Ф-5).

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