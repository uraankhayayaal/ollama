# Стеки и языки

Определение стека проекта определяет, какие роли задействовать, какие команды
использовать и какой LSP-сервер поднимать.

## `DetectStack`

`tools/stacktool.go:44-59`. Инструмент без параметров, выдаётся архитектору.

Ответ:

```json
{
  "status": "ok",
  "kind": "go",
  "stack": "monorepo",
  "roles": {"frontend": false, "backend": true, "devops": true, "qa": true},
  "summary": "Go-бэкенд; есть инфраструктура; тесты в tests/",
  "makefile": true,
  "markers": ["go.mod", "Makefile", "docker-compose", "tests/"]
}
```

| Поле | Смысл |
|---|---|
| `kind` | Базовый тип: `go`, `php`, `node`, `python`, `unknown` |
| `roles` | Какие направления работы активны |
| `makefile` | Есть ли `Makefile` — признак обязательного эпика |
| `markers` | Найденные маркеры (что именно повлияло на вывод) |

## Определение `kind`

`stackdetect.DetectKind` (`stackdetect/stackdetect.go:30-49`) — по маркерам
в корне, с фиксированным приоритетом:

```
go.mod  →  composer.json  →  package.json  →  требования питона  →  unknown
```

> **`composer.json` проверяется раньше `package.json`:** Laravel и
> пакетные PHP-проекты несут оба маркера, и приёмка по стеку PHP
> (`composer + php -l + artisan`) корректнее, чем трактовка такого корня
> как Node-проекта.

Маркеры Python: `requirements.txt`, `pyproject.toml`, `setup.py`, `main.py`,
`app.py`.

## Определение ролей

`tools/stacktool.go:74-168`. Роль включается по наличию каталога или файла.

### Frontend

Каталоги `frontend`, `web`, `app`, `client` или файл `index.html`.

### Backend

| Стек | Условие |
|---|---|
| Go / PHP / Python | Всегда `true` |
| Node | Есть каталог `server`, `backend` или `api` |
| `unknown` | Есть `server`, `backend`, `api`, `internal` или `cmd` |

Node может быть frontend-only — поэтому проверка каталогов, а не только
`package.json`.

### DevOps

Каталог `infra`, `deploy`, `ops`; либо файл `docker-compose.yml` /
`docker-compose.yaml` / `Dockerfile` / `Makefile`; либо `.github/workflows`;
либо k8s-манифесты.

### QA

Каталог `tests`, `test`, `__tests__`, `spec` или `e2e`.

> **Важно для архитектора:** консольный проект без фронтенда не получает
> Frontend Lead — роль выводится из `roles`, а не назначается по умолчанию.

## Влияние на остальную систему

| Компонент | Что делает со стеком |
|---|---|
| Архитектор | Назначает `assigned_role` по `roles`; требует эпик Makefile при `makefile: true` |
| Приёмка | Выбирает команды проверки по стеку (`acceptor/detect.go`) |
| [Песочница](sandbox.md) | Выбирает образ: `golang:1.24`, `node:22`, `python:3.12`, `php:8.3-cli` |
| [LSP](lsp.md) | Выбирает сервер по расширению файла, с фолбэком на стек |
| [Ревью](code-review.md) | Определяет язык диффа для промпта ревьюера |

## Языки для ревью

`langdetect/` — 21 язык. Определение по диффу:

1. Расширения файлов в заголовках `+++`.
2. Заголовки `diff --git` и хедеры `index` как фолбэк.
3. При равенстве кандидатов выбор **детерминирован** — `dominant`
   (`langdetect/langdetect.go:137-147`), без зависимости от порядка обхода
   map.

Результат: `Language.String()` (`:150`) подставлятся в промпт ревьювера.

## Настройка

Отдельных переменных окружения нет: всё детектируется. Только
`CODEGEN_LANG` и `CODEGEN_MODULE` влияют на **генерацию** кода
(целевой язык по умолчанию — Go, имя модуля `go.mod` — пусто, модель
придумывает сама).

## Проверить вручную

```bash
# Что видит детектор
ls go.mod composer.json package.json requirements.txt pyproject.toml 2>/dev/null
ls -d frontend web app client server backend api internal cmd 2>/dev/null
ls -d infra deploy ops tests test __tests__ spec e2e 2>/dev/null
```

## Добавить стек

1. Добавить `Kind` в `stackdetect/stackdetect.go:15-19`.
2. Добавить ветку в `DetectKind` (`:30-49`) с правильным приоритетом.
3. Добавить кейс в `detectStackAt` (`tools/stacktool.go:81-160`): роли и
   маркеры.
4. Обновить образ песочницы (`tools/sandbox.go:268-279`).
5. Обновить `acceptor/detect.go` — команды проверки.
6. Обновить [эту таблицу](../30-reference/agent-tools.md) и
   [чеклист](../40-operations/adding-a-feature.md).

## Связанное

- [Агенты и роли](agents-and-roles.md)
- [Архитектор](architect.md) — назначение ролей
- [Приёмка](acceptor.md) — команды по стеку
- [Песочница](sandbox.md) — образы
- [LSP](lsp.md) — серверы по стеку
