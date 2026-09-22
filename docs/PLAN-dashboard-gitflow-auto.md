# План: Автоматизация git-workflow — ветки/MR/коммиты без ручных шагов

Статус: **РЕАЛИЗОВАНО (Ф-1..Ф-4, верификация Ф-5; остался ручной E2E)**.
Формат — как в `PLAN-webui.md` / `PLAN-lsp.md`.
Дополняет уже выполненный `PLAN-dashboard-workflow.md` (Ф-1..Ф-5: ветки эпиков/задач,
«done → мёрдж в релиз», «Залить в main», авто-резолв конфликтов) и закрывает
оставшиеся пункты TODO из `PLAN-dashboard-gitflow.md` (строки 1–8).

Авто-шаги выполнены на сервере (`server/gitflow_auto.go`): хуки веток/MR/коммитов
(Ф-1), `has_commits` и скрытие кнопки MR (Ф-2), worktree задачи + авто-коммит +
авто-MR на done (Ф-3), sync релизной ветки с main на done эпика (Ф-4). Кнопки
остались как ручная страховка. Сборка/vet/тесты (hermetic + real-git E2E:
`TestAutoCreateBranchesRealGit`, `TestAutoCommitAndMergeTaskRealGit`,
`TestAutoSyncEpicWithMainTrivialResolve`) и `npm run build` зелёные.

## Цель

Снять с человека ручные git-шаги, оставив кнопки только как страховку от сбоев:

- ветки эпиков/задач **создаются автоматически** при создании записи на доске;
- если в ветке **нет коммитов**, кнопка «Создать MR» **не показывается** и
  запрос к серверу не делается (показывается «коммитов ещё нет»);
- при выполнении работы по задаче — **авто-коммит**, **авто-MR эпика** и
  **авто-заливка ветки задачи в эпик** (часть уже есть: done → мёрдж);
- когда завершены все задачи эпика — релизная ветка **поддерживается в синхроне
  с main** (main→rebase+авто-резолв конфликтов), поэтому «Залить в main» по клику
  всегда проходит без конфликтов.

## Текущее состояние (что уже есть и чего нет)

| Пункт TODO | Сейчас |
|---|---|
| ветка у каждой задачи, свои коммиты | ветки `ai/epic/<id>` / `ai/task/<id>` **создаются автоматически** через хуки `EpicCreatedHook`/`TaskCreatedHook` (`board/store.go`) → `attachGitHooks` (`server/gitflow.go`); кнопки остались страховкой |
| коммитов нет → не показывать MR | есть: `gitStatus` заполняет `gitLinkView.has_commits` (`server/gitflow_mr.go`); при 0 коммитов фронт (`GitBlock.tsx`) показывает «коммитов ещё нет» и не зовёт сервер |
| задача готова = ветка залита в эпик | есть: авто-мёрдж через `TaskDoneHook` (`server/gitflow.go:442`) |
| эпик готов = ветка залита в main | есть: кнопка «Залить в main» (`handleReleaseEpic`, `server/gitflow.go:254`) |
| авто-коммит во время работы по задаче | есть: специалисты пишут в **worktree** ветки задачи (`taskWorktree`/`taskOutputDir`, `server/gitflow_auto.go`), на done — авто-коммит (`commitTaskWorktree`) |
| авто-MR в эпик | есть: на done при отсутствии MR и наличии токена форджа — `createTaskMROnce` (`server/gitflow_mr.go`) |
| конфликты с main решены до клика | есть: на done эпика — `syncEpicWithMain` → `syncEpicMainOnce` + авто-резолв `autoResolveMainSync` (тривиальные конфликты), hard-конфликт → abandon с логом (клик-флоу `gitflow_resolve.go` остался) |
| ветка эпика собрана и подготовлена к main | есть: непрерывная подготовка на done эпика (п. выше), «Залить в main» после синхрона проходит без конфликтов |

## Решения (предлагаемые)

1. **Серверные хуки доски вместо ручных кнопок.** По образцу существующего
   `board.Store.TaskDoneHook` (`board/store.go:58`) добавить опциональные
   `EpicCreatedHook` / `TaskCreatedHook`, вызываемые из `CreateEpic`/`CreateTask`.
   Сервер (`boardStore`, `server/gitflow.go:34`) вешает на них создание ветки
   через существующий `ensureBranch` + запись в side-реестр. Ветка задачи
   создаётся только если уже есть ветка её эпика (база = релизная ветка).
   Ошибки ветки **не ломают** сохранение записи: логируются, на доске остаются
   кнопки «Создать ветку …» как ручная страховка (сбой → человек продвигает).
