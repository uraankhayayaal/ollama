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
> Пункт 3.1 закрыт: фактическая преднастройка Docker-стека и проверка
> готовности вынесены в CI (`.github/workflows/sandbox-readiness.yml`) и в
> исполняемый чеклист `make sandbox-readiness` — см. «Rollout в CI».

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
- [ ] Стек поднимается: `make infra.up` — Postgres `healthy`, порт `9000` отвечает.
- [ ] Миграции проходят: `make migrate` — exit 0.
- [ ] Интеграционный прогон возможен: `make e2e` — exit 0.
- [ ] Проставлены `SANDBOX_HAS_DOCKER/POSTGRES/MINIO` (`eval "$(bin/sandbox-provision env)"`).
- [ ] При недоступности инфры окружение явно помечается `INFRA_DEGRADED`
      (не молчаливый пропуск).

Чеклист исполняемый: `make sandbox-readiness` (см. ниже) проверяет каждый
пункт по факту и печатает вердикт — ручной обход списка выше нужен только
для первичной настройки машины.

## Команды make (единый источник истины)

| Цель | Что делает | Код возврата |
|---|---|---|
| `make infra.up` | подъём стека Postgres + S3 (`docker compose up -d --wait` + ожидание портов) | 0 — ок, 2 — `INFRA_DEGRADED` |
| `make infra.down` | снятие стека | 0 — ок |
| `make infra.ps` / `make infra.logs` | состояние и логи контейнеров | 0 — ок |
| `make migrate` | применение DDL песочницы (`server/testdata/todoapp/schema.sql`) | 0 — ок, 2 — `INFRA_DEGRADED` |
| `make e2e` | самозавершающийся прогон: up → migrate → e2e-тесты → down | 0 — ок, 2 — `INFRA_DEGRADED` (нет Chrome/Docker), 1 — падение тестов |
| `make sandbox-readiness` | полный чеклист acceptance criteria (AC1–AC7) | 0 — `READY`, 2 — `INFRA_DEGRADED`, 1 — `FAIL` |
| `make backend-test` | канонический набор тестов без `temp/` (Handoff Gate) | как у `go test` |

`make e2e` **снимает стек** по завершении (в том числе при ошибке) — после
прогона песочницу нужно поднять заново (`make infra.up`).

## Чеклист acceptance criteria (`make sandbox-readiness`)

`cmd/sandbox-readiness check` проверяет пункты acceptance criteria плана
`docs/plans/PLAN-2026-10-18-todo-devops-sandbox-provisioning.md` по факту
окружения и печатает вердикт:

| Пункт | Проверка |
|---|---|
| AC1 | `docker info` — exit 0 |
| AC2 | `docker compose version` — exit 0 |
| AC3 | `make infra.up` — Postgres `healthy`, порт `9000` отвечает |
| AC4 | `make migrate` — exit 0 |
| AC5 | `make e2e` — exit 0 (пропуск тестов = `INFRA_DEGRADED`, а не «зелёный») |
| AC6 | `SANDBOX_HAS_*` не противоречат фактическому состоянию |
| AC7 | недоступная инфра помечена явно: `SANDBOX_HAS_*=0` + `INFRA_DEGRADED` |

Коды возврата: `0` — `READY` (все пункты выполнены), `2` — `INFRA_DEGRADED`
(инфраструктура недоступна; **не** дефект кода), `1` — `FAIL` (пункт
acceptance criteria не выполнен).

> `SANDBOX_HAS_DOCKER` отражает **демон** (пинг сокета), а чеклист AC1/AC3 —
> **CLI** (`docker info`, `docker compose`). Среда с сокетом, но без `docker`
> в `PATH` даёт `SANDBOX_HAS_DOCKER=1` и одновременно `INFRA_DEGRADED` по
> AC1/AC3–AC5: compose и миграции без CLI невозможны.

Флаги: `--skip-integration` (без `make migrate`/`make e2e`), `--json`,
`--report=FILE` (markdown-отчёт, артефакт CI). В CI вердикт дополнительно
публикуется в `$GITHUB_OUTPUT` (`verdict=READY|INFRA_DEGRADED|FAIL`).

## Rollout в CI

Джоб `.github/workflows/sandbox-readiness.yml` (события: push в `main`,
pull request, ручной запуск):

1. checkout, `setup-go` (версия из `go.mod`), `setup-node` + сборка `web/dist`
   (нужен для `go:embed` в `web/webui.go`);
2. `make infra.up` — подъём стека;
3. экспорт `SANDBOX_HAS_*` **по факту** в `$GITHUB_ENV`:
   `bin/sandbox-provision env | sed 's/^export //' >> "$GITHUB_ENV"`;
