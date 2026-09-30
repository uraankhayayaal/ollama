# Песочница выполнения

Команды инструмента `Run` приходят от модели. По умолчанию они выполняются
**на хосте** — это осознанное решение. Песочница включается явно.

## Почему по умолчанию хост

Пробный запуск с режимом `auto` показал, почему (`tools/sandbox.go:112-129`):
«docker доступен» не значит «в контейнере всё работает». В песочницу уезжал
LSP-чекер (он вызывает тот же `runCommand`) и падал на проектах без
распознанного стека, потому что образ выбирался по манифесту, которого у
чекера нет.

Молчаливый перенос **всего** исполнения (сборка, тесты, LSP) в контейнер —
поведенческое изменение, ломающее проекты по неочевидной причине.

## Настройка

```bash
CODEGEN_SANDBOX=             # пусто (по умолчанию) = хост
CODEGEN_SANDBOX_IMAGE=       # пусто = выбор по стеку проекта
CODEGEN_IMAGE=               # legacy-алиас для CODEGEN_SANDBOX_IMAGE
CODEGEN_SANDBOX_NETWORK=default    # default | none | bridge
CODEGEN_SANDBOX_MEMORY=2g
CODEGEN_SANDBOX_CPUS=2
CODEGEN_SANDBOX_RO=false     # read-only корень контейнера
CODEGEN_SANDBOX_ALLOW_WRITE=true    # монтировать проект на запись
CODEGEN_SANDBOX_DOCKER=      # путь к docker; читается ДО поиска в PATH
CODEGEN_SANDBOX_BIN=         # docker, который песочница подставит дочернему compose
CODEGEN_SANDBOX_USER=        # UID хоста для сборки образа
CODEGEN_DOCKER_HOST=         # альтернативный сокет демона
CODEGEN_DOCKER_TLS=
CODEGEN_DOCKER_CERT=
```

### Режимы

| Значение | Поведение |
|---|---|
| пусто / `0` / `off` / `false` / `no` / `local` / `host` | **Хост** (по умолчанию) |
| `container` / `1` / `on` / `true` / `yes` / `docker` | Всегда контейнер; нет Docker → **ошибка конфигурации**, не тихий возврат на хост |
| `auto` | Контейнер при доступном Docker, иначе хост с пометкой `sandbox_note` в ответе |

> `CODEGEN_DOCKER_*` существуют потому, что переменные `DOCKER_*` в окружении
> агента могут быть заняты чем-то ещё.

## Выбор образа

`CODEGEN_SANDBOX_IMAGE` → по стеку проекта → dev-образ `ai-sandbox:latest`.

| Маркер | Образ |
|---|---|
| `go.mod` | `golang:1.24` |
| `package.json` | `node:22` |
| `requirements.txt` / `pyproject.toml` | `python:3.12` |
| `composer.json` | `php:8.3-cli` |
| не распознан | `ai-sandbox:latest` |

Dev-образ нужен для монорепо и make-целей вида `make build`. Собирается
один раз:

```bash
SANDBOX_UID=$(id -u) SANDBOX_GID=$(id -g) docker compose -f sandbox/compose.yaml build
```

Последний шаг обязателен: у LSP-чекера манифеста проекта нет, и без dev-образа
контейнерный режим был бы недостижим вовсе (`tools/sandbox.go:301-310`).

## Монтирование

| Что | Куда | Режим |
|---|---|---|
| Рабочий каталог | `/workspace` (`sandboxWorkspace:47`) | `:rw` по умолчанию |
| tmpfs | `/tmp` (`sandboxTmp:53`) | кэши и HOME |

`CODEGEN_SANDBOX_ALLOW_WRITE=false` монтирует проект `:ro` — команда,
которая пишет в проект, упадёт с ошибкой, а не «успешно» не сделает работу.

`CODEGEN_SANDBOX_RO=true` делает корень контейнера read-only; кэши и HOME в
tmpfs, поэтому тулчейн продолжает работать. Рекомендуется для приёмки и
режима «только посмотреть».

## Сеть

`CODEGEN_SANDBOX_NETWORK=default` (мост docker) — по умолчанию сеть **есть**:
без неё `go mod download` и `npm ci` не работают.

`none` отключает сеть полностью. Тогда модели нужны локальные кэши
(`vendor/`, `GOMODCACHE`, `node_modules`, `.venv`), а в ответе появляется
подсказка `sandboxNetworkHelp` (`tools/sandbox.go:453`).

## Отбраковка разрушительных команд

Песочница **не спасает** от `rm -rf` внутри рабочего каталога: `/workspace`
смонтирован на файловую систему хоста. Поэтому есть предварительный фильтр
`tools/destructive.go`.

Песочница спасает от `rm -rf /` **внутри контейнера**. То есть опасность
зависит от того, где выполняется команда, а не от самой команды.

### Правила

| Категория | Примеры |
|---|---|
| Корень ФС | `rm -rf /`, `rm -fr /`, `rm -r -f /`, `rm --recursive --force /` |
| Домашний каталог | `rm -rf ~/.ssh`, `~/.aws`, `~/.config`, `~/.gnupg`, `~/` |
| Разметка разделов | `mkfs`, `fdisk`, `parted`, `diskutil erase` |
| Блочные устройства | `dd of=/dev/`, `>/dev/sd`, `of=/dev/disk` |
| Стирание данных | `shred`, `wipefs`, `srm` |
| Системные каталоги | `rm -rf /etc`, `/usr`, `/var`, `/bin`, `/lib`, `/boot`, `/opt`, `/sys`, `/proc` |
| Права и владелец | `chmod -R 777 /`, `chown -R root`, `chmod 777 /etc` |
| Запись в системные файлы | `>/etc/`, `tee /etc/`, `>/usr/`, `>/boot/` |
| Остановка хоста | `shutdown`, `reboot`, `poweroff`, `init 0`, `systemctl poweroff` |

> **Что НЕ блокируется сознательно:** `curl \| sh`, `sudo`, `dd`,
> `git push --force`. Ложное срабатывание хуже пропуска: агент отказался
> выполнить `rm -rf node_modules` перед сборкой и застрял (`destructive.go:8-15`).

Сравнение — по нижнему регистру с нормализацией пробелов: `rm  -rf  /tmp` и
`RM -RF /` отсекаются одинаково (`destructive.go:87-104`).

Ошибка возвращается модели с объяснением, **что делать вместо**.

## Ошибки конфигурации

`sandboxSpecFor` (`tools/sandbox.go:281-310`) — чистая функция без запуска;
тесты проверяют именно её. Отказ до старта контейнера:

- Команда разрушительна → причина запрета.
- Рабочий каталог не найден → явная ошибка.
- **Путь содержит `:` или `,`** → эти символы ломают `--volume`
  (`src:dst:opts`): контейнер стартовал и не видел проекта. Лучше явная
  ошибка конфигурации — перенесите проект в каталог без этих символов.

## Проверить

```bash
CODEGEN_SANDBOX=container go run . plan <проект> "..."
```

В ответе `Run` появляются sandbox-поля (`sandbox_note` при `auto`-деградации).
Проверить изоляцию:

```bash
CODEGEN_SANDBOX=container CODEGEN_SANDBOX_ALLOW_WRITE=false go run . plan <проект> "..."
```

Запись в проект должна упасть с ошибкой монтирования.

## Связанное

- [Инструменты](tools.md) — `Run` и его лимиты
- [Приёмка](acceptor.md) — где песочница особенно полезна
- [Стеки и языки](stacks-and-languages.md) — детектор стека
- [Makefile-контракт](makefile-contract.md) — цели, исполняемые в песочнице
