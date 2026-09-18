# План: LSP-интеграция — диагностика и навигация «глазами IDE»

Статус: **Ф-1, Ф-2 и Ф-3 ВЫПОЛНЕНЫ** (Ф-3 — только навигация; нативный
`publishDiagnostics` сознательно отложен в Ф-4, `LspCheck` пока остаётся CLI).
Дальше — Ф-4 (полировка и docs).
Обновлять этот файл по мере выполнения (чекбоксы `[x]`), как в `PLAN-webui.md`.

## Решения пользователя (зафиксировано на обсуждении)

- **Гейт:** `LspCheck` выдаётся модели ВСЕГДА, с graceful degrade (нет чекера →
  возвращает заметку «используй Run (go vet / tsc / ruff)»), без env-гейта.
- **Чекеры по стекам:** go → `gopls check` (фолбэк `go vet ./...`);
  ts/js → `tsc --noEmit --pretty false` (локальный бинарь сначала, фолбэк сборка);
  python → `pyright --outputjson` (фолбэк `ruff check`).
- **Детект стека:** выносим `DetectKind` из `agents/acceptor` в нейтральный
  пакет `stackdetect`; акцептор и `tools` используют единый источник.
- **Объём первого захода Ф-1:** инструмент + регистрация + hermetic-тесты +
  промышленные промпты разработчика (шаг «сборка упала → LspCheck»). Ф-2
  (авто-лечение в раннере) — отдельная фаза потом.

## Откуда берём LSP и как запускаются

**Среда:** агент исполняется на хосте (в `compose.yaml` только
ollama/open-webui/qdrant/redis, Dockerfile агента нет). Команды — через
`runCommand` (`tools/fileops.go:883`, `sh -c` в OutputDir, Setpgid + kill группы
по таймауту). PATH — хостовый.

**Ф-1 — однократные CLI-чекеры (не сервера):**

| Стек | Основной чекер (источник установки) | Фолбэк |
|---|---|---|
| Go | `gopls` (`go install golang.org/x/tools/gopls@latest`, GOPATH/bin в PATH) | `go vet ./...` |
| TS/JS | `tsc` — локальный `node_modules/.bin/tsc`, затем в PATH (npx НЕ используем: качает из сети) | `npm run build` |
| Python | `pyright --outputjson` (`npm i -g pyright`/`pip install pyright`) | `ruff check`, затем `python3 -m compileall` |

Выбор чекера: `exec.LookPath` → каскад фолбэков → пусто = `skipped` с заметкой
«используй Run». Запуск — однократный subprocess (ничего долгоживущего).

**Ф-3 — нативный клиент:** те же бинарники в режиме language server (`gopls`,
`typescript-language-server`, `pyright-langserver`, все понимают `--stdio`).
Запуск — `exec.Command` с pipe stdin/stdout, долгоживущий процесс,
`initialize` → `didOpen/didChange` → стрим `publishDiagnostics`, реконнект при
падении. Не через `runCommand` (тот для коротких команд с таймаутом).

## Цель

Дать агенту-кодеру доступ к языковому серверу (gopls / typescript-language-server
/ pyright), чтобы вместо «угадывания» ошибок и бесконечного парсинга логов
сборки он получал точные строки: *файл:строка:колонка:описание*. Это закрывает
главную болевую точку — выданный код, который банально не компилируется или
содержит опечатки в именах. Постепенно — и навигацию (definition/references)
для лидов/архитектора, чтобы «работать как Cursor/opencode».

## Ключевое решение: оба пути на Go

Управляющая логика агента полностью на Go (`tools/`, цикл `runner.Generate`,
оркестрация `agents/planner`, приёмка `agents/acceptor`). Поэтому:

- **Простой путь = Go-инструмент `LspCheck`**, оборачивающий CLI-чекеры через
  уже существующий `runCommand` (`tools/fileops.go:883`, с таймаутом и kill
  группы процессов). Вывод фильтруется в компактный JSON-массив диагностик.
- **Продвинутый путь = нативный Go JSON-RPC клиент** (`tools/lspclient/`):
  долгоживущий процесс языкового сервера на `stdio`, `initialize` →
  `didOpen/didChange` → стрим `publishDiagnostics` + навигация
  `definition/references/hover`.

