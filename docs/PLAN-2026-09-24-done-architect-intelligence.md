# План: «Умный Системный архитектор» — RAG, роли, глубина, смежные системы

Статус: **DONE с находками** (Ф-1..Ф-8 реализованы и верифицированы; ручной
E2E 2026-09-25: RAG-кнопка и декомпозиция с ролями пройдены, полный сценарий
недостижим из Web UI — см. `PLAN-2026-09-25-todo-e2e-findings.md`).
Формат — как остальные `PLAN-*.md`: текущее состояние (`file:line`), решения
пользователя, архитектурные решения, новые компоненты, этапы с чекбоксами,
верификация. Обновлять по мере выполнения (чекбоксы `[x]`), статус менять
только после зелёной верификации.

## Цель

Сделать агента «Системный архитектор» (`agents/architect`) **умнее и полезнее**
по шести направлениям, запрошенным пользователем:

1. **Активное использование RAG.** Архитектор должен опираться на векторную
   память проекта (семантические находки в промпте + инструмент `CodeSearch`),
   а если проект ещё не проиндексирован — **предложить пользователю построить
   индекс в фоне** (а не молча работать вслепую).
2. **Осознание ролей по содержанию проекта.** Архитектор должен определять,
   какие направления реально нужны: консольное/серверное приложение → не
   создавать эпики Frontend Lead; библиотека → упрощать набор лидов. Стек и
   состав проекта определять **по коду** (детект + RAG + LSP), а не по шаблону
   «всегда Go + React + 4 лида».
3. **Глубина декомпозиции.** Эпик должен учитывать всю вложенность функции:
   «кнопка» — это не только вёрстка, но и API-контракт, обработка на сервере,
   модель данных/БД, валидация, тесты, документация, деплой.
4. **Исследование смежного функционала.** Перед объявлением эпика, меняющего
   существующие разделы, архитектор обязан посмотреть **шире**: что уже
   реализовано рядом, как заденет смежный код — через `CodeSearch` (RAG),
   `LspDefinition/LspReferences/LspHover`, чтение файлов.
5. **Паттерны и корректность задачи.** Смотреть вперёд, применять паттерны
   проектирования, **задавать пользователю уточняющие вопросы**, если задача
   поставлена некорректно/противоречиво (AskUser), предлагать правильное
   решение вместо выполнения абсурдного ТЗ.
6. **Кросс-функциональная ценность.** Оценивая/изучая функционал, фиксировать,
   чем ещё он может помочь смежным разделам (оптимизации, новые полезные
   фичи) — и доносить это до лидов/пользователя.
7. **Эпики из чата — только через архитектора.** Эпики, созданные
   чат-ассистентом, всегда проходят анализ/ревизию Системного архитектора ДО
   декомпозиции лидами; чат-ассистент **не создаёт задачи лидам и
   специалистам** напрямую.

## Текущее состояние (исследование агента)

### Кто такой архитектор и как он встроен

- `agents/architect/agent.go:1-13` — публикует бэклог эпиков на общую
  Kanban-доску через инструмент `submit_architecture_backlog`; эпики
  декомпозируют лиды направлений.
- `agents/planner/kanban.go:580-614` `phaseArchitect` — оркестратор (KanbanRunner)
  запускает архитектора один раз, если эпиков на доске ещё нет.
- `agents/planner/kanban.go:1090-1091` — режим экспертизы бабрепортов
  (`.AsBugExpert()`), отдельный промпт.
- Сервер Web UI (`server/session.go:227`) тоже использует KanbanRunner; HITL-
  затворы (`HumanGate`, `SetGate`) уже блокируют цикл до решения человека, а
  `AskUser` (`server/ask.go:210`, мост `askTool`) — для ассистента чата.
- Чат-ассистент (`agents/chatassist/agent.go:24-35`) имеет **полный**
  write-набор доски: `BoardCreateEpic` + `BoardCreateTask`/`BoardUpdateTask`/
  `BoardDeleteTask` — может создать эпик с `architect_review=false` и назначать
  задачи специалистам **в обход архитектора**. Флаг `architect_review`
  (`tools/board_tools.go:384`) сейчас — только описание в схеме, ни один элемент
  раннера его не исполняет; `phaseArchitect` создаёт эпики только когда доска
  пуста (`kanban.go:580-614`), а board-only режим пропускает архитектора вовсе
  (`kanban.go:318`).

### Что есть у архитектора сейчас

