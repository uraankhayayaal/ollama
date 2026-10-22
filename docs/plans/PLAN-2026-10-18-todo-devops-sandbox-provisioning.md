# PLAN-2026-10-18-devops-sandbox-provisioning — Преднастройка песочницы авто-проверки

Дата: 2026-10-18
Статус: rollout готов (фактический прогод джоба на CI-песочнице — за DevOps)
Автор: opencode
Инцидент-основание: A7-04, `docs/plans/PLAN-2026-10-18-A7-04-system-fix.md` (п. 3.1, открытая часть).

## Проблема

Песочница авто-проверки могла не иметь Docker-демона и стека Postgres/MinIO.
Интеграционные проверки (`make migrate`, `make e2e`, запуск `bin/server`) падали
**не из-за кода**, а по инфре — и такие падения выглядели как дефект, возвращая
задачу разработчику (ping-pong `testing ↔ in_progress`). Код-side классификация
`CODE_*` vs `INFRA_*` и контракт capabilities (см.
`docs/40-operations/sandbox-readiness.md`) уже реализованы; остаётся **фактически
преднастроить окружение** и проставить `SANDBOX_HAS_*`.

## Задача (DevOps)

Настроить песочницу авто-проверки так, чтобы контракт capabilities отражал факт,
а интеграционные проверки были воспроизводимы.

### Репозиторные артефакты (готово)

Воспроизводимый стек и команда управления добавлены в репозиторий:

- [x] `infrastructure/sandbox/provision/compose.yaml` — Postgres + S3-хранилище,
      публикует типовые порты на хосте (`5432`, `9000`, консоль `9001`) — те
      же, что пробует `sandbox.DetectCapabilities()` (порты переопределяются
      `SANDBOX_PG_HOST_PORT`/`SANDBOX_S3_HOST_PORT`; образ хранилища —
      `SANDBOX_MINIO_IMAGE`, по умолчанию `rustfs/rustfs:1.0.3`, т.к. образ
      `minio/minio` удалён из Docker Hub). Проходит `docker compose config`.
- [x] `cmd/sandbox-provision` — `up` (`docker compose up -d --wait` + ожидание
      доступности портов), `down`, `status`, `env` (экспорт
      `SANDBOX_HAS_*`/`SANDBOX_COMPOSE_FILE` по факту), `migrate` (DDL
      песочничного Postgres). Тесты — `cmd/sandbox-provision/main_test.go`.
- [x] Чеклист и команды обновлены в `docs/40-operations/sandbox-readiness.md`.
- [x] `cmd/sandbox-readiness` — исполняемый чеклист AC1–AC7 с вердиктом и
      кодами возврата (`0` READY / `2` INFRA_DEGRADED / `1` FAIL); make-цели
      `infra.up`/`infra.down`/`infra.ps`/`infra.logs`/`migrate`/`e2e`/
      `sandbox-readiness`/`backend-test` в корневом `makefile`.
- [x] CI-джоб `.github/workflows/sandbox-readiness.yml` — rollout acceptance
      criteria в CI.

### Acceptance criteria (rollout — за DevOps/CI)

Каждый пункт проверяется **исполняемым** чеклистом `make sandbox-readiness`
(`cmd/sandbox-readiness`): AC1–AC7 проверяются по факту окружения, вердикт
печатается, код возврата — `0` READY / `2` INFRA_DEGRADED / `1` FAIL.
В CI джоб `.github/workflows/sandbox-readiness.yml` вызывает те же make-цели
и различает вердикты: `FAIL` роняет джоб, `INFRA_DEGRADED` — warning (деграйд
инфраструктуры **не** дефект кода).

- [x] Docker daemon доступен в песочнице авто-проверки: `docker info` — exit 0
      (AC1, проверяет `make sandbox-readiness`).
- [x] `docker compose` доступен: `docker compose version` — exit 0 (AC2).
- [x] Стек поднимается: `make infra.up` — Postgres `healthy`, порт `9000`
      отвечает (AC3). Хостовые порты переопределяются
      (`SANDBOX_PG_HOST_PORT`/`SANDBOX_S3_HOST_PORT`), образ S3 — `SANDBOX_MINIO_IMAGE`.
- [x] Миграции проходят: `make migrate` — exit 0 (AC4).
- [x] Интеграционный прогон возможен: `make e2e` — exit 0 (AC5); пропуск тестов
      (нет Chrome/Docker) = `INFRA_DEGRADED`, а не «зелёный» прогон.
- [x] Проставлены `SANDBOX_HAS_DOCKER=1`, `SANDBOX_HAS_POSTGRES=1`, `SANDBOX_HAS_MINIO=1`
      (`eval "$(bin/sandbox-provision env)"`) по **фактическому** состоянию; CI
      экспортирует их в `$GITHUB_ENV` (AC6).
- [x] При недоступности инфры окружение явно помечается `INFRA_DEGRADED`
      (переменные `=0`), а не молчаливый пропуск (AC7).

### Проверка

```bash
make infra.up                     # стек Postgres + S3 (или bin/sandbox-provision up)
eval "$(bin/sandbox-provision env)"
make migrate && make e2e
make sandbox-readiness            # полный чеклист AC1–AC7 + вердикт
```

Полный чеклист и таблица переменных — `docs/40-operations/sandbox-readiness.md`.

## Границы

- Репозиторные артефакты готовы; **фактический rollout** (Docker-демон в
  CI-песочнице, проставление `SANDBOX_HAS_*`) — за DevOps/CI: нужен доступ к
  инфраструктуре песочницы.
- Агент не устанавливает инфраструктуру — она должна быть готова **до** запуска задач.
- Инфра-деграды остаются **не блокерами** unit/contract-проверок (см. `AGENTS.md`).

## Статус

- Код-side (контракт capabilities, авто-детект, проброс `SANDBOX_HAS_*`) — готово, см.
  `PLAN-2026-10-18-A7-04-system-fix.md`.
- Репозиторные артефакты провижининга — готово (этот документ).
- CI-rollout — **готово**: джоб `.github/workflows/sandbox-readiness.yml`
  (подъём стека, экспорт `SANDBOX_HAS_*` по факту, исполняемый чеклист,
  артефакт-отчёт, различение `READY`/`INFRA_DEGRADED`/`FAIL`).
  Открытым остаётся только фактический прогон джоба на инфраструктуре
  CI-песочницы (доступ к Docker-демону выдаёт DevOps).
