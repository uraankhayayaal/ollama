# План: находки ручных E2E (дефекты Ф-1..Ф-7)

Статус: **DONE** (все дефекты закрыты, включая Ф-4b; остался только опциональный
пункт Ф-4 — кнопка «Спланировать» в Web UI — и ручной E2E Ф-4b на реальном
проекте с инфраструктурой Redis/Qdrant/модель).

Дефекты, найденные при выполнении пяти ручных E2E-пунктов из планов
`2026-09-22-done-assistant-ask-user`, `2026-09-24-done-architect-intelligence`,
`2026-09-24-done-contextual-diff`, `2026-09-24-done-makefile`,
`2026-09-24-done-merge-conflict-board`. Каждый пункт воспроизводится на тестовых
проектах в `temp/e2e-*` (git-демон `git://127.0.0.1:9418`), логи — в
`/tmp/opencode/e2e-logs/`.

## Статус на 2026-09-26

Закрыто: Ф-1, Ф-2, Ф-3, Ф-4b, Ф-5, Ф-6, Ф-7 (вместе с тестами). Частично: Ф-4
(режим запуска выбран, отдельной кнопки «Спланировать» в Web UI нет — это
опциональный удобный пункт, а не дефект). Открытых дефектов нет; остался ручной
E2E Ф-4b на реальном проекте (автономный резолв конфликта эпика чат-ассистентом).

Верификация после правок: `go build`/`go vet` по `. ./server/ ./agents/... ./tools/
./board/ ./rag/ ./workspace/` — зелёные; `go test` по тем же пакетам, включая
`ai/server` (и под `-race`), — зелёные; `npm run build` web/ — зелёный.
Прежние флаки чат-ассистента (`TestChatAssistantCreatesBugAndTask`,
`TestChatAssistantDeleteTaskAfterConfirm`, `TestChatAssistantPublishesBoardWhenIdle`)
больше не воспроизводятся — причины в Ф-6/Ф-7.

## Ф-1 — Бинарный конфликт не распознаётся — ЗАКРЫТО

- [x] `gitops/merge.go:mergeTreeConflicts` учитывать бинарные конфликты
      (`git merge-tree` для бинарника отдаёт `warning: Cannot merge binary
      files: <path> (.our vs. .their)` + `changed in both`, без маркеров
      `<<<<<<< .our`), а не только текстовые. Разбор вынесен в
      `binaryConflictPath` (`gitops/merge.go:245`).
- [x] `POST /api/projects/{id}/epics/{eid}/release` при бинарном конфликте
      отдавать 409 со списком файлов, а не 502 «Конфликт слияния в …».
      Тесты: `TestReleaseEpicConflict409`, `TestReleaseEpicConflictBinary409`.
- [x] `POST …/rebase` не должен отвечать «конфликтов не найдено», когда конфликт
      есть: прогноз `merge-tree` выполняется ДО снятия worktree, конфликтные
      файлы пишутся на доску, остаток уходит модели
      (`server/gitflow_resolve.go:194`). Тесты: `TestRebaseEpicConflictDryRun`,
      `TestRebaseEpicNoConflicts`.
- [x] Тест: бинарный конфликт эпика → 409/`files` → бейдж с путём бинарника.

## Ф-2 — Ручной релиз эпика не ставит признак конфликта — ЗАКРЫТО

- [x] `server/gitflow.go:handleReleaseEpic` при 409 записывать
      `epic.MergeConflictFiles = files` (как это делают `handleTaskMerge`,
      `autoResolveMainSync`, `handleEpicResolve`) и слать `RoleStatus`-уведомление.
- [x] Тест REST: релиз эпика с конфликтом → 409 и
      `merge_conflict_files == ["app.py"]`; повторный `GET` доски показывает
      признак; успешный резолв снимает его. Тесты: `TestReleaseEpicConflict409`,
      `TestMergeTaskConflictIdempotent`, `TestMergeTaskClearsConflictField`,
      `TestMergeTaskConflict409`, `TestTaskDoneAutoMergeConflict`,
      `TestMergeConflictFilesJSON` (board), `TestChatAssistPromptShowsMergeConflict`.

## Ф-3 — Взаимная блокировка лидов при зависимости задачи от другого эпика — ЗАКРЫТО

- [x] `agents/planner/kanban.go:phaseLeads` не выдавать следующий эпик лиду,
      только пока `pipelineIdle`, если на доске есть задачи, заблокированные
      зависимостью от ещё не разобранного эпика: добавлена разблокировка
      очереди лидов для эпиков-зависимостей.
- [x] Вариант выбран: (а) приоритетно разбирать эпики-зависимости pending-задач —
      лид получает эпик, блокирующий чужую задачу, даже если тот не первый в
      очереди.
- [x] Тест: доска с двумя эпиками, задача второго зависит от первого → цикл
      доходит до выполнения, а не падает «нет прогресса».
      `TestKanbanLeadQueueWaitsUntilTasksDone`, `TestKanbanLeadQueueOrderQALast`.

