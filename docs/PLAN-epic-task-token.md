# План: Учёт и прогноз токенов по эпикам и задачам

Статус: **ПЛАН (реализация позже)**. Источник — прежний `PLAN-epic-task-token.md`
(3 строки: записывать итоговый токен при завершении каждой задачи/эпика,
хранить его, позже предсказывать оценку для новых единиц из scrum-системы).
Формат — как в `PLAN-webui.md` / `PLAN-lsp.md`: статус, решения, привязка к
текущему коду, этапы с чекбоксами, верификация. Обновлять по мере выполнения.

## Цель

1. **Фиксировать факт**: при завершении (done) каждой задачи и каждого эпика
   записывать на доску итоговый расход токенов (вход / выход / итого =
   вход+выход) и хранить его в сущностях доски.
2. **Прогнозировать позже**: для новых эпиков и задач предсказывать примерный
   расход токенов на старте (аналог scrum-оценки: вместо story points —
   «токены»), сравнивать прогноз с фактом.

## Текущее состояние (что уже есть)

| Файл / сущность | Что есть сейчас |
|---|---|
| `tokens/store.go` | счётчик токенов **только на уровне проекта**: Redis-хэш `tokens:<project>` (поля `in`/`out`), атомарный `Add`/`Get`. Судень «какой шаг/задача съел токены» отсутствует |
| `runevents/reporter.go` | событие `TypeTokenCount` c полями `In`/`Out`; `Router.WithAgent` именует агента. Scope-атрибуции (задача/эпик) нет |
| `runner/runner.go:583` | формирует `in`/`out` (`EstimateUsage` → фактический usage провайдера) и зовёт `rep.OnTokens` |
| `server/session.go:44,335` | `sess.tok *tokens.Store`; `chatEvent` на `TypeTokenCount` → `addTokens` → `tok.Add` → WS `type=tokens`. Единственный путь накопления токенов в оркестрации |
| `board/entity.go:144,168` | `Epic` и `Task` (TaskSpec + Status + EpicID/Assignee). **Полей токенов нет**; терминальные статусы — `done`/`cancelled` |
| `board/store.go` | `SaveEpic`/`SaveTask`/`SetEpicStatus`/`SetTaskStatus` (`:210`/`:352`), `AllDone` (`:722`) — точки фиксации завершения |
| `agents/planner/kanban.go` | единственное место, где известен момент завершения: задача — `phaseExecute` (~`:694` Generate специалиста, `:705-713` перевод в done); эпик — готовность по `phaseEpicsProgress`/`AllDone`. `provider.Generate` вызывается **однажды за фазу** — точная граница атрибуции |
| `server/server.go:659` `GET /api/projects/:id/tokens` + WS | живой счётчик проекта в Web UI (`TokensCounter`) |
| `web/src/Components/Dashboard` | `EpicRow`/`TaskRow` показывают статусы и быстрые действия (`EpicActionBar`) — сюда добавится лэйбл «прогноз / факт токенов» |

## Решения пользователя (зафиксировано)

1. **Записывать факт при завершении** (`done`) задачи и эпика: вход, выход,
   итого. Поля кладём в сами сущности доски (`Task`/`Epic`), а не в отдельный
   реестр.
2. **Хранить** в Redis-доске вместе с сущностью (переживает перезапуски).
3. **Атрибуция расхода**:
   - специалист (выполнение задачи) — 100% на задачу;
   - лид (декомпозиция эпика) — 100% на эпик;
   - архитектор (генерация бэклога эпиков) — распределяется между эпиками
     пропорционально числу их задач (сниппет-ориентир) либо остаётся в
     счётчике проекта, если эпиков несколько и пропорция слишком условна;
   - остальное (чат-ассистент, вне доски) — только в глобальный счётчик проекта.
4. **Прогноз**: на старте (создание эпика архитектором / задачи лидом)
   проставляется оценка `TokenEstimate`; после завершения фиксируется факт;
   дальше — предсказание по истории. Модель — детерминированная регрессия
   поверх фактов доски (не LLM на каждый прогноз).

## Новые компоненты

