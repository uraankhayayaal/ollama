# План: git submodule и мульти-репо задачи (одна задача → несколько проектов)

Статус: **НЕ РЕАЛИЗОВАНО.** План внедрения работы с git submodule и
мульти-репозиторными задачами: одна задача пользователя может затрагивать
несколько git-репозиториев одновременно (родитель + его submodule-пакеты),
каждый — со своими коммитами, ветками и MR. Отмечать чекбоксы `[x]` по мере
выполнения, как в `PLAN-lsp.md`.

## Проблема

Сейчас всё строго однопроектное:

- `board.Task`/`board.Epic` несут единственный `ProjectName`
  (`board/entity.go:144-182`), Redis-ключи доски `board:<project>:*`
  (`board/store.go:85-102`);
- оркестратор `KanbanRunner.Run(ctx, projectName, taskText)` — один проект
  (`agents/planner/kanban.go:205`), `outputDir func(project, taskID) string`
  (kanban.go:55-60, `:110`, применение `:922-923`), единственный каталог из
  `taskOutputDir` (`server/gitflow_auto.go:84-91`);
- планировщик/агенты работают в `projects.ProjectDir(projectName)`
  (`agents/planner/executor.go:555,572,712,965`, `agents/developer/developer.go:121-156`);
- diff/accept/reject — один репозиторий (`server/git.go:297-378`,
  `server/diff.go:50-106`).

Git submodule не поддерживается вообще:

- `gitops.Clone` без `--recurse-submodules` (`gitops/gitops.go:196`);
- в diff gitlink упадёт как смена одного SHA, содержимое не видно
  (`gitops/gitops.go:315-327` — `git diff <base>`, без `--submodule`);
- commit/push внутри сабмодуля не выполняются; push только родителя
  (`server/git.go:156-161`);
- реестр запрещает вложенность корней: `checkNesting`
  (`workspace/registry.go:255-275`, ошибки `ErrNested`/`ErrContains` `:80-81`);
- `grep -ri submodule` по Go-коду — 0 совпадений.

## Цель

1. Открытие git-проекта, содержащего субмодули: clone с рекурсией, распознавание
   `.gitmodules`, регистрация сабмодулей как самостоятельных git-проектов
   (свой remote/base/branch), diff видит содержимое сабмодуля.
2. Одна задача может исполниться сразу в нескольких репозиториях; приёмка
   даёт N коммитов/N MR (сначала submodule, потом родительский gitlink).
3. Submodule-cовместимые reject (reset в сабмодуле + deinit) и merge.

## Ключевые решения

- **Модель регистрации.** Субмодуль из `.gitmodules` при открытии родителя
  регистрируется в реестре как отдельная запись `KindGit` с именем
  `<родитель>--<путь в родителе>` (например `app--packages/auth`) и полем
  `Parent` → имя родительского проекта. Корень — каталог сабмодуля (лежит
  внутри корня родителя). Блокер `checkNesting` ослабляется: вложенность
  разрешается, если вложенный проект — submodule зарегистрированного родителя.
- **Транзакционный порядок ≠ транзакция:** коммиты и MR строятся на каждом
  репозитории независимо, но с фиксированным порядком: внутренние (submodule)
  раньше внешних (родитель). gitlink родителя поднимается после коммита в
  сабмодуле. Полный атомарный мульти-коммит (одна транзакция) — вне рамок:
  разносится на N MR.
- **Мульти-задача — это N выходных каталогов**, а не N процессов: `KanbanRunner`
  получает список `outputDir` (worktree каждого репозитория задачи), все агенты
  знают весь список и адресуют файлы относительными путями с префиксом репо.
- **Дифф по умолчанию `--submodule=log`** — в диффе родителя gitlink показывается
  как заголовок коммита сабмодуля; полное содержимое — через diff самого сабмодуля.
- **Пуш с токеном на сабмодуль**: у сабмодуля свой remote; `tokenPushURL`
  (`server/git.go:140-148`) применяется к каждому репозиторию по его remote.
- **Гит-сабмодуль ≠ git-подпроект с `git subtree`**: не путать; субмодуль —
  это gitlink, неделимое pointer-состояние в родителе.
- Рекурсия ограничивается глубиной 1 (субмодули сабмодулей) — первая версия.

## Точки изменений

### Ф-1: submodule-aware gitops (фундамент, самодостаточен)

- [ ] `gitops/gitops.go` `Clone` (`:181-221`): добавить
  `git clone --recurse-submodules --depth=1 <remote> <dest>` и
  `git submodule update --init --recursive` (после checkout фича-ветки);
  `--depth` настраиваемый env `GITOPS_SUBMODULE_DEPTH`
