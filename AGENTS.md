# AGENTS.md — правила для агентов (opencode / помощники)

## `temp/` — хранилище проектов (не трогать)

Каталог `temp/` — рабочее хранилище проектов, которые генерируют и дорабатывают
автоматические агенты-разработчики (backend/frontend и другие). Это **не исходный код
проекта**, а артефакты выполнения.

Правила для любого агента (включая opencode):

- **НЕ удаляй `temp/` целиком** и его содержимое по команде «почисти/очисти» —
  там живут пользовательские проекты.
- **НЕ редактируй** файлы внутри `temp/` напрямую, если задача не касается
  конкретного проекта: этот каталог — выходная директория агентов, а не
  редактируемый исходник.
- **НЕ коммить** содержимое `temp/` в git: оно игнорируется (см. `temp/.gitignore`),
  но не добавляй его в индексацию вручную.
- Агенты, работающие с проектом, используют единый путь
  `temp/<имя_проекта>/` (см. `projects.ProjectDir`) и не выходят за
  его пределы.
- Если задача требует изменить код внутри уже созданного проекта — работай через
  агента-разработчика (`backend`/`frontend`) соответствующей специализации, а не правь
  файлы в `temp/` как исходник.

## Сборка и проверка

Рабочая сборка (обходит известный блокер в `temp/`):

```
go build . ./agents/... ./tools/ ./board/
go vet . ./agents/... ./tools/ ./board/
go test . ./agents/... ./tools/ ./board/
```

Известные нюансы:
- `go build ./...` падает на `temp/moon-distance/internal/middleware/cors.go`
  (артефакт сгенерированного проекта) — используй перечень выше.
- Предсуществующие неформатированные файлы (`agents/acceptor/checks.go`,
  `agents/acceptor/run.go`) не трогать.

Состояние Ф-2-3 (последняя сессия): сервер открывает git-проекты через
`git_url` — `handleOpenGitProject` клонирует репозиторий в `temp/<имя>`
(фича-ветка `ai/<имя>`, база = ветка по умолчанию), регистрирует
workspace KindGit; REST: `GET /api/projects/{id}/diff` (git — unified-дифф
`git diff base` от рабочего каталога; обычно папки — `Snap.Diff` по
baseline-снимку), `POST /api/projects/{id}/accept` (dirty → commit → push →
`forges.NewByRemote` → `CreateMergeRequest`; токен GITHUB_TOKEN/GITLAB_TOKEN
по remote), `POST /api/projects/{id}/reject-branch` (delete на remote +
`reset --hard` базы + удаление ветки). Push на HTTPS в headless-среде:
`server.pushRepo` встраивает токен в URL (`https://x-access-token:<токен>@…`)
и зовёт `gitops.Repo.PushTo` (без `-u`, чтобы upstream/токен не попадали в
конфиг; SSH-remote — обычный `git push origin`). Web: Diffboard — дифф,
«Принять → MR» и «Отклонить ветку» (`projectDiff`/`acceptProject`/`rejectBranch`
в Api.ts>, тип DiffView в Types.ts). Hermetic-тесты (fake-исполнитель git +
stub-фордж, без сети) и E2E на реальном git-протоколе (gitops/cli_test.go)
зелёные; `npm run build` web/ проходит. VI плана выполнен: end-to-end на
реальном проекте GitHub (`uraankhayayaal/my-rust-app`) — открытие по git_url,
дифф, accept → push + PR (#1).