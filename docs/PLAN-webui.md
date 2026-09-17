# План: Web UI-оболочка «Доска + Чат + Дифф» с полным HITL

Статус: **В РАБОТЕ**. Обновлять файл по мере выполнения этапов (чекбоксы).

## Цель

Веб-оболочка над существующей Kanban-оркестрацией: выбор рабочего проекта
(любая папка / git-проект / temp-проект) → три панели (Kanban-доска, чат,
изменения кода) + полный контроль человека над процессом (HITL) + выдача
результата через ветку/MR.

## Конфигурация (решения пользователя)

1. **HITL: полный контроль** — утверждение эпиков, редактирование и approve
   задач, ручные статусы, reject/переделка.
2. **Git**: работа в изолированной ветке + кнопка «Принять → MR» (создание MR).
3. **Фронтенд**: React-приложение.
4. **Чат**: красивый, с live-состоянием (таймлайн tool-call'ов в реальном времени).
5. **MVP граница: сразу с HITL-паузами.**

Дополнительно:
- React живёт в этом репо, папка `web/` (Vite + TS), прод-сборка embed'ится
  в тот же Go-бинарь.
- MR/PR для **обоих форджей** (GitLab + GitHub), фордж определяется по
  `origin` remote проекта (gitlab.com / github.com / self-hosted по хосту).
- Безопасность: bind `127.0.0.1` по умолчанию; опциональный
  `AI_WEB_PASSWORD` → простой логин + httpOnly-сессия + CSRF-token.
  Токены форджей/LLM в UI не отдаются, только в сервер.

## Архитектура (обзор)

```
React (web/) ──WS/IP──► server/ (новый Go-пакет, в этом же процессе)
                              ├─ SessionRegistry (один раннер на проект, single-flight, cancel)
                              ├─ REST-эндпоинты (проекты, доска, chat, дифф, accept)
                              ├─ WS-хаб (события: board/chat/tool/agent/diff)
                              ├─ Chat Store (Redis Streams)
                              ├─ Workspace Registry (имя → абсолютный путь)
                              └─ GitOps (worktree, commit, push, MR/PR)
        ┌──────────────────────────────┬─────────────────────────────┐
        ▼                              ▼                             ▼
   board.Store (Redis)          runner.Generate (+Reporter)    projects/*, forges/*
```

Существующие пакеты НЕ переписываются — над ними добавляются тонкие слои.

## Новые компоненты

| Пакет / папка | Назначение |
|---|---|
| `server/` | HTTP+WS сервис, `SessionRegistry`, REST-хендлеры, `resolve.go` (провайдер), `embed` `web/dist` |
| `workspace/` | реестр проектов (имя → абс.путь, тип: temp/папка/git, remote), `.ai-workspaces.json`, безопасность путей |
| `gitops/` | worktree-изоляция, `git add/commit/push`, определение форджа по remote, создание MR/PR |
| `chat/` | Redis Streams `chat:<proj>` + pub/sub `events:<proj>` (история XREVRANGE + live-подписка) |
| `runevents/` | `Reporter` (события раунда и tool-call'ов) через context (по образцу `WithResumeState`) |
| `web/` | React-приложение (Vite + TS), три панели |

## Интеграции с существующим кодом

- **HITL gates** — `agents/planner/kanban.go`: новый интерфейс `HumanGate`
  (nil по умолчанию → автономный CLI без изменений). Gates вставляются в `Run`
  после публикации эпиков (архитектор), после декомпозиции (задачи «готова к
  работе») и перед `phaseExecute`. В UI gates = блокирующие каналы + WS-ответы.
  Статусы доски НЕ меняем — gates живут в логике раннера, `ValidateTransition`
  остаётся источником истины для ручных переходов.
- **Live tool-calls** — `runner.generate` информирует `runevents.Reporter`
  после `ChatOnce` и после каждого `agent.CallFunction`
  (`runner/runner.go:387`, `:468`): onRound / onToolStart / onToolResult.
- **Провайдер** — switch из `main.go:80` выносится в `server/resolve.go`
  (поведение CLI не меняется).
- **Модель сообщений** — прод-чат на `ChatOnce` (полный ответ); live-таймлайн
  через Reporter. Стрим токенов — Ф-3.
- **Магазин доски** — `board.Store` уже покрывает epics/tasks/bugs/statuses/
  deps/order: используем как есть (SaveTask/SaveEpic/SetTaskStatus/MoveTask/
  DeleteTask + ValidateTransition).

## API (REST + WS)

```
GET   /api/projects                      — список workpace'ов + статус сессий
POST  /api/projects                      — открыть/создать проект (path | git_url)
GET   /api/projects/:id                 — доска + meta + статус сессии + сводка диффа
POST  /api/projects/:id/chat             — {message} → запуск оркестрации, событие в Stream
WS    /api/projects/:id/ws               — события: chat, board, tool, agent, hitl, diff
POST  /api/projects/:id/epics/:eid/approve — утвердить эпик (gate)
POST  /api/projects/:id/epics/:eid/reject  — переделать эпик (gate)
PUT   /api/projects/:id/tasks/:tid         — редактирование (title/desc/status/deps/order/assignee)
POST  /api/projects/:id/tasks/:tid/execute — запустить специалиста (gate)
POST  /api/projects/:id/session/stop       — cancel текущего раннера
POST  /api/projects/:id/accept             — commit + push + создать MR/PR
POST  /api/projects/:id/reject-branch      — отклонить ветку/worktree
GET   /api/projects/:id/diff               — дифф (git diff или Snap.Diff)
```

## UI (web/, три панели)

- **Доска**: колонки по статусам; DnD карточек между колонками с
  `ValidateTransition` (невалидные → отказ); редактирование карточек;
  баджи ±/параллельность; фильтры по эпикам/ролям.
- **Чат**: лента (user/модель/агент/инструменты), markdown + подсветка кода,
  live-таймлайн tool-call'ов (имя, args collapsed, ok/fail), индикатор
  «работает: <агент>», кнопки Старт/Стоп.
- **Дифф**: дерево изменённых файлов с +/−, inline/side-by-side, кнопки
  «Принять → MR» / «Отклонить».
- Глобальный селектор проекта, старт/стоп сессии, HITL-панель «ожидает
  подтверждения».

## Этапы и чеклист

### Ф-1 (фундамент)
- [x] `workspace/`: реестр проектов + `.ai-workspaces.json` + безопасность путей + тесты
- [x] `runevents/`: Reporter + передача через context + тесты
- [x] `chat/`: Redis Streams + pub/sub + тесты
- [x] HITL gates в `agents/planner/kanban.go` (`HumanGate`, nil = автономный CLI) + контракт-тесты
- [x] `server/`: `resolve.go`, HTTP+WS хаб, SessionRegistry, REST-хендлеры
- [x] `web/`: каркас React (Vite+TS), три панели, WS-клиент, embed+dev-прокси
- [x] Верификация Ф-1: build/vet/тесты (в т.ч. `-race`), регресс CLI без gates, `npm run build` web/

### Ф-2 (полный контроль + Git)
- [x] Полный контроль в UI: редактирование задач, DnD, ручные статусы, approve/reject
- [x] `gitops/`: worktree-изоляция, commit, push, fallback branch-in-place, reject-branch
- [x] MR/PR обоих форджей (автоопределение по remote; github+gitlab — hermetic-тесты)
- [x] Кнопка «Принять → MR» + REST accept/reject-branch/diff: сервер открывает
      git-проекты (`git_url` → clone → фича-ветка в temp/<имя> через gitops),
      REST `/diff` (унифицированный дифф от базы / Snap.Diff() для папок),
      `/accept` (commit+push+MR/PR через forges.NewByRemote, GITHUB/GITLAB_TOKEN),
      `/reject-branch`; web: Diffboard с диффом, кнопками и ссылкой на MR
- [x] Дифф-вью: `git diff` от точки отхода / `Snap.Diff()` для обычных папок
- [x] Верификация Ф-2: тесты gitops (dry-run + E2E на реальном git CLI),
      определения форджа, DnD/статусов, серверные hermetic-тесты, `npm run build` web/
- [x] VI: проверено на реальном git-проекте end-to-end (ветка → MR на GitLab):
      `https://github.com/uraankhayayaal/my-rust-app` — открытие по `git_url`,
      дифф правки, accept → commit+push+PR (`#1`, head `ai/my-rust-app` → main).
      Для headless-сервера push на HTTPS идёт с токеном, встроенным в URL
      (`x-access-token`), без сохранения upstream (gitops.Repo.PushTo)

### Ф-3 (полировка)
- [ ] Потоковый ответ модели: рефактор `ChatOnce` → стрим
- [ ] Безопасность: `127.0.0.1` + `AI_WEB_PASSWORD` (httpOnly + CSRF), rate-limit
- [ ] Большие диффы/доска: пагинация, ленивая загрузка файлов
- [ ] Док-та: `docs/project-map.md` (структура, env, раздел WebUI)

## Верификация

```
go build . ./server/... ./workspace/... ./gitops/... ./chat/... ./runevents/... ./agents/... ./board/ ./tools/
go vet  . ./server/... ./workspace/... ./gitops/... ./chat/... ./runevents/... ./agents/... ./board/ ./tools/
go test . ./server/... ./workspace/... ./gitops/... ./chat/... ./runevents/... ./agents/... ./board/ ./tools/  # -race на новых
npm run build   # в web/
```

Известные нюансы репозитория (не убирать):
- `go build ./...` падает на `temp/moon-distance/internal/middleware/cors.go`
  (артефакт сгенерированного проекта) — использовать перечень выше.
- Не форматировать `agents/acceptor/checks.go`, `agents/acceptor/run.go`.
- Единственный pre-existing падающий тест — `ai/agents/codereviewer::TestReviewMrParsesInterfaceArrayComments`.
- Комментарии/промпты/UI-тексты — на русском.

## Как продолжить с другого компьютера

1. Открыть этот файл, прочитать разделы «Конфигурация» и «Этапы и чеклист».
2. Взять первый незачёркнутый пункт Ф-1, выполнить, отметить `[x]`, закоммитить
   (`docs/` + код). При желании завести in-session todo из оставшихся пунктов.
3. После каждой фазы — верификация (блок выше) и краткая запись о сделанном в
   этот файл.