## Ф-4 — Web UI не доходит до main-фазы архитектора — ЧАСТИЧНО

- [x] Режим запуска стал параметром, а не константой: `Session.start` принимает
      `boardOnly bool` (`server/session.go:217`) и зовёт
      `runner.SetBoardOnly(boardOnly)` вместо жёсткого `true`.
      `ActionsBackend.KanbanStart(ctx, task)` выбирает режим по наличию текста
      задачи (`server/actions.go:229`):
      - `task=""` → `boardOnly=true` — только доска (кнопка «Продолжить»,
        `handleContinue` передаёт `true`, `server/server.go:663`);
      - `task!=""` → `boardOnly=false` — полный конвейер (Архитектор → Эпики →
        Утверждение → Декомпозиция → Исполнение), доступен из чата:
        «заведи задачу …» → `KanbanStart` с текстом задачи.
      Описание аргумента `task` добавлено в `actionTool` (`server/actions.go:129`).
- [ ] Не сделано: отдельная кнопка «Спланировать» в Web UI. Сейчас полный
      конвейер достижим только через чат-ассистента; если нужен прямой вход из
      UI — добавить кнопку/REST-эндпоинт с непустым `task`.
- [x] Тест: `TestKanbanStartTaskSelectsPipeline` — аргумент `task` доходит до
      backend и различает режимы (`server/actions_test.go`).
- [x] **Ф-4b** Чат-ассистент не вызывал резолв конфликтов сам: на прямой просьбе
      ограничивался `KanbanStart` и уточняющим вопросом. Причина: инструмент
      `tools.ResolveGitConflicts` есть в реестре (`tools/gitresolve.go:32`), но
      работает через `FileOps.OutputDir` — каталог проекта, тогда как резолв Ф-4
      идёт в постоянном worktree `.conflict-<проект>-<эпик>` ВНЕ каталога
      проекта. Выдать модели файловые инструменты с этим OutputDir нельзя: они
      действуют весь сеанс, и ассистент (который по замыслу не пишет файлы
      проекта) начал бы писать «не туда». Решение — серверные мосты поверх того
      же ядра, что и REST (`server/actions_resolve.go`, Ф-4b):
      - [x] `Session.ConflictResolve` (`action=status|start|apply`) и
            `Session.ConflictFinish` — `ActionsBackend` расширен, мосты
            подключены в `serverActionTools` (`server/actions.go:211`), значит
            доступны и ассистенту, и тестам.
      - [x] Мост НЕ дублирует git-логику: `callResolveCore` прогоняет
            `handleEpicRebase` / `handleEpicResolve` через in-memory
            `http.ResponseWriter` (`captureWriter`) и разбирает JSON-ответ, так
            что REST и чат не могут разойтись.
      - [x] `status`/`start` отдают модели содержимое конфликтных файлов
            (`conflicts.content`) — единственный способ их «прочитать»;
            файлы > 48 КБ отдаются фрагментом вокруг маркеров и помечаются в
            `conflicts.truncated`.
      - [x] `apply` принимает `files` (полное содержимое) либо `edits`
            (`file`/`old`/`new`, `old` должен встречаться ровно один раз).
            Границы: править можно только файлы с оставшимися маркерами
            текущего резолва, только относительные пути внутри worktree, только
            обычные файлы (symlink/каталог отклоняются), только файлы ≤ 48 КБ
            для `files`; после записи маркеров остаться не должно.
      - [x] `ConflictFinish` (= `EpicResolve`) деструктивен: приёмка worktree,
            коммит резолва, merge в main, push — только через confirm-гейт
            `actionTool` («да» в чате). `409` (ещё конфликт / приёмка не прошла /
            чужой эпик) НЕ считается ошибкой инструмента — модель получает
            `resolve_status` и продолжает работу.
      - [x] Абсолютный путь worktree убран из ответа моста (файловые инструменты
            ассистента туда не умеют — только провоцировали бы неудачные
            `ReadFiles`).
      - [x] Промпты синхронизированы: полный цикл эпика
            `start → status → apply → EpicResolve` в `agents/chatassist/agent.go`
            и блоке «Конфликты мёрджа» (`server/chatassist.go:282`); для
            конфликта ЗАДАЧИ (ветка задачи ↔ релиз эпика) автоматики нет —
            ассистенту сказано предлагать пересоздание ветки/ручной rebase, а
            ложное обещание моста убрано из сообщения `TaskMerge`
            (`server/actions.go:287`).
      - [x] Тесты: `TestConflictResolveToolsWiring` (схемы, разбор `files`/
            `edits`, confirm-гейт `EpicResolve`), `TestConflictReplySemantics`
            (200/409/400), `TestConflictSafeRelAndWithinDir`,
            `TestConflictViewTruncatesBigFiles`,
            `TestChatAssistantResolvesEpicConflictViaBridges` (реальный git:
            start → status → отказы apply на не-конфликтном файле / выходе из
            worktree / оставшихся маркерах → apply → confirm-гейт → «да» →
            main в origin, worktree снят, признак конфликта с доски снят),
            `TestConflictApplyEditsForBigFile`,
            `TestConflictResolveNoProcess`,
            `TestAssistantPromptHasConflictResolveFlow` (agents/chatassist) и
            расширенный `TestChatAssistPromptShowsMergeConflict`.

