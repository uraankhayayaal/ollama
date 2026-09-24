# План: Git-workflow «эпик = релизная ветка, задача = фича-ветка, кнопка «Залить в main»»

Статус: **РЕАЛИЗОВАНО** (Ф-1..Ф-5 выполнены). Формат — как в `PLAN-2026-09-17-wip-webui.md` / `PLAN-2026-09-19-done-lsp.md`:
статус, решения, привязка к текущему коду, этапы с чекбоксами, верификация.
Обновлять файл по мере выполнения этапов (чекбоксы `[x]`).

## Цель

Превратить текущую «одну фича-ветку на весь проект» в полноценный git-workflow
по уровням:

- **эпик = релизная ветка** (`ai/epic/<epic_id>`, база = main);
- **каждая задача = своя ветка** (`ai/task/<task_id>`, база = ветка эпика),
  изолированная и влитая в ветку эпика;
- после **done**-статуса эпика в UI появляется ручная кнопка
  **«Залить в main»** (человек сам нажимает);
- если конфликтов нет — релизная ветка сразу вливается в main;
- если есть — работает **инструмент авто-решения конфликтов**: вливаем main
  в релизную ветку, решаем конфликты там, затем обязательно тестируем
  (детерминированная приёмка) и только потом вливаем в main.

Важно: сам «main» уже существует — это текущий контур приёмки через
MR/PR (`server/git.go`); инкриминг (incoming) нужно аккуратно встроить в него.

## Текущее состояние (что уже есть)

| Файл / сущность | Что есть сейчас |
|---|---|
| `server/git.go:27` `handleOpenGitProject` | клонирует репозиторий в `temp/<имя>`, создаёт **одну** ветку `ai/<имя>` от базовой ветки (default), реестр `workspace.Info` хранит внешние `GitBranch`/`GitBase` |
| `gitops/gitops.go` | `Clone`/`Worktree`/`BranchInPlace`, `Commit`, `Push`/`PushTo` (токен в URL), `Dirty`, `RejectBranch`, `Diff`. **Нет**: `merge`, `rebase`, детекции/резолва конфликтов, `merge-tree` |
| `server/git.go:287` `handleAccept` | commit + push проектной фича-ветки + `forges.CreateMergeRequest(source=фича, target=база)` |
| `server/git.go:372` `handleRejectBranch` | удаление фича-ветки на remote + сброс на базу |
| `workspace/registry.go:36` `Info` | один `GitRemote/GitBranch/GitBase` на **проект**; о ветках эпиков/задач реестр не знает |
| `board/entity.go:144` `Epic`, `board/store.go:430` `DeleteEpic` | у эпика/задачи нет поля ветки git; статусы `new→analysis→ready→in_progress→done` (`ValidateTransition`) |
| `web/src/Components/Dashboard/EpicActionBar/EpicActionBar.tsx` | быстрые кнопки под названием эпика (сейчас — только «Удалить») — сюда же ляжет «Залить в main» |
| `web/src/Components/Diffboard/Diffboard.tsx` | существующий контур приёмки «Принять → MR» / «Отклонить ветку» |
| `agents/acceptor/` | детерминированная приёмка без LLM (сборка → запуск → логи) — ею тестируем после мёрджа |

Ограничение модели: ветки у нас реально «живут» в одном клоне `temp/<имя>`
(branch-in-place). Поэтому пер-эпиковый/пер-задачный режим делается **внутри
этого клона** через `git checkout -b`, а все git-команды — через абстракцию
`gitops.Executor` (dry-run в hermetic-тестах).

## Решения пользователя (зафиксировано)

1. Ветки эпиков и задач создаются **от main / от ветки эпика** с логичным
   префиксом по ID: `release/<epic_id>` (или `ai/epic/<epic_id>`) для эпиков,
   `ai/task/<task_id>` для задач.
2. Каждая задача решается в **изолированной** ветке и вливается в ветку своего
   эпика (релизную) — автоматически при достижении done либо кнопкой в UI.
3. Кнопка «Залить в main» — **ручная**, показывается только у эпика со статусом
   `done` (в `EpicActionBar`).
4. Merge без конфликтов → сразу в main; с конфликтами → вливаем main в релизную
   ветку, решаем конфликты (авто-инструмент + LLM-дотачка), **обязательно
   приёмка** и только потом финальный merge в main.

## Новые компоненты