2. **Признак «есть коммиты» в git-статусе доски.** В `gitLinkView`
   (`server/gitflow_mr.go:29`) добавить `has_commits bool`. Заполняется в
   `gitStatus` локально через gitops (см. ниже). Фронт `GitBlock`: если ветка
   есть и `has_commits=false` — вместо «Создать MR» показывать muted-метку
   «коммитов ещё нет», **не** вызывая `createTaskMR/createEpicMR`.
3. **Работа специалиста в ветке задачи.** Специалисты оркестратора должны
   писать в **worktree своей задачи**, а не в общую копию клона
   (`git worktree add <dir> ai/task/<id>`). Планировщик получает хук на «какой
   каталог использовать для OutputDir специалиста» (`SetOutputDir`), сервер
   выдаёт каталог worktree задачи. На «в работе» — worktree создан; на
   «выполнена» (done) — авто-коммит незакоммиченного в worktree, затем штатный
   авто-мёрдж в релиз (`mergeTaskBranch`), при отсутствии MR — авто-MR
   (`createMR`, логика `handleCreateTaskMR`), затем worktree снят.
4. **Поддержка синхрона релизной ветки с main.** При переводе эпика в `done`
   (и, как бюджет-вариант, периодически в `boardFlusher`): `git merge main` в
   релизную ветку → при конфликтах авто-резолв (переиспользовать
   `tools/gitresolve.go`-механику) → результат снова в ветке эпика. Тогда клик
   «Залить в main» всегда попадает в «быстрый путь без конфликтов»
   (`handleReleaseEpic` 200).
5. Авто-шаги 3–4 выполняются в **фоне** (не блокируют цикл оркестрации): каждая
   операция идёт под пер-проектным `mergeLock` (`server/gitflow.go:427`).

## Новые компоненты

| Пакет / файл | Назначение |
|---|---|
| `board/store.go` | хуки `EpicCreatedHook`/`TaskCreatedHook` (по образцу `TaskDoneHook`); сохранение `git_branch` уже есть в `Epic`/`Task` (`board/entity.go`) |
| `gitops` | `CountCommits(base, branch)` — `git rev-list --count <base>..<branch>` (детект «нет коммитов»); `WorktreeAdd/WorktreeRemove` (сейчас worktree живёт только внутри `MergeFeature`, `gitops/merge.go`) |
| `server/gitflow.go` | `attachGitHooks(project, store)` — автоматика веток/MR/коммитов/MR-синхрона; кнопки остаются |
| `server/gitflow_mr.go` | `gitStatus` заполняет `has_commits`; авто-MR-путь задачи/эпика (общий хелпер с `handleCreateTaskMR`) |
| `agents/planner/kanban.go` | `SetOutputDir(func(project, taskID) string)` — каталог специалиста = worktree задачи |
| `web/src/Components/Dashboard/GitBlock/GitBlock.tsx` | скрытие «Создать MR» без коммитов; muted-метка |
| `web/src/Types/Types.ts` | `GitLinkView.has_commits` |

## API (дополнения к существующему)

```
(нет новых REST-маршрутов — только внутренние хуки и поле в снимке доски)
GET /api/projects/{id}   — git.epics[]/git.tasks[] дополняются has_commits
```

## Этапы и чеклист

### Ф-1 — Авто-создание веток (хуки доски)
- [x] `board/store.go`: `EpicCreatedHook`/`TaskCreatedHook` + вызовы в `CreateEpic`/`CreateTask`
- [x] `server/gitflow.go`: `attachGitHooks` — ветка эпика (от `git_base`), ветка задачи (от ветки эпика, при её наличии); запись в side-реестр + `GitBranch` на доске
- [x] Вешется во всех точках создания: `boardStore()` (REST/инструменты), `newSession` (`server/session.go:66`), `handleUpdateTask`
- [x] Кнопки «Создать ветку …» остаются для сбоев; ошибки хука → лог + `kickBoard`
- [x] Тесты: hermetic (fake-git) + real-git E2E (`TestAutoCreateBranchesRealGit` — создание эпика/задачи через hooked-store → `git branch` клона показывает `ai/epic/…`, `ai/task/…`)

