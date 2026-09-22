# План: поддержка стека PHP (детекция, сборка, LSP, приёмка)

Статус: **НЕ РЕАЛИЗОВАНО.** План внедрения PHP как стека проекта.
Код-ревью PHP уже работает (см. «Что уже есть»); ниже — только недостающая
часть. Отмечать чекбоксы `[x]` по мере выполнения, как в `PLAN-lsp.md`.

## Цель

Дать PHP-проектам полноправный статус стека наравне с Go/Node/Python:

- детекция проекта по маркеру `composer.json`;
- команды приёмки: сборка, запуск, формат, анализ, установка зависимостей;
- LSP-чекинг и LSP-навигация (диагностика publishDiagnostics, definition/references);
- подпроекты монорепозитория (DetectProjects) с PHP-корнями.

## Что уже есть (не требует изменений)

- **Код-ревью PHP:** `langdetect` знает `.php`/`.phtml`,
  `langdetect/langdetect.go:39-40`; ревьюер проставляет фреймворк Laravel
  (`agents/codereviewer/agent.go:248-250`); опытный промпт PSR-12/Laravel —
  `langdetect/prompts.go:76-93`. Ничего не трогаем.
- **Промпт разработчика под язык** — `projects/config.go:36` свободная строка,
  достаточно `CODEGEN_LANG=PHP` (правки кода не нужны).

## Решения по умолчанию (уточнить при реализации)

| Аспект | Выбор | Пояснение |
|---|---|---|
| Маркер детекта | `composer.json` в корне | как go.mod/package.json; конфликтов с Go/Node/Python нет |
| Сборка | `composer install --no-dev` затем `php -l` по исходникам | `php` берём из PATH; если composer нет — `php -l` остаётся |
| Запуск | `php artisan serve` если `artisan` есть, иначе `php -S 127.0.0.1:8080` | покрывает Laravel и «голый» PHP |
| Формат | `php-cs-fixer --dry-run --diff` | локальный `vendor/bin/php-cs-fixer`, иначе skip |
| Анализ | `vendor/bin/phpstan analyse ` (если есть конфиг), иначе `php -l` | фолбэк на syntax-check |
| Установка | `composer install [--no-dev]` | локальный composer (не npx-аналог из сети) |
| LSP-чекер | `phpstan analyse` (local vendor bin) | фолбэк `php -l` |
| LSP-сервер | `intelephense --stdio` (или `phpactor`) | через `tools/binpath`, degrade-сообщение как у других стеков |
| Пакетные каталоги | `vendor/` — в ignore-списки | `isIgnoredDir` (`agents/acceptor/detect.go:362-368`) и `lspIgnoredDir` (`agents/acceptor/lsp.go:18-21`) |

Замечание: composer устанавливается в **проект** (`vendor/bin`), поэтому
команды вида `vendor/bin/phpstan` должны проверяться **после** install-этапа
приёмки и в заказе команд (install → build/lint/analyze). Для LSP чекеров
бинарник ищем через `binpath.Look` + локальный `vendor/bin`.

## Точки изменений

### Ф-1: детект стека (ядро)

- [ ] `stackdetect/stackdetect.go`
  - `:14-19` — добавить константу `KindPHP Kind = "php"`
  - `:29-44` — в `DetectKind` case `HasFile(dir, "composer.json")` → `KindPHP`
    (приоритет после go.mod/package.json)
- [ ] `agents/acceptor/detect.go` — реэкспорт константы `KindPHP`
  (`:20-25`) + комментарий ProjectRoot (`:40-41`)
- [ ] ignore-списки: `agent/acceptor/detect.go:isIgnoredDir` (`:362-368`)
  и `agents/acceptor/lsp.go:lspIgnoredDir` (`:18-21`) — добавить `vendor`
- [ ] тексты ошибок со списком маркеров: `agents/acceptor/accept.go:55`,
  `agents/acceptor/config.go:13-14` — упомянуть composer.json

### Ф-2: команды приёмки (`agents/acceptor/detect.go`)

5 switch по Kind; для `KindPHP`:

