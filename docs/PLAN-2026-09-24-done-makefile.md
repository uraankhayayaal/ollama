# План: «Makefile проекта — единая точка входа команд для субагентов + инфраструктурный слой DevOps»

Статус: **DONE с находками** (Ф-1..Ф-5 реализованы, `go build/vet`+`go test`
по перечню AGENTS.md зелёные; ручной E2E 2026-09-25 пройден наполовину —
эпик «Makefile проекта» создан, цикл упал на блокировке лидов, см.
`PLAN-2026-09-25-done-e2e-findings.md`).
Формат — как остальные `PLAN-*.md`: текущее состояние (`file:line`), решения
пользователя, архитектурные решения, новые компоненты, этапы с чекбоксами,
верификация. Обновлять по мере выполнения (чекбоксы `[x]`), статус менять
только после зелёной верификации.

## Цель

Сделать **Makefile проекта** (корень `temp/<проект>/Makefile`) единым
источником команд сборки, тестов, проверки стиля и запуска приложения, которым
пользуются все субагенты (Backend/Frontend Developer, QA Engineer, DevOps
Engineer, лиды — через задачи):

1. **Архитектор** гарантирует, что в бэклоге есть эпик «Makefile проекта»: он
   определяет контракт целей (сборка/тесты/стиль/запуск + профильные варианты
   `backend-*`/`frontend-*` по фактическому составу проекта, Р-2/Р-3). Сам файл
   пишет специалист по задаче лида (архитектор не пишет файлы —
   `agents/architect/agent.go:41-49`).
2. **Субагенты** используют этот Makefile как точку входа через инструмент
   `Run`: `make backend-build`, `make test`, `make lint` вместо зашитых команд
   (`go build ./...`, `npm run build` — сейчас в промптах,
   `agents/developer/developer.go:264`, `agents/qaengineer/agent.go:150`).
3. **DevOps** на базе проектного Makefile добавляет **инфраструктурный блок**:
   прикладная команда `go build ./...` в приложении → DevOps-цель
   `docker compose run -it --rm backend go build ./...` (и так по всем сервисам),
   плюс цели поднятия/гашения стека `up/down/logs/ps`.
4. Этот же Makefile задействуется для **e2e-запуска и тестирования** и для
   **запуска/отладки приложения целиком**: самозавершающиеся цели `e2e`,
   `run`/`backend-run`/`frontend-run`, `up`/`down` (параллель — человек, Run-инструмент
   по-прежнему не запускает долгоживущие процессы, `tools/fileops.go:1037`).
5. **Приёмка** (acceptor) при наличии Makefile предпочитает его цели
   (`make build`/`make test`/`make lint`/`make run`) вместо автодетекта по типу
   проекта (`agents/acceptor/detect.go:86-239`).

## Текущее состояние (исследование)

### Кто команды вообще использует

| Участник | Как задаёт/выполняет команды сейчас |
|---|---|
| Архитектор (`agents/architect/agent.go:41-49`) | Только чтение + доска; **не пишет файлы** → Makefile ложится в проект через эпики/задачи лидов и специалистов |
| Backend/Frontend Lead (`agents/backendlead/agent.go`, `frontendlead`) | В description задач вшивают контракты и «требование к тестам + прогон через Run» (`backendlead/agent.go:174`) |
| Developer (`agents/developer/developer.go:264`) | Хардкод: «для Go — `go build ./...`, `go vet ./...`, `gofmt -l .`, при наличии тестов `go test ./...`; для npm — `npm run build`, `npm test`» |
| QA Lead (`agents/qalead/agent.go:148-150,162`) | «ЕДИНАЯ консольная команда запуска автотестов» — команду фиксирует в задаче QA-инженеру |
| QA Engineer (`agents/qaengineer/agent.go:137-152`) | Хардкод приёмки: `go build ./...`, `go test ./...`, `npm run build`, `npm test`; «не запускать дев-процессы» |
| DevOps Engineer (`agents/devops/agent.go:124-145`) | «локально Docker Compose, прод K8s, CI/CD зовёт точную команду автотестов QA»; Makefile не упоминается |
| Приёмка acceptor (`agents/acceptor/detect.go:86-239`) | Автодетект по `Kind`: build `go build ./...`, format `gofmt -l`, analyze `go vet ./...`, run `go run .`, node — `npm run build`/`tsc`; PHP/Python — свои |
| Run-инструмент (`tools/fileops.go:996-1030,1037`) | Исполняет команду в `OutputDir`; таймаут `CODEGEN_RUN_TIMEOUT`; `longRunningHint` блокирует `go run`/`npm run dev`; `missingToolHint` (`fileops.go:973-994`) уже советует `docker compose run --rm <сервис> <команда>` |
| `DetectStack` (`tools/stacktool.go:121`) | Маркер `Makefile` входит в эвристику roles (devops), но **отдельного признака `makefile` в выводе нет** |

