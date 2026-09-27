# План: «Промышленные зоны роста» — радар и дорожная карта

Статус: **ДОРОЖНАЯ КАРТА (реализация по приоритетам)**. Это переработанный
`PLAN-2026-09-19-todo-owerview-for-prom.md`: вместо списка «чего не хватает» — радар зон роста с
привязкой к текущему коду, статусом (что уже сделано) и этапами внедрения.
Часть пунктов закрыта в `PLAN-2026-09-17-done-webui.md`, `PLAN-2026-09-19-done-lsp.md`, `PLAN-2026-09-19-todo-bash.md`; часть
уехала в отдельные планы (`PLAN-2026-09-19-done-qdrant.md`).

Формат этапов — как в `PLAN-2026-09-17-done-webui.md`: чекбоксы `[x]`, верификация, ссылки на
файлы. Обновлять по мере выполнения.

## Цель

Превратить исследование «перспективы и недостатки» в исполняемую дорожную
карту с приоритетами. Каждая зона — своя фаза с явным «что уже есть» и
«что делаем».

## Текущее состояние (что уже есть по каждой зоне)

| Зона роста | Статус сейчас | Где в коде |
|---|---|---|
| **1. RAG (векторная память)** | нет: Qdrant задекларирован в `compose.yaml:42`, Go-код его не использует | карта проекта = только имена (`agents/planner/project_map.go`, `maxMapFiles=200`); эмбеддингов в `models/` нет |
| **2. Роли DevOps/QA** | частично: агенты есть (`agents/devops`, `agents/qaengineer`, `agents/qalead`, `agents/devopslead`) и подключены в CLI (`main.go:140-190`); QA-шаг сейчас — сборка/тесты в приёмке | `agents/acceptor/` (детерминированная приёмка без LLM); автотесты пишутся мало |
| **3. Runtime-наблюдаемость** | частично: приёмка запускает приложение и читает логи (`agents/acceptor/run.go`); Logboard показывает логи проекта `logs/` (`server/logs.go`, `web/.../Logboard`) | лог-файлы агентов (`logging/`), а не живые логи контейнера |
| **4. Точечные правки ≠ только Go** | ограничено: `PatchGoFunction` — только `go/ast` (`tools/gopatch.go`); для TS/Python — текстовый `SearchReplace` (`tools/searchreplace.go`) | CODEGEN_LANG=Go по умолчанию |
| **5. Песочница выполнения** | отсутствует: `Run` исполняет команды в `OutputDir` на хосте (`tools/fileops.go` `runCommand`, `sh -c`, timeout + kill группы) | Dockerfile агента нет; в `compose.yaml` только ollama/qdrant/redis |
| **6. Web UI (было 4-м пунктом)** | **сделано**: `PLAN-2026-09-17-done-webui.md` Ф-1..Ф-3 закрыты | `server/`, `web/`, `workspace/`, `gitops/`, `chat/`, `runevents/` |

## Приоритизация

1. **RAG** — отдельный план `PLAN-2026-09-19-done-qdrant.md` (Ф-1..Ф-6). Самая большая
   ценность для точности планировщика и разработчиков.
2. **Наблюдаемость и стоимость** — не «фича», а инфраструктура: метрики шагов,
   токены/цена, дашборд (см. Ф-1 ниже). В `runevents.Reporter`, чекпоинтах и
   web-счётчике (`web/src/Components/TokensCounter`) частично уже есть.
3. **Runtime-наблюдаемость** — инструмент чтения живых логов запущенного
   приложения/контейнера (закрыть TODO из `readme.md:282`).
4. **Роли DevOps/QA вглубь** — полноценные QA (интеграционные/E2E-тесты на
   Playwright/Go по ТЗ) и DevOps (CI/CD, сложные Dockerfile/K8s).
5. **Песочница** — изолированный запуск `Run` в контейнере (защита от
   деструктивных команд, изоляция окружения).
6. **Мультиязычная точечная правка** — tree-sitter-абстракция над AST
   (TS/Python/Go), чтобы `PatchFunction` работал не только на Go.

## Этапы и чеклист

### Ф-1 — Наблюдаемость и стоимость (инфраструктура) — СДЕЛАНО
- [x] Экспорт метрик шага: `runevents.Reporter` + счётчики времени/токенов на
      шаг (уже есть `TrackMetric` в чекпоинте — вынести в единый реестр)
      → `runmetrics/registry.go` (+ `prom.go`): агрегаты по инструментам,
      единицам работы и ролям, гистограммы длительностей, dedup несвязанных
      событий, потолок кардинальности (`LLM_PRICE_IN/OUT`, `LLM_CURRENCY`).
      Мост из чекпоинта — `agents/planner/executor.go` (`SetMetrics`).