| Пакет / файл | Назначение |
|---|---|
| `gitops/merge.go` | `git merge --no-ff`, ff-детект, `git merge-tree` (детекция конфликтов без изменения рабочей копии), маппинг конфликтных путей, `git checkout --ours/theirs` + notes |
| `gitops/conflicts.go` | авто-резолв: для mergeable (бинарно тривиальных, гуманитарных) путей — авто; остальное — контекст для LLM-инструмента (см. ниже) |
| `workspace/branches.go` (или поле в `Info`) | реестр «epic_id/task_id → ветка/база/статус слияния»; сериализация в реестре workspace |
| `server/gitflow.go` | REST: создать ветку эпика/задачи (по статусным событиям), мёрдж задачи в релиз, «Залить в main», статус слияния |
| `tools/gitresolve.go` | инструмент агента `ResolveGitConflicts` (по конфликтному диффу правит файлы, запускает приёмку) |
| `web/.../EpicActionBar` | кнопка «Залить в main» + индикатор «конфликты, решается…» |
| `docs/PLAN-2026-09-20-done-dashboard-workflow.md` | этот план |

## Интеграции с существующим кодом

- **gitops** — `Repo` уже знает `Root/Branch/Base/Remote`; добавляем методы
  `MergeBranch`, `HasConflicts`, `ConflictingFiles`, `MergeBranchIn` (main→release),
  `RebaseFeature`. Push — переиспользуем `PushTo` (токен в URL, `server/git.go:143`).
- **workspace** — расширяем `Info` (опциональные `GitReleaseBranch`,
  `GitMergeTarget`) или ведём side-реестр `workspace/branches.go` по ключу
  `project -> epic/task -> branch`. Реестр сохраняется в том же JSON
  (`workspace/registry.go:331`), hermetic-тесты есть.
- **board** — статус `done` эпика (после `SetEpicStatus`, `board/store.go:210`) —
  триггер валидности кнопки «Залить в main». Можно добавить поле
  `git_branch` в `board/entity.go` для прозрачности (ревью-панель показывает,
  какая ветка соответствует эпику/задаче).
- **server routes** (`server/server.go:150`) — новые маршруты под `authHandler`
  (мутации → CSRF, `server/auth.go`).
- **accept/reject** — не ломаем: для простейшего случая (один эпик-стендалон)
  старый flow остаётся; новый flow включается, когда эпик получил ветку.

## API (REST, новые маршруты)

```
POST /api/projects/:id/epics/:eid/branch      — создать ветку эпика от main (release), вернуть имя
POST /api/projects/:id/tasks/:tid/branch       — создать ветку задачи от ветки эпика
POST /api/projects/:id/tasks/:tid/merge        — влить ветку задачи в ветку эпика (ff или merge)
POST /api/projects/:id/epics/:eid/release      — «Залить в main»: релизная ветка → main
GET  /api/projects/:id/epics/:eid/release      — статус слияния (ok | conflicts:<files> | merging)
POST /api/projects/:id/epics/:eid/rebase       — main → релизная ветка (для решения конфликтов)
```

## Этапы и чеклист

### Ф-1 — Ветки эпиков и задач, реестр (основа)
- [x] `gitops/merge.go`: `MergeBranch`, детектор конфликтов через `git merge-tree`
      (не трогает рабочую копию), `ConflictingFiles`; hermetic-тесты (dry-run
      исполнитель, как в `gitops/cli_test.go`)
- [x] `workspace/`: side-реестр веток эпиков/задач (или поля в `Info`);
      сохранение/чтение JSON, тесты
- [x] `server/gitflow.go`: REST `.../epics/:eid/branch`, `.../tasks/:tid/branch`;
      создание ветки от main (эпик) и от релизной (задача) через `gitops`
- [x] `board/entity.go` (опционально): поле `git_branch` у эпика/задачи,
      проставляется при создании ветки
- [x] Регистрация маршрутов в `server/server.go:150` (`authHandler` — CSRF)
- [x] Верификация Ф-1: сборка/вет/тесты; ручной E2E: открыть git-проект,
      создать ветку эпика и задачи, убедиться в `git branch` клона

Статус Ф-1 (реализовано):
- git 2.35 на машине не имеет `git merge-tree --write-tree` (появился в 2.38),
  поэтому ЕДИНЫЙ путь детекции конфликтов — старый трёхаргументный
  `git merge-tree <base> <ours> <theirs>`: конфликтующие пути извлекаются из
  diff-вывода по маркерам `<<<<<<< .our`/`=======`/`>>>>>>> .their`
  (`gitops/merge.go:mergeTreeConflicts`), рабочая копия/index не трогаются.
- `gitops`: `CreateBranch`/`BranchExists` заводят ветки эпиков/задач БЕЗ
  переключения (текущая ветка агента `ai/<имя>` и рабочая копия не трогаются),
  поэтому прежний flow «Принять → MR» не ломается. `SanitizeBranchName` чистит
  ID (юникод сохраняется, пробелы/`..`/запрещённые символы → `_`).
- `workspace`: side-реестр в поле `Info.GitBranches` (`Epics`/`Tasks` карты
  `epic_id|task_id → {ветка, база}`), сохраняется через штатный `saveLocked`
  того же JSON; методы `Set/Get/Delete{Epic,Task}Branch`.