Не «или/или» — гибрид по фазам: Ф-1 даёт результат за день, Ф-2 — авто-
самоисправление без участия модели (пайплайн из обсуждения), Ф-3 — полный
аналог IDE-возможностей.

## Принципы (наследуем из PLAN-brainstorm.md)

1. **Токен-эффективность** — возвращаем ТОЛЬКО строки ошибок
   (`файл:строка:колонка:описание` + severity), сортированные по файлу/строке,
   с лимитами на количество и объём. Никаких сырых логов сборки в контексте.
2. **Graceful degrade** — если чекер не установлен (в dev-контейнере ещё нет
   gopls/pyright), инструмент возвращает понятную заметку «используй Run
   (go vet/go test)», а НЕ падение шага. Аналогично acceptor: «инструмент не
   найден» — это `Skipped`, а не сбой.
3. **Не переписывать существующие пакеты** — тонкие слои сверху. Новые
   инструменты регистрируются в `tools/newTool` (`tools/registry.go`), цикл
   раннера почти не меняется (Ф-2 — точечный хук), всё состояние мутаций уже
   живёт в `*tools.FileOps` под `withProjectLock`.
4. **Параллельность** — `LspCheck` НЕ помечается `IsParallelSafe`
   (`tools/parallel.go`): это запуск процессов/сборок, они не конфликтуют с
   чтениями, но одновременные сборки не нужны.
5. **Стек проекта** — детект по маркерам, повторно используя логику
   `agents/acceptor/detect.go` (go.mod → package.json → requirements.txt/
   pyproject.toml). Логика акцептора живёт в пакете `agents` и не импортируема
   из `tools` (иначе цикл зависимостей) — перенесём маленький детект в `tools`
   (или вынесем в нейтральный пакет `stackdetect`), акцептор переключаем на него.

## Конфигурация (env)

| Переменная | По умолчанию | Описание |
|---|---|---|
| `LSP_MAX_DIAGS` | `30` | Максимум диагностик в одном ответе (сортировка по важности) |
| `LSP_MAX_OUTPUT` | `4000` | Лимит суммарного объёма вывода (символов) |
| `LSP_AUTO_FIX` | `1` | Авто-самоисправление после мутирующих инструментов (Ф-2) |
| `LSP_MAX_FIX_ROUNDS` | `3` | Лимит скрытых итераций исправления на один мутирующий шаг |
| `LSP_TIMEOUT` | `30s` | Таймаут одного запуска чекера / RPC для нативного клиента |
| `LSP_SERVER` | `` | Явный путь к языковому серверу (Ф-3); пусто — автопоиск в PATH |
| `LSP_MAX_LOCATIONS` | `50` | Максимум позиций (definition/references) в ответе Ф-3 |

## Архитектура (обзор)

```
┌─ runner.Generate (агентский цикл, runner/runner.go) ─────────────┐
│  • обычные tool_calls → agent.CallFunction → tools.Set.Execute    │
│  • Ф-2: хук авто-лечения после мутирующего инструмента:          │
│      touched-файлы (FileOps) → LspCheck (компактно) →            │
│      ошибки есть? → скрытый user-промпт «исправь» (≤N итераций)   │
└──────────────────────────────────┬────────────────────────────────┘
                                   ▼
  tools/ (реестр tools/registry.go)
   ├─ LspCheck        (Ф-1) CLI-обёртки: gopls check / tsc --noEmit /
   │                          pyright --outputjson → parse → фильтр
   ├─ LspDefinition   (Ф-3) tools/lspclient/ — нативный JSON-RPC клиент
   ├─ LspReferences   (Ф-3)  (голос клиента одинаковый: FAQ-парсинг)
   └─ LspHover        (Ф-3)
                                   │
                           process/stdio
                                   ▼
                 gopls | typescript-language-server | pyright
                 (установлены в dev-контейнере, см. compose.yaml)
```

Промпты агентов (`agents/developer`, `agents/planner`) дополняются шагом:
«сборка упала → вызови LspCheck с файлами правки → исправь по точным строкам».

## Новые компоненты

