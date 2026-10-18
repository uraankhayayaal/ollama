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

> **Текущее состояние.** Потребитель реализован: `sandbox.ReadCapabilities()`
> (`infrastructure/sandbox/capabilities.go`) читает `SANDBOX_HAS_*`, а QA-агент
> добавляет сводку в системный промпт (`agents/qaengineer/agent.go`, `envRule`).
> Если переменные **не объявлены**, промпт не утверждает, что проверки
> недоступны, а просит определить доступность по факту запуска — чтобы на
> чистом dev/CI интеграционные тесты не пропускались ошибочно.
> Открытая часть п. 3.1 — фактическая преднастройка Docker-стека в CI-песочнице
> силами DevOps.

| Переменная | Значения | Смысл |
|---|---|---|
| `SANDBOX_HAS_DOCKER` | `1`/`0` | демон Docker доступен (`docker info` ok) |
| `SANDBOX_HAS_POSTGRES` | `1`/`0` | Postgres поднят и принимает соединения |
| `SANDBOX_HAS_MINIO` | `1`/`0` | MinIO (S3) поднят и отвечает |
| `SANDBOX_COMPOSE_FILE` | путь | compose-файл стека (если есть) |

Отсутствие переменных трактуется как `0` (возможности нет) — fail-safe:
неизвестное окружение не считается «полным».

Уже существующие в коде пробы (переиспользовать, не дублировать):

- `infrastructure/sandbox/engine.go` — `EngineAvailable()` (ping демона, auto-режим).
- `tools/` — `haveTool("docker")` (наличие бинаря, для `AppLogs`).
- `agents/qaengineer/agent.go` — правила `degrade` для golangci/`ReadAppLogs`.

## Чеклист DevOps (преднастройка песочницы)

Песочница авто-проверки должна быть готова **до** запуска задач; агент не
устанавливает инфраструктуру.

- [ ] Docker daemon доступен в песочнице: `docker info` — exit 0.
- [ ] `docker compose` доступен: `docker compose version` — exit 0.
- [ ] Стек поднимается: `docker compose up -d` — Postgres и MinIO в статусе `running`/`healthy`.
- [ ] Миграции проходят: `make migrate` (или эквивалент) — exit 0.
- [ ] Интеграционный прогон возможен: `make e2e` — exit 0.
- [ ] Проставлены `SANDBOX_HAS_DOCKER/POSTGRES/MINIO` по фактическому состоянию.
- [ ] При недоступности инфры окружение явно помечается `INFRA_DEGRADED`
      (не молчаливый пропуск).

## Команды проверки

```bash
docker info >/dev/null 2>&1 && echo "docker: ok" || echo "docker: INFRA_DEGRADED"
docker compose version >/dev/null 2>&1 && echo "compose: ok" || echo "compose: LINT_DEGRADED"
docker compose up -d && docker compose ps
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