- [x] HTTP-эндпоинт `/metrics` (Prometheus-формат) или экспорт в openmetrics;
      в Web UI — суммарная стоимость запуска (вход/выход токены уже есть:
      `server/tokens.go` + `TokensCounter`)
      → `GET /api/metrics` (Prometheus, под `authHandler` — имена проектов не
      утекают), `GET /api/projects/{id}/metrics` (JSON),
      `POST /api/projects/{id}/metrics/reset`; цены — `tokens/pricing.go`.
- [x] Дашборд: список проектов/запусков с метриками (в расширение `Logboard`
      или отдельная вьюха)
      → отдельная вьюха `web/src/Components/Metricsboard` (шторка + FAB).
- [x] Тесты: агрегация метрик, формат `/metrics`, дедуп
      → `runmetrics/registry_test.go`, `runmetrics/prom_test.go`,
      `server/metrics_test.go`.

### Ф-2 — Runtime-наблюдаемость (закрывает TODO `readme.md:282`) — СДЕЛАНО
- [x] Инструмент `ReadAppLogs`: детект способа запуска проекта (локальный
      процесс / docker-compose), запуск окружения, стрим логов рантайма модели
      (переиспользуем шаблон запуска и таймауты из `agents/acceptor/run.go`)
      → `tools/applogs.go`: детект (Makefile run/dev/start/serve → npm/pnpm/
      yarn dev/start/serve → типовой по стеку), `os.Pipe` на stdout+stderr,
      остановка ГРУППЫ процессов (SIGTERM→SIGKILL), `source=docker` читает
      `docker compose logs` безопасно (только чтение), все деграды — `skipped`
      с подсказкой.
- [x] Real-time: лог-строки в `runevents` (по образцу `TypeMessageDelta`) →
      Logboard показывает живые строки рантайма, не только `logs/` агентов
      → `runevents.TypeAppLog` + `Event.Source` + `OnAppLog`; кольцевой буфер и
      подписка по каталогу проекта — `server/applog.go`; `GET
      /api/projects/{id}/applog`; WS `applog`; отдельная вьюха
      `web/src/Components/Runtimes` (шторка «Рантайм»).
- [x] В цикл самокоррекции: падение в рантайме → модели отдаются последние
      N строк `ReadAppLogs` (по образцу ЛСП-хука `runner/autofix.go`)
      → `runner/applog.go`: интерфейс `RuntimeLogger` (реализует `FileOps`
      через внедрённый указатель), скрытый user-промпт, `APP_LOG_AUTO_FEED` +
      `APP_LOG_MAX_FEED_ROUNDS`, подавление повторов. Промпты ролей
      (`developer` п.6, `qaengineer` п.6, `devops` п.7) и наборы инструментов
      дополнены; согласованность промпт↔инструменты проверяет
      `agents/promptcheck` + тесты ролей.
- [x] Hermetic-тесты: fake-процесс пишет логи → инструмент возвращает строки
      → `tools/applogs_test.go` (16 тестов: stdout+stderr, код выхода,
      остановка долгого процесса, хвост, деграды, детект запуска),
      `runner/applog_test.go`, `server/applog_test.go` (буфер, шина, REST).

### Ф-3 — Роли DevOps/QA вглубь — СДЕЛАНО
- [x] `agents/qaengineer`: настоящие тесты (по ТЗ — unit + интеграционные;
      web — Playwright/E2E), команда запуска как у приёмки (`agents/acceptor`)
      → раздел «СОСТАВ АВТОТЕСТОВ»: unit (падает на текущем коде / проходит
      после правки), интеграционные (реальный HTTP + БД в compose, свои
      моки запрещены), E2E веба на Playwright с `playwright.config.ts`,
      детерминированная синхронизация (динамические порты, опрос health, без
      sleep). Команды проверки больше не перечислены прозой: блок
      «КОМАНДЫ ПРОВЕРКИ» вычисляется `acceptor.VerifyPlanFor` — тем же кодом,
      что и приёмка (env ACCEPT_* → Makefile → автодетект), поэтому тесты QA
      запускаются ровно так же, как их потом проверит acceptor.
