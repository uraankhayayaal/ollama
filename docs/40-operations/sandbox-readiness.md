# Готовность песочницы (DevOps)

Основание: `docs/plans/PLAN-2026-10-18-A7-04-system-fix.md`, п. 3.1 (инцидент A7-04).

## Зачем

QA-агент и приёмка запускаются в песочнице (контейнер `Run`). Если в ней нет
Docker или не поднят стек (Postgres/MinIO), интеграционные проверки
(`make e2e`, `make migrate`, `bin/server`) падают **не из-за кода**. Без явной
классификации такие падения выглядели как дефект и возвращали задачу
разработчику — источник ping-pong `testing ↔ in_progress`.

**Граница ответственности:**

| Класс | Признак | Куда |
|---|---|---|
| `CODE_FAIL` | падает unit/contract-тест, расхождение с контрактом | разработчик/QA с конкретным дефектом |
| `INFRA_DEGRADED` | нет Docker/стека, `connection refused`, `docker: not found` | **DevOps**, не блокер unit-проверок |
| `LINT_DEGRADED` | `golangci-lint` отсутствует (Error 127) | **DevOps**, не дефект |
| `INFRA_BLOCKED` | интеграционные проверки невозможны по инфре | фиксируется с причиной, не смешивается с `CODE_FAIL` |

## Контракт возможностей

Целевой единый источник истины о возможностях окружения — переменные окружения
песочницы. Оркестратор/QA читают их и помечают отчёт `infra_degraded`, если
требуемой возможности нет.

> **Текущее состояние.** Потребитель реализован:
> `infrastructure/sandbox/capabilities.go` — tri-state (`Unknown/Available/
> Unavailable`): `ReadCapabilities` (env), `DetectCapabilities` (авто-детект:
> docker ping, `dialPort` 5432/9000), `ResolveCapabilities` (env приоритетнее
> факта; при отсутствии env — детект) и `Summary()`. QA-агент добавляет сводку
> в системный промпт (`agents/qaengineer/agent.go`, `capabilities`/`envRule`).
> Если переменные **не объявлены**, промпт не утверждает, что проверки
> недоступны, а просит определить доступность по факту запуска — чтобы на
> чистом dev/CI интеграционные тесты не пропускались ошибочно.
> `SANDBOX_HAS_*` пробрасываются в контейнер: `sandbox.CapabilityEnv()` входит
> в `BaseEnv()`, объявлены в `Dockerfile` и `compose.yaml`.
> Открытая часть п. 3.1 — фактическая преднастройка Docker-стека в CI-песочнице
> силами DevOps (`docs/plans/PLAN-2026-10-18-todo-devops-sandbox-provisioning.md`).

| Переменная | Значения | Смысл |
|---|---|---|
| `SANDBOX_HAS_DOCKER` | `1`/`0` | демон Docker доступен (`docker info` ok) |
| `SANDBOX_HAS_POSTGRES` | `1`/`0` | Postgres поднят и принимает соединения |
| `SANDBOX_HAS_MINIO` | `1`/`0` | MinIO (S3) поднят и отвечает |
| `SANDBOX_COMPOSE_FILE` | путь | compose-файл стека (если есть) |

Отсутствие переменных — не «0», а «неизвестно»: `ResolveCapabilities`
дополняет их авто-детектом (docker ping, порт 5432/9000). Явно проставленный
`0` означает «проверено — недоступно» и не переопределяется детектом.

Уже существующие в коде пробы (переиспользовать, не дублировать):

- `infrastructure/sandbox/engine.go` — `EngineAvailable()` (ping демона, auto-режим).
- `tools/` — `haveTool("docker")` (наличие бинаря, для `AppLogs`).
- `agents/qaengineer/agent.go` — правила `degrade` для golangci/`ReadAppLogs`.

## Чеклист DevOps (преднастройка песочницы)

Песочница авто-проверки должна быть готова **до** запуска задач; агент не
устанавливает инфраструктуру.

- [ ] Docker daemon доступен в песочнице: `docker info` — exit 0.
- [ ] `docker compose` доступен: `docker compose version` — exit 0.
- [ ] Стек поднимается: `go run ./cmd/sandbox-provision up` — Postgres `healthy`, порт `9000` отвечает.
- [ ] Миграции проходят: `make migrate` (или эквивалент) — exit 0.
- [ ] Интеграционный прогон возможен: `make e2e` — exit 0.
- [ ] Проставлены `SANDBOX_HAS_DOCKER/POSTGRES/MINIO` (`eval "$(go run ./cmd/sandbox-provision env)"`).
- [ ] При недоступности инфры окружение явно помечается `INFRA_DEGRADED`
      (не молчаливый пропуск).

## Провижининг стека (репозиторные артефакты)

Чтобы песочница авто-проверки имела доступный Postgres/MinIO и `SANDBOX_HAS_*`
были достоверны, в репозитории есть воспроизводимый стек и команда управления:

- `infrastructure/sandbox/provision/compose.yaml` — Postgres + MinIO, публикует
  типовые порты на хосте (`5432`, `9000`, консоль `9001`) — те же, что пробует
  `sandbox.DetectCapabilities()`.
- `cmd/sandbox-provision` — подъём/снятие и вывод возможностей:

```bash
go run ./cmd/sandbox-provision up        # docker compose up -d --wait + ожидание портов
eval "$(go run ./cmd/sandbox-provision env)"   # экспорт SANDBOX_HAS_*/SANDBOX_COMPOSE_FILE
go run ./cmd/sandbox-provision status    # состояние Docker/Postgres/MinIO
go run ./cmd/sandbox-provision down      # снять (down -v — вместе с томами)
```

Переопределения: `SANDBOX_PROVISION_COMPOSE` (путь к compose),
`SANDBOX_PROVISION_TIMEOUT` (сек ожидания готовности, дефолт 90),
`SANDBOX_PG_*` / `SANDBOX_MINIO_*` (учётные данные).

Стек публикует порты на **хосте** оркестратора; адрес для проекта внутри
контейнера-песочницы задаётся в самом проекте (`DATABASE_URL`/`S3_ENDPOINT`):
на Linux — IP шлюза bridge-сети, на Docker Desktop — `host.docker.internal`.

## Команды проверки

```bash
docker info >/dev/null 2>&1 && echo "docker: ok" || echo "docker: INFRA_DEGRADED"
docker compose version >/dev/null 2>&1 && echo "compose: ok" || echo "compose: LINT_DEGRADED"
go run ./cmd/sandbox-provision up && go run ./cmd/sandbox-provision status
```

## Проверка классификации (в отчёте QA)

```
runtime-проверка невозможна в sandbox (БД/инфраструктура недоступна),
покрывается make e2e в CI — INFRA_DEGRADED, не дефект кода.
```

## Связанное

- [Тестирование](testing.md)
- [Решение проблем](troubleshooting.md)
- План: `docs/plans/PLAN-2026-10-18-A7-04-system-fix.md`
