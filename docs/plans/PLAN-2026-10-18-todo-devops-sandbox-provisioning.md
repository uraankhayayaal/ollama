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

### Acceptance criteria

- [ ] Docker daemon доступен в песочнице: `docker info` — exit 0.
- [ ] `docker compose` доступен: `docker compose version` — exit 0.
- [ ] Стек поднимается: `docker compose up -d` — Postgres и MinIO `running`/`healthy`.
- [ ] Миграции проходят: `make migrate` — exit 0.
- [ ] Интеграционный прогон возможен: `make e2e` — exit 0.
- [ ] Проставлены `SANDBOX_HAS_DOCKER=1`, `SANDBOX_HAS_POSTGRES=1`, `SANDBOX_HAS_MINIO=1`
      (и `SANDBOX_COMPOSE_FILE`, если есть) по **фактическому** состоянию.
- [ ] При недоступности инфры окружение явно помечается `INFRA_DEGRADED`
      (переменные `=0`), а не молчаливый пропуск.

### Проверка

```bash
docker info >/dev/null 2>&1 && echo "docker: ok" || echo "docker: INFRA_DEGRADED"
docker compose version >/dev/null 2>&1 && echo "compose: ok" || echo "compose: LINT_DEGRADED"
docker compose up -d && docker compose ps
make migrate && make e2e
```

Полный чеклист и таблица переменных — `docs/40-operations/sandbox-readiness.md`.

## Границы

- Правкой кода **не** закрывается: нужен доступ к CI/инфраструктуре песочницы.
- Агент не устанавливает инфраструктуру — она должна быть готова **до** запуска задач.
- Инфра-деграды остаются **не блокерами** unit/contract-проверок (см. `AGENTS.md`).