| Пакет / файл | Назначение |
|---|---|
| `board/entity.go` | `Task`/`Epic`: `TokensInput`, `TokensOutput`, `TokensTotal` (факт), `TokenEstimate` (прогноз, заполняется позже) |
| `board/store.go` | атомарное накопление и чтение токенов сущности: `AddTaskTokens(ctx, id, in, out)` / `AddEpicTokens`; при завершении — `FinalizeTaskTokens`/`FinalizeEpicTokens` (одна запись) |
| `tokens/store.go` | scoped-счётчики: `AddScoped(ctx, scope, in, out)` (Redis `tokens:<project>:scoped:<scope>`), `GetScoped`, `ResetScoped`; `tokens/history.go` — выборка завершённых задач/эпиков для прогноза |
| `runevents/reporter.go` | `Event.Scope`, `Router.WithScope(scope)`, `ScopeFromContext` (по образцу `WithAgent`) |
| `agents/planner/kanban.go` | выставить scope вокруг каждого `provider.Generate`; при `done` задачи/эпика — снять scoped-итог и записать в сущности доски |
| `server/session.go` | в `addTokens` учитывать scope (писать и в глобальный, и в scoped-ключ) |
| `tokens/predict.go` | регрессия по признакам (title/description/role/число задач) → прогноз + базовая точность; подставляется в `TokenEstimate` |
| `web/.../Dashboard` | в `EpicRow`/`TaskRow` лэйбл «≈<оценка> / <факт> токенов»; в сводке доски — суммы по статусам |
| `docs/PLAN-epic-task-token.md` | этот план |

## Интеграции с существующим кодом

- **Канал токенов не трогаем**: `runevents.TypeTokenCount` → `sess.addTokens`
  остаётся единственным путём накопления. Добавляется только сквозной `scope`
  в событие и параллельная запись в scoped-ключ.
- **KanbanRunner** получает опциональный `*tokens.Store` (или узкий интерфейс
  `ScopeCounter`) в `NewKanbanRunner` — в консольном режиме (`go run . kanban`),
  где нет Redis-счётчика, токены просто не пишутся (nil-safe, не роняем).
- **Снимок vs scope**: снимков (`Get` до/после `Generate`) не используем из
  первичного подхода — одновременно с доской в том же проекте может писать
  чат-ассистент, дельты станут грязными. Scope-ключ в `addTokens` изолирует
  атрибуцию на уровне события.
- **Финальный аккорд эпика**: итог эпика = сумма `TokensTotal` его задач +
  вклад лидов/архитектора по scoped-ключам `epic:<id>`; записывается один раз
  при переводе эпика в `done` (в `phaseEpicsProgress`, где `AllDone`).
- **Сериализация**: сущности доски сохраняются JSON-ами (`SaveEpic`/`SaveTask`);
  новые поля подхватываются автоматически, но миграция старых записей —
  через маппинг «нет поля = 0» (фолбэк уже заложен в `hashInt`-паттерне).

## Этапы и чеклист

### Ф-1 — Модель данных: токены в сущностях доски
- [ ] `board/entity.go`: `Task` и `Epic` — поля `TokensInput`, `TokensOutput`,
      `TokensTotal`, `TokenEstimate` (int64, json: `tokens_in`/`tokens_out`/
      `tokens_total`/`tokens_estimate`)
- [ ] `board/store.go`: `AddTaskTokens`/`AddEpicTokens` (атомарно, read-modify-
      write под локом, как `SetTaskStatus`); `FinalizeTaskTokens`/`FinalizeEpicTokens`
- [ ] Тесты: сохранение/чтение round-trip, накопление несколькими порциями,
      старые записи → 0

### Ф-2 — Scoped-учёт токенов (атрибуция расхода)
- [ ] `runevents/reporter.go`: `Event.Scope`, `Router.WithScope(scope)` (клон
      роутера, как `WithAgent`), `ScopeFromContext`
- [ ] `tokens/store.go`: `AddScoped`/`GetScoped`/`ResetScoped` (ключ
      `tokens:<project>:scoped:<scope>`) + тесты
- [ ] `server/session.go:addTokens`: если событие несёт `Scope` — дополнительно
      пишем в scoped-ключ (глобальный счётчик не меняется)
