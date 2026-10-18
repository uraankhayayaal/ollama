# PLAN-2026-10-18-devops-sandbox-provisioning — Преднастройка песочницы авто-проверки

Дата: 2026-10-18
Статус: todo (владение: DevOps)
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

- [x] `infrastructure/sandbox/provision/compose.yaml` — Postgres + MinIO, публикует
      типовые порты на хосте (`5432`, `9000`, консоль `9001`) — те же, что пробует
      `sandbox.DetectCapabilities()`. Проходит `docker compose config`.
- [x] `cmd/sandbox-provision` — `up` (`docker compose up -d --wait` + ожидание
      доступности портов), `down`, `status`, `env` (экспорт
      `SANDBOX_HAS_*`/`SANDBOX_COMPOSE_FILE` по факту). Тесты —
      `cmd/sandbox-provision/main_test.go`.
- [x] Чеклист и команды обновлены в `docs/40-operations/sandbox-readiness.md`.

### Acceptance criteria (rollout — за DevOps/CI)

- [ ] Docker daemon доступен в песочнице авто-проверки: `docker info` — exit 0.
- [ ] `docker compose` доступен: `docker compose version` — exit 0.
- [ ] Стек поднимается: `go run ./cmd/sandbox-provision up` — Postgres `healthy`,
      порт `9000` отвечает.
- [ ] Миграции проходят: `make migrate` — exit 0.
- [ ] Интеграционный прогон возможен: `make e2e` — exit 0.
- [ ] Проставлены `SANDBOX_HAS_DOCKER=1`, `SANDBOX_HAS_POSTGRES=1`, `SANDBOX_HAS_MINIO=1`
      (`eval "$(go run ./cmd/sandbox-provision env)"`) по **фактическому** состоянию.
- [ ] При недоступности инфры окружение явно помечается `INFRA_DEGRADED`
      (переменные `=0`), а не молчаливый пропуск.

### Проверка

```bash
docker info >/dev/null 2>&1 && echo "docker: ok" || echo "docker: INFRA_DEGRADED"
docker compose version >/dev/null 2>&1 && echo "compose: ok" || echo "compose: LINT_DEGRADED"
go run ./cmd/sandbox-provision up && go run ./cmd/sandbox-provision status
eval "$(go run ./cmd/sandbox-provision env)"
make migrate && make e2e
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
- CI-rollout — открыт за DevOps.