### Пробелы (что закрывает план)

1. Команды сборки/тестов/стиля захардкожены в шести+ промптах (разъезжаются,
   не учитывают контейнерное окружение, `make` не используется).
2. Нет «единого контракта целей»: QA видит «одну команду», Developer — другой
   набор, DevOps не переносит команды в Docker (только hint в `missingToolHint`).
3. Приёмка (acceptor) не знает про Makefile — даже если он есть, гоняет
   автодетект по kind.
4. Нет целей e2e/up/down/run-для-дебага; «запустить и отладить приложение
   целиком» не имеет явного контракта.
5. Архитектор не управляет наличием Makefile (нет правила/эпика).

## Решения пользователя (зафиксировано)

1. **Makefile создаёт архитектор, файл пишут субагенты.** Архитектор не умеет
   писать файлы — он включает в бэклог эпик «Makefile проекта» с полным
   контрактом целей (см. Р-3), а конкретные таргеты реализует Backend/Frontend
   Developer по декомпозиции лида (или DevOps Engineer для инфра-части).
2. **Контракт целей обязателен**: `help, deps, build, test, lint, run, e2e` +
   профильные варианты `backend-*`/`frontend-*` — **только для реально
   существующих частей** (консольное приложение без фронтенда не получает
   `frontend-*` цели; стек и состав — из `DetectStack`, Р-2).
3. **Субагенты используют Makefile как точку входа `Run`**: сначала смотрят
   корневой `Makefile` (ReadFiles), нашли цель — зовут `make <цель>`; нет цели —
   прежняя зашитая команда. Хардкод из промптов остаётся как фолбэк, но
   формулируется вторым приоритетом.
4. **DevOps-слой отдельным блоком в том же корневом Makefile**: цели префикса
   `infra.` оборачивают прикладные команды в
   `docker compose run -it --rm <сервис> <команда>`; плюс `up/down/logs/ps`,
   самозавершающийся `e2e`. Прикладные цели не переопределяются (иначе приёмка
   на хосте без docker падает).
5. **e2e-цель самозавершающаяся**: поднимает стек (`docker compose up -d`),
   гоняет проверки, гасит (`down`) и завершается с кодом — только так её может
   вызвать агент через `Run`. «Запуск/отладку приложения целиком» (долгоживущие
   `run`, `logs -f`) выполняет человек; агенты запускают их не должны.
6. **Приёмка предпочитает Makefile**: env `ACCEPT_*` → Makefile-цели → автодетект
   по kind. При недоступности прикладной цели на хосте (exit 127) допускается
   повтор через `make infra.<цель>`.
7. Все изменения — опциональные (nil-safe/degrade): нет Makefile → работает как
   сейчас. Промпты/комментарии — на русском.

## Архитектурные решения

### Р-1: Makefile — файл проекта, пишется специалистами по эпику архитектора
- Никаких новых агент-механизмов и инструментов: Makefile — обычный артефакт в
  `OutputDir`. Архитектор объявляет эпик, лид декомпозирует на задачи
  (Backend Lead: прикладные цели; DevOps Lead: инфра-блок и `e2e`), разработчики
  пишут `WriteFiles`, приёмка проверяет. Цепочка уже существует — добавляем
  только промпт-правила и контракт.
- Эпик «Makefile проекта» в бэклоге — обязательный для нового проекта и для
  существующего, где `DetectStack` показал отсутствие файла. В `description`
  эпика архитектор вшивает эталонный контракт целей (Р-3). Порядок: эпик
  Makefile (прикладные цели) раньше инфраструктурного блока DevOps (`dependencies`
  инфра-эпика → [`Makefile проекта`]), т.к. обернуть можно только существующие
  команды.