| Сущность | Что есть сейчас → проблема |
|---|---|
| `agents/architect/agent.go:39-45` `toolNames` | `List, ReadFiles, LspDefinition/LspReferences/LspHover, BoardListEpics/BoardListTasks/BoardListBugs/BoardGetBug, BoardCreateEpic/BoardUpdateEpic/BoardDeleteEpic/BoardSetEpicStatus, BoardReviewBug`. **Нет** `CodeSearch` (RAG), **нет** `BoardGetEpic`/`BoardGetTask` (не может прочитать детали эпика/задачи), нет детекта стека, нет статуса RAG-индекса, нет `AskUser`/`IndexBackground` |
| `agents/architect/agent.go:77-88` `NewArchitectWithStore` | собирает `tools.Select(toolNames, tools.Deps{FileOps: ops, Board: store})`. **RAG никуда не передаётся** — `tools.Deps.RAG` пуст, `CodeSearch` недоступен |
| `agents/planner/kanban.go:596` | архитектор создаётся без RAG: `architect.NewArchitectWithStore(k.store.Project(), meta.Task, k.store)` |
| `agents/planner/kanban.go:42-68` `KanbanRunner` | поля `provider/store/gate/boardOnly/onStandby/outputDir/wake/log` — **нет** поля RAG и хуков для server-мостов |
| `agents/architect/agent.go:108-113` `RequiredToolFirstRound` | форсирует `submit_architecture_backlog` **первым** раундом — архитектор не может в первом раунде задать уточняющий вопрос или предложить индексацию (подсказку даёт раннер, но формат жёсткий) |
| `agents/architect/agent.go:138-195` `architectureSystemPrompt` | стек **жёстко** «Golang + React + Docker Compose + Kubernetes», всегда 4 лида, обязательный порядок «инфраструктура → приложение → тестирование». Для рефакторинга/работы с существующим не-Go/не-React проектом это некорректно; правил про RAG, глубину, смежные системы, паттерны и инсайты **нет** |
| `agents/architect/agent.go:200-219` `bugExpertSystemPrompt` | то же: без RAG-контекста и глубины при вердикте `fix` |
| `tools/registry.go:98-172` | `CodeSearch` (`:128`), `BoardGetEpic`/`BoardGetTask` (`:141`, `:145`) **уже зарегистрированы** — просто не выдаются архитектору |
| `tools/codesearch.go:29` + `tools/tool.go:34,38` | инструмент и слот `Deps.RAG` готовы; degrade в `skipped` с подсказкой `go run . index` |
| `agents/planner/ragcontext.go` / `agents/chatassist/ragcontext.go` | готовые образцы RAG-блока в промпте (`ragProjectBlock`, `assistantRAGBlock`) — переносим приём на архитектора |
| `stackdetect/stackdetect.go:30` | `DetectKind(dir)` уже определяет go/php/node/python по маркерам корня — нейтральный пакет, импортируем из `tools` |
| `runner/reindex.go:32-33,96-101` | авто-переиндексация RAG после мутаций (`RAG_AUTO_REINDEX`) уже есть — фоновая индексация не вызовет гонок с авто-обновлением |
| `main.go:240` | в CLI-канбане `rag.NewClientSafe(rag.Config{})` уже создаётся (для сжатия) — переиспользуем для `SetRAG` |
| `server/actions.go:115-177` + `server/ask.go:39` | готовые паттерны server-мостов (`ActionsBackend`, `AskBackend`) — по ним делаем `IndexBackground` и отдаём `AskUser` архитектору |

### Пробелы (что закрывает план)

1. Архитектор работает «вслепую»: без RAG и без чтения деталей эпиков/задач.
2. Не знает фактический стек/состав проекта — всегда штампует 4 лида Go+React.
3. Промпт не требует глубины декомпозиции и исследования смежного функционала.
4. Не может спросить пользователя (нет AskUser) и не может предложить
   фоновую индексацию (нет статуса индекса и инструмента-действия).
5. Не фиксирует кросс-функциональные инсайты.
6. Чат-ассистент создаёт эпики и задачи напрямую, минуя ревизию архитектора:
   флаг `architect_review` доски никем не обрабатывается, задачи можно завести
   сразу на специалиста.

## Решения пользователя (зафиксировано)

1. **RAG — по умолчанию включён у архитектора.** Блок «Релевантный код» в
   системный промпт (как у ассистента) + инструмент `CodeSearch`. Управление —
   env-флаг `RAG_ARCHITECT_CONTEXT` (по умолчанию вкл).
2. **Не проиндексирован → предложить в фоне.** Если `RagIndexStatus` говорит
   «индекс не построен» и у архитектора доступен мост `IndexBackground` —
   сначала спросить пользователя (AskUser: «Построить RAG-индекс в фоне?»),
   при согласии запустить фоновую индексацию и **продолжить проектирование**;
   если AskUser недоступен (консоль) — пометить в `architecture_summary`, что
   проект не проиндексирован, и работать через `ReadMap/ReadFiles/LSP`.