- `server`: `POST /api/projects/{id}/epics/{eid}/branch` (база = `git_base`,
  т.е. main) и `.../tasks/{tid}/branch` (база = ветка эпика; без неё — 400),
  идемпотентны; проставляют `git_branch` на доске; `handleDeleteEpic` снимает
  ветки эпика/задач из реестра. Hermetic-тесты (fake-git) + E2E на реальном
  git (`TestGitflowRestEndToEndRealGit`: клон → ветки через REST → `git branch`
  клона показывает `ai/epic/epic-1`, `ai/task/task-1`; HEAD не переключается).

### Ф-2 — Мёрдж задач в релизную ветку
- [x] `gitops.MergeFeature`: задача (ветка `ai/task/<id>`) → релизная ветка
      (ff-возможный → `--no-ff`), push через `PushTo`
- [x] REST `.../tasks/:tid/merge`; авто-вызов при статусе задачи `done`
      (хук в `runner` или при `SetTaskStatus`, как авто-лечение `runner/autofix.go`)
- [x] Конфликты задачи → вернуть список путей + перейти в Ф-4-инструмент
      (общая механика резолва)
- [x] Тесты: dry-run git, маппинг «done → merge», отказ при конфликте

Статус Ф-2 (реализовано):
- `gitops.MergeFeature` — вливание `ai/task/<id>` → `ai/epic/<eid>` во временном
  git worktree (`worktree add` → `merge --no-ff` → `push` → `worktree remove`):
  рабочая копия клона и HEAD (ветка агента `ai/<имя>`) НЕ трогаются, ветки живут
  в одном репозитории. Перед merge — прогноз конфликтов через старый
  `merge-tree` (унифицирован с Ф-1); конфликт → `*MergeConflictError{Files}` без
  единой git-мутации. Идемпотентность — `MergedInto` (merge-base == tip фичи):
  повторный мёрдж не плодит пустые merge-коммиты. Push инжектится URL-ом
  (`PushTo`, токен в URL для HTTPS, см. Ф-2-3) — сервер выбирает его из `inf.GitRemote`.
- `board.Store.TaskDoneHook` — опциональный колбэк после перевода задачи в done
  (вне блокировок, после сохранения; доска о git не знает). Сервер внедряет его
  в `boardStore()` (+ `handleUpdateTask`, `newSession` — покрывает REST, инструмент
  `BoardSetTaskStatus` агентов и kanban): **done → авто-мёрдж** ветки в релизную.
  Ошибки хука не ломают переход статуса; конфликты логируются и ждут Ф-4.
  Слияния проекта сериализуются пер-проектным локом (`s.mergeLocks`).
- `POST /api/projects/{id}/tasks/{tid}/merge`: 200 `{status:"ok", already_merged}`;
  409 `{status:"conflicts", files}` (вход Ф-4); 400 — нет веток/не git.
- Унифицированный `tokenPushURL`/`remotePushURL` (вынесен из `pushRepo`).
- Тесты: dry-run в gitops (happy/конфликт/уже слито/без push)+ реальный-git E2E
  `TestGitCLIMergeFeatureEndToEnd` (мёрдж, push в origin, идемпотентность,
  конфликт не трогает релиз); server hermetic: REST-мёрдж happy/409/already/400,
  маппинг «done → merge» и «done при конфликте».

### Ф-3 — Кнопка «Залить в main» + быстрый путь без конфликтов
- [x] REST `.../epics/:eid/release`: `git merge` релизной ветки в main;
      нет конфликтов → сразу результат (`ok`, главный MR/commit)
- [x] `EpicActionBar.tsx`: кнопка «Залить в main», видна только при
      `epic.status === "done"`; обращение к REST, показ ошибки/успеха
- [x] WS-событие `board` после мержа (переиспользуем `s.kickBoard`,
      `server/server.go:797`)
- [x] Тесты сервера (hermetic: fake-git), фронт-сборка `npm run build`

Статус Ф-3 (реализовано):
- ЕДИНЫЙ примитив вливания без трогания рабочей копии — `gitops.MergeFeature`
  (Ф-2): переиспользуется для «Залить в main» с аргументами
  `MergeFeature(main, ai/epic/<id>, {Message, PushURL})`. merge-tree-прогноз
  конфликтов → 409 без мутаций; идемпотентность — `MergedInto(main, релиз)`
  (`already_merged:true`, без git-вызовов); merge-коммит `--no-ff` — во
  временном worktree, push main через `PushTo` (токен в URL для HTTPS).