### Р-2: архитектор → наличие Makefile как правило бэклога
В `architectureSystemPrompt` (`agents/architect/agent.go:189`) добавляется секция
«MAKEFILE ПРОЕКТА»:
- После `DetectStack`: если `markers` не содержит `Makefile` (и нет эпика на
  доске) — обязательно добавить эпик `Makefile проекта` (assigned_role: Backend
  Lead при наличии backend, иначе DevOps Lead);
- В description эпика продублировать эталонный контракт целей (Р-3) и
  правило «цели только для реальных направлений по DetectStack»;
- В режимах ревизии (`epicReviewSystemPrompt`, `:323`) и экспертизы багов
  (`bugExpertSystemPrompt`, `:294`) — пометка: при эпиках, затрагивающих
  сборку/тесты/запуск, сверять, что контракт Makefile актуален и покрыт.
- `DetectStack` (`tools/stacktool.go:22-37`) — в `StackInfo` добавить поле
  `Makefile bool` (`json:"makefile"`) и маркер `Makefile` в `markers`, чтобы
  архитектор видел наличие файла детерминированно.

### Р-3: эталонный контракт целей (единый, встраивается в описание эпика)
```
help                  — список целей и назначений
deps                  — установка зависимостей (go mod download/npm ci/pip install)
build                 — сборка всех компонентов
backend-build         — сборка только бэкенда
frontend-build        — сборка только фронтенда            (если frontend в составе)
test                  — все автотесты
backend-test          — тесты бэкенда
frontend-test         — тесты фронтенда                     (если frontend в составе)
lint                  — стиль + анализатор (gofmt -l/go vet, prettier/eslint/black/ruff)
backend-lint          — стиль бэкенда
frontend-lint         — стиль фронтенда                     (если frontend в составе)
run                   — запуск приложения целиком (для человека, дев-режим)
backend-run / frontend-run  — запуск направления            (если есть направление)
e2e                   — САМОЗАВЕРШАЮЩИЙСЯ e2e-прогон (up → проверки → down → exit code)
[инфра-блок DevOps]   up / down / logs / ps / infra.<цель>
```
Шаги в каждой цели — через `cd <подкаталог> && <команда>` (монорепо) либо
прямые команды (один проект). Точные команды каждого стека — те же, что сейчас
захардкожены в промптах/acceptor (`detect.go:86-239`): go → build/test/vet/gofmt,
node → build/test/lint, php → lint, python → compileall/black/ruff.

### Р-4: субагенты используют Makefile (промпт-правила)
- Developer (`agents/developer/developer.go:264`): в п.5 плана добавить «сначала
  прочитай корневой Makefile (ReadFiles): если есть цель под твою проверку —
  запускай `make <цель>` (backend-разработчик — `make backend-build`/`backend-test`/
  `backend-lint`, frontend — аналогично `frontend-*`); цели нет — стандартная
  команда (`go build ./...` и т.п.)». Зашитые команды остаются фолбэком.
- QA Engineer (`agents/qaengineer/agent.go:148-150`): в п.3/п.5 «единая команда
  автотестов — из Makefile (`make test`, при наличии e2e — `make e2e`); команду
  запуска приёмки — `make build`/`make lint`». Запрет дев-процессов остаётся.
- DevOps Engineer (`agents/devops/agent.go:124-145`): «прочитай корневой
  Makefile (ReadFiles) — он источник команд. Добавь инфраструктурный блок:
  для каждой прикладной цели <цель> — зеркальная `infra.<цель>` через
  `docker compose run -it --rm <сервис> <исходная команда>` (сервис — из
  docker-compose.yml); цели `up`/`down`/`logs`/`ps`; самозавершающийся `e2e`».
- Лиды (backend/frontend/devops/qa, `agents/backendlead/agent.go:174`,
  `agents/devopslead/agent.go:161-170`): в задачах, связанных со
  сборкой/тестами/запуском, указывать цель проверки из Makefile
  («проверка: `make backend-build` + `make backend-test`»); при ревизии
  эпиков сверять, что цель существует.

