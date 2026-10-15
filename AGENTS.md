# AGENTS.md — правила для агентов (opencode / помощники)

## Текущее состояние структуры

Направление зависимостей — только внутрь:

```
domain  ←  application  ←  infrastructure  ←  outer (server, main, runner, agents, tools, cmd, web)
```

| Слой | Где | Можно импортировать |
|---|---|---|
| domain + порты | `domain/`, `domain/services/` | stdlib |
| use-cases | `application/` | domain, порты, `ai/shared` |
| реализации | `infrastructure/` | domain, порты |
| shared-утилиты | `shared/` (logging, projects, tokens, langdetect) | stdlib |
| приватно модулю | `internal/` | всё внутри `ai/` |
| outer | `agents/`, `runner/`, `server/`, `tools/`, `cmd/`, `web/` | domain, application, infrastructure, shared |

**Гейт:** `make depgate` — `application/...` не импортирует ничего вне `ai/{domain,application,shared}`.
**Composition root:** `main.go`.

> Подробная история — в `docs/agents-history.md`, планы — в `docs/plans/`.

## `temp/` — проекты агентов

Каталог `temp/` — выходные артефакты агентов-разработчиков, **не код проекта**.

- НЕ удалять, не коммитить, не читать «полностью» — только по пути `temp/<имя_проекта>/`.
- Править — через агентов (`backend`/`frontend`), а не прямые редиректы.
- Контакты: `projects.ProjectDir`.

## Сборка и проверка

Канонический набор (обходит `temp/`):

```
go build . ./agents/... ./application/... ./tools/... ./cmd/... ./server/ ./runner/ ./infrastructure/... ./domain/...
go vet   . ./agents/... ./application/... ./tools/... ./cmd/... ./server/ ./runner/ ./infrastructure/... ./domain/...
go test  . ./agents/... ./application/... ./tools/... ./cmd/... ./server/ ./runner/ ./infrastructure/... ./domain/...
```

- `go build ./...` падает на `temp/moon-distance/…` — не трогай.
- `gofmt -l .` → 29 предсуществующих отклонений (не гейт; форматируй только свои файлы).
- `make depgate` — обязательная проверка перед комментом.

Живая песочница (нужен Docker): `SANDBOX_LIVE=1 go test ./infrastructure/sandbox/ -run TestLiveSession -v`

Известные флаки (не регресс):
- `TestAcceptBuild*` → fail на GNU Make 3.81 (marker mismatch)
- `TestChatAssistant*` → fail без живой Ollama

## Токено-эффективные команды

**ЛSP — приоритет.** Bash — фолбэк: git-контекст, `tail -N`, `grep -E` с фильтрами.

| Что | Команда |
|---|---|
| поиск в коде | `go tool cgo`, `grep -rnw '<символ>' .` |
| инвок через LSP | `StructureRefresh`, `LspDefinition`, `LspHover`, `LspReferences` |
| git / diff | `git log -n 3 --oneline`, `git blame -L 10,20 путь` |
| сократить вывод | `2>&1 \| tail -40`, `grep -E 'FAIL' \| head -60` |

**Запрещено:** `cat`/`head`/`tail` (аналог `ReadFiles`), `grep -r` без `--exclude-dir`, полные сценарии сборки без `-v`/`-q`.

Внутренние агенты (`agents/*`) имеют только `Run`/`ReadFiles`/`Write` — правила сокращения те же.