# Git-flow

Эпик = релизная ветка, задача = фича-ветка. Агенты не работают в общей
ветке: каждая задача живёт в своём worktree и автоматически вливается.

## Настройка

```bash
GITOPS_SUBMODULES=1             # вкл по умолчанию; 0 — выключить
GITOPS_SUBMODULE_DEPTH=         # пусто = полная история; число = shallow

GITHUB_TOKEN=...                # scope: repo (или public_repo)
GITLAB_TOKEN=...                # scope: api, read_api
GITLAB_URL=...                  # адрес GitLab без завершающего слэша
```

> `GITOPS_SUBMODULES` считается включённым при любом значении, **кроме
> `"0"`** (`gitops/gitops.go:251,271`). Пустое значение = включено.

`GIT_ALLOW_PROTOCOL` в Go-коде **не читается** — это внешняя переменная
самого git.

## Ветки

| Сущность | Ветка | База |
|---|---|---|
| Эпик | `ai/epic/<id>` | `git_base` |
| Задача | `ai/task/<id>` | Ветка эпика |

Префиксы — `server/gitflow.go:27,29`. Side-реестр — `workspace.GitBranchMap`
(`workspace/branches.go:29-32`): `epics` и `tasks` → `BranchRef{Branch, Base,
Worktree}`.

## Автоматические шаги

Хуки доски устанавливаются сервером (`server/gitflow.go:485-517`).
Ошибка git-шага **не ломает** переход статуса.

| Событие | Действие | Где |
|---|---|---|
| Эпик создан | `autoCreateEpicBranch` — ветка от `git_base` | `gitflow_auto.go` |
| Задача создана | `autoCreateTaskBranch` — от ветки эпика (требует её существования) | `gitflow_auto.go` |
| Задача → `in_progress` | `taskWorktree` — подготовить worktree | `gitflow_auto.go` |
| Задача → `done` | `autoCommitAndMergeTask` | `gitflow_auto.go:281-391` |
| Эпик → `done` | `syncEpicWithMain` (фон, таймаут 5 мин) | `gitflow.go:397-403` |

## Порядок авто-merge задачи

`autoCommitAndMergeTask` (`server/gitflow_auto.go:281-391`):

```
1. Коммит worktree задачи
2. Авто-MR задачи (если есть remote) — ДО мёрджа
2.5. Задача без коммитов, уже влитая в релиз → считается смердженной
3. Merge в релизную ветку эпика
4. Снятие worktree
```

Авто-MR создаётся **до** мёрджа, чтобы в истории репозитория сохранилась
связь задача → MR, даже если merge был fast-forward.

## Конфликты мёрджа

### Задача

При конфликте:

1. Попытка LLM-авторезолва (`tryTaskLLMResolve`).
2. Если не удалось — записывается `task.MergeConflictFiles`, статус
   **откатывается в `in_progress`** (`gitflow_auto.go:349-360`), приходит
   `RoleStatus`-уведомление.

> **Расхождение с планом:** план merge-conflict-board описывал сохранение
> `done` с маркировкой конфликта. Код откатывает задачу в `in_progress` —
> это честнее: задача не сделана.

Ручной `TaskMerge` из чата (`server/actions.go:274-318`) при конфликте
**не меняет состояние вообще** — только пишет `merge_conflict_files` и
возвращает детерминированную строку со списком файлов и точкой резолва.

### Эпик

`syncEpicWithMain` при конфликте записывает `epic.MergeConflictFiles`.
Снятие признака — `clearEpicMergeConflict` (`gitflow_auto.go:471`) при
успешном `EpicResolve` или `EpicRelease`.

### LLM-авторезолв

`server/gitflow_ar.go`:

- `resolveMaxAttempts = 3` (`:55`), контекст с таймаутом `resolveCtx` (`:47`).
- `ResolveWithLLM` (`:57`), проверка результата `verify` (`:295`).
- `clearConflictField` (`:339`).
- Хендлеры: `handleTaskLLMResolve` (`:541`), `handleEpicLLMResolve` (`:586`).

### Инструмент агента

`ResolveGitConflicts` (`tools/gitresolve.go`) — три шага: `status` →
`start` → `apply`. Ассистент ведёт конфликт эпика через эту цепочку,
затем вызывает `EpicResolve`.