4. `make sandbox-readiness SANDBOX_READINESS_ARGS=--report=readiness-report.md`
   (шаг с `continue-on-error`, вердикт разбирает следующий шаг);
5. итог: `READY` — зелёный, `INFRA_DEGRADED` — warning (джоб **не** падает:
   деграйд инфраструктуры — не дефект кода), `FAIL` — ошибка джоба;
6. отчёт `readiness-report.md` публикуется как артефакт.

Джоб не дублирует команды песочницы руками — он вызывает те же make-цели,
что и локальная проверка.

Автотесты репозитория (unit/contract, Handoff Gate «Ready for QA») живут в
**отдельном** джобе `.github/workflows/autotest.yml`: там нет Docker и стека —
набор герметичен (Docker, Ollama и внешняя сеть не нужны), поэтому
интеграционные проверки не могут маскировать падение юнит-тестов и наоборот.
Единый источник пакета — `GO_PACKAGES` в `makefile` (цели `ci-build`, `ci-vet`,
`backend-test`): локально и в CI зовутся одни и те же команды.

## Провижининг стека (репозиторные артефакты)

Чтобы песочница авто-проверки имела доступный Postgres/S3 и `SANDBOX_HAS_*`
были достоверны, в репозитории есть воспроизводимый стек и команда управления:

- `infrastructure/sandbox/provision/compose.yaml` — Postgres + S3-совместимое
  хранилище, публикует типовые порты на хосте (`5432`, `9000`, консоль
  `9001`) — те же, что пробует `sandbox.DetectCapabilities()`.
- `cmd/sandbox-provision` — подъём/снятие и вывод возможностей:

```bash
bin/sandbox-provision up        # docker compose up -d --wait + ожидание портов
eval "$(bin/sandbox-provision env)"   # экспорт SANDBOX_HAS_*/SANDBOX_COMPOSE_FILE
bin/sandbox-provision status    # состояние Docker/Postgres/S3
bin/sandbox-provision migrate   # применить DDL песочницы
bin/sandbox-provision down      # снять (down -v — вместе с томами)
```

> Бинарь `bin/sandbox-provision` собирается make-целями (`make infra.up`
> и др.). `go run ./cmd/sandbox-provision …` тоже работает, но **не пробрасывает
> код возврата** (`go run` всегда даёт 1) — в скриптах используйте бинарь.

Переопределения: `SANDBOX_PROVISION_COMPOSE` (путь к compose),
`SANDBOX_PROVISION_TIMEOUT` (сек ожидания готовности, дефолт 90),
`SANDBOX_PG_*` / `SANDBOX_MINIO_*` (учётные данные).

### Образ S3-хранилища

Образ `minio/minio` **удалён из Docker Hub** (репозиторий больше не существует),
поэтому по умолчанию используется `rustfs/rustfs:1.0.3` — тот же S3 API, тот же
health-endpoint (`/minio/health/live`), та же раскладка портов. Настоящий MinIO
подключается переопределением `SANDBOX_MINIO_IMAGE`
(например `quay.io/minio/minio:<tag>`).

### Порты стека на хосте

Хостовые порты переопределяются — на машине, где `5432`/`9000` уже заняты
другим стеком, песочница поднимается на свободных:

```bash
SANDBOX_PG_HOST_PORT=25433 SANDBOX_S3_HOST_PORT=9002 SANDBOX_S3_CONSOLE_PORT=9003 make infra.up
```

Проба возможностей (`sandbox.DetectCapabilities()`) читает те же переменные —
иначе стек поднялся бы на нестандартном порту, а проверка считала бы Postgres
недоступным.

Стек публикует порты на **хосте** оркестратора; адрес для проекта внутри
контейнера-песочницы задаётся в самом проекте (`DATABASE_URL`/`S3_ENDPOINT`):
на Linux — IP шлюза bridge-сети, на Docker Desktop — `host.docker.internal`.

## Chrome для интеграционного прогона

`make e2e` требует Chrome (chromedp): путь берётся из `CHROME_PATH`, затем из
`PATH` (`google-chrome`, `google-chrome-stable`, `chromium`,
`chromium-browser`), затем — macOS-путь. Если Chrome не найден, прогон
помечается `INFRA_DEGRADED` (пропуск виден, а не выглядит успехом).

## Команды проверки

```bash
docker info >/dev/null 2>&1 && echo "docker: ok" || echo "docker: INFRA_DEGRADED"
docker compose version >/dev/null 2>&1 && echo "compose: ok" || echo "compose: LINT_DEGRADED"
make infra.up && bin/sandbox-provision status
make sandbox-readiness   # полный чеклист acceptance criteria
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
