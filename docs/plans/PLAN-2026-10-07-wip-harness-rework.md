# PLAN-2026-10-07-todo-harness-rework

Перестройка мульти-агентного харнесса: системный архитектор ведёт ветку эпика
и верхнеуровневую структуру, лиды пишут только скелетон и контракты,
разработчики реализуют и сдают в **«На тестирование»**, тестировщик
перехватывает задачу и доводит до **«Готово»**. Новая сущность —
**комментарии** к задаче (замечания тестировщика, инъекции пользователя и
системы), новый статус — **«Помощь человека»** (вместо `paused`), роль
**qalead упраздняется**. Только план; код не меняется.

---

## Ключевые решения

| # | Решение | Обоснование |
|---|---|---|
| Р-1 | Архитектор **не ставит задачи тестировщику напрямую**: он создаёт ветку эпика, пишет структуру 1-го уровня и ключевые корневые файлы, пушит и создаёт задачи лидам с **отсылками к файлам** | Тестировщик включается только после разработчика (перехватывает статус «На тестирование»); прямая постановка от архитектора ломала бы порядок фаз |
| Р-2 | Лиды пишут **скелетон** (интерфейсы, абстрактные классы, каркасы фреймворков, зависимости) без реализации; контракты — в коде (интерфейсы, аннотации, DTO, комментарии-подсказки); в задачи — **отсылки к файлам**, а не вставки кода | Контракт должен жить в коде ветки эпика, а не в тексте задачи: разработчик читает код и комментарии, а не гигантские спецификации. Скелетон + «must build» даёт разработчику проверяемую точку входа |
| Р-3 | Разработчик делает `done → testing` («На тестирование»), тестировщик — `testing → done` («Готово») либо комментарий-замечание и возврат в работу | Текущий фолбэк «непонятный исход → done» (`kanban.go:1444-1487`) маскировал незавершённость; acceptance переезжает к тестировщику |
| Р-4 | **Один** статус «Помощь человека» (`human_help`) вместо `paused`: сюда идут форсмажоры, зацикливания, эскалации и ручной запрос помощи | Сейчас эскалация (`escalateLoop:1521`) идёт в `StatusPaused`, а лимит бюджета — в `StatusReady`; два статуса путают UI и каскады. Один боковой статус упрощает FSM и дашборд |
| Р-5 | **Комментарии** — единая сущность (`author`, `type`, `created_at`, `body`) вместо `Task.Injections`: замечания тестировщика, инъекции пользователя и системы; полный CRUD в доске и доставка в модель по типу | Инъекции — уже готовая механика доставки в контекст (`runner/injections.go`, `kanban.go:1386-1398`); расширение сущности автором/типом покрывает и замечания QA, и HITL-инъекции, без второго параллельного поля |
| Р-6 | Порядок колонок: `new → analysis → ready → human_help → in_progress → testing → done → cancelled` | «Помощь человека» — строго между «Готовы к работе» и «В работе» (помощь запрашивают до/в работе), «На тестирование» — между «В работе» и «Готово» |

## Находки разведки (проверено в коде)

- **Н-1. Тестировщику сейчас задачи ставит `phaseLeads`** (`kanban.go:871`):
  роль берётся из бэклога архитектора → `leadFor` (`kanban.go:1853`) → ветка
  `qa` → `qalead.NewQALead` (`:1858`), иначе `SpecialistForRole` (`:1947`,
  `isRole(role,"qa","тест","testing")` → `qaengineer.NewQAEngineer`).
  Архитектор напрямую QA-задачи не ставит — «перехват после разработчика»
  добавляется фазой, а не постановщиком.
- **Н-2. Пайплайн** (`runPhases`, `kanban.go:471`): `phaseArchitect →
  waitEpics (HITL) → phaseArchitectReview → phaseLeads → phaseReady →
  waitReadyTasks (HITL) → phaseExecute → phaseBugs → phaseComplete`.
  Новая фаза `phaseTesting` встаёт после `phaseExecute` (`kanban.go:526-531`).
  `phaseBugs` (`:1569`) использует `qalead.NewQALead` (`:1588`) как триажёр —
  заменить на qaengineer, экспертиза архитектора (`:1612`) остаётся.
