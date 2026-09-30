# План: Переработка процесса доски — «тикер» и событийная связь доска↔чат

Статус: **РЕАЛИЗОВАНО** (Ф-1..Ф-4 выполнены). Формат — как в `PLAN-2026-09-17-done-webui.md` / `PLAN-2026-09-19-done-lsp.md`.
Закрывает TODO из `PLAN-dashboard-gitflow.md` (строки 18–21).

## Цель

Привести в порядок процесс работы доски по двум осям:

1. **Кнопка «Стоп/Продолжить» = «тикер»**: единый источник состояния
   оркестрации, от которого зависит работа остальных компонентов (доска →
   перерисовка/флашинг, чат → ассистент, кнопка → Стоп/Продолжить, git-статус
   → MR/ветки). Без дублирующих «вручную позвал kickBoard» из десяти мест.
2. **Событийная связь «доска ↔ чат»**: перепроектировать механизм соприкосновения
   доски и чата на **паттерн событий/слушателей** (или то, что лучше подходит),
   чтобы код был поддерживаемым и легко расширяемым.

## Текущее состояние (код) — честная картина

| Узел | Файл | Как устроено сейчас |
|---|---|---|
| Кнопка | `web/src/Components/RunButton/RunButton.tsx` | читает `status` (running/waiting/standby → «Стоп», иначе «Продолжить»); сам по себе «нажал → REST» |
| Сессия/тик | `server/session.go` `Session` | `ticks chan struct{}` (буфер 64), `kickBoard()` (неблокирующий send), `boardFlusher` каждые 500 мс проверяет канал тиков (`session.go:405`) |
| Рассылка событий | `server/hub.go` `Hub.Publish(project, typ, payload)` | WS-хаб: JSON `{type,payload}` всем клиентам проекта |
| События агентного цикла | `runevents/reporter.go` `Router` | `WithAgent`/`OnMessage/OnToolStart/...` → `sink`; сессия подключает sink, играющий в `chatEvent` (`session.go:306`) |
| Точки «доска изменилась» | разбросаны: `s.kickBoard`/`sess.kickBoard` в ~10+ местах (gitflow, gitflow_mr, actions, session) | каждый хендлер сам зовёт kickBoard; без события-причины |
| Доска↔чат | `session.go` `chatEvent`, `hub.publish(type=board/chat/status/tokens/gate/tool)` | события публикуются в WS напрямую из сессии; ассистент читает доску через `chatAssistantPrompt` (REST-опросами) |

Ключевая недоработка: механизм «кто и когда оповещает» не централизован —
тики отправляются вручную из многих мест, а потребители (фронт) получают
сырые типованные события без единой модели «событие проекта».

## Решения (предлагаемые)

1. **Единый «тикер» сессии.** Ввести в `server/session.go` обобщённый канал
   событий проекта: `events chan ProjectEvent` вместо `ticks chan struct{}`.
   `ProjectEvent` несёт **тип** (board_changed / chat_updated / git_status_changed /
   tokens_updated / session_status) и опционально данные. Все «kickBoard» заменяются
   на «emit board_changed». `boardFlusher` становится **обработчиком-подписчиком**
   на события (остаётся таймер для бэтчинга грязи, MR-сверки).
2. **Шина «слушатели» поверх WS-хаба.** В `runevents` или новом `server/events.go`
   сделать интерфейс:
   - `type Listener func(ProjectEvent)`
   - `hub.Subscribe(project, name string, l Listener)` / `Unsubscribe`;
   - `hub.Emit(project, ev ProjectEvent)` — раз в слушатели и в WS-клиентов.
   Тогда связь доска↔чат выражается явно: ассистент подписан на
   `board_changed`/`chat_updated` и сам решает, нужна ли пересборка промпта;
   доска подписана на `session_status` (standby/running) и т.д.
3. **Кнопка — клиент тикера.** web/RunButton остаётся тонким: подписка WS на
   `status`; «лайк тикеру» уже есть (`broadcastStatus`). В плане — вынести
   вычисление статуса в одно место на сервере (`computeStatus`), уже есть
   (`session.go:computeStatus`), и дополнить событиями `detail`/причины standby.
4. **Расширяемость:** новые сущности (баги уже есть, эпики/задачи, будущие
   «аналитики») добавляются как новые типы событий + подписки, без правки
   хендлеров.

## Новые компоненты

| Пакет / файл | Назначение |
|---|---|
| `server/events.go` (новый) | `ProjectEvent` (type+payload), интерфейс `Listener`, подписки, short-хелперы |
| `server/hub.go` | добавить local-подписки (без WS) для internal-слушателей поверх `Hub` |
| `runevents/events.go` | общий тип событий (или переиспользовать `Event` в `reporter.go`) |
| `server/session.go` | `emit(ev)` вместо `kickBoard()`; `boardFlusher` как обработчик, `chatEvent` как обработчик |
| `tools/...`, `server/gitflow*.go`, `actions.go` | заменить разрозненные `kickBoard` на `emit(board_changed)` (с указанием причины) |

## Этапы и чеклист

### Ф-1 — Тип событий + эмит вместо тиков
- [x] Составить карту всех точек «kickBoard» (см. grep: session.go, gitflow.go, gitflow_mr.go, actions.go)
- [x] Ввести `ProjectEvent` + `Session.emit`; `kickBoard` объявить deprecated-обёрткой (`emit(BoardChanged)`)
- [x] `boardFlusher` читает события (board_changed → публикация снимка, бэтчинг 500 мс; периодический MR-тикер остаётся)

### Ф-2 — Слушатели (доска↔чат)
- [x] `Listener`-механика поверх hub; подписка ассистента (server/chatassist.go) на `board_changed`, чтобы при изменении доски перечитывать `chatAssistantPrompt`
- [x] Подписка доски на `session_status` (standby/active) → пересчёт `hasWork` без опроса-цикла
- [x] Подписка Web UI — существующий WS-канал остаётся (типы `board/chat/status/tokens` сохраняем, backwards-compatible)

### Ф-3 — Рефакторинг хендлеров/инструментов
- [x] Замена всех прямых `sess.kickBoard()` на `emit(ev)` с причиной
- [x] Публикация статусов: единый `computeStatus` + причина (detail) для standby
- [x] Удалить (или оставить обёртку) старый `ticks chan struct{}`

### Ф-4 — Верификация
- [x] `go build . ./agents/... ./tools/ ./board/ ./server/`
- [x] `go vet  . ./agents/... ./tools/ ./board/ ./server/`
- [x] `go test . ./agents/... ./tools/ ./board/ ./server/`
- [x] `npm run build` (web/)
- [x] Ручной E2E: доска и чат в одном окне — создание эпика из чата мгновенно обновляет доску (WS), кнопка Стоп/Продолжить реагирует на standby

## Верификация

```
go build . ./agents/... ./tools/ ./board/ ./server/
go vet  . ./agents/... ./tools/ ./board/ ./server/
go test . ./agents/... ./tools/ ./board/ ./server/
npm run build   # web/
```

Нюансы: не трогать `agents/acceptor/{checks,run}.go`; не использовать
`go build ./...` из-за артефакта в `temp/`.

## Как продолжить

1. Ф-1 (тик = событие) — фундамент, рефакторинг без изменения поведения.
2. Ф-2 — связь доска↔чат на слушателях; здесь же учёт standby как события.
3. Ф-3, Ф-4 — полный перевод и верификация.