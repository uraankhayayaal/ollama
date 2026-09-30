# LSP

Language Server Protocol даёт агенту «взгляд IDE»: точные диагностики,
переходы к определению, поиск ссылок, hover-документацию. Оба пути
реализованы на Go.

## Настройка

```bash
LSP_SERVER=             # готовая команда запуска сервера; пусто = авто по стеку
LSP_NATIVE=1            # нативные publishDiagnostics (вкл по умолчанию)
LSP_TIMEOUT=15s         # таймаут запроса к серверу
LSP_DIAG_WAIT=3s        # ожидание публикации диагностик
LSP_IDLE_TTL=10m        # эвикция простаивающего сервера

LSP_MAX_DIAGS=30        # лимит строк диагностик
LSP_MAX_LOCATIONS=50    # лимит позиций definition/references
LSP_MAX_OUTPUT=4000     # лимит символов вывода

LSP_BIN_PATH=           # доп. каталоги поиска бинарников (высший приоритет)
LSP_STEP_GATE=1         # scope-гейт по шагу плана (вкл по умолчанию)
LSP_AUTO_FIX=1          # авто-починка по диагностикам (вкл по умолчанию)
LSP_MAX_FIX_ROUNDS=3    # максимум раундов автопочинки

ACCEPT_LSP=1            # LSP-стадия приёмки (вкл по умолчанию)
```

> **Весь LSP-контур (13 переменных) не описан в `.env.example`**, хотя
> читается кодом. Флаги активны по умолчанию, но их нельзя «убрать из
> `.env`» — нужно явно задать `0`/`false`/`off`.

> `GO_LSP_HELPER` — **не production-переменная**: это флаг фейкового
> LSP-сервера в тестах (`tools/lspclient/client_test.go:242-271`).

## Серверы

| Стек | Сервер | Пакет установки |
|---|---|---|
| Go | `gopls` | `go install golang.org/x/tools/gopls@latest` |
| PHP | `intelephense`, `phpactor` | composer |
| Node/TS | `typescript-language-server` | `npm i -g typescript-language-server typescript` |
| Python | `pyright-langserver` | `npm i -g pyright` |

`LSP_SERVER` переопределяет всё (`tools/lspclient/servers.go:32-41`).
Иначе сервер выбирается по расширению файла, с фолбэком на стек проекта
(`languageFor:77-105`).

Бинарники ищутся через `tools/binpath`: `PATH`, `~/go/bin`, `GOPATH/bin`,
`~/.local/bin`, npm/nvm, Homebrew, `/usr/local`. `LSP_BIN_PATH` имеет
высший приоритет.

**Если сервера нет — `LspCheck` возвращает `skipped`, а не ошибку.**

## Транспорт

JSON-RPC 2.0 по stdio с заголовками `Content-Length`. Сервер — долгоживущий
процесс `--stdio` (`tools/lspclient/servers.go:1-8,22-30`).

Менеджер держит клиенты по паре (вид, каталог) и эвиктит простаивающие не
чаще раза в минуту (`reapInterval`, `manager.go:144-179`).

**Адаптивный бюджет ожидания диагностик:** `4 ×` измеренной латентности
публикаций, в границах `[250ms, LSP_DIAG_WAIT]` (`client.go:659-682`).
Латентность — экспоненциальное среднее, обновляется только при полном
приходе публикаций (`:684-698`). Это ускоряет первые проверки и не даёт
тормозить на больших проектах.

## Инструменты

| Инструмент | Параметры | Результат | Кому выдан |
|---|---|---|---|
| `LspCheck` | `files[]` (опц.; пусто — весь проект) | Дедуплицированные отсортированные диагностики `{file, line, col, severity, message}` + `truncated` | Разработчик |
| `LspDefinition` | `file`, `line`, `col` | `{count, locations[]}`, пути относительно проекта | Почти все агенты |
| `LspReferences` | `file`, `line`, `col`, `include_declaration` | То же | Почти все агенты |
| `LspHover` | `file`, `line`, `col` | `{contents}`; при отсутствии символа — пояснение | Почти все агенты |

Планировщик получает только навигацию, без `LspCheck` (`agents/planner/planner.go:14`).

Коды ответа: `error` (нет `OutputDir`, сбой запроса), `skipped` (сервер
недоступен, с подсказкой использовать `ReadMap`/`ReadFiles`), `success`.

## Два пути получения диагностик

1. **Нативный** (`LSP_NATIVE=1`, по умолчанию) — `publishDiagnostics`
   от сервера. Точнее и быстрее.
2. **CLI-чекеры** — `LspCheck` через отдельный запуск. Используется как
   запасной путь и в приёмке (`agents/acceptor/lsp.go`).

## Step Gate

`agents/planner/lspgate.go:120-165` — проверка строго по файлам, изменённым
шагом:

1. Флаг `LSP_STEP_GATE` (вкл по умолчанию).
2. Из файлов `snap.Diff` берутся только исходники: `.go .js .jsx .ts .tsx
   .py .pyi`, максимум 100 файлов (`:49-56,128,169-184`).
3. Нативные диагностики строго по этим файлам. Если сервера нет
   (`handled=false`) — гейт молча не срабатывает (`:132-136`).
4. Ошибки и предупреждения считаются в метриках шага (`:153`).
5. **Есть `error` → шаг падает**, его scope откатывается, план
   останавливается. В сообщение идут до 6 примеров
   `file:line:col: message` (`:159-164`).

Предупреждения шаг не роняют.

Это главный предохранитель против «красивого, но не компилирующегося» кода:
ошибка ловится сразу после шага, а не в финальной приёмке.

## Автопочинка

`runner/autofix.go` — по `LSP_AUTO_FIX` (вкл) и `LSP_MAX_FIX_ROUNDS`
(по умолчанию 3). Агент получает диагностики и правит код без участия
модели-рефактора.

## LSP в приёмке

`ACCEPT_LSP=1` (по умолчанию) — стадия приёмки. Сканирует
`.go .js .jsx .ts .tsx .py .pyi .php .phtml`, максимум **100 файлов**
(`agents/acceptor/lsp.go:16-31`). Отсутствие сервера → `Skipped`, не ошибка
(`agents/acceptor/checks.go:107-130`).

Ошибка LSP-диагностик ⇒ `reject`.

## Оглавления файлов

`tools/outliner.go` — `documentSymbol`, максимум 4 файла (`:18-26`).
Используется в Ф-9 (сжатие контекста): вместо удалённого кода в памятку
модели попадает оглавление.

## Установка серверов

```bash
go install golang.org/x/tools/gopls@latest
npm i -g typescript-language-server typescript pyright
```

Проверить, что сервер найден: вызвать `LspCheck` — если вернулся
`skipped`, сервер не найден. Проверить путь:

```bash
LSP_BIN_PATH=/custom/path go run . ...
```

## Связанное

- [Сжатие контекста](context-compression.md) — Ф-9 использует оглавления
- [Приёмка](acceptor.md) — LSP-стадия
- [Планировщик](planner.md) — step-gate
- [Инструменты](tools.md)
- [Стеки и языки](stacks-and-languages.md)