## Диффы

`server/diff.go` — ленивая разборка `git diff`:

| Статус | Значение |
|---|---|
| `added` | Новый файл |
| `modified` | Изменён |
| `removed` | Удалён |
| `renamed` | Переименован |
| `submodule` | Сабмодуль |

Структура `diffFileEntry{path, status, added, deleted}` (`:32-37`).
Патч одного файла запрашивается отдельно. Кэш `cachedDiff` с ключом
`project|ref|base` (`:41-47,107`), инвалидация — `invalidateDiffs` (`:93`).

Подробнее — [Контекстные диффы](contextual-diff.md).

## Merge Request

Создаётся через форджи (`forges/registry.go`):

- `forges/github` — GitHub (`GITHUB_TOKEN`).
- `forges/gitlab` — GitLab (`GITLAB_TOKEN`, `GITLAB_URL`).
- `forges/local` — локальный репозиторий без MR.

`RemoteBuilder` используется для кнопки «Принять → MR» в HITL-режиме.

## Сабмодули

`GITOPS_SUBMODULES` (вкл по умолчанию) — инициализация вложенных
git-репозиториев при открытии проекта. `GITOPS_SUBMODULE_DEPTH` — положительное
значение делает клоны shallow и ограничивает историю.

Подробнее — [Сабмодули и мульти-репо](submodules-multirepo.md).

## Начать с нуля

Проект без git — обычный сценарий. Для работы с ветками нужен репозиторий
с remote: при добавлении проекта в Web UI укажите URL, система клонирует
в `temp/<имя>` и создаст фича-ветку.

### Пустой удалённый репозиторий

Репозиторий с нулём коммитов (только что созданный на хостинге) — тоже
штатный сценарий: агент строит проект с нуля.

`git clone` такого репозитория проходит успешно (exit 0 + предупреждение
«You appear to have cloned an empty repository»), но ветки в нём нет вообще —
HEAD нерождённый, поэтому `git rev-parse --abbrev-ref HEAD` падает с 128.
Система определяет базу по имени unborn-ветки, **создаёт её первым коммитом и
пушит в remote**:

| Шаг | Команда | Зачем |
|---|---|---|
| Имя базы | `git symbolic-ref --short HEAD` | Ответ на `rev-parse` для нерождённого HEAD |
| Проверка | `git rev-parse --verify --quiet <base>^{commit}` | Базы нет → репозиторий пуст |
| Посев | `git add README.md` + `git commit` | Осмысленный первый коммит вместо пустого |
| Публикация | `git push -u origin <base>` | MR/merge/release работают с первого дня |

Посев пропускается, если база уже есть (обычный клон). Detached HEAD у клона
(`rev-parse` отдаёт «HEAD») разбирается через `origin/HEAD`. Если имя ветки
не удалось определить вовсе — берётся `main` (`gitops.DefaultBaseBranch`):
неудачный клон лучше открыть с базой по умолчанию, чем не открыть.

Первый коммит идёт от identity хоста (`~/.gitconfig`), а при его отсутствии —
от сервисной (`AI <ai@localhost>`), поэтому сервер без git-конфига тоже
работает. **Нужен доступ на push**: без него открытие падает с понятной
ошибкой, а не оставляет проект без базовой ветки.

Если клон упал уже после создания каталога, мусорный `temp/<имя>` убирается
автоматически, иначе повторное открытие упиралось бы в 409. Каталог от
неудачного клона **того же** репозитория (тот же origin, ни одного коммита,
чистое состояние) распознаётся и убирается при следующей попытке; чужое
содержимое — никогда.

## Команды

```bash
cd temp/<проект>
git log --oneline --graph --all | head -30
git branch -a
```

Проверить, что в проекте видны ветки агентов:

```bash
git branch --list 'ai/*'
```

## Связанное

- [Kanban-доска](kanban-board.md) — хуки и статусы
- [Контекстные диффы](contextual-diff.md)
- [Web UI](web-ui.md) — операции с ветками и MR
- [Чат-ассистент](chat-assistant.md) — `TaskMerge`, `EpicRelease`
- [Сабмодули и мульти-репо](submodules-multirepo.md)