- [ ] `agents/planner/kanban.go`: scope вокруг `Generate` — специалист
      `task:<id>`, лид `epic:<epicID>`, архитектор `architecture` (раскладка
      по эпикам на закрытии); тесты на kanban-уровне (fake provider с scope)

### Ф-3 — Фиксация итогов при завершении
- [ ] `phaseExecute` (`:705-713`): после перевода задачи в `done` снять
      `GetScoped(task:<id>)` → `FinalizeTaskTokens`, сбросить scoped-ключ;
      fallback — дельта без scope не пишется
- [ ] Эпик: при `done` (ветка `AllDone`/`phaseEpicsProgress`) — сумма токенов
      задач + scoped-вклад лида, раскладка архитектуры; `FinalizeEpicTokens`
- [ ] Тесты: полный прогон `KanbanRunner` → задача/эпик содержат корректные
      итоги; отмена (`cancelled`) — итоги по факту на момент отмены

### Ф-4 — Web UI
- [ ] `server/server.go`: поля токенов попадают в снимок доски (`GET
      /api/projects/:id/board`) автоматически; проверка сериализации
- [ ] `web/.../Dashboard`: в `EpicRow`/`TaskRow` лэйбл «≈оценка / факт токенов»
      (рядом с `EpicActionBar`); в сводке (шапка доски) — сумма факта и
      средней ошибки прогноза
- [ ] `TokensCounter` проекта остаётся как есть (глобальный пульс); тестовая
      сборка `npm run build`

### Ф-5 — Прогноз (scrum-оценка «в токенах»)
- [ ] `tokens/history.go`: выборка завершённых задач/эпиков с признаками
      (title, description, `assigned_role`, число задач у эпика, действие) +
      факт `tokens_total`
- [ ] `tokens/predict.go`: регрессия (ход-линейная/ridge по роли и длине
      описания) + бейзлайн «среднее по роли»; метрика точности (MAE)
- [ ] Подстановка `TokenEstimate`: при создании эпиков (архитектор) и задач
      (лид) — сразу после `CreateEpic`/`CreateTask` в `kanban.go`
- [ ] Тесты: обучение на синтетике, разумные пределы прогноза, degrade при
      скудной истории (бейзлайн)

### Ф-6 — Верификация
- [ ] `go build . ./agents/... ./tools/ ./board/ ./server/ ./workspace/ ./tokens/`
- [ ] `go vet . ./agents/... ./tools/ ./board/ ./server/ ./workspace/ ./tokens/`
- [ ] `go test . ./agents/... ./tools/ ./board/ ./server/ ./workspace/ ./tokens/`
- [ ] Ручной E2E: Web UI → запуск доски на реальном проекте → задача/эпик
      получают факт токенов при done; новая задача показывает оценку

## Верификация

```
go build . ./agents/... ./tools/ ./board/ ./server/ ./workspace/ ./tokens/
go vet  . ./agents/... ./tools/ ./board/ ./server/ ./workspace/ ./tokens/
go test . ./agents/... ./tools/ ./board/ ./server/ ./workspace/ ./tokens/
npm run build   # web/
```

Известные нюансы репозитория (не убирать):
- `go build ./...` падает на `temp/moon-distance/internal/middleware/cors.go`
  (артефакт сгенерированного проекта) — использовать перечень выше.
- Не форматировать `agents/acceptor/checks.go`, `agents/acceptor/run.go`.
- Комментарии/промпты/UI-тексты — на русском.
- Все новые счётчики токенов — опциональные (nil-safe): консольный режим без
  Redis не должен падать, degradation как у ЛСП.

## Связанные планы

- `PLAN-dashboard-workflow.md` — git-ветки эпиков/задач: кнопка «Залить в main»
  может показывать и статистику токенов эпика.
- `PLAN-owerview-for-prom.md` — зона «наблюдаемость и стоимость» (дашборд)
  базируется на тех же фактах (`tokens_total` в сущностях доски).

## Как продолжить

1. Открыть этот файл, прочитать «Решения пользователя» и «Этапы и чеклист».
2. Взять первый незачёркнутый пункт Ф-1, выполнить, отметить `[x]`, закоммитить.
3. После каждой фазы — верификация (блок выше) и краткая запись в этот файл.