- `POST /api/projects/{id}/epics/{eid}/release` (`server/gitflow.go`,
  маршрут в `server/server.go`): 200 `{status:"ok", branch:main, source,
  already_merged}`; 409 `{status:"conflicts", files}` (вход Ф-4); 400 — эпик не
  done / нет ветки эпика / проект не git / нет git_base. Кнопка ручная:
  **сервер тоже требует `epic.status == done`** (необратимая операция) +
  статус в чат (`sess.append`) и `s.kickBoard` → WS-board.
- `web`: `releaseEpic` в `Api.ts`; `EpicActionBar` принимает `epic`+`onRelease`,
  кнопка «Залить в main» видна при `status==="done"` рядом с «Удалить»
  (локальный busy/ошибка/успех); пропсы проброшены `App → Dashboard →
  EpicRow → EpicActionBar`.
- Тесты: hermetic fake-git happy/409/уже-слито/не-done/без-ветки/не-git/404 +
  реальный-git E2E `TestReleaseEpicEndToEndRealGit` (клон → ветки → коммит
  задачи → merge в релиз → release в main: main продвинута и запушена в
  origin, HEAD агента не тронут, worktree сняты). `npm run build` web/ зелёный.

### Ф-4 — Инструмент авто-решения конфликтов
- [x] `gitops/conflicts.go`: детект конфликтных файлов; авто-резолв тривиальных
      (маркеры обоих деревьев, форматтеры); список «сложных» путей наружу
- [x] `tools/gitresolve.go`: инструмент `ResolveGitConflicts` — модель получает
      конфликтный unified-дифф (`git diff`), правит файлы через
      `SearchReplace`/`WriteFiles` (переиспользуем `FileOps.touched` для
      повторной ЛСП-проверки, `runner/autofix.go`)
- [x] CSP: «сначала вливаем main в релизную ветку» (`.../epics/:eid/rebase`),
      решаем конфликты там, потом **обязательно** приёмка (`agents/acceptor`)
      и только потом merge в main
- [x] Hermetic-тесты: fake-git конфликт → список путей; E2E инструмента
      резолва на сгенерированном конфликте

### Ф-5 — Верификация и полировка
- [x] `go build . ./agents/... ./tools/ ./board/ ./gitops/ ./server/ ./workspace/`
- [x] `go vet . ./agents/... ./tools/ ./board/ ./gitops/ ./server/ ./workspace/`
- [x] `go test . ./agents/... ./tools/ ./board/ ./gitops/ ./server/ ./workspace/`
- [x] `npm run build` (web/)
- [x] Ручной E2E на реальном git-проекте: эпик → ветки задач → done →
      «Залить в main» без конфликтов; второй прогон с намеренным конфликтом →
      rebase main→release → авто-резолв → приёмка → merge

Статус Ф-5 (реализовано):
- Сборка/вет/тесты по перечню выше — зелёные (без кеша `-count=1` для E2E).
- `npm run build` web/ — зелёный (vite: 85 модулей, JS 263 КБ).
- Оба сценария «ручного» E2E автоматизированы на реальном git (без сети):
  - без конфликтов — `TestReleaseEpicEndToEndRealGit` (клон → ветка эпика →
    ветка задачи → мёрдж задачи в релиз → «Залить в main»: main продвинута и
    запушена в origin, HEAD агента не тронут, worktree сняты);
  - с намеренным конфликтом — `TestEpicRebaseResolveEndToEndRealGit`
    (main «v2» vs релиз «v1» → release 409 → rebase в конфликтный worktree →
    резолв «моделью» → обязательная приёмка acceptor (go build/go run в
    worktree) → merge в main → cleanup worktree, HEAD не тронут).
  Прогон подтверждён `-count=1 -v`: все 6 real-git E2E (gitops + server) PASS.
- Полировка: правки кода не потребовались — все Ф-1..Ф-4 накопленные изменения
  в рабочем дереве, фаза завершена. План целиком выполнен (Ф-1..Ф-5).

## Верификация

```
go build . ./agents/... ./tools/ ./board/ ./gitops/ ./server/ ./workspace/
go vet  . ./agents/... ./tools/ ./board/ ./gitops/ ./server/ ./workspace/
go test . ./agents/... ./tools/ ./board/ ./gitops/ ./server/ ./workspace/
npm run build   # web/
```

Известные нюансы репозитория (не убирать):
- `go build ./...` падает на `temp/moon-distance/internal/middleware/cors.go`
  (артефакт сгенерированного проекта) — использовать перечень выше.
- Не форматировать `agents/acceptor/checks.go`, `agents/acceptor/run.go`.
- Комментарии/промпты/UI-тексты — на русском.

## Как продолжить

1. Открыть этот файл, прочитать «Решения пользователя» и «Этапы и чеклист».
2. Взять первый незачёркнутый пункт Ф-1, выполнить, отметить `[x]`, закоммитить.
3. После каждой фазы — верификация (блок выше) и краткая запись в этот файл.