| Пакет / файл | Назначение |
|---|---|
| `tools/lspcheck.go` | Инструмент `LspCheck` (Ф-1): детект стека, выбор чекера, `runCommand`, парсеры вывода (JSON/text), лимиты, формат результата |
| `tools/lspcheck_test.go` | Hermetic-тесты: fake-чекер в `testdata/`, разбор gopls/tsc/pyright вывода, лимиты, degrade «чекер не найден» |
| `tools/stackdetect/` (или функции в `tools`) | Детект стека по маркерам (переезд логики из `agents/acceptor/detect.go`, чтобы не было цикла импортов) |
| `tools/lspclient/` | Нативный JSON-RPC клиент (Ф-3): запуск сервера, `initialize`, `didOpen/didChange`, навигация; `client.go`/`servers.go`/`manager.go` |
| `tools/lspnav.go` | Инструменты `LspDefinition`/`LspReferences`/`LspHover` (Ф-3): degrade, лимиты, формат результата |
| `runner/autofix.go` | Хук авто-лечения в цикле (Ф-2): затронутые файлы `FileOps.touched`, компактная диагностика, скрытый промпт, счётчик итераций |
| `runner/autofix_test.go` | Тесты: мутация → авто-LspCheck → исправление → лимит итераций |
| `docs/PLAN-lsp.md` | Этот план |
| `compose.yaml` | Установка gopls / typescript-language-server / pyright в dev-образ |

## Интеграции с существующим кодом

- **Реестр** — `tools/registry.go:newTool`: регистрация имён `LspCheck`
  (Ф-1) и навигационных (Ф-3). Контекст — существующий `*FileOps`
  (`OutputDir`, `ResolvePath`), нового поля в `tools.Deps` не нужно.
- **Наборы инструментов агентов** — `agents/developer/developer.go`:
  `devToolNames` пополняется `LspCheck` (backend/frontend). Опционально —
  `devops`, `qalead`, добавление в системный промпт шага 5 (раздел «ТОЧЕЧНЫЕ
  ПРАВКИ» уже учит использовать ReadFiles перед SEARCH — добавляем: при
  падении сборки использовать LspCheck для точных строк).
- **Запуск команд** — переиспользуем `runCommand`/`runTimeout`
  (`tools/fileops.go:870-943`) и `longRunningHint`; команды `gopls check`,
  `tsc --noEmit` короткие, хинт не срабатывает.
- **Автодетект стека** — логика `agents/acceptor/detect.go:DetectKind`
  выносится в нейтральный пакет, `acceptor` и `tools` используют один источник.
- **Точки мутаций для Ф-2** — все пишущие инструменты идут через
  `withProjectLock` в `*FileOps` (`Write`/`AppendTo`/`Remove`/`PatchGoFunction`/
  `SearchReplace`). Добавляем в FileOps потокобезопасную очередь
  `touched []string` (append внутри блокировки) — авто-лечение знает, какие
  файлы перепроверять, без изменения сигнатур инструментов.
- **Цикл раннера** — `runner/runner.go`: после прохода вызовов раунда (там же,
  где loop-детект `maxRepeatedToolCalls`) — хук `LSP_AUTO_FIX`: если есть
  `FileOps.touched` и модель НЕ завершила (нет финального текста), выполняем
  `LspCheck` по затронутым файлам; при диагностиках добавляем скрытый
  user-промпт (см. ниже) и увеличиваем счётчик; после `LSP_MAX_FIX_ROUNDS`
  — прекращаем. Видимость: событие в `runevents` (type=tool, tool=LspCheck),
  веб-чат его увидит как обычный tool-call.

## API инструментов

### Ф-1: `LspCheck` (простой путь)

```json
{
  "files": ["server/internal/user/service.go"]   // optional; пусто = проект
}
```
→ `{ "status": "success|skipped", "checker": "gopls", "diagnostics": [
     { "file": "server/internal/user/service.go", "line": 42, "col": 9,
       "severity": "error", "message": "undefined: User" } ] }`

Команды чекеров (по стеку проекта, компактный флаг вывода):
- Go: `gopls check <files>` → text-формат `file:line:col: message`;
  если gopls нет — `go vet ./...` через тот же парсер как fallback.
- TS/JS: локальный `./node_modules/.bin/tsc --noEmit --pretty false`, иначе
  `tsc` в PATH → `file(line,col): message`; fallback — `npm run build`
  (только если tsc отсутствует).
- Python: `pyright --outputjson` (или `basedpyright`) → парсим JSON
  (severity 1=error); fallback — `ruff check` (text).
