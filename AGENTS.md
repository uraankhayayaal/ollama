# AGENTS.md — правила для агентов (opencode / помощники)

## Текущее состояние структуры

Направление зависимостей — только внутрь:

```
domain  ←  application  ←  infrastructure  ←  outer (server, main, runner, agents, tools, cmd, web)
```

| Слой | Где | Можно импортировать |
|---|---|---|
| domain + порты | `domain/`, `domain/services/` | stdlib |
| use-cases | `application/` | domain, порты, `ai/shared` |
| реализации | `infrastructure/` | domain, порты |
| shared-утилиты | `shared/` (logging, projects, tokens, langdetect) | stdlib |
| приватно модулю | `internal/` | всё внутри `ai/` |
| outer | `agents/`, `runner/`, `server/`, `tools/`, `cmd/`, `web/` | domain, application, infrastructure, shared |

**Гейт:** `make depgate` — `application/...` не импортирует ничего вне `ai/{domain,application,shared}`.
**Composition root:** `main.go`.

> Подробная история — в `docs/agents-history.md`, планы — в `docs/plans/`.

## `temp/` — проекты агентов

Каталог `temp/` — выходные артефакты агентов-разработчиков, **не код проекта**.

- НЕ удалять, не коммитить, не читать «полностью» — только по пути `temp/<имя_проекта>/`.
- Править — через агентов (`backend`/`frontend`), а не прямые редиректы.
- Контакты: `projects.ProjectDir`.

## Сборка и проверка

Канонический набор (обходит `temp/`):

```
go build . ./agents/... ./application/... ./tools/... ./cmd/... ./server/ ./runner/ ./infrastructure/... ./domain/...
go vet   . ./agents/... ./application/... ./tools/... ./cmd/... ./server/ ./runner/ ./infrastructure/... ./domain/...
go test  . ./agents/... ./application/... ./tools/... ./cmd/... ./server/ ./runner/ ./infrastructure/... ./domain/...
```

- `go build ./...` падает на `temp/moon-distance/…` — не трогай.
- `gofmt -l .` → 29 предсуществующих отклонений (не гейт; форматируй только свои файлы).
- `make depgate` — обязательная проверка перед комментом.

Живая песочница (нужен Docker): `SANDBOX_LIVE=1 go test ./infrastructure/sandbox/ -run TestLiveSession -v`

Известные флаки (не регресс):
- `TestAcceptBuild*` → fail на GNU Make 3.81 (marker mismatch)
- `TestChatAssistant*` → fail без живой Ollama

## Токено-эффективные команды

**ЛSP — приоритет.** Bash — фолбэк: git-контекст, `tail -N`, `grep -E` с фильтрами.

| Что | Команда |
|---|---|
| поиск в коде | `go tool cgo`, `grep -rnw '<символ>' .` |
| инвок через LSP | `StructureRefresh`, `LspDefinition`, `LspHover`, `LspReferences` |
| git / diff | `git log -n 3 --oneline`, `git blame -L 10,20 путь` |
| сократить вывод | `2>&1 \| tail -40`, `grep -E 'FAIL' \| head -60` |

**Запрещено:** `cat`/`head`/`tail` (аналог `ReadFiles`), `grep -r` без `--exclude-dir`, полные сценарии сборки без `-v`/`-q`.

Внутренние агенты (`agents/*`) имеют только `Run`/`ReadFiles`/`Write` — правила сокращения те же.

## Системные гейты доски (QA / оркестратор / песочница)

Основание: `docs/plans/PLAN-2026-10-18-A7-04-system-fix.md` (инцидент A7-04 — ping-pong `testing ↔ in_progress` без дефекта).

### Классификация ошибок: CODE vs INFRA
- **`CODE_FAIL`** — реальный дефект кода: несоответствие контрактам, падение unit-тестов, баги логики. Требует **конкретного замечания** (`файл:строка`, команда воспроизведения или шаги).
- **`CODE_OK | LINT_DEGRADED`** — `golangci-lint` отсутствует в песочнице → **не блокер**, не дефект.
- **`INFRA_DEGRADED`** — `migrate: connection refused` (нет Postgres), `docker: not found`/`DOCKER_UNAVAILABLE`, `make infra.up` не поднимает стек. **Не блокер** unit/contract-проверок → эскалация в **DevOps**, **не** возврат разработчику. Контракт capabilities и чеклист преднастройки песочницы — `docs/40-operations/sandbox-readiness.md`.
- **`INFRA_BLOCKED`** — явная невозможность интеграционных проверок; фиксируется с причиной, **не смешивается** с `CODE_FAIL`.

### Handoff Gate «Ready for QA»
Разработчик переводит задачу в `testing` (или агент отвечает текстом) **только** когда:
1. `git status --short` — пусто (или только разрешённые артефакты); изменения **закоммичены**.
2. `go build ./...` — exit 0.
3. `go test -race -count=1 ./...` (или `make backend-test`) — exit 0.
4. `gofmt -l .` — пусто.
5. `go vet ./...` — exit 0.

- **Post-write build-gate:** сразу после серии `WriteFiles`/`SearchReplace` — `go build ./...`; при ошибке **остановиться**, починить атомарно, не запускать тесты.
- В комментарии при переводе в `testing` фиксировать SHA коммита (HEAD).
- **Запрещено** передавать в QA код с нерабочей сборкой.
- **Код-гейт:** `BoardSetTaskStatus` при `in_progress → testing` сам проверяет чистоту дерева (`git status --short --untracked-files=no`) и сборку (`go build ./...` по маркерам проекта; override — `CODEGEN_HANDOFF_CMD`, выключение — `CODEGEN_HANDOFF_GATE=off`) — `tools/board_handoff.go`. Инфра-деграды (нет песочницы/тулчейна/git) не блокируют.

### Anti-ping-pong
- Возврат `testing → in_progress` возможен **только** с конкретным `reason` (файл:строка / команда воспроизведения / шаги). Общее «нужна доработка» **отклоняется** инструментом `BoardSetTaskStatus` (`tools/board_defect.go`), QA-комментарий с дефектом сохраняется в задаче.
- Fallback/`pulse expired` **не** является основанием для реверса `testing → in_progress`.
- `testing → quiet revert` без дефекта = инцидент; фиксируется `loop_prevented`.

### Shell / Workdir / Encoding safety
- **Shell:** не использовать `${PIPESTATUS[...]}` и прочие bashisms под `/bin/sh`; при необходимости — `bash -lc` или разбить команды.
- **Workdir:** запрещены жёсткие пути (`/home/user/project`, `/var/task`) вне `workdir` задачи (`temp/.wt-task-*`); `cd` заменять параметром `workdir`.
- **Encoding:** записи в UTF-8 без BOM; не использовать невалидный UTF-8/mojibake в `search` — брать бóльший уникальный контекст.
- **Patches:** `search` уникален в файле; избегать коротких дрейфующих фрагментов.

### Re-entry guard («старая версия ПО»)
- На входе в задачу: `git log -n 1 --oneline`, `git status --short`, `git diff --name-only`. Противоречивый WIP — сначала довести до **build green**, затем править.
- Контракты (`internal/service/interfaces.go`, `internal/repo/interfaces.go`, `internal/api/handlers/handlers.go`) — **источник истины**; при расхождении тестов с контрактом править реализацию под контракт, не наоборот.

### Правила приёма QA
- Приёмка только готового артефакта (build/vet/race+cover unit-тесты зелёные).
- `CODE_FAIL` → возврат с **конкретным** дефектом; `INFRA_*` → DevOps; `CODE` зелёные + только INFRA degrade → **done**.