- [x] `agents/devops`: генерация CI/CD (GitHub Actions/GitLab CI), сложные
      Dockerfile, K8s-манифесты; проверка «командами» (`Run`) уже есть
      → п.7 CI/CD (`.github/workflows/ci.yml` / `.gitlab-ci.yml`: кэш
      зависимостей, build/lint/test по целям Makefile, teardown сервисов в
      finally, без dev-серверов и без ручного дублирования команд), п.8
      многостадийный Dockerfile (non-root, без дев-зависимостей в финале,
      `.dockerignore`, версии из манифеста, не `latest`), п.9 K8s
      (Deployment + Service, ConfigMap/Secret ссылками, `readinessProbe`/
      `livenessProbe`, requests/limits, реплики).
- [x] Промпты/наборы инструментов QA/DevOps: `tools/registry.go:79` + добавление
      в `devToolNames`; degrade-правила, как в LSP
      → п.10 DevOps «ПРОВЕРЯЙ, А НЕ ПРЕДПОЛАГАЙ»: недоступный инструмент
      (docker/kubectl, Error 127 / command not found) — это degrade с явным
      «не проверено» в отчёте и проверкой максимум доступного; «манифест
      валиден» допустимо только после успешного `docker compose config` /
      `kubectl apply --dry-run=client`. У QA — свой degrade для окружения без
      Playwright/БД. `DetectStack` добавлен в `devopsToolNames` (базовый образ
      и рантайм выводятся из стека, а не выдумываются), `ReadAppLogs` — в
      оба набора.
- [x] Тесты промптов: инструменты в наборе совпадают с упомянутыми в промпте
      → `agents/promptcheck` (промпт называет отсутствующий инструмент или
      содержит опечатку в имени) + `TestPromptMentionsOnlyAvailableTools` в
      ролях developer/qaengineer/devops, `TestDevopsPromptCICDAndContainers`,
      `TestDevopsPromptDegradeRules`, `TestQAPromptTestPortfolio`,
      `TestQAPromptUsesAcceptorVerifyPlan`.
      Тесты приёмки: `TestVerifyPlanMatchesAcceptCommands` (план не расходится
      с `acceptOne`), `TestVerifyPlanWithoutTests`, `TestVerifyPlanTestHint`,
      `TestVerifyPlanLintPrefersMakeTarget`.

### Ф-4 — Песочница выполнения — СДЕЛАНО (реальный прогон контейнера — ручной E2E)
- [x] `tools/sandbox.go`: конфиг запуска `Run` в контейнере (образ по стеку,
      mount рабочего каталога, `--read-only` где можно, без host-network);
      фолбэк на хост при `CODEGEN_SANDBOX=0`
      → `sandboxSpecFor`/`dockerArgs`: `--rm --init`, `--network` (не host),
      `--memory`/`--cpus`, `--cap-drop ALL`, `--security-opt
      no-new-privileges`, `--user UID:GID` хоста (иначе файлы в /workspace
      получают чужого владельца), кэши в tmpfs, `GOPROXY=off` при
      `--network none` + подсказка модели про офлайн. Образ по стеку:
      `golang:1.24` / `node:22` / `python:3.12` / `php:8.3-cli`, иначе
      `ai-sandbox:latest`. Фолбэк ВИДЕН в результате: поля `sandbox` и
      `sandbox_note` — иначе «изоляция» осталась бы только в коде.
      Активация явная: `CODEGEN_SANDBOX=container|auto|local` (по умолчанию
      хост — см. «Два решения» ниже).
- [x] Интеграция в `runCommand` (`tools/fileops.go`) — выбор исполнителя
      (local/container) без изменения контракта инструмента
      → `runCommand` → `runCommandSandbox(command, workdir, cfg)`; контракт Run
      сохранён (`command/workdir/stdout/stderr/status/exit_error`),
      добавлены необязательные `sandbox`, `sandbox_note`, `hint`. Исполнитель
      хоста внедряется полем `LocalCommand` — это и есть seam для hermetic-тестов.
- [x] Безопасность: таймауты, работа как non-root, запрет опасных путей;
      конфиг `compose.yaml` — dev-образ песочницы
      → таймаут и убийство ГРУППЫ процессов уже были в `runCommand` и
      сохранились; non-root через `--user`; опасные команды
      (`tools/destructive.go`) отбрасываются ДО запуска — таблица запретов
      (корень ФС, `~/.ssh`, mkfs, dd в блочные устройства, chmod/chown
      системных каталогов, shutdown/reboot) с объяснением для модели.
      `sandbox/compose.yaml` + `sandbox/Dockerfile` — dev-образ
      `ai-sandbox:latest` (go+node+python+php, make/git/jq/ripgrep, кэши в
      /tmp, non-root), с теми же cap_drop/no-new-privileges/лимитами, что и
      `dockerArgs`. Отдельный файл, а не корень `compose.yaml`: там стек
      платформы (qdrant, redis), и `docker compose down` не должен ронять
      инфраструктуру вместе с разовым запуском песочницы.
