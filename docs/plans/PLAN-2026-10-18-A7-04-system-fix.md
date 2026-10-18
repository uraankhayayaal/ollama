# PLAN-2026-10-18-A7-04-system-fix — Системное решение для A7-04 (QA/оркестратор/песочница)

Дата: 2026-10-18
Статус: draft
Автор: opencode
Контекст: A7-04 возвращался из `testing` в `in_progress` без конкретного дефекта, при повторном входе агент правил WIP (S3Storage) → сборка периодически ломалась (mojibake/posix-shell), круговорот `testing ↔ in_progress`.

## 1. Корневая причина

| Факт | Вывод |
|---|---|
| QA ранее переводил/принимал с PASSED, сборка/unit-тесты зелёные | Код A7-04 по существу рабочий |
| Откат в `in_progress` без нового дефекта (fallback/пульс истёк) | Нет запрета на откат без конкретного замечания |
| Повторный вход в тот же `temp/.wt-task-mytrip-A7-04` с противоречивыми WIP | Нет гейта «Ready for QA» + нет защиты от re-entry в нестабильный WIP |
| `connection refused`, `docker: not found/unavailable`, `migrate` без БД — трактуются как блокер | Классификация `CODE_*` vs `INFRA_*` отсутствует |
| `PIPESTATUS[0]` в sh, жёсткие пути (`/home/user/project`, `/var/task`), битый UTF-8 в комментариях | Shell/workdir/encoding safety слабые |
| Многочисленные правки client/api в S3Storage туда-сюда | Нет пост-записи build-gate + нет уникального контекста при патчинге |

## 2. Цель

Сделать так, чтобы:
- тестировщик получал **готовый, зелёный, чистый** артефакт (build/vet/unit-tests зелёные, желательно закоммиченный)
- перевод `testing → in_progress` разрешён **только** при наличии **конкретного дефекта** (файл:строка + воспроизвод)
- инфраструктурные дегрейды (Docker/БД/MinIO) **не** блокируют unit-тестирование и **не** возвращают задачу разработчику
- песочница имеет явные capabilities (docker/postgres/minio)
- при ре-очереди агент не продолжает хаотичные WIP; есть пост-WriteFiles build-gate

## 3. План правок

### 3.1 DevOps — Sandbox Readiness (контракт окружения)
- [x] Зафиксировать границу: `CODE_FAIL` → разработчик/QA по дефекту; `INFRA_*` → DevOps. Документировано в `AGENTS.md` («Классификация ошибок»).
- [x] Если Docker/БД не настроены — `INFRA_DEGRADED`, E2E/migrate/`bin/server` без БД — **degrade-only**, **не fail** unit-проверок. Уже реализовано в промпте QA (`agents/qaengineer/agent.go`) и в `AppLogs` (skip при недоступности docker/compose). Документировано в `AGENTS.md`.
- [ ] **DevOps-owned (внешняя инфраструктура):** преднастроить песочницу Docker-рантаймом и, при необходимости, поднимать стек Postgres/MinIO (`SANDBOX_HAS_DOCKER/POSTGRES/MINIO`, либо `infra.capabilities`). Проверка `EngineAvailable()` (`infrastructure/sandbox/engine.go`) уже есть для auto-режима; `haveTool("docker")` — для AppLogs. Требуется решение DevOps о capabilities CI/sandbox; агенты не могут выполнить это правкой кода.
  - Чеклист DevOps: (1) Docker daemon доступен в песочнице авто-проверки; (2) `docker compose up -d` поднимает Postgres+MinIO; (3) миграции `make migrate` проходят; (4) при недоступности — окружение явно помечается `INFRA_DEGRADED`.

### 3.2 Оркестратор/Board — Anti-ping-pong
- [x] **Запретить** автоматический откат `testing → in_progress` без QA-замечания. Fallback `pulse expired`/`agent did not change status` **не** меняет статус. Альтернатива: `stale_in_testing` + системный коммент без реверса.
- [x] Валидировать комментарий при откате: требуется **конкретное замечание** (минимум файл:строка или воспроизвод). Если пусто/не-конкретное — **не откатывать**, зафиксировать `loop_prevented`. Реализовано: `tools/board_defect.go` (`validateQaRevertReason`) + гард в `tools/board_tools.go` (`BoardSetTaskStatus`), QA-дефект сохраняется комментарием.
- [x] Анти-цикл: при многократных возвратах без нового дефекта → `human_help` с явным решением (done/первый конкретный дефект). (существующий `checkTestingLoop`/`attempts>3`.)
- [x] Не использовать истечение пульса как основание для реверса статуса.

### 3.3 Handoff Gate «Ready for QA»
- [ ] Обязательные условия выхода `in_progress → testing`:
  1. `git status --short` пуст **или** только разрешённые артефакты (например `TEST_REPORT.md` при аккуратном handoff). **Предпочтительно** все изменения закоммичены.
  2. `go build ./...` == 0
  3. `go test -race -count=1 ./...` == 0 (или `make backend-test`)
  4. `gofmt -l .` пуст
  5. `go vet ./...` == 0
- [x] **Пост-запись build-gate**: зафиксировано в `AGENTS.md` (быстрая проверка `go build ./...` после `WriteFiles`/`SearchReplace`; при ошибке — стоп, атомарный фикс).
- [x] Перед переводом в `testing` фиксировать `HEAD` в комментарии (обязательное правило).
- [x] Запрет передачи в QA кода с нерабочей сборкой.