- Стрим вывода ограничен `LSP_MAX_OUTPUT`, число диагностик — `LSP_MAX_DIAGS`:
  сначала errors, затем warnings (как `ReviewMr` отсекает «можно лучше»).

### Ф-3: навигация (нативный клиент, позднее)

`LspDefinition(path, line, col)`, `LspReferences(path, line, col)`,
`LspHover(path, line, col)` — без grep, ответы компактные (символ, сигнатура,
файлы/строки упоминаний, лимит `LSP_MAX_OUTPUT`). Это инструменты лидов/
архитектора (работают как Cursor/opencode: go to definition / find references).

## Пайплайн авто-самоисправления (Ф-2)

Описание из обсуждения, встроенное в цикл:

1. Кодер изменяет код (`WriteFiles`/`SearchReplace`/`PatchGoFunction`/
   `AppendFile`/`DeleteFiles`) → `FileOps.touched` пополнен.
2. Раннер (не дожидаясь модели) выполняет `LspCheck` по затронутым файлам.
3. Если диагностики ЕСТЬ — модель НЕ видит их как обычный результат: раннер
   добавляет СКРЫТЫЙ user-промпт:
   `«Твоя правка вызвала ошибки компиляции: <файл>:<строка>: <сообщение> ... .
    Исправь код с учётом этого. Итерация <n>/<LSP_MAX_FIX_ROUNDS>.»`
4. Модель исправляет; цикл повторяется до чистого результата или лимита
   `LSP_MAX_FIX_ROUNDS` (не жечь токены и раунды).

Важно: хук срабатывает только когда модель ещё НЕ завершила итоговый ответ
(иначе подсказка «из воздуха» ломает окончание цикла); при чистой диагностике
затрат нет (0 токенов).

## Этапы и чеклист

### Ф-1: `LspCheck` (CLI-обёртки) — старт
- [x] `stackdetect` (или функции в `tools`): маркеры go.mod/package.json/
      requirements.txt/pyproject.toml + unit-тесты; `acceptor` переключён на него
- [x] `tools/lspcheck.go`: выбор чекера по стеку, `runCommand`, парсеры
      (gopls-text, tsc-text, pyright-json, ruff-text), лимиты `LSP_MAX_DIAGS`/
      `LSP_MAX_OUTPUT`, формат результата `{checker, diagnostics[], skipped}`
- [x] регистрация в `tools/registry.go` (`newTool`) + добавление `LspCheck`
      в `devToolNames` (backend/frontend, опционально devops)
- [x] промпт разработчика (`agents/developer`): шаг «сборка упала → LspCheck →
      исправь по точным строкам»; degrade-инструкция «чекер не найден → Run»
- [x] Hermetic-тесты: fake-чекер в `testdata/` (отдаёт тексты gopls/tsc и
      JSON pyright), лимиты, сортировка/дедуп, degraded `skipped`
- [x] Верификация Ф-1: билд/вет/тесты (см. блок ниже), ручной прогон на
      сломанном проекте: TS (реальный tsc из PATH — 2 точные строки на
      сломанном src/index.ts, пустые при зелёном) и Go (go vet fallback в
      TestLSPResultGoReal). gopls/pyright/ruff на машине не установлены —
      их ветки покрыты hermetic-тестами и degrade-подсказкой

### Ф-2: авто-самоисправление (пайплайн)
- [x] `FileOps.touched`: потокобезопасная очередь затронутых файлов (внутри
      `withProjectLock` во Write/AppendTo/Remove/PatchGoFunction/SearchReplace);
      `tools/autofix.go` (`recordTouched`/`takeTouched`/`LspAutoFix`) + тесты
- [x] `runner/autofix.go`: хук после прохода вызовов раунда (до loop-детекта) —
      LspCheck по `touched`, скрытый user-промпт, счётчик `LSP_MAX_FIX_ROUNDS`,
      сброс счётчика на чистом раунде; событие в `runevents` (Web UI видит как
      tool-вызов `LspAutoFix`)
- [x] управление фичей: `LSP_AUTO_FIX`/`LSP_MAX_FIX_ROUNDS`; в Web UI — обычный
      tool-таймлайн без новых вьюх; док-во в `readme.md`