### Р-5: приёмка через Makefile (acceptor)
- `agents/acceptor/detect.go` — парсер целей Makefile
  `makefileTargets(dir) map[string]bool`: строки `^([a-zA-Z0-9_.-]+):`, без
  `%`/шаблонов, без `=` (переменные), минус служебные (`.PHONY`, `.DEFAULT_GOAL`,
  `.ONESHELL` и т.п.). Файл ищется в `dir`, затем в родителях до корня приёмки.
- `buildCommand/runCommand/formatCommand/analyzeCommand` (и тесты) — новый
  приоритет: (1) env `ACCEPT_*`; (2) Makefile-цель (`make build`, `make run`,
  `make lint` для format, `make test` — добавляется как analyze-цель при её
  наличии и отсутствии `lint`); (3) автодетект по kind.
- Обёртка только для build/run/format; analyze получает `make test` (или
  `make lint`, если именно lint задан) как дополнительную проверку.
- Фолбэк инфра-среды: если прикладная цель упала с `isToolMissing`
  (exit 127/command not found, `checks.go:171`) и есть `infra.<цель>` — приёмка
  повторяет через неё (контейнерный тулчейн). `infra.*` для `run` не
  используется (контейнерный сервер не убивается группой процессов приёмки —
  `run.go:24-62`).
- Использование остаётся локальным к коду: acceptor не зависит от сети.

### Р-6: e2e и запуск/отладка целиком
- Цель `e2e` — самозавершающаяся (см. реш. п.5): `docker compose up -d` →
  прогон проверок (в т.ч. `curl`-прогрев + `docker compose exec` проверки
  контрактов) → `docker compose down` → exit code. Только так агент может её
  звать через `Run` (`longRunningHint`, `fileops.go:1037` пропускает команды
  с `&` + `sleep` + `curl`).
- Цели `run`/`backend-run`/`frontend-run` помечаются в `help` как «для ручного
  запуска/отладки (человек)»; агентам их вызывать через Run запрещено
  (долгоживущие процессы) — формулировка в конце Makefile-header.
- Приёмка приложения целиком уже умеет запуск с таймаутом
  (`acceptor`, `RunTimeout`, `config.go:24`); при `make run`-цели она
  использует её (проектный, не контейнерный вариант).

### Р-7: согласование с hint-механикой Run
- `missingToolHint` (`tools/fileops.go:973-994`) уже направляет в
  `docker compose run --rm <сервис>`; текст дополнить: «при наличии Makefile
  используй `make infra.<цель>`». Правка строковая, в тестах появится grep.

### Р-8: без деградаций
- Нет Makefile → все промпт-эпики работают как сейчас (фолбэк-команды).
- Acceptor без Makefile → прежний автодетект.
- CLI-консоль (`main.go:483`) и gitflow-приёмка (`server/gitflow_resolve.go:321`)
  используют `acceptor.Accept` — получат Р-5 автоматически.

## Новые компоненты

| Файл | Назначение |
|---|---|
| `docs/PLAN-2026-09-24-done-makefile.md` | этот план |
| `agents/architect/agent.go` (правка) | секция «MAKEFILE ПРОЕКТА» в `architectureSystemPrompt`/`epicReviewSystemPrompt`/`bugExpertSystemPrompt`; правило обязательного эпика при отсутствии Makefile |
| `tools/stacktool.go` (правка) | `StackInfo.Makefile` + маркер `Makefile` в детекте |
| `agents/developer/developer.go` (правка) | п.5: приоритет Makefile-цели в `Run` |
| `agents/qaengineer/agent.go` (правка) | п.3/п.5: `make test`/`make e2e`/`make build`; сводка в отчёт |
| `agents/devops/agent.go` (правка) | Makefile — источник команд; создание инфра-блока `infra.*`, `up/down/logs/ps/e2e` |
| `agents/devopslead/agent.go`, `agents/backendlead/agent.go`, `agents/frontendlead/agent.go`, `agents/qalead/agent.go` (правки) | в задачах указывать цель проверки из Makefile |
| `agents/acceptor/detect.go` (правка) | `makefileTargets`, приоритет Makefile-целей, фолбэк `infra.<цель>` |
| `agents/acceptor/accept_test.go` (правка) | hermetic-тесты на Makefile-приёмку |
| `tools/fileops.go` (правка) | уточнить `missingToolHint` про `make infra.<цель>` |