- [ ] `gitops/submodule.go` (новый): парсинг `.gitmodules`
  (git config формат), функции:
  - `ListSubmodules(root)` → `[]{Path, URL, Branch}`
  - `EnsureSubmodules(ctx, root)` — `git submodule update --init --recursive`
  - `SubmoduleDiff(ctx, repo)` — `git diff <base> --submodule=log`
  - `CommitInSubmodules(ctx, repo, msg)` — `git -C <sub> add -A && commit` для
    каждого изменённого сабмодуля
  - `UpdateGitlinks(ctx, repo)` — `git add -A` в родителе (поднимает gitlink)
  - `PushSubmodules(ctx, repo, urlFor func(path, remote) string)` — пуш
    каждого сабмодуля до пуша родителя
  - `ResetSubmodules(ctx, root, base)` — для reject: `git submodule foreach
    git reset --hard <base>` + `git submodule deinit -f` по необходимости
- [ ] `gitops/gitops.go` `Diff` (`:315-327`): параметр/новая функция
  `DiffSubmodules` → `git diff --submodule=log <base>` (gitlink показывается
  заголовком коммита), содержимое сабмодуля берётся из его собственного diff
- [ ] `gitops/gitops.go` `Push`/`PushTo` (`:161-172`): у `Repo` появляется
  список сабмодулей; `Push` сначала пушит сабмодули (том же order), потом себя
- [ ] `server/diff.go` `gitProjectDiff` (`:50-67`) и разбор
  `parseUnifiedDiff` (`:70-185`): обработка `Subproject commit <sha>` блоков
  (gitlink) — метка статуса `submodule <путь>` + ссылка на дифф сабмодуля
- [ ] Реестр: `workspace/registry.go`
  - `Info` (`:36-58`): поле `Parent string `json:"parent,omitempty"``;
  - `checkNesting` (`:255-275`): разрешить вложенность, если вложенный
    проект уже имеет `Parent == <имя родителя>` или создаётся как сабмодуль
    существующего (передаём allowSub bool в Add);
  - `workspace/registry.go` `Add`: валидация «сабмодуль должен лежать внутри
    корня родителя» + имя-схема `<родитель>--<path>`
- [ ] `server/git.go` `handleOpenGitProject` (`:22-72`): после клона —
  `EnsureSubmodules`, регистрация сабмодулей из `.gitmodules`
  (у каждого: `clone`, фича-ветка `ai/<имя-сабмодуля>`, base ветки по умолчанию)
- [ ] `gitops/taskmerge.go`/`merge.go`: `MergeFeature` и merge-tree не ломать —
  validate, что при наличии submodule используется `--submodule=log`
- [ ] Тесты: `gitops/submodule_test.go` (fake-исполнитель: .gitmodules парсинг,
  gitlink-команды, порядок push), E2E на реальном git-протоколе в
  `gitops/cli_test.go` (обновить), `server/diff_test.go` (парсинг
  `Subproject commit`), `workspace/registry_test.go` (checkNesting с Parent)

### Ф-2: модель «задача → N репозиториев»

- [ ] `board/entity.go` `Task`/`Epic` (`:144-182`): новое поле
  `Repositories []string `json:"repositories,omitempty"`` — имена проектов из
  реестра (родитель + сабмодули); `ProjectName` остаётся «основным» для
  совместимости с UI/доской. Redis-ключи — по `ProjectName`, репозитории —
  в данных задачи
- [ ] `agents/planner/kanban.go`:
  - `outputDir`-поле (`:55-60`) и `SetOutputDir` (`:110`): расширить контракт
    до списка `[]string` (или держать функцию `outputDirs(project, taskID) []string`)
  - `Run` (`:205`): на старте задачи расположить ветки сабмодулей параллельно
    ветке родителя (здесь задействуется `s.reg`), прокинуть список в
    `SetOutputDir`
- [ ] `server/gitflow_auto.go` `taskOutputDir` (`:84-91`): вернуть список —
  worktree ветки задачи в родителе + worktree/клон каждого сабмодуля;
  `taskWorktree` (`:34-79`) создаёт worktree и в сабмодулях
- [ ] `agents/planner/executor.go` (`:555,572,712,965`) и
  `agents/developer/developer.go` (`:121-156`): `OutputDir` становится набором
  (карта `имя репо → путь`); `FileOps` умеет адресовывать файлы с префиксом
  репозитория (`repo/path/...`); промптам добавляется описание мульти-репо
- [ ] `agents/chatassist/agent.go:54` и другие промпты: снять/уточнить формулировку
  «задача затрагивает несколько направлений — оформляй эпиком», теперь это
  одна задача может затрагивать несколько репозиториев
- [ ] `server/session.go:173` — `taskOutputDir` как набор каталогов
- [ ] Тесты: `board/store_test.go` (задача с Repositories), `kanban_test.go`
  (fan-out outputDirs), `hard`-проверка single-project-регресса