3. **Роли — по факту проекта.** Использовать `DetectStack` (детект типа +
   наличие frontend/backend/infra/test дирректорий); назначать только лидов
   реально задействованных направлений. Задача-консолька → без Frontend Lead.
4. **Глубина обязательна, но без переусложнения.** Эпик обязан называть все
   слои: UI → API → модель/БД → валидация → тесты → деплой; при KISS-ограничении
   — осознанный минимум, а не «просто кнопка».
5. **Смежные системы исследовать до публикации.** Правило-чек: перед эпиком,
   меняющим существующий функционал, `CodeSearch`/LSP/ReadFiles дают список
   затрагиваемых модулей; они фиксируются в description эпика.
6. **Корректность задачи и паттерны.** При противоречивом/невыполнимом ТЗ —
   AskUser (уточняющий вопрос с вариантами и recommended) ДО публикации
   бэклога; в `architecture_summary` обосновывать выбор паттернов.
7. **Кросс-возможности** — отдельное опциональное поле `opportunities` в
   `submit_architecture_backlog` (список: направление + оптимизация/фича),
   доезжает в `Summary` эпиков и видно лидам/пользователю.
8. В консоли (нет серверных мостов) всё это деградирует без падений: без
   AskUser/IndexBackground архитектор работает автономно (как сейчас).
9. **Эпики из чата — всегда через ревизию архитектора.** У чат-ассистента
   убираем write-инструменты задач (`BoardCreateTask`/`BoardUpdateTask`/
   `BoardDeleteTask`) — он создаёт только эпик верхнего уровня (черновик
   для архитектора).
   Каждый чат-эпик помечается «требует ревизии» и попадает лиду на
   декомпозицию только после анализа/корректировки Системным архитектором —
   в том числе в board-only режиме (архитектор больше не «только когда доска
   пуста»).

## Архитектурные решения

### Р-1: RAG у архитектора (инструменты + контекст в промпте)
- `NewArchitectWithStore` не меняем сигнатуру (тесты), добавляем поле
  `RAG tools.RAGSearcher` и метод `SetRAG(r)` (по образцу
  `Executor.SetRAG`, `agents/planner/executor.go:104-108`).
- `toolNames` дополняем: `tools.CodeSearch`, `tools.RagIndexStatus`,
  `tools.DetectStack`, `tools.BoardGetEpic`, `tools.BoardGetTask`.
- Новый `agents/architect/ragcontext.go`: `architectRAGBlock` (по образцу
  `agents/chatassist/ragcontext.go:38-56`) — семантическая выборка по тексту
  задачи, блок «Релевантный код по задаче» в системный промпт требований
  архитектора и режима экспертизы багов.

### Р-2: статус RAG-индекса и фоновая индексация
- `rag/status.go`: `ProjectInfo(ctx, project, scope)` через Count API Qdrant
  (`QdrantStore` в `rag/client.go:55-62` дополняется `Count`); возвращает число
  чанков и список файлов (приблизительно). Непроиндексированный проект — 0
  чанков (чёткий признак, в отличие от пустой выдачи `Search`).
- `tools/ragstatus.go`: инструмент `RagIndexStatus` (deps `FileOps+RAG`) →
  `{"status":"ok","indexed":bool,"chunks":n}`; при nil-RAG/Qdrant — `skipped` с
  подсказкой (degrade, как `CodeSearch`).
- `server/actions.go`: мост `IndexBackground` (безопасный, подтверждения не
  требует) поверх нового метода `ActionsBackend.IndexBackground(ctx)`;
  реализация `Session::IndexBackground` запускает горутину: `rag.WalkProject` →
  `IndexProject` → отчёт в лог проекта и `chat.RoleStatus` (прогресс: файлы/
  чанки). Опционально REST `POST /api/projects/{id}/index`.
- Герметичность: фоновая индексация идемпотентна (`IndexProject` сперва
  удаляет старые точки проекта), не конфликтует с `RAG_AUTO_REINDEX`
  (`runner/reindex.go:33`).

### Р-3: определение стека и набора ролей
- `tools/stacktool.go`: инструмент `DetectStack` (read-only, deps `FileOps`):
  `stackdetect.DetectKind` + эвристика состава (дирректории `server/`,
  `backend/`, `frontend/`, `web/`, `infra/`, `deploy/`, `tests/`, `scripts/`;
  маркеры `package.json` + react/vue/next, `docker-compose.yml`,
  `*.k8s/*.yaml`, `README`) → `{kind, stack, roles: {frontend, backend,
  devops, qa}, summary, markers}`. Никакой сети, всё детерминированно.