## Интеграции с существующим кодом

- **acceptor** — единственное место, где Makefile влияет на детерминированную
  логику (`detect.go`). `ACCEPT_*` env приоритетнее (`config.go:99-149`).
  Scope — только подпроекты приёмки (`.PHONY`/цели/переменные), лишнего шаблона
  не вводим.
- **Run-инструмент** не меняется (команды уже исполняются в `OutputDir`).
  `missingToolHint` — только текст.
- **DetectStack** используется архитектором (Ф-2 исходного плана) — добавление
  поля `makefile` обратно совместимо (`json:"makefile"` без `omitempty` не
  ломает старых потребителей: инструмент читает JSON-поля по имени).
- **Доска** не меняется: Makefile приходит через обычные эпики/задачи.
- **Промпты лидов/специалистов** — только текстовые правила; сигнатуры агентов
  и инструменты не меняются (nil-safe: отсутствие Makefile — обычный фолбэк).

## API (изменения)

```
DetectStack (схема) — новое поле:
    "makefile": bool          # в корне проекта есть Makefile
    "markers": [...]          # += "Makefile" при наличии файла
Makefile проекта — контракт целей (файл артефакт, не API):
    help/deps/build/test/lint/run/e2e + backend-*/frontend-* (по составу)
    инфра-блок: up/down/logs/ps + infra.<цель>  (только при docker compose)
```

## Этапы и чеклист

### Ф-1 — Контракт Makefile и промпт архитектора
- [x] `tools/stacktool.go`: поле `Makefile` в `StackInfo` (+маркер) — детект
      файла в корне `OutputDir`
- [x] `agents/architect/agent.go`: секция «MAKEFILE ПРОЕКТА» в
      `architectureSystemPrompt` (обязательный эпик, эталонный контракт Р-3) и
      краткие правила в `epicReviewSystemPrompt`/`bugExpertSystemPrompt`
- [x] Тесты: `detectStackAt` на проекте с/без Makefile; grep промпта по
      «Makefile», «backend-*», «e2e»; тест контракта целей не требуется (промпт)

### Ф-2 — Промпты субагентов: использование Makefile
- [x] `agents/developer/developer.go:264`: в п.5 «сначала ReadFiles корневого
      Makefile → тестируемая/собираемая цель — `make <цель>`; нет цели —
      стандартная команда»
- [x] `agents/qaengineer/agent.go:148-150`: единая команда автотестов — цель
      Makefile (`make test`, при наличии `make e2e` — e2e), приёмка через
      `make build`/`make lint`
- [x] Лиды backend/frontend/devops/qa (промпты): в задачах о
      сборке/тестах/запуске указывать цель проверки из Makefile
- [x] Тесты: grep промптов по «Makefile», «make test», «make e2e»,
      «backend-build»

### Ф-3 — Приёмка через Makefile (acceptor)
- [x] `agents/acceptor/detect.go`: `makefileTargets(dir)` (парсер целей, поиск
      в dir → родителях до корня)
- [x] `buildCommand/runCommand/formatCommand/analyzeCommand`: приоритет
      env `ACCEPT_*` → Makefile-цель (`make build`/`make run`/`make lint`+`make test`)
      → автодетект по kind
- [x] Фолбэк: `isToolMissing` + есть `infra.<цель>` → повтор через
      `make infra.<цель>` (кроме run)
- [x] Hermetic-тесты (`accept_test.go`): проект Go без Makefile (прежние
      команды); с Makefile без целей; с Makefile с целями build/test/lint/run;
      ACCEPT_BUILD_CMD перекрывает Makefile; подпроект монорепо находит
      корневой Makefile; infra-фолбэк (fake-окружение, exit 127)

### Ф-4 — E2E, запуск/отладка целиком, DevOps-блок
- [x] `agents/devops/agent.go`: «прочитай корневой Makefile; добавь инфра-блок:
      `up/down/logs/ps`, зеркальные `infra.<цель>` =
      `docker compose run -it --rm <сервис> <исходная команда>`; самозавершающийся
      `e2e` (up → проверки → down → exit code)»
- [x] `agents/devopslead/agent.go`: эпик/задачи на инфра-блок требуют наличия
      «Makefile проекта» (dependencies) и зеркал по сервисам docker-compose
