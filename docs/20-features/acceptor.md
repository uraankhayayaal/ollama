# Приёмка

Детерминированная проверка собранного приложения **без LLM**. LLM
подключается только к составлению плана исправлений, когда приёмка провалилась.

Два способа запуска:

```bash
go run . accept <имя_проекта>          # отдельно
```

или финальным шагом плана (`plan`) / фазой в оркестрации (`kanban`).

## Настройка

```bash
# Команды (пусто = автодетект по стеку или Makefile)
ACCEPT_BUILD_CMD=
ACCEPT_RUN_CMD=
ACCEPT_TEST_CMD=
ACCEPT_FORMAT_CMD=
ACCEPT_ANALYZE_CMD=

# Зависимости
ACCEPT_INSTALL_DEPS=true
ACCEPT_INSTALL_CMD=
ACCEPT_INSTALL_TIMEOUT=5m

# Таймауты
ACCEPT_BUILD_TIMEOUT=2m
ACCEPT_RUN_TIMEOUT=10s

# Проверки
ACCEPT_FORMAT=true       # формат — только предупреждения
ACCEPT_AUTOFORMAT=true   # автоформатирование
ACCEPT_ANALYZE=true      # анализатор — ошибка ⇒ reject
ACCEPT_LSP=true          # LSP-стадия

# Прочее
ACCEPT_MAX_LOG=6000      # лимит символов вывода в отчёте
ACCEPT_MAX_ROUNDS=3      # циклов «приёмка → исправления → приёмка» (0 = выкл)
```

> `ACCEPT_TEST_CMD`, `ACCEPT_AUTOFORMAT`, `ACCEPT_LSP` **не описаны в
> `.env.example`**, но читаются кодом (`agents/acceptor/config.go:118,135,141`).

## Порядок этапов

`agents/acceptor/accept.go:108-401`:

```
1. makefileLocate          — найти Makefile от подпроекта вверх до корня
2. Выбор команд            — ACCEPT_* → make-цель → автодетект
3. install                 — установка зависимостей
4. build                   — сборка
   РАННИЙ ВЫХОД при провале: format/analyze/LSP/run не выполняются
5. format                  — gofmt/prettier/black. Только предупреждения
6. analyze                 — go vet/eslint/ruff/phpstan. Ошибка ⇒ reject
7. LSP                     — диагностики. Ошибка ⇒ reject
8. run                     — запуск под таймаутом + разбор логов
```

Таймаут запуска считается **успехом**, если в логах нет критичных маркеров
(`accept.go:361-381`).

## Приоритет команд

```
ACCEPT_*_CMD (явная настройка)
      ↓ если пусто
make-цель из Makefile проекта
      ↓ если Makefile нет / цели нет
автодетект по типу проекта
```

Реализация — `agents/acceptor/detect.go:104-107`. Подробнее —
[Makefile-контракт](makefile-contract.md).

## Автодетект по стеку

`agents/acceptor/detect.go`:

| Этап | Go | Node/TS | Python | PHP |
|---|---|---|---|---|
| install | `go mod download` | `npm ci` (если есть `package-lock.json`) иначе `npm install` | `pip install -r requirements.txt` | `composer install --no-dev` |
| build | `go build ./...` | `npm run build` / `npx tsc --noEmit` | `compileall` | `php -l` |
| run | бинарь из `go.mod` | точка входа: `index.js`/`main`/`server.js`/`app.js` | точка входа | точка входа |
| format | `gofmt -l` | `prettier` | `black` | `php-cs-fixer` |
| analyze | `go vet` | `npm run lint` / `eslint` | `ruff` / `flake8` | `phpstan` → `php -l` |
| test | `go test ./...` | `npm test` | `pytest -q` / `unittest` | `vendor/bin/phpunit` |

Инструмент не найден — шаг **пропускается**, а не считается ошибкой.

## Монорепозитории

Если в корне проекта нет маркеров, а в прямых подкаталогах (`frontend/`,
`server/`) есть — это монорепозиторий. Каждый подпроект принимается
отдельно, отчёт содержит блоки по подпроектам.

Любой `reject` подпроекта обесценивает всё монорепо (`accept.go:67-97`).

Неизвестный тип без явных команд → `reject` с подсказкой про
`ACCEPT_BUILD_CMD` (`accept.go:47-60,151-153`).

## Вердикт

| Вердикт | Условие |
|---|---|
| `approve` | Все подпроекты приняты |
| `reject` | Любой подпроект отклонён, либо тип неизвестен без явных команд |

## Инфраструктурный слой

Когда на хосте нет нужного инструмента (например, `make` или `docker`),
приёмка может выполнить **инфра-зеркало** — make-цель `infra.<цель>` через
`docker compose run` (`agents/acceptor/detect.go:192-198`).

Применяется к `build`, `format`, `analyze`, но **не к `run`**
(`accept.go:209-219,270-272,297-299,413-446`): зеркало `run` мешало бы
проверке живого приложения.

Спецдетект: `make` возвращает код 2, но печатает `Ошибка 127`, если
инструмента нет — это распознаётся и даёт подсказку вместо падения
(`accept.go:405-411`).

## Контракт для QA

`agents/acceptor/plan.go:75-114` (`VerifyPlanFor`) — единый контракт проверок,
которым пользуются и приёмка, и QA-инженер. Методы: `TestHint()`,
`HasTarget()`, `TestTargets()`.

## Отчёт

`agents/acceptor/report.go`. В отчёт попадают:

- Вердикт по каждому подпроекту.
- Вывод команд, обрезанный до `ACCEPT_MAX_LOG` символов.
- Предупреждения формата (не влияют на вердикт).
- Ошибки анализатора и LSP (влияют).
- Критичные маркеры в логах запуска.

## Проверить вручную

```bash
go run . accept storageService
cat logs/storageService.log | tail -50
```

## Связанное

- [Makefile-контракт](makefile-contract.md) — приоритет команд
- [Стеки и языки](stacks-and-languages.md)
- [Планировщик](planner.md) — цикл исправления по приёмке
- [Песочница](sandbox.md) — где выполняются команды
- [LSP](lsp.md) — LSP-стадия приёмки