- Промпт: «Сначала `DetectStack` → назначай эпики только реально нужным лидам.
  Для нового проекта применяй стек из задания, если он не противоречит
  обнаруженному».

### Р-4: промпт — глубина, смежные системы, паттерны, инсайты
Переписываем `architectureSystemPrompt` (`agent.go:138-195`) и
`bugExpertSystemPrompt` (`:200-219`):
- секция «Глубина декомпозиции»: эпик перечисляет все слои и контракты;
- секция «Исследование смежного функционала»: обязательный этап
  `CodeSearch`/LSP/ReadFiles перед эпиком-изменением + список затрагиваемых
  модулей в description;
- секция «Корректность задачи и паттерны»: при противоречии ТЗ — AskUser
  (если доступен) или явные допущения в `architecture_summary`;
- секция «Кросс-функциональные возможности»: `opportunities` в бэклоге;
- стек больше не жёсткий: «бери фактический стек проекта из `DetectStack`/
  файлов; задай пользователю вопрос о стеке, если он не ясен».

### Р-5: возможность спрашивать пользователя в фазе архитектора
- Формат ответа смягчаем: `RequiredToolFirstRound` оставляем, но разрешаем
  предварительные инструменты чтения/AskUser; runner уже умеет группы
  обязательных инструментов (`AgentRequiredGroups`,
  `runner/runner.go:518-529`) — `submit_architecture_backlog` переводим в
  группу обязательных к концу цикла, а не «строго первый вызов».
- Server-инъекция мостов в архитектора: `KanbanRunner.SetArchitectExtras(
  extra ...tools.Tool)` (по образцу `Assistant.AddTools`,
  `agents/chatassist/agent.go:194`); `sess.start`
  (`server/session.go:227`) передаёт `askTool{sess}` +
  `IndexBackground`. Хуки применяются в `phaseArchitect` и в фазе экспертизы.

### Р-6: RAG в раннер
- `KanbanRunner.SetRAG(r tools.RAGSearcher)` (как `Executor.SetRAG`);
  `phaseArchitect` и фазa экспертизы делают `.SetRAG(k.rag)`. CLI (`main.go:240`)
  и сервер (`sess.start`) передают `rag.NewClientSafe(rag.Config{})`.

### Р-7: эпики из чата всегда проходят ревизию архитектора
- Чат-ассистент: из `assistantToolNames` (`agents/chatassist/agent.go:33`)
  убираем `BoardCreateTask`/`BoardUpdateTask`/`BoardDeleteTask` — задачи лидам и
  специалистам напрямую из чата не создаются; назначать специалистов может
  только архитектор (бэклог) и лид (декомпозиция эпика).
- `board/entity.go:156-177`: у `Epic` новое поле `RequiresReview bool` —
  признак «ожидает ревизии архитектора». Безопасный дефолт для новых записей
  на доске — `RequiresReview=true`; архитектор в бэклоге `submit_architecture_backlog`
  создаёт эпики с явным `RequiresReview=false` (свои планы ревизии не требуют).
- `architect_review` в `BoardCreateEpic` (`tools/board_tools.go:384`) становится
  **инвариантом канала чата**, а не опцией модели: chat-эпик всегда
  `RequiresReview=true` (промпт + дефолт), даже если модель решила иначе.
- Новая фаза `phaseArchitectReview` в `runPhases`
  (`agents/planner/kanban.go:315-378`, ДО `phaseLeads`), работает и в board-only,
  и в обычном режиме: собирает эпики с `RequiresReview`, запускает архитектора
  в режиме ревью (новый режим `.AsReviewer()`, по образцу `.AsBugExpert()`) —
  тот читает `BoardGetEpic`/`BoardGetTask`, исследует код (RAG/CodeSearch/LSP),
  при необходимости правит эпик `BoardUpdateEpic` (глубина, контракты, смежные
  модули, роли, `architecture_summary`) — и снимает флаг (эпик «готов к
  декомпозиции», `LeadSyncedRev` синхронизируется).
- `nextLeadEpic` (`agents/planner/kanban.go:859-884`): эпики с `RequiresReview`
  пропускаются — декомпозиция лидом только после ревизии.
- Промпт ассистента (`agents/chatassist/agent.go:53-55`): «Эпик — черновик для
  Системного архитектора, не план для лидов»; строфа «СВОДКА ДЛЯ ЗАДАЧИ» и
  опция «ревью не требуется» удаляются.
- Ревизия — не одноразовый акт: если архитектор позже меняет эпик
  `BoardUpdateEpic`, растёт `Revision` и лид делает ре-синк задач
  (уже заложено в `nextLeadEpic` `needResync`, `kanban.go:871`).