- **Н-3. Статусы** (`board/entity.go:26-37`): `new, analysis, ready,
  in_progress, done, cancelled, paused`; `Label:40`, `Valid:68`,
  `Terminal:63`, FSM `ValidateTransition:87-123` (комментарий `:79-84`
  описывает допустимые переходы). `human_help`/`testing` нет. `MOVES` во
  фронте (`web/src/Components/Dashboard/board.ts:32`) — `Record<Status,
  {prev,next}>` с **одним** соседом: новые колонки его ломают, нужен переход
  на списки (`Record<Status, Status[]>`).
- **Н-4. Хуки статуса** (`board/store.go:661-698`): `TaskDoneGuard` до
  `done`, `TaskInProgressHook`, `TaskDoneHook`; `attempts++` при входе в
  `in_progress`. Каскад паузы эпика (`store.go:315-406`:
  `pauseEpicTasks`/`resumeEpicTasks`/`setEpicTasksStatus` + `ResumeStatus`)
  завязан на `paused` — перевести на `human_help`.
- **Н-5. Комментариев нет.** Ближайшее — `Task.Injections`
  (`board/injection.go`: `Injection:58`, `Normalize:96`, `Validate:125`,
  `DecodeInjections:193`, контекст/живой источник `:234-340`; хранение
  `store.go:506-572`; REST `server/server.go:224-231`, `PUT /tasks/{tid}`
  `:1251-1274`, `handleAddInjection:1469`; инструмент `BoardUpdateTask`
  (`tools/board_tools.go:634-750`, проп `injections` `:731-740`); доставка
  `kanban.go:1386-1398`, `runner/injections.go` (`newInjectionSet:40`,
  `AllInjections`). **UI-слоя у инъекций во фронтенде нет** — для
  комментариев он делается с нуля (TaskModal).
- **Н-6. У архитектора нет ни `WriteFiles`, ни `Run`, ни `BoardCreateTask`**
  (`agents/architect/agent.go:41-49`): он только читает и рисует (`BoardCreateEpic`,
  `BoardUpdateEpic`, `BoardReviewBug`, `SubmitBacklogToolName:34`).
  Писать структуру 1-го уровня и пушить ветку эпика сейчас физически нечем.
- **Н-7. У лидов `WriteReadmeOnly:true`** (`backendlead/agent.go:92`,
  `devopslead/agent.go:80`, аналогично frontendlead) и **нет `Run`**
  (`backendLeadToolNames:21-26`): собрать скелетон и прогнать билд они не
  могут. `leadPrompt` (`kanban.go:1898`) прямо говорит «Ты НЕ пишешь код»
  (`:1927`) и кладёт полный контракт в description задачи (`:1925`).
- **Н-8. Разработчик** в конце шага переводит задачу в `done`
  (`developer.go:kanbanStep:371`, `taskPrompt:1982`); план юнит-тестов —
  `developer.go:384-402`. Фолбэк оркестратора тоже целится в `done`
  (`kanban.go:1444-1487`, инъекция при отказе гарда `:1466-1479`).
- **Н-9. Тестировщик** (`qaengineer/agent.go`): промпт `:169-202` (три
  уровня тестов, контракты, приёмка, `verifyPlan`), инструменты
  `qaToolNames:20-23` (WriteFiles/ReadFiles/DeleteFiles/AppendFile/List/
  Run/LSP/ReadAppLogs) — уже включают всё нужное для нового флоу; доска —
  `qaBoardToolNames:28-30` (`BoardGetTask`/`BoardSetTaskStatus`/
  `BoardCreateBug`/`BoardListBugs`) — не хватает создания комментария.
- **Н-10. Ветки**: эпик `ai/epic/<id>` создаётся автоматом
  (`EpicCreatedHook`, `server/gitflow.go:519` → `autoCreateEpicBranch:553`),
  задача `ai/task/<id>` — от ветки эпика (`TaskCreatedHook:526`), worktree —
  `TaskInProgressHook` (`gitflow.go:531` → `taskWorktree`,
  `gitflow_auto.go:133`). На `done` — `TaskDoneGuard:514` + `TaskDoneHook` →
  `autoCommitAndMergeTask` (`gitflow_auto.go:449`); эпик `done` →
  `syncEpicWithMain:568`. **Пробел**: worktree на ветке эпика для
  архитектора/лидов автомат не создаёт — точка входа только через задачу.
- **Н-11. qalead используется в шести местах**: `leadFor:1858`,
  `phaseBugs:1588`, CLI `main.go:198-206` (+ `:301`), plan-режим
  `executor.go:580` (`AgentQALead`), `leadName:2033`, тесты
  `kanban_test.go:346`, `agents/tokeneconomy_test.go:144`; плюс ранжирование
  `epicPhase:1123`/`epicPhaseRank:1136` (infra → app → **test**).
- **Н-12. Прочие статусные места под правку**: `hasWork` (`kanban.go:610`,
  эпики `:655`), `pipelineIdle:1041`, `epicWorkable:701`,
  `recoverStuckTasks:1092` (`in_progress→ready`), `blockingDependencyEpic:
  1182-1187`, `nextLeadEpic:1211`, `phaseArchitectReview:812`,
  `phaseComplete:1647` (цепочка `:1663-1665`), `noteEpicProgress:1718`,
  `readyTasks:1730`, `server/chatassist.go:397`, UI: `board.ts`
  (`STATUS_ORDER:8`, `STATUS_LABEL:18`, `MOVES:32`), `Types.ts:7-14`,
  `Dashboard/styles.scss` (`.status-filters button.active.<status>:76-81`,
  `.grid-head.<status>:141-146`, карточка `&.in_progress:290`,
  `&.paused:299`), `Cell.tsx:40`, `TaskCard.tsx:19`, `TaskModal.tsx:45,
  258-259`, `Dashboard.tsx:90,187,204`, `App.tsx:544-574` (каскад паузы),
  `EpicActionBar.tsx:63-64`, `EpicModal.tsx:44-45`, `GateBanner.tsx:5`.

## Этап 1: Статусы `human_help` и `testing`

- [x] 1.1. `board/entity.go`: константы `StatusHumanHelp = "human_help"` и
      `StatusTesting = "testing"`; `Label` («Помощь человека», «На
      тестирование»); `Valid`; `Terminal` не меняется (`done|cancelled`).
- [x] 1.2. `ValidateTransition:87-123`: `human_help` — **боковая ветка**:
      вход из любого нетерминального статуса, выход в `ready` (помощь
      получена, задача в очередь) и `in_progress` (продолжаем). Ключевые
      рёбра: `in_progress → testing`, `testing → done` (только тестировщик),
      `testing → in_progress` (замечания). Соседность сохраняем по
      Р-6: `ready → human_help → in_progress`, `in_progress → testing →
      done`. Обновить комментарий `:79-84`.
- [x] 1.3. `board/store.go`: `SetTaskStatus`-хуки (`:661-698`) —
      `TaskDoneGuard`/`TaskDoneHook` остаются на `done` (их выполняет
      тестировщик: автокоммит его тестов + мёрдж задачи в эпик),
      `TaskInProgressHook` — на вход в `in_progress`; `attempts++` считать
      по-прежнему по входу в `in_progress` (возврат с testing — новая
      попытка).
- [x] 1.4. Каскад паузы (`store.go:315-406`): `pauseEpicTasks`/
      `resumeEpicTasks`/`setEpicTasksStatus` + `ResumeStatus` перевести с
      `paused` на `human_help`; `paused` удалить из `Valid`.
- [x] 1.5. `agents/planner/kanban.go`: `hasWork:610,655`,
      `pipelineIdle:1041`, `epicWorkable:701`, `recoverStuckTasks:1092`
      (`human_help` **не** трогать автоматически — только ручной выход),
      `blockingDependencyEpic:1182-1187`, `nextLeadEpic:1211`,
      `phaseArchitectReview:812`, `readyTasks:1730`,
      `noteEpicProgress:1718`, `phaseComplete:1647` (цепочка `:1663-1665`),
      баг-фильтры `phaseBugs:1569`.
- [x] 1.6. `escalateLoop:1521` и `setStopReason:141-152`: отказ бюджета
      эскалации → `StatusHumanHelp` + комментарий (этап 3) вместо
      `StatusPaused`; при непустом бюджете — как сейчас (`model_tier=large`
      + продолжение в `ready`).
- [x] 1.7. `server/chatassist.go:397` — список статусов ассистента.
- [x] 1.8. Тесты: `board/entity_test.go:145-174` (FSM), `board/store_test.go:
      251-362`, `server/epicstatus_test.go`, `kanban_pause_test.go` (→
      переименовать логику в human-help), `kanban_loop_test.go:107`,
      `kanban_autonomy_test.go:363`.

## Этап 2: Фронтенд — колонки и статусы

- [x] 2.1. `web/src/Components/Dashboard/board.ts`: `STATUS_ORDER:8` —
      порядок Р-6; `STATUS_LABEL:18`; `MOVES:32` →
      `Record<Status, {prev: Status[]; next: Status[]}>` (или два списка) —
      ручной перенос карточки проверяется по списку.
- [x] 2.2. `web/src/Types/Types.ts:7-14` — union типов статусов.
- [x] 2.3. `Dashboard/styles.scss`: фильтры (`:76-81`), шапка таблицы
      (`:141-146`), карточка (вместо `&.paused:299` — `&.human_help` и
      `&.testing:290`-семейство), цвета колонок.
- [x] 2.4. `Cell.tsx:40` (проверка дропа по `MOVES`), `TaskCard.tsx:19`,
      `TaskModal.tsx:45,258-259` (кнопки ←/→), `Dashboard.tsx:90,187,204`
      (фильтр `!== "paused"` → `!== "human_help"`), `App.tsx:544-574`
      (каскад паузы эпика), `EpicActionBar.tsx:63-64`,
      `EpicModal.tsx:44-45`, `GateBanner.tsx:5` (локальный `STATUS_LABEL`).
- [x] 2.5. Сборка фронта: `cd web && npm run build` (`tsc -b && vite
      build`); веб-тестов нет.

## Этап 3: Сущность «комментарии» (вместо инъекций)

- [x] 3.1. `board/comment.go`: структура `Comment{id, task_id, author, type,
      body, created_at}`, `type ∈ {qa, user, system}`; `Normalize`/`Validate`/
      `DecodeComments`. Поле `Task.Comments` добавлено в `board/entity.go`
      (JSON-теги).
- [x] 3.2. `board/store.go`: `SetTaskComments`/`AddTaskComment`/
      `RemoveTaskComment` по образцу методов для инъекций.
- [x] 3.3. REST `server/server.go`: CRUD комментариев
      `GET/POST/PUT/DELETE /api/projects/{id}/tasks/{tid}/comments` добавлен.
- [x] 3.4. Инструменты `tools/board_tools.go`: проп `comments` в
      `BoardUpdateTask` (полный список заменяет прежний) + отдельный
      `BoardAddComment` (автор — роль агента, тип — `qa`/`system`). Инструмент
      зарегистрирован в `tools/registry.go`.
- [x] 3.5. Доставка в модель: добавлен контекст/живой источник комментариев
      в `board/comment.go` (`AllComments`, `NewCommentContext`,
      `WithCommentSource`). В оркестраторе (`agents/planner/kanban.go`)
      комментарии задачи передаются в контекст (снапшот + живой источник из
      доски) — подготовка к интеграции с механизмом доставки.
- [ ] 3.6. Упразднение `Task.Injections` отложено (не ломать существующие тесты
      и обратную совместимость хранения).
- [ ] 3.7. UI: вкладка/секция «Комментарии» в `TaskModal.tsx` — не реализовано.

## Этап 4: Системный архитектор — ветка эпика и верхний уровень

- [x] 4.1. Рабочая точка: создание/подключение worktree на ветке эпика
      `ai/epic/<id>` для архитектора (сейчас worktree есть только у задачи,
      Н-10) — `server/gitflow_auto.go`/`gitflow.go`: хук на создание эпика
      или ленивое создание при первом запуске архитектора.
      Сделано ленивое создание: `epicWorktree` (резолвер `SetEpicOutputDir`,
      вызывается архитектором в `submit_architecture_backlog` после
      `EpicCreatedHook`) + `dropEpicWorktreeLocked` перед merge-операциями,
      которым нужен свободный checkout релизной ветки (`MergeFeature`,
      конфликтные worktree резолвов) — автокоммит правок перед снятием.
- [x] 4.2. `agents/architect/agent.go:41-49`: добавить `WriteFiles`
      (**гард области**: только папки 1-го уровня `./server`, `./client`,
      `./cicd`, `./docs` и корневые `readme.md`, `AGENTS.md`,
      `compose.yaml`, `Makefile`, `.env.example` — allowlist-проверка в
      инструменте, не только в промпте) и `Run` (git add/commit/push в
      ветку эпика + сборка). `BoardCreateTask` — если постановка задач лидам
      идёт напрямую; иначе достаточно бэклога (см. 4.4).
      Дополнительно: архитектор получил общие фрагменты
      `agents.RunTokenEconomy` + `agents.LSPGrepFallback` (добавлен в
      `runAgents` токен-теста).
- [x] 4.3. `architectureSystemPrompt:191`: новые обязанности — создать и
      запушить ветку эпика, выстроить структуру 1-го уровня и ключевые
      корневые файлы (каркас, не реализация), потом задачи лидам.
- [x] 4.4. Задачи лидам с **отсылками к файлам** («контракт в
      `./server/...`, см. `./docs/...`») вместо вставки структуры кода —
      будь то `SubmitBacklogToolName:34` → `phaseArchitect` или новый
      `BoardCreateTask`; единый формат описания — в `taskPrompt`.
- [x] 4.5. Форсмажор архитектора → «Помощь человека» + комментарий с
      анализом логов (`ReadAppLogs`/`Run`-логи) — механизм этапа 9.
      Форсмажор (`forceMajeure`/`architectFail` → `StatusHumanHelp` +
      `type=system` комментарий) готов; обогащение комментария анализом
      логов остаётся за 9.1.

## Этап 5: Лиды — скелетон в ветке эпика

- [x] 5.1. Снять `WriteReadmeOnly:true` (`backendlead/agent.go:92`,
      `devopslead/agent.go:80`, frontendlead), добавить `Run`
      (установка зависимостей, сборка, автостиль) в `backendLeadToolNames`
      и аналоги — иначе «обязан проходить билд» невыполним. Гард записи —
      общий `agents.SkeletonWriteAllowlist` (`agents/writeallowlist.go`)
      в инструменте; QA-лид остаётся readme-only (снимает Этап 8).
- [x] 5.2. Точка входа лидов — worktree на ветке эпика (4.1); пуш в
      `ai/epic/<id>`.
      Тот же резолвер `srv.epicWorktree` (как `runner.SetEpicOutputDir` в
      `server/session.go`): worktree создаётся лениво и пересоздаётся после
      снятия merge-путями (`dropEpicWorktreeLocked`).
      Подключение — `phaseLeads` после создания лида: `epicOutputDir(project,
      epic.TaskID)` → `SetOutputDir` (вызов на каждой итерации — резолвер
      лениво пересоздаёт worktree).
- [x] 5.3. `leadPrompt` (`kanban.go:1898`): переформулировать «Ты НЕ
      пишешь код» (`:1927`) → «Ты пишешь **только скелетон**: интерфейсы,
      абстрактные классы, каркасы, зависимости; реализация методов —
      запрещена»; контракты в коде (интерфейсы, аннотации, DTO);
      комментарии-подсказки в пустотах для разработчика, девопса и
      тестировщика; обязательный билд/сборка/автостиль перед пушем;
      описание задачи — отсылки к файлам, полный текст контракта в
      description больше не кладём (`:1925`). Секция скелетона и отсылки —
      условно (не-QA лиды), QA-лид сохраняет старый формат; те же правки в
      системных промптах `backendlead`/`frontendlead`/`devopslead`.
- [x] 5.4. Форсмажор лида → «Помощь человека» + комментарий (этап 9).
      Механизм готов: `leadFail` (`kanban.go`) — `setStopReason` +
      `SetEpicStatus(human_help)` (каскад `pauseEpicTasks` ставит задачи
      эпика на паузу), ошибка возвращается вызывающему; обогащение
      комментария к эпику анализом логов — за 9.1 (у эпиков комментариев
      пока нет).

## Этап 6: Разработчики — сдача в «На тестирование»

- [x] 6.1. `developer.go:kanbanStep:371` и `taskPrompt:1982`: финальный
      шаг — `SetTaskStatus(testing)`, не `done`; unit-тесты обязательны
      (`developer.go:384-402`), сборка/unit/автостиль — как сейчас.
- [x] 6.2. Изучение описания и **комментариев** задачи — в промпт
      (снапшот комментариев уже приходит из 3.5).
- [x] 6.3. Фолбэк оркестратора (`kanban.go:1444-1487`): «непонятный
      исход» → `testing` (как сейчас фактически `done`), «отказ гарда»
      (`:1466-1479`) → `human_help` + комментарий.
- [x] 6.4. Форсмажор (зацикливание, непонятный случай) →
      `StatusHumanHelp` + комментарий ошибки и анализа логов.

## Этап 7: Фаза `phaseTesting` и переработка тестировщика

- [ ] 7.0. **Предотвращение циклов между субагентами (dev ↔ qa).**
      Зацикливание случается не только внутри одного прогона, но и в
      большем масштабе: разработка → тестирование → разработка →
      тестирование … Разработчик делает небольшую правку, тестировщик
      каждый раз указывает на одну и ту же ошибку, и задача бесконечно
      крутится между `in_progress` и `testing`. Такие циклы, не дающие
      результата, нужно предотвращать: отслеживать повторяющиеся
      возвраты `testing → in_progress` с похожими замечаниями (отпечаток
      замечания, а не его текст — как у `verifyIdentity` в loop-breaker),
      и по достижении бюджета попыток останавливать задачу с
      `human_help` + комментарий `type=qa`/`system` с диагнозом (что
      именно повторяется и почему правки не помогают), а не давать ей
      крутиться дальше.
- [ ] 7.1. `phaseTesting` в `runPhases` после `phaseExecute`
      (`kanban.go:526-531`): кандидаты — задачи в `testing`;
      `SetTaskStatus` при взятии (аналог `phaseExecute:1336`), worktree
      задачи, снапшот+живые комментарии.
- [ ] 7.2. Исход прогона: замечания → комментарий (`type=qa`) +
      `testing → in_progress` (возврат разработчику, счётчик попыток растёт
      — повторная сдача); ок → `testing → done` (срабатывает
      `TaskDoneGuard`/`TaskDoneHook`: автокоммит тестов + мёрдж в эпик);
      форсмажор → `human_help` + комментарий.
- [ ] 7.3. `qaengineer` промпт (`agent.go:169-202`) — новая приёмка:
      согласованность контрактов и реализация интерфейсов лидов; отсутствие
      лишнего/мусора в diff; проверка/актуализация существующих тестов
      краевыми кейсами; создание/правка смок- или e2e-теста в рамках
      задачи; запуск авто-стилизатора, сборки, билда и тестов; фиксация
      результата комментарием.
- [ ] 7.4. Инструменты `qaBoardToolNames:28-30`: добавить
      `BoardAddComment` (3.4); `BoardSetTaskStatus` — переходы
      `testing→done|in_progress|human_help`.
- [ ] 7.5. `phaseBugs:1569`: триажёр `qalead.NewQALead:1588` →
      `qaengineer` (или `SpecialistForRole("qa")`); баги по-прежнему
      создаются `BoardCreateBug`, экспертиза — архитектор (`:1612`).

## Этап 8: Упразднение роли qalead

- [ ] 8.1. Удалить пакет `agents/qalead`; вычистить упоминания:
      `leadFor:1858` (qa-ветка → qaengineer), `phaseBugs:1588` (7.5),
      CLI `main.go:198-206` + `case` `:301`, plan-режим
      `executor.go:580` (`AgentQALead` → `AgentQAEngineer`),
      `leadName:2033`, `epicPhase:1125`/`epicPhaseRank:1136` (фаза `test`
      теперь принадлежит тестировщику — порядок infra → app → test
      сохранить).
- [ ] 8.2. Тесты: `kanban_test.go:346`,
      `agents/tokeneconomy_test.go:144`.
- [ ] 8.3. Документация: `docs/20-features/agents-and-roles.md` — роль
      снимается, флоу архитектор/лиды/разработчик/тестировщик описывается
      заново.

## Этап 9: «Помощь человека» как единый форсмажор

- [ ] 9.1. Единый сценарий: зацикливание (loop-breaker
      `LOOP_STATE_WARN/REPEATS`), непонятный случай, отказ эскалации —
      `StatusHumanHelp` + комментарий `type=system` с текстом ошибки и
      анализом логов (`ReadAppLogs`).
- [ ] 9.2. Каскады: эпик в `human_help` переводит зависшие подзадачи
      (перенос каскада из 1.4); возврат с `human_help` — только вручную из
      UI (кнопка в `TaskModal`/фильтр дашборда).
- [ ] 9.3. `recoverStuckTasks:1092` и `resetStaleInProgress` **не**
      автоснимают `human_help` — иначе авто-цикл заменит собой ручной
      разбор.
- [ ] 9.4. Хук выхода: при переводе из `human_help` задача получает
      комментарий-подтверждение (кто вернул и куда) — для аудита.

## Этап 10: Документация

- [ ] 10.1. `docs/20-features/agents-and-roles.md` (8.3) — новый харнесс.
- [ ] 10.2. `docs/20-features/kanban-board.md` — статусы, FSM, колонки,
      комментарии, фазы (`phaseTesting`), отсутствие qalead.
- [ ] 10.3. `docs/20-features/web-ui.md` — новые колонки и вкладка
      комментариев.
- [ ] 10.4. `docs/plans/README.md` — строка этого плана.

## Риски

| Риск | Что делать |
|---|---|
| `TaskDoneGuard`/`TaskDoneHook` на `done` выполняет уже тестировщик, а не разработчик: мёрдж в эпик и автокоммит захватывают чужой контекст | Гард не трогаем (он проверяет дерево/ветку, а не роль); тестировщик работает в том же worktree задачи — коммит его тестов происходит штатным `TaskDoneHook`. Проверить на одном сценарии: dev → testing → qa → done |
| Разработчик после замечаний возвращается в `in_progress` — возможен цикл dev↔qa | Счётчик `attempts` растёт на каждом входе в `in_progress`; бюджет как у существующей эскалации (3) → `human_help` (детально — 7.0: отпечаток повторяющихся замечаний) |
| Старые доски с полем `injections` | Миграцию не делаем (Н-5/3.6); доска — JSON в Redis, поле просто игнорируется новым кодом |
| FSM «боковой ветки» `human_help` ломает допущение «только соседние статусы» в `MOVES` и `Cell.tsx` | Этапы 1.2 и 2.1 вводят списки вместо одного соседа; дроп-проверка по списку |
| Архитектор/лиды пишут в общий эпик-веточный worktree параллельно с задачами | Задачи — в своих worktree от ветки эпика; эпик-ветка у архитектора/лидов своя (серийность фаз: phaseArchitect/phaseLeads до phaseExecute) |
| `gofmt`/сборка: новые файлы должны быть отформатированы | `gofmt -w` только на своих файлах (29 пред-существующих — не трогать) |

## Проверка

```bash
gofmt -l board/ server/ agents/ tools/ runner/
go build . ./agents/... ./tools/ ./board/ ./sandbox/ ./cmd/... ./server/ ./runner/ ./gitops/
go vet    . ./agents/... ./tools/ ./board/ ./sandbox/ ./cmd/... ./server/ ./runner/ ./gitops/
go test   . ./agents/... ./tools/ ./board/ ./sandbox/ ./cmd/... ./server/ ./runner/ ./gitops/
cd web && npm run build
```

Ручной E2E: эпик → архитектор пушит ветку и структуру → лиды создают
скелетон (билд зелёный) → задачи с отсылками → разработчики реализуют и
сдают в «На тестирование» → тестировщик комментарий/возврат и затем
«Готово» → «Помощь человека» при зацикливании с комментарием логов.

## Связанное

- [PLAN-2026-10-05-todo-kanban-rollback.md](PLAN-2026-10-05-todo-kanban-rollback.md) — FSM и статусы до этой перестройки
- [Домены и фичи](../20-features/README.md), [Канбан-доска](../20-features/kanban-board.md), [Роли агентов](../20-features/agents-and-roles.md)