## Ф-5 — `GateBanner` не восстанавливается после перезагрузки страницы — ЗАКРЫТО

Корень проблемы был на сервере, а не во фронте: событие `gate` одноразовое, а
`broadcastSnapshot` (ответ на WS-подписку) публиковал только
`status`/`board`/`tokens`. Перезагруженный клиент знал, что `status=waiting`, но
не получал список утверждаемых эпиков/задач.

- [x] `Session` помнит payload активного затвора (`gateMsg`: заполняется в
      `publishGate`, снимается в `waitGate`/`approve`/по завершении раннера) и
      переигрывает его в `broadcastSnapshot` (`server/session.go:626`).
- [x] `statusEvent.Gating` сериализуется всегда (без `omitempty`) — клиент
      отличает «затвор снят» от «поля нет».
- [x] `web/src/App.tsx`: обработчик `status` снимает `gate` при `gating === false`
      (баннер не «залипает», если решение принято в другой вкладке).
- [x] Тест: `TestSessionSnapshotReplaysActiveGate` — снапшот живой сессии с
      затвором отдаёт `gate` с `ids`/`summary`; после `approve` payload забыт.

## Ф-6 — Снимок доски не публикуется после board-инструмента в idle-чате — ЗАКРЫТО

Найдено при разборе флака `TestChatAssistantPublishesBoardWhenIdle`: реальный
дефект продукта того же класса, что и Ф-5.

- [x] `runChatAssistant` собирал собственный `runevents.Router` с голым
      `sess.chatEvent(ev)`, обходя `sess.router`, где живёт реакция
      «board-инструмент → `emitBoard`». В idle-сессии `boardFlusher` не крутится,
      поэтому снимок доски не публиковался вообще: эпик/баг, созданные чатом,
      появлялись в Web UI только после ручного F5 — ровно тот дефект, который
      комментарий у `emitBoard` описывал как уже решённый.
- [x] Обработка вынесена в `Session.routeRunEvent` (`server/session.go:155`),
      её используют оба репортёра — оркестрации (`sess.router`) и ассистента
      (`server/chatassist.go:68`).
- [x] Тест: `TestChatAssistantPublishesBoardWhenIdle` (эпик, созданный чатом,
      появляется в снимке доски без F5).

## Ф-7 — Протухшие тесты чат-ассистента — ЗАКРЫТО

- [x] `TestChatAssistantCreatesBugAndTask` и
      `TestChatAssistantDeleteTaskAfterConfirm` скриптовали `BoardCreateTask` /
      `BoardDeleteTask`, которых у ассистента нет с Ф-8 (задачи внутри эпика
      создают лиды направлений, коммит `295dc16`). Раннер падал с
      `function BoardCreateTask not in tool set`, цикл завершался
      `RoleStatus`-ошибкой вместо ответа — тесты падали «на чистой базе» и
      маскировали реальный дефект Ф-6.
- [x] Переписаны под фактический контракт:
      `TestChatAssistantCreatesEpicAndBug` (эпик-черновик с `RequiresReview`,
      задач нет — их заводят лиды; затем баг) и
      `TestChatAssistantDeleteEpicAfterConfirm` (деструктивный сценарий
      подтверждения на `BoardDeleteEpic`).
- [x] Попутно исправлена тестовая обвязка, всплывшая при разборе:
      `blockingIndexer.Close` падал `close of closed channel` (фабрика отдаёт
      один индексатор на каждый вызов) — закрыт через `sync.Once`
      (`server/ragindex_test.go`); `readFramePayload` не понимал extended length
      WS-кадров (>125 байт) и падал на первом длинном кадре
      (`server/server_test.go`).

## Смежное (исправлено попутно)

- [x] `agents/chatassist/agent.go`: промпт больше не обещает модели
      несуществующие инструменты правки задач (`BoardCreateTask` и др.) — иначе
      модель планировала вызовы, которых нет в наборе. Учтён тест
      `TestAssistantPromptHasCreationChecklist`, запрещающий упоминание
      `BoardCreateTask` в промпте.

## Не входит

- Правка `git daemon` в тестовом стенде (push через `git://` требует
  `--enable=receive-pack`; без него авто-синхрон падал на push) — это была
  проблема тестового окружения, не продукта.
- Модельные формулировки чат-ассистента — ограничение поведения LLM,
  а не кода; промпт усилен в Ф-4b, но «человеческие» формулировки остаются
  на стороне модели.
- Отдельная кнопка «Спланировать» (прямой запуск новой задачи мимо чата) в Web UI
  — опциональный п. Ф-4, к текущим дефектам не относится.