## Новые компоненты

| Файл | Назначение |
|---|---|
| `rag/status.go` | `ProjectInfo` (Count API) + расширение `QdrantStore` (`Count`) |
| `tools/ragstatus.go` | инструмент `RagIndexStatus` → {indexed, chunks}; degrade → skipped |
| `tools/stacktool.go` | инструмент `DetectStack` → {kind, roles, markers} |
| `agents/architect/ragcontext.go` | RAG-блок «Релевантный код по задаче» в промпт архитектора/эксперта |
| `agents/architect/agent.go` (правка) | `RAG`, `SetRAG`, расширение `toolNames`, промпты (Р-4), смягчение `RequiredToolFirstRound` (Р-5), опциональное поле `opportunities` в схеме `submit_architecture_backlog` |
| `agents/planner/kanban.go` (правка) | `SetRAG`, `SetArchitectExtras`, передача их архитектору в обеих фазах |
| `agents/planner/kanban.go` (правка, Р-7) | фаза `phaseArchitectReview`, режим ревью `.AsReviewer()`, пропуск эпиков `RequiresReview` в `nextLeadEpic` |
| `board/entity.go` (правка) | `Epic.RequiresReview` + статус «ожидает ревизии» (Ф-8) |
| `agents/chatassist/agent.go` (правка) | без task-write доски; промпт «эпик = черновик для архитектора» (Ф-8) |
| `server/actions.go` (правка) | мост `IndexBackground` + `ActionsBackend.IndexBackground` |
| `server/ragindex.go` | фоновая индексация: walk + `IndexProject` + отчёт в чат/лог |
| `main.go` (правка) | CLI-канбан: `runner.SetRAG(ragClient)` |
| `docs/PLAN-2026-09-24-done-architect-intelligence.md` | этот план |

## Интеграции с существующим кодом

- **`tools.Deps`** (`tools/tool.go:34-38`) уже имеет `RAG` — заполняем его в
  архитекторе. `tools.Set.Add` (`tools/registry.go:48-60`) уже позволяет
  серверу инжектировать мосты (`AskUser`, `IndexBackground`) без изменения
  реестра (паттерн `Assistant.AddTools`).
- **`tools/registry.go`** — `newTool` дополняется ветками `RagIndexStatus` и
  `DetectStack` (оба — через `FileOps`/`RAG`, без новых зависимостей).
- **`rag/client.go`** — интерфейс `QdrantStore` (+`Count`); hermetic-тесты
  обновляются fake-реализацией (как в `PLAN-2026-09-19-done-qdrant.md`).
- **KanbanRunner** — RAG/мосты опциональны (nil-safe): консоль без Qdrant и
  без сервера работает как сейчас (degrade, как ЛСП/CodeSearch).
- **Board** — у `Epic` появляется `RequiresReview`; write-инструменты задач
  (`BoardCreateTask` и др.) остаются у архитектора и лидов, из чата —
  изымаются (`assistantToolNames`); ревизия в board-only режиме не требует
  серверных мостов: вопрос пользователю (AskUser) — опциональный degrade.
- **REST** — новых обязательных маршрутов нет; опционально
  `POST /api/projects/{id}/index` (для кнопки в UI рядом с «Продолжить»).
- **AskUser** (`server/ask.go`) переиспользуется без изменений: `Session`
  уже реализует `AskBackend`; мост блокирует агентский цикл до ответа (тот же
  механизм, что у чат-ассистента).

## API (изменения)

```
submit_architecture_backlog (схема)  — новое опциональное поле:
    "opportunities": [{ "target_role": "…", "suggestion": "…" }]   # кросс-фичи/оптимизации
RagIndexStatus (новый инструмент):
    {} → {"status":"ok","indexed":bool,"chunks":int}
    {отсутствует RAG/Qdrant} → {"status":"skipped","message":"…"}
DetectStack (новый инструмент):
    {} → {"status":"ok","kind":"go|node|php|python","roles":{…},"markers":[…]}
IndexBackground (server-мост, безопасный):
    {} → {"status":"success","message":"Индексация запущена в фоне"}
[опц.] POST /api/projects/{id}/index — запустить фоновую индексацию (REST)
```

## Этапы и чеклист

### Ф-1 — RAG у архитектора (инструменты + контекст)
- [x] `rag/status.go`: `ProjectInfo` (Count API), расширить `QdrantStore` в
      `rag/client.go:55-62` методом `Count`; hermetic-тесты (fake-хранилище)
- [x] `tools/ragstatus.go`: `RagIndexStatus` (deps `FileOps+RAG`), degrade →
      skipped с подсказкой; регистрация в `tools/registry.go:newTool`