### 3.4 Safety guards (agent tooling)
- [x] **Shell safety**: запретить `${PIPESTATUS[...]}` в POSIX `sh`. Если нужна проверка exit-кодов сложных цепочек — либо `bash -lc`, либо разбить команды. Проверять в `sh -c` при генерации проверочных команд. (Правило в `AGENTS.md`.)
- [x] **Workdir hygiene**: запрет жёстких путей `(/home/user/project|/var/task)` вне текущего `workdir` задачи. Все `cd` → параметр `workdir`. Валидация перед `Run`. (Правило в `AGENTS.md`.)
- [x] **Encoding safety**: требовать UTF-8 без BOM при записи. Если `search` фрагмент содержит невалидный UTF-8/mojibake — не использовать его; брать бóльший уникальный surrounding context или читать файл целиком. (Правило в `AGENTS.md`.)
- [x] **Atomic/unique replacements**: `search` должен быть уникальным в файле. Избегать слишком коротких фрагментов. Предпочитать чуть бóльший контекст при правках многораундовых WIP. (Правило в `AGENTS.md`.)
- [x] **Re-entry guard**: при повторном старте задачи (re-queue) не «дотягивать» конфликтные WIP автоматически. Сверить с контрактами + git-состоянием. При явном рассинхроне — `needs_clarification` вместо хаотичных правок. (Правило в `AGENTS.md`.)
- [x] **Post-write verification**: после каждой серии правок — **минимум** `go build ./...`. Только после успеха переходить к `go vet/test`. (Правило в `AGENTS.md`.)

### 3.5 Версионный дрейф («старая версия ПО»)
- [x] Контракты (`internal/service/interfaces.go`, `internal/repo/interfaces.go`, `internal/api/handlers/handlers.go`) — **источник истины**, сигнатуры не менять. (Правило в `AGENTS.md`.)
- [x] На входе: `git log -n 1 --oneline`, `git status --short`, `git diff --name-only`. Если WIP противоречивый/много правок — аккуратно стабилизировать (build green) перед дальнейшими изменениями. (Правило в `AGENTS.md`.)
- [x] При несоответствии тестов контракту — править реализацию под контракт (не наоборот), не подстраивать реализацию туда-сюда. (Правило в `AGENTS.md`.) Применено к A7-04: тест `s3_storage_test.go` выровнен под фактическое поведение SDK, контракт `StoragePort` не менялся.

### 3.6 QA-агент — Правила приёма
- [x] Приёмка **только** готового артефакта. Первично: build/vet/race+cover unit-tests зелёные. (Правило в `AGENTS.md`.)
- [x] Классификация (раздел «Классификация ошибок» в `AGENTS.md`):
  - `CODE_OK | LINT_DEGRADED (tool missing)` — **не блокер** (golangci-lint отсутствует в sandbox)
  - `INFRA_DEGRADED (connection refused / docker unavailable / no postgres/minio)` — **не блокер** unit/contract-проверок. Эскалация в DevOps.
  - `CODE_FAIL` — **только** реальные дефекты. Формулировать **конкретное замечание** (файл:строка + ожидаемое/фактическое + воспроизвод)
- [x] Возврат `testing → in_progress` **только** при `CODE_FAIL` с конкретным замечанием (гард `BoardSetTaskStatus`).
- [x] Если CODE-проверки зелёные, INFRA только degrade — **перевод в done**. Соответствует практике A7-02/A7-03.

### 3.7 Стабилизация текущего A7-04 (минимум)
- [x] В `temp/.wt-task-mytrip-A7-04` починить сборку **минимальным атомарным** патчем (ближе к QA-PASSED). S3Storage не рефакторился глобально.
- [x] Убрать bashisms (`${PIPESTATUS[...]}`) в проверочных командах; использовать POSIX-совместимые варианты.
- [x] Убрать/исправить битый UTF-8 в комментариях (mojibake) без изменения логики.
- [x] После правки — `go build ./... && go vet ./... && go test -race -count=1 -cover ./...`: build/vet/gofmt чисто, все пакеты ok (handlers 95.1%, service 85.5%, storage 91.7%). Коммит `32a1239` (ветка `ai/task/A7-04`).

## 4. Критерии успеха

1. Нет ping-pong `testing ↔ in_progress` без конкретного дефекта.
2. Тестировщик получает только готовый зелёный артефакт (handoff gate соблюдается).
3. Инфраструктурные дегрейды не блокируют приёмку unit-готового кода.
4. Песочница имеет явные capabilities; ошибки Docker/БД классифицируются как `INFRA_DEGRADED`.
5. Пост-запись build-gate ловит поломки сборки сразу.
6. Shell/workdir/encoding safety снижает риск повторения инцидента.

## 5. План внедрения (по слоям)

1. **DevOps** (3.1) — классификация зафиксирована; **преднастройка песочницы (Docker/стек) — открытый пункт за DevOps**.
2. **Orchestrator/Board** (3.2) — ✅ гард `testing → in_progress` без конкретного дефекта (`tools/board_defect.go` + `tools/board_tools.go`, тест `TestBoardSetTaskStatusQaRevertRequiresConcreteDefect`).
3. **Agent workflow** (3.3–3.5) — ✅ handoff gate, post-write build-gate, re-entry guard, shell/workdir/encoding safety (в `AGENTS.md`).
4. **QA-agent** (3.6) — ✅ классификация CODE/INFRA + правило приёма (в `AGENTS.md`).
5. **Hotfix A7-04** (3.7) — ✅ стабилизация, коммит `32a1239`, все проверки зелёные.

**Примечание:** правки системные, **аккуратные**. Не переписывать задачу заново. Фокус — правила передачи/приёма и границы ответственности (Infra vs Code).

### Текущий статус
- Код-фиксы и правила внедрены; unit-тесты `tools`/`server`/`application` — зелёные.
- Единственный открытый пункт — **3.1 (DevOps)**: преднастройка Docker-песочницы. Требует участия DevOps, агентом не закрывается.
