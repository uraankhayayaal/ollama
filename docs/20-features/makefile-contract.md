# Makefile-контракт

Makefile проекта — **единая точка входа команд** для субагентов и приёмки.
Если в `temp/<проект>/Makefile` есть цели, система использует их вместо
собственного автодетекта.

## Настройка

Явные команды `ACCEPT_*_CMD` всегда важнее Makefile. Когда они пусты:

```
ACCEPT_*_CMD  →  make-цель  →  автодетект по стеку
```

```bash
ACCEPT_BUILD_CMD=    # например: make build
ACCEPT_RUN_CMD=
ACCEPT_TEST_CMD=
ACCEPT_FORMAT_CMD=
ACCEPT_ANALYZE_CMD=
```

## Стандартные цели

| Цель | Что значит | Используется для |
|---|---|---|
| `build` | Сборка | `ACCEPT_BUILD_CMD` |
| `run` | Запуск | `ACCEPT_RUN_CMD` |
| `test` | Тесты | `ACCEPT_TEST_CMD` |
| `lint` | Анализ | `ACCEPT_ANALYZE_CMD` (приоритет `test` → `lint`, `detect.go:178-186`) |
| `format` | Форматирование | `ACCEPT_FORMAT_CMD` |
| `infra.<цель>` | Зеркало через `docker compose run` | Подмена любой цели при недоступном инструменте на хосте |

Специальная форма `infra.build`, `infra.test` и т.п. — это **инфраструктурное
зеркало**: та же цель, но выполненная в контейнере сервиса.

## Как это работает

### Поиск Makefile

`makefileLocate` (`agents/acceptor/detect.go:84-99`) идёт **от подпроекта
вверх** до корня приёмки. Это важно для монорепозиториев: у `frontend/` может
быть свой Makefile.

### Парсер целей

`makefileTargets` (`detect.go:106-150`) понимает:

- Множественные цели: `build test:` — обе регистрируются.
- Присваивания `:=`, `=`, `+=` — **пропускаются**, чтобы не принять
  переменную за цель.
- `.PHONY` и `.DEFAULT_GOAL` — игнорируются как служебные.
- Шаблонные цели с `%` и `$` — не регистрируются как конкретные
  (`detect.go:121`).
- Имя цели должно матчить `[A-Za-z0-9_.-]` (`makefileTargetName:154-164`).

### Выполнение

Make-цели исполняются в каталоге Makefile, а не в каталоге приёмки —
поэтому монорепозиторий собирается правильно.

### Инфра-зеркало

Применяется к `build`, `format`, `analyze`. **Не применяется к `run`**
(`accept.go:344-347`): зеркало запуска мешало бы проверке живого приложения
и размывало бы результат.

Если `make` недоступен на хосте, `makefileLocate` даёт фолбэк
`make infra.<цель>` (`detect.go:192-198`), а команда исполняется через
`sandbox/compose.yaml`.

## Договорённость с агентами

Системный архитектор обязан создать эпик «Makefile проекта» — эталонный
контракт целей. Назначение роли: Backend Lead / DevOps Lead
(`tools/stacktool.go`, секция «MAKEFILE ПРОЕКТА» в
`architectureSystemPrompt`).

`DetectStack` возвращает поле `makefile` — факт наличия Makefile и его
маркеры (`tools/stacktool.go:29-38`).

### Что от агентов ожидается

| Роль | Правило |
|---|---|
| Архитектор | Makefile — обязательный эпик; описать цели в `architecture_summary` |
| Разработчик | Сначала `ReadFiles` Makefile, затем `make backend-*` / `make frontend-*` |
| QA | `make test`, `make e2e`; приёмка — `make build`, `make lint` |
| DevOps-инженер | Инфра-блок: `up`/`down`/`logs`/`ps`, зеркальные `infra.*` |
| Лиды | Все инструкции по проверке — отсылаются к целям Makefile |

## Соглашение об именовании целей

Цели извлекаются парсером, поэтому имя должно матчить `[A-Za-z0-9_.-]`:

```makefile
.PHONY: build test lint infra.build

build:      ## Сборка
	@go build ./...

test:       ## Тесты
	@go test ./...

lint:       ## Анализ
	@go vet ./...

infra.build:   ## Зеркало build
	@docker compose run --rm app make build
```

> Переменные с `:=` пропускаются — не пишите цели через переменные.

## Отладка

```bash
# Какие цели видит система
grep -nE '^[a-zA-Z0-9_.-]+:' temp/<проект>/Makefile

# Проверить приоритет: явная команда важнее make
grep -n 'ACCEPT_BUILD_CMD' .env
```

## Связанное

- [Приёмка](acceptor.md) — где применяется
- [Архитектор](architect.md) — обязательный эпик
- [Стеки и языки](stacks-and-languages.md)
- [Песочница](sandbox.md) — исполнение команд