- [ ] `buildCommand` (`:87-108`): если есть `artisan` — `composer install --no-dev --no-interaction`, затем `find . -name '*.php' -not -path './vendor/*' -print0 | xargs -0 -r php -l` (возврат ненулевой при первом синтаксисе)
- [ ] `runCommand` (`:112-147`): `artisan` есть → `php artisan serve --host=127.0.0.1 --port=8080`; иначе `php -S 127.0.0.1:8080`
- [ ] `formatCommand` (`:152-171`): `vendor/bin/php-cs-fixer fix --dry-run --diff` (tool `php-cs-fixer`); нет бинаря → `"", ""`
- [ ] `analyzeCommand` (`:175-199`): `vendor/bin/phpstan analyse --no-progress` (конфиг `phpstan.neon`/`phpstan.neon.dist` есть) → фолбэк `php -l` syntax-check
- [ ] `installCommand` (`:204-227`): `composer install --no-dev --no-interaction` (tool `composer install`); composer нет в PATH → `"", ""`

### Ф-3: LSP

- [ ] `tools/lspcheck.go` `lspCheckerCommand` (`:299-338`): case `KindPHP` —
  локальный `vendor/bin/phpstan` (если конфиг есть) → `binpath.Look("phpstan")`
  → фолбэк `php -l`; `parseLSPOutput` (`:441-451`) — phpstan выводит в формате
  `file:line:col: message`, подходит существующий `parseFileColonLine`
- [ ] `tools/lspclient/servers.go`:
  - `ServerCommand` (`:31-64`): case `KindPHP` → `intelephense --stdio`
    (фолбэк `phpactor`), degrade-сообщение «используй ReadMap/ReadFiles»
  - `languageFor` (`:68-93`): `.php` → `protocol.LanguageKindPHP` (и php для
    `php` default-stack)
- [ ] `agents/acceptor/lsp.go` `lspSourceExt` (`:24-27`): добавить `".php"`
  (иначе PHP-файлы молча пропустятся нативной диагностикой приёмки)
- [ ] `tools/lspnative.go`/`tools/lspnav.go` — проверить, что стеки в
  языке-подпроекте (`lspProject`) покрывают PHP

### Ф-4: монорепозиторий

- [ ] `agents/acceptor/detect.go:DetectProjects` (`:53-78`) — уже работает по
  маркерам в подкаталогах: PHP-подпроекты появятся автоматически после Ф-1.
  Проверить, что `composer.json` в корне не конфликтует с Node-фронтендом
  монорепозитория (корневой маркер PHP → корень как один PHP-проект)

### Ф-5: тесты

- [ ] `stackdetect/stackdetect_test.go`: кейс composer.json → KindPHP, приоритет
  против go.mod
- [ ] `agents/acceptor/accept_test.go`: build/run/format/analyze/install для
  KindPHP (artifact? нет — conductor и syntax-check через fake php), detect
  подпроекта `vendor/`-ignore
- [ ] `tools/lspcheck_test.go`: phpstan-вывод как text-чанк, фолбэк `php -l`,
  degrade «phpstan не найден»
- [ ] `tools/lspclient/*_test.go`: `ServerCommand`/`languageFor` для PHP
- [ ] `agents/acceptor/lsp_test.go`: LSP-диагностика по `.php`-файлам

## Верификация

```
go build . ./agents/... ./tools/ ./stackdetect/ ./board/
go vet  . ./agents/... ./tools/ ./stackdetect/ ./board/
go test . ./agents/... ./tools/ ./stackdetect/ ./board/
```

Известные нюансы (не убирать):
- `go build ./...` падает на `temp/moon-distance/internal/middleware/cors.go` —
  использовать перечень выше.
- Не форматировать `agents/acceptor/checks.go`, `agents/acceptor/run.go`.
- Комментарии/промпты/тексты ошибок — на русском.
- Валидные degrade на всех уровнях: composer/phpstan/интелефензы нет на машине
  → skip/подсказка, а не падение шага.
- `composer install` зависит от сети: при голой среде без сети приёмка должна
  опираться на `php -l`, а не требовать сетевой установки.

## Оценка

Низкая–средняя сложность, ~6 файлов, ~12 точечных аддитивных правок
(новые case/константы/расширение). Архитектурных переделок нет.

## Как продолжить

1. Реализация Ф-1 → Ф-2 (минимальный полезный объём: детект + приёмка).
2. Ф-3 (LSP) и Ф-5 (тесты hermetic).
3. После Ф-3 — ручной E2E на PHP-проекте с Laravel: детект, приёмка по
   phpstan, нативная ЛСП-диагностика.