- [x] `agents/architect/agent.go`: поле `RAG tools.RAGSearcher` + `SetRAG`;
      `toolNames` += `CodeSearch`, `RagIndexStatus`, `BoardGetEpic`, `BoardGetTask`
- [x] `agents/architect/ragcontext.go`: `architectRAGBlock` (по образцу
      `agents/chatassist/ragcontext.go:38`), env `RAG_ARCHITECT_CONTEXT`
      (по умолчанию вкл)
- [x] Промпт: «Изучи проект через CodeSearch/RAG; если вернулся skipped со
      словом про индекс — проверь `RagIndexStatus`»
- [x] Тесты: набор инструментов архитектора содержит `CodeSearch`/`RagIndexStatus`/
      `BoardGetEpic`/`BoardGetTask`; RAG-блок в промпте при fake-поиске; без RAG
      (nil) — промпт без блока, агент жив

### Ф-2 — Определение стека и ролей
- [x] `tools/stacktool.go`: `DetectStack` (stackdetect + эвристика состава);
      регистрация в реестре; hermetic-тесты на синтетических проектах
      (консоль без frontend/, монорепо с server+frontend, php/laravel)
- [x] `toolNames` += `DetectStack`; промпт: «Сначала DetectStack; назначай
      эпики только нужным лидам; при неясном стеке — AskUser»
- [x] Промпт: убрать жёсткий «всегда Go+React+4 лида»; для существующего
      проекта стек берётся из кода
- [x] Тест (fake provider): «консольное приложение на Go» → бэклог не
      содержит эпиков Frontend Lead

### Ф-3 — Глубина декомпозиции + смежные системы
- [x] Промпт, секция «Глубина декомпозиции» (слои UI→API→модель→тесты→деплой;
      KISS-минимум, а не «просто кнопка»)
- [x] Промпт, секция «Исследование смежного функционала»: перед эпиком-
      изменением CodeSearch/LSP/ReadFiles → список затрагиваемых модулей в
      description (+ зависимости)
- [x] У архитектора `toolNames` уже есть LSP; при необходимости расширить
      чтение `BoardGetEpic`/`BoardGetTask` (Ф-1) промптом «смотри детали перед
      правкой контрактов»
- [x] Hermetic-тест промпта: грepируется наличие фраз «слои», «CodeSearch»,
      «затрагиваемых», «AskUser»

### Ф-4 — Корректность задачи, паттерны, AskUser
- [x] `runner/runner.go` (если нужно): `RequiredToolFirstRound` архитектора →
      разрешены предварительные чтения/AskUser (группа обязательных
      инструментов, по образцу `RequiredToolGroups`); функционально runner уже
      умел группы — обновлены только doc-комментарии `ToolRequiringAgent`
- [x] `agents/planner/kanban.go`: `KanbanRunner.SetArchitectExtras(...)`;
      передать extras в `phaseArchitect` (`:596`), ревизию (`phaseArchitectReview`)
      и фазу экспертизы (`:1090`)
- [x] `server/session.go:227`: `sess.start` передаёт runner-у
      `SetRAG(ragClient)` и `SetArchitectExtras(askTool{sess})` (`&askTool{b: sess}`)
- [x] Промпт: «если ТЗ противоречиво/невыполнимо/не соответствует проекту —
      задай уточняющий вопрос AskUser (с рекомендуемым вариантом) ДО
      публикации; иначе — явные допущения в architecture_summary»; секция
      «Паттерны» (REST/12-factor/KISS, анти-ГрафQL/devcontainer)
- [x] Тесты: fake-провайдер вызывает AskUser до `submit_architecture_backlog`;
      без AskUser (консоль) — архитектор автономен; grep промпта по
      «противоречиво»/«допущения»/«Паттерны»/«12-factor»/«GraphQL»/«devcontainer»;
      AskUser-нотки в bug/review-промптах

### Ф-5 — Фоновая индексация RAG
- [x] `server/ragindex.go`: фоновая горутина (walk + `IndexProject`) + отчёт в
      `chat.RoleStatus`/лог; `server/actions.go`: мост `IndexBackground`
      (безопасный, без подтверждения) + `ActionsBackend.IndexBackground`
- [x] `sess.start` передаёт `IndexBackground` в extras архитектора
      (`SetArchitectExtras(&askTool{b: sess}, newIndexBackgroundTool(sess))`)
- [x] Промпт: «если `RagIndexStatus` показывает не проиндексирован — AskUser
      „Построить RAG-индекс в фоне?" → при «да» вызови `IndexBackground` и
      продолжай проектирование; при «нет»/недоступности — работай
      ReadMap/ReadFiles/LSP и пометь в architecture_summary, что RAG пуст»