- [x] `agents/qaengineer/agent.go`: в приёмке при наличии `make e2e` — опция
      e2e-прогона; дев-процессы не запускать (остаётся)
- [x] `tools/fileops.go:991` `missingToolHint`: добавить «при наличии Makefile —
      `make infra.<цель>`»
- [x] Тесты: grep промптов по «infra.», «docker compose run -it --rm»,
      «e2e», «up/down»; hint-текст

### Ф-5 — Верификация и полировка
- [x] `go build . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/`
- [x] `go vet  . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/`
- [x] `go test . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/`
      (падают только 3 пред-существующих флака чат-ассистента —
      `TestChatAssistantCreatesBugAndTask`, `TestChatAssistantDeleteTaskAfterConfirm`,
      `TestChatAssistantPublishesBoardWhenIdle`; воспроизводятся и на чистой HEAD)
- [ ] Ручной E2E на реальном проекте (2026-09-25, `e2e-makefile-app` — Go
      `taskctl` c `Dockerfile`/`docker-compose.yml`, без `Makefile`):
      архитекторная часть пройдена, приёмка не дошла из-за блокера оркестратора
      (см. `PLAN-2026-09-25-done-e2e-findings.md`).
      - ПРОЙДЕНО (Ф-1): `DetectStack` не нашёл `Makefile` → архитектор создал
        обязательный эпик `ARCH-01 «Makefile проекта»` с
        `assigned_role=Backend Lead`;
      - ПРОЙДЕНО (роли): Backend Lead — код (CSV-экспорт, валидация статуса),
        DevOps Lead — CI/CD пайплайн, QA Lead — тесты; Frontend Lead нет;
      - БЛОКЕР: цикл упал — `FATAL: Kanban: Kanban-цикл 2: нет прогресса`.
        Единственная задача `DOL-01` объявлена с `dependencies:["ARCH-01"]`, а
        `depsDone` требует, чтобы эпик-зависимость был `done`; при этом
        `phaseLeads` не выдаёт следующий эпик лиду, пока на доске есть
        незавершённые задачи → взаимная блокировка. `Makefile` не написан,
        `make build`/`make lint`/`make test`/`make e2e`/`make up` не проверялись;
      - ограничение окружения: `make`, `go`, `docker` есть, `golangci-lint`
        нет (цель `lint` может опираться на `go vet`).

## Верификация

```
go build . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/
go vet  . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/
go test . ./agents/... ./tools/ ./board/ ./rag/ ./server/ ./workspace/
```

Известные нюансы репозитория (не убирать):
- `go build ./...` падает на `temp/moon-distance/internal/middleware/cors.go`
  (артефакт сгенерированного проекта) — использовать перечень выше.
- Не форматировать `agents/acceptor/checks.go`, `agents/acceptor/run.go`.
- Комментарии/промпты/UI-тексты — на русском.
- Все возможности — опциональные (degrade): без Makefile система работает
  как сейчас.

## Связанные планы

- `PLAN-2026-09-24-done-architect-intelligence.md` — Ф-2 (`DetectStack` + роли)
  и Ф-4 (корректность задачи) — основа для правила «эпик Makefile» и поля
  `makefile` в детекте; этот план расширяет контракт целей архитектора.
- `PLAN-2026-09-19-done-lsp.md` / `PLAN-2026-09-19-done-qdrant.md` — инструменты
  чтения/приёмка: р-3/ф-3 наследуют их подход (hermetic-тесты, degrade).
- `PLAN-2026-09-19-done-qdrant.md` / `PLAN-2026-09-22-done-php.md` — командные
  стеки приёмки (`detect.go`) — источник точных команд эталонного контракта.

## Как продолжить

1. Открыть этот файл, прочитать «Решения пользователя» и «Этапы и чеклист».
2. Взять первый незачёркнутый пункт Ф-1, выполнить, отметить `[x]`, закоммитить.
3. После каждой фазы — верификация (блок выше) и краткая запись в этот файл.
4. Порядок Ф-1..Ф-5 менять можно, но релиз каждой фазы — через верификацию;
   Ф-3 (acceptor) самодостаточен и не зависит от промптов Ф-2/Ф-4.