### Ф-2 — Кнопка MR без коммитов
- [x] `gitops.CountCommits(base, branch)` + hermetic-тест
- [x] `gitStatus` заполняет `has_commits` (0 ⇒ false); для не-git/без ветки — незаполнено
- [x] `Types.ts` + `GitBlock.tsx`: «коммитов ещё нет» вместо кнопки; **никакого** запроса на сервер
- [x] `npm run build`
- [x] Тесты: снимок доски для ветки-без-коммитов и с коммитами (`TestGitStatusHasCommits`)

### Ф-3 — Авто-коммит и авто-MR задачи
- [x] `gitops.WorktreeAdd/WorktreeRemove` (публичные, вне `MergeFeature`) + тесты
- [x] `KanbanRunner.SetOutputDir`: `specialist` в `phaseExecute` получает OutputDir = worktree ветки задачи (каталог создаётся сервером на in_progress)
- [x] На done: авто-коммит dirty-изменений worktree в `ai/task/<id>`; затем штатный `mergeTaskBranch`; при отсутствии MR — авто-MR через `createMR` (переиспользовать `handleCreateTaskMR`); снять worktree
- [x] Тесты: real-git E2E (`TestAutoCommitAndMergeTaskRealGit`) — задача без токена форджа MR не создаёт, но коммит+мёрдж+снятие worktree проходят; hermetic (`TestTaskInProgressCreatesWorktree`, `TestTaskDoneAutoMergeConflict`)

### Ф-4 — Релизная ветка в синхроне с main
- [x] На `done` эпика: `merge main` в релизную ветку (через существующий флоу rebase/резолв `gitflow_resolve.go`), без отмены статуса
- [x] При авто-синхроне конфликты → авто-резолв переиспользует механику `tools/gitresolve.go` (det-тест E2E намеренного конфликта: `TestAutoSyncEpicWithMainTrivialResolve`)
- [x] Кнопка «Залить в main» по-прежнему ручная; после синхрона гарантированно проходит без 409
- [x] Тесты: real-git — эпик done с расхождением main → авто-синхрон → release без конфликтов

### Ф-5 — Верификация
- [x] `go build . ./agents/... ./tools/ ./board/ ./gitops/ ./server/ ./workspace/`
- [x] `go vet  . ./agents/... ./tools/ ./board/ ./gitops/ ./server/ ./workspace/`
- [x] `go test . ./agents/... ./tools/ ./board/ ./gitops/ ./server/ ./workspace/`
- [x] `npm run build` (web/)
- [x] `npm run build` (web/)
- [x] Ручной E2E на реальном git-проекте (2026-09-22, проект `uraankhayayaal/my-rust-app`): создать эпик/задачу через чат → ветки появились сами → выполнить задачу → авто-коммит/MR/мёрдж → эпик done → авто-синхрон main → клик «Залить в main» без конфликтов. E2E выявил и починил баг: авто-MR задачи создавался ПОСЛЕ мёрджа в релиз эпика, поэтому GitHub отклонял его 422 («No commits between»); авто-MR перенесён ДО мёрджа (`autoCommitAndMergeTask`, `server/gitflow_auto.go`), добавлен hermetic-тест порядка

## Верификация

```
go build . ./agents/... ./tools/ ./board/ ./gitops/ ./server/ ./workspace/
go vet  . ./agents/... ./tools/ ./board/ ./gitops/ ./server/ ./workspace/
go test . ./agents/... ./tools/ ./board/ ./gitops/ ./server/ ./workspace/
npm run build   # web/
```

Нюансы репозитория (не убирать): `go build ./...` падает на артефакте
`temp/moon-distance/...` — использовать перечень выше; не форматировать
`agents/acceptor/checks.go`, `agents/acceptor/run.go`; комментарии на русском.

## Как продолжить

1. Прочитать «Решения», затем выполнять этапы по порядку (Ф-1 → Ф-5), отмечая `[x]`.
2. После каждой фазы — верификация и краткая запись в этот файл.
3. Порядок зависит от `PLAN-dashboard-chat-create.md` (создание через чат) — там
   автологика веток из Ф-1 должна работать сразу же.