- [x] Опц.: `POST /api/projects/{id}/index` + кнопка в Web UI (EntryPoint —
      `server/server.go`, `web/src/Components/Dashboard`) — сделано: REST
      `handleProjectIndex` (409 при идущей, 503 при недоступном RAG) +
      кнопка «Индекс RAG» в `web/src/App.tsx` (head-actions)
- [x] Тесты: мост `IndexBackground` запускает индексацию (fake-walker/фейк
      Qdrant), не блокирует цикл; повторный запуск идемпотентен; два запуска
      подряд при идущей индексации отклоняется (single-flight)

### Ф-6 — Кросс-функциональные инсайты
- [x] `submit_architecture_backlog`: опциональное поле `opportunities`
      (список `{target_role, suggestion}`) в схеме (`agent.go:236-271`) и
      сериализации; сохранение в `Summary` эпиков (как складывается
      `architecture_summary`); валидация обязательных полей — запись без
      role/suggestion деградирует в `skipped_opportunities` (не валит бэклог)
- [x] Промпт: «фиксируй возможности оптимизации/новые фичи для смежных
      направлений (opportunities)» — в основной фазе (секция
      «КРОСС-ФУНКЦИОНАЛЬНЫЕ ВОЗМОЖНОСТИ») и экспертизе багов (пометка
      «opportunities: <роль> — <предложение>» в описании эпика исправления)
- [x] Тесты: парсинг предложений, round-trip в доску (Summary эпиков),
      опциональность (старые вызовы без поля валидны), скип грязных записей

### Ф-7 — Верификация и полировка
- [x] `go build . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/`
- [x] `go vet  . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/`
- [x] `go test . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/`
      (падают только 3 пред-существующих флака ассистента —
      `TestChatAssistantCreatesBugAndTask`, `TestChatAssistantDeleteTaskAfterConfirm`,
      `TestChatAssistantPublishesBoardWhenIdle` — падают и на чистой базе;
      не связаны с Ф-4..Ф-6)
- [x] `npm run build` (web/) — зелёный (включая кнопку «Индекс RAG» Ф-5)
- [ ] Ручной E2E на реальном проекте (2026-09-25, Web UI + CLI, `e2e-makefile-app`,
      `e2e-console-app`): кнопка RAG и декомпозиция проверены, полный сценарий
      недостижим из Web UI (см. новый план `PLAN-2026-09-25-todo-e2e-findings.md`).
      - ПРОЙДЕНО (Ф-5, кнопка): «Индекс RAG» на `e2e-makefile-app` →
        `RAG-индекс проекта обновлён: файлов 10, чанков 28, ошибок 0`;
      - ПРОЙДЕНО (декомпозиция, CLI `ai kanban`): архитектор создал
        `ARCH-01 Makefile проекта` (Backend Lead), `ARCH-02 CSV-экспорт задач`
        (Backend Lead), `ARCH-03 Валидация статуса в CLI` (Backend Lead),
        `ARCH-04 CI/CD пайплайн` (DevOps Lead), `ARCH-05 Тесты на экспорт и
        валидацию` (QA Lead) — консольный проект, Frontend Lead не создан;
      - БЛОКЕР 1: Web UI не доходит до main-фазы архитектора —
        `server/session.go:247` всегда `SetBoardOnly(true)`, а
        `agents/planner/kanban.go:360` пропускает `phaseArchitect`; чат создаёт
        черновик с `requires_review`, и его ревизует архитектор, но обязательного
        эпика «Makefile проекта» этот путь не создаёт;
      - БЛОКЕР 2: в чат-ревизии `RagIndexStatus` вернул
        `{"chunks":0,"indexed":false}`, `IndexBackground` не вызывался; в CLI
        AskUser/IndexBackground не подключены вовсе — согласие пользователя на
        фоновую индексацию проверить не удалось;
      - БЛОКЕР 3: цикл упал на `Kanban-цикл 2: нет прогресса` (см. план
        находок: задача `DOL-01` зависит от эпика `ARCH-01`, а лид не
        разбирает новый эпик, пока есть незавершённые задачи).

### Ф-8 — Эпики из чата: обязательная ревизия архитектора
- [x] `board/entity.go`: `Epic.RequiresReview` (сериализация, статус «ожидает
      ревизии»); безопасный дефолт новых записей — `true`; бэклог архитектора
      `submit_architecture_backlog` — явный `false`
- [x] `tools/board_tools.go:384,397`: `architect_review` для чат-эпиков —
      принудительно `true` (инвариант канала чата), независимо от решения
      модели; `CreateEpic` выставляет `RequiresReview`