- [x] Hermetic-тесты: фолбэк, изоляция, запрет деструктивных команд
      (обязан быстрее, чем печатать в контейст)
      → `tools/sandbox_test.go` + `tools/destructive_test.go`: hermetic-тесты без
      docker и без shell-побочек (чистые функции + подменённый
      `LocalCommand`) — режимы и фолбэк, обязательные флаги изоляции,
      read-only/tmpfs, режим без сети, блокировка ДО exec (нет ни exit-кода, ни
      вывода, и файл-след не создаётся), обе стороны списка запретов
      (блокируется и НЕ блокируется: `rm -rf node_modules` обязан проходить),
      синхронизация кода/теста/compose. `TestSandboxRealContainer` — условный,
      пропускается без собранного образа.

**Два решения, принятых по ходу (важно не переоткрывать):**
1. Песочница выключена по умолчанию (`CODEGEN_SANDBOX` пусто → хост). Пробный
   запуск с `auto`-по-умолчанию уронил ЛСП-чекер: он вызывает тот же
   `runCommand`, образ выбирается по манифесту, которого у чекера нет. Молчаливый
   перенос ВСЕГО исполнения (сборка, тесты, LSP) в контейнер — поведенческое
   изменение, ломающее проекты по неочевидной причине.
2. Песочница не даёт полноценной изоляции файловой системы на хосте: том
   `/workspace` — это файлы проекта, которые агент правит по заданию, поэтому
   защита здесь — отбраковка разрушителей до exec + ревью диффа, а не «стена».
   Сеть по умолчанию ЕСТЬ (`go mod download`/`npm ci` иначе не работают);
   отключается `CODEGEN_SANDBOX_NETWORK=none`.


### Ф-5 — Мультиязычная точечная правка (tree-sitter)
- [ ] Оценка: подключить `tree-sitter` (Go-биндинги `alecthomas/go_tree_sitter_tsx`/
      вариации) поверх `tools/gopatch.go` — универсальный `PatchFunction`
- [ ] Абстракция узлов AST: для Go сохраняем `go/ast` (без регресса),
      для TS/Python — tree-sitter; интерфейс единый (`tools/astpatch.go`)
- [ ] Наборы по ролям: `CODEGEN_LANG` больше не ограничивает; prompt developer
      упоминает точечные правки для нескольких языков
- [ ] Тесты: правка функции в TS/Python/Go без повреждения соседнего кода

### Ф-6 — Верификация дорожной карты
- [ ] `go build . ./agents/... ./tools/ ./board/ ./server/ ./workspace/ ./gitops/ ./runner/`
- [ ] `go vet . ./agents/... ./tools/ ./board/ ./server/ ./workspace/ ./gitops/ ./runner/`
- [ ] `go test . ./agents/... ./tools/ ./board/ ./server/ ./workspace/ ./gitops/ ./runner/`
- [ ] `npm run build` (web/)

## Верификация

```
go build . ./agents/... ./tools/ ./board/ ./server/ ./workspace/ ./gitops/ ./runner/
go vet  . ./agents/... ./tools/ ./board/ ./server/ ./workspace/ ./gitops/ ./runner/
go test . ./agents/... ./tools/ ./board/ ./server/ ./workspace/ ./gitops/ ./runner/
npm run build   # web/
```

Известные нюансы репозитория (не убирать):
- `go build ./...` падает на `temp/moon-distance/internal/middleware/cors.go`
  (артефакт сгенерированного проекта) — использовать перечень выше.
- Не форматировать `agents/acceptor/checks.go`, `agents/acceptor/run.go`.
- Комментарии/промпты/UI-тексты — на русском.
- Фичи с внешними сервисами (Qdrant, песочница) — обязательный degrade.

## Связанные планы

- `PLAN-2026-09-19-done-qdrant.md` — RAG (зона 1 дорожной карты, отдельный детальный план).
- `PLAN-2026-09-17-done-webui.md` — Web UI (зона 6, выполнена).
- `PLAN-2026-09-19-done-lsp.md` / `PLAN-2026-09-19-todo-bash.md` — токен-гигиена и точность правок (контекст для зон 3/4).

## Как продолжить

1. Взять первый незачёркнутый пункт с наивысшим приоритетом (Ф-1 → Ф-2 → …),
   выполнить, отметить `[x]`, закоммитить.
2. Крупные зоны (RAG, песочница, мультиязык) — вести отдельными планами, здесь
   держать только статус-ссылку.
3. После каждой фазы — верификация (блок выше) и краткая запись в этот файл.