- [x] Тесты `runner/autofix_test.go`: fake-агент (диагностики при мутации) →
      скрытый промпт с точными строками, чисто/без мутаций, лимит итераций,
      выключение `LSP_AUTO_FIX=0`; `tools/autofix_test.go` (очередь/дедуп/E2E на
      реальном `go vet`); прогон `utils`/`tools`/`runner` под `-race`
- [x] Верификация Ф-2: `go build/vet/test` по перечню пакетов зелёные; регресс
      поведения без LSP (нет мутаций или `LSP_AUTO_FIX=0` — хук не срабатывает)

### Ф-3: нативный Go JSON-RPC клиент (навигация)
- [x] `tools/lspclient/`: запуск сервера (`gopls`/`typescript-language-server`/
      `pyright-langserver`) как child process со `stdio`; `initialize` +
      capabilities (definition/references/hover); handshake, JSON-RPC framing
      (Content-Length через `go.lsp.dev/jsonrpc2`), перезапуск мёртвого клиента
      в `Manager` (кэш по проекту+стеку), `LSP_TIMEOUT`
- [x] `didOpen` (текст файла из FS) / `didChange` (при изменении файла) —
      синхронизация документов для точной навигации. Стрим `publishDiagnostics`
      как нативный источник диагностик сознательно отложен в Ф-4 (сейчас
      `LspCheck` остаётся CLI, Ф-1)
- [x] `LspDefinition`/`LspReferences`/`LspHover` (`tools/lspnav.go`) + регистрация
      в реестре; добавлены разработчикам/QA и лидам (`agents/backendlead`,
      `frontendlead`, `architect`, `planner`) как НЕобязательные
      (graceful: сервер не найден → «используй ReadMap/ReadFiles»)
- [x] Зависимости: `go.lsp.dev/jsonrpc2` + `go.lsp.dev/protocol` (+ тесты
      hermetic: in-memory fake-сервер и реальный stdio-процесс); hand-rolled
      минимальный JSON-RPC не понадобился
- [x] Верификация Ф-3: hermetic-тесты клиента (`tools/lspclient/client_test.go`:
      channel-stream-pair + stdio через тестовый бинарник), формат/лимиты/degrade
      инструментов (`tools/lspnav_test.go`), `-race` для `tools/lspclient`/`tools`

### Ф-4: полировка и docs
- [ ] `compose.yaml`: установка gopls/typescript-language-server/pyright в
      dev-контейнер (точные версии); заметка в readme про окружение
- [ ] Токен-бюджет: проверить, что ЛСП-вывод не раздувает историю (`LSP_MAX_*`),
      дедуп повторных диагностик между итерациями (не слать модель дважды одно)
- [ ] Опционально: интеграция с приёмкой (`agents/acceptor`) и с волнами плана —
      ЛСП-замечания по scope шага; док-та в `readme.md` раздела env

## Верификация

```
go build . ./agents/... ./tools/ ./board/ ./runner/
go vet  . ./agents/... ./tools/ ./board/ ./runner/
go test . ./agents/... ./tools/ ./board/ ./runner/    # новые — под -race
# Ручной E2E на сломанном проекте (если чекеры установлены):
#   сломанный Go → LspCheck возвращает точные строки → агент исправляет
#   до чистого LspCheck; авто-лечение Ф-2 — те же правки без участия модели
```

Известные нюансы репозитория (не убирать):
- `go build ./...` падает на `temp/moon-distance/internal/middleware/cors.go`
  (артефакт сгенерированного проекта) — использовать перечень выше.
- Не форматировать `agents/acceptor/checks.go`, `agents/acceptor/run.go`.
- Комментарии/промпты/UI-тексты — на русском.
- ЛСП-серверы могут отсутствовать на машине/в контейнере: degrade обязателен
  (as `Skipped`, без сбоя шага).
- Не помечать LspCheck как `IsParallelSafe` — это запуск процессов.

## Как продолжить

1. Открыть этот файл, взять первый незачёркнутый пункт (сейчас — Ф-4),
   выполнить, отметить `[x]`, закоммитить (`docs/` + код).
2. Ф-1 не требует сети/новых зависимостей: только Go + установленные чекеры
   (для hermetic-тестов — fake в `testdata/`).
3. После фазы — верификация (блок выше) и краткая запись о сделанном.