- [x] `agents/chatassist/agent.go:33`: убрать `BoardCreateTask`/
      `BoardUpdateTask`/`BoardDeleteTask` из `assistantToolNames`; из промпта
      удалить «СВОДКА ДЛЯ ЗАДАЧИ» и опцию «ревью не требуется»; заменить на
      «эпик — черновик для Системного архитектора»
- [x] `agents/planner/kanban.go`: фаза `phaseArchitectReview` (в `runPhases`
      ДО `phaseLeads`, включая board-only); режим архитектора `.AsReviewer()`;
      `nextLeadEpic` пропускает эпики `RequiresReview`; после ревизии флаг
      снимается и `LeadSyncedRev` синхронизируется
- [x] `KanbanRunner.SetArchitectExtras` (Ф-4/Р-5) передаёт `AskUser`/
      `IndexBackground` и в режим ревью — архитектор может уточнить ТЗ
      у пользователя до публикации лидам (prepareArchitect применяется в
      phaseArchitectReview и фазе экспертизы)
- [x] Промпт архитектора (режим ревью): «проанализируй эпики, созданные
      в чате: корректность ТЗ, стек (Р-3), глубина (Р-4), смежные модули;
      скорректируй `BoardUpdateEpic` ДО декомпозиции»
- [x] Тесты: (1) у чат-ассистента нет task-write инструментов (запрос на
      создание задачи → предложить эпиком/skipped); (2) chat-эпик
      `RequiresReview` не декомпозируется лидом до ревизии; (3) после ревизии
      архитектора — декомпозируется; (4) ревизия работает и в board-only,
      и в обычном режиме; (5) бэклог архитектора не попадает в ревизию
      (свой план не ревьюится собой)

## Верификация

```
go build . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/
go vet  . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/
go test . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/
npm run build   # web/ (при UI-части Ф-5)
```

Известные нюансы репозитория (не убирать):
- `go build ./...` падает на `temp/moon-distance/internal/middleware/cors.go`
  (артефакт сгенерированного проекта) — использовать перечень выше.
- Не форматировать `agents/acceptor/checks.go`, `agents/acceptor/run.go`.
- Комментарии/промпты/UI-тексты — на русском.
- Все новые возможности архитектора — опциональные (nil-safe): консоль
  (без RAG/AskUser/IndexBackground) не должна падать, degrade как у ЛСП и
  CodeSearch (skipped, а не error).
- Модель обязана по-прежнему завершать цикл вызовом `submit_architecture_backlog`
  (или `BoardReviewBugReport` в режиме экспертизы).

## Связанные планы

- `PLAN-2026-09-19-done-lsp.md` — LSP-навигация (LspDefinition/LspReferences/LspHover) уже у
  архитектора; Ф-3 опирается на неё для исследования смежного функционала.
- `PLAN-2026-09-19-done-qdrant.md` — RAG/Qdrant: фоновая индексация наследует идемпотентность
  `IndexProject`, авто-переиндексация (`runner/reindex.go`, `RAG_AUTO_REINDEX`).
- `PLAN-2026-09-21-todo-assistant.md` — паттерн server-мостов (`ActionsBackend`, `AskUser`) и
  RAG-блока в промпте (Ф-1/Ф-3 ассистента) — первоисточник для этого плана.
- `PLAN-2026-09-22-done-dashboard-chat-create.md` — сводка перед созданием эпиков и поле
  «ревью архитектора» доски; AskUser-поток архитектора продолжает его.
- `PLAN-2026-09-19-todo-epic-task-token.md` — прогноз/факт токенов по эпикам: правила ролей
  эпиков (кто назначен) пригодятся для прогноза.

## Как продолжить

Есть лог от предыдущей работы ./docs/ses_f32ffb918ffevT6sgwoZ3LNN0Q.json

1. Открыть этот файл, прочитать «Решения пользователя» и «Этапы и чеклист».
2. Взять первый незачёркнутый пункт Ф-1, выполнить, отметить `[x]`, закоммитить.
3. После каждой фазы — верификация (блок выше) и краткая запись в этот файл.
4. Ф-1..Ф-4 — фундамент (RAG/стек/промпт), Ф-5..Ф-6 — UX-надстройки; порядок
   менять можно, но релиз каждой фазы должен проходить верификацию.
5. Ф-8 (ревизия чат-эпиков) опирается на Ф-4 (`SetArchitectExtras`) и на
   RAG/стек из Ф-1/Ф-2; минимальный срез Ф-8 без надстроек — флаг
   `RequiresReview` + фаза `phaseArchitectReview` + убрать task-write из чата.