### Ф-3: batch accept/MR и reject

- [ ] `server/git.go` `handleAccept` (`:297-378`) → обёртка `handleAcceptMany`:
  цикл по `Repositories` задачи/проекта с фиксированным порядком
  (сабмодули → родитель), каждый через существующий `repo.Commit` → `pushRepo`
  (`:156-161`) → `forge.CreateMergeRequest` (`forges/forges.go:52`);
  ответ — сводка `{repo → {url, branch, base}}`
- [ ] `server/git.go` `handleRejectBranch` (`:382-406`) → по списку:
  родитель через `repo.RejectBranch` (+ `ResetSubmodules`), сабмодули — их
  собственный reject на remote
- [ ] `gitops` `Commit` (`:104-121`): вызывать `CommitInSubmodules` перед
  коммитом родителя; `Dirty` (`:234-243`) учитывать и gitlink-изменения
- [ ] forges: N MR по N remote — интерфейс не меняется, вызвать N раз;
  единая сводка в ответе и в Web UI
- [ ] Web: `Api.ts`/`Types.ts` — типы `AcceptResult` → `{[repo]: {url,...}}`,
  view «Accept → N MR» в модалке Diffboard с учётом `Repositories`
- [ ] Тесты: `server/git_test.go` (batch accept на fake-исполнителе + stub-фордж,
  порядок push), E2E на реальном git-протоколе с двумя репозиториями
  (родитель + submodule), `web` typecheck

### Ф-4: полировка и docs

- [ ] Фоновая сверка статусов MR (`server/gitflow_mr.go:405-491`) — учесть N MR
  на проект (по репозиториям)
- [ ] Авто-шаги gitflow (`server/gitflow_auto.go`) — авто-коммит/авто-мёрдж
  для multi-repo задач не ломать (работают в worktree каждого репо)
- [ ] env-конфиг: `GITOPS_SUBMODULE_DEPTH`, `GITOPS_SUBMODULES` (0 — выключить
  рекурсию), документация в `readme.md`
- [ ] Обновить этот план чекбоксами и «известные нюансы»

## Верификация

```
go build . ./agents/... ./tools/ ./board/ ./gitops/ ./workspace/ ./server/
go vet  . ./agents/... ./tools/ ./board/ ./gitops/ ./workspace/ ./server/
go test . ./agents/... ./tools/ ./board/ ./gitops/ ./workspace/ ./server/
npm run build  # web/
```

Ручной E2E (реальные gит-протокол, два репозитория): локальный родитель +
submodule → открыть по `git_url` → правка в сабмодуле + правка в родителе →
diff видит содержимое сабмодуля → accept → 2 коммита/2 MR (сабмодуль раньше) →
reject сбрасывает оба.

Известные нюансы (не убирать):
- `go build ./...` падает на `temp/moon-distance/...` — использовать перечень выше.
- Не форматировать `agents/acceptor/checks.go`, `agents/acceptor/run.go`.
- Комментарии/промпты/UI-тексты — на русском.
- Submodule-remote может требовать свой токен (не автоматически равен
  родительскому): `tokenPushURL` должен уметь брать токен по remote сабмодуля
  (GITHUB_TOKEN/GITLAB_TOKEN по хосту remote), иначе пуш сабмодуля упадёт.
- `--depth=1` в сабмодулях экономит время, но ломает `git merge-tree`-проверки
  в `gitops/merge.go`: мини-клоны мержить нельзя — merge идёт в родителе по
  pointer'у (версия задачи Ф-1 фиксирует только push/diff/commit, не merge).
- Параллельные агенты одного проекта и worktree сабмодулей не должны
  пересекаться каталогами (`mergeLock` на проект уже есть).
- Реестр JSON обратной совместимости: `Parent`/`Repositories` — omitempty,
  старые файлы открываются без изменений.

## Оценка

Три фазы, общая сложность высокая (~вторая по величине переделка после Ф-2-3):

| Фаза | Объём | Сложность |
|---|---|---|
| Ф-1 submodule gitops | ~6 файлов (gitops/*, workspace/registry.go, server/git.go, diff) | средняя |
| Ф-2 модель N-репозиториев | ~7 файлов (board, kanban, gitflow_auto, executor, developer) | высокая |
| Ф-3 batch accept/MR | ~5 файлов (server/git.go, gitops, forges, web) | средняя |

Рекомендация: Ф-1 самодостаточна — улучшает даже однопроектные задачи
(дифф/коммит/пуш в сабмодулях) без мульти-модели. Ф-2 — отдельная серия.

## Как продолжить

1. Согласовать решения (глубина рекурсии, имя-схема регистрации, порядок MR)
   и реализовать Ф-1.
2. Ф-2 — модель мульти-репо на доске и в оркестрации.
3. Ф-3 — batch accept/reject/MR и Web UI.