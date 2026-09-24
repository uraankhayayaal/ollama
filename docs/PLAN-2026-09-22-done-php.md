# План: поддержка стека PHP (детекция, сборка, LSP, приёмка)

Статус: **РЕАЛИЗОВАНО (Ф-1..Ф-5).** План внедрения PHP как стека проекта.
Код-ревью PHP уже работает (см. «Что уже есть»); ниже — только недостающая
часть. Отмечать чекбоксы `[x]` по мере выполнения, как в `PLAN-2026-09-19-done-lsp.md`.

Отклонения от плана, принятые при реализации:

- Константа названа `KindPhp` (не `KindPHP`) — в Go не приняты
  ALL-CAPS-идентификаторы; консистентно с `KindGo`/`KindNode`/`KindPython`.
- Приоритет маркеров: `go.mod` → `composer.json` → `package.json` → python.
  composer.json **перед** package.json: Laravel/пакетные PHP-проекты несут оба
  маркера, и корень должен трактоваться как PHP (иначе Node-сборка ломает
  приёмку PHP-проекта).
- `buildCommand` = только `php -l` по всем исходникам
  (`phpLintCmd()`, `xargs -n1`). `composer install` живёт в `installCommand`
  и выполняется до build/analyze когда `cfg.InstallDeps=true` — как в
  замечании «install → build/lint/analyze» ниже; дублировать установку в build
  не стали (уважает `InstallDeps=false` и не требует сети при голой среде).
- phpstan выводит error-format=raw как `file:line:message` **без колонки** —
  план предполагал `file:line:col:`; добавлен отдельный парсер `parsePhpstanRaw`
  (не `parseFileColonLine`).
- phpstan (локальный `vendor/bin/phpstan` или глобальный `phpstan`) запускается
  только при наличии конфига `phpstan.neon`/`phpstan.neon.dist`/`phpstan.dist.neon`;
  без конфига — фолбэк `php -l`.
- LSP-сервер PHP: `intelephense --stdio` (фолбэк `phpactor`); команды ищутся
  через `tools/binpath`, degrade-сообщение «…навигация недоступна, используй
  ReadMap/ReadFiles».
- `lspSourceExt` дополнен `.php` **и `.phtml`**.

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

- [x] `stackdetect/stackdetect.go`
  - константа `KindPhp Kind = "php"` (в стиле `KindGo`/`KindNode`)
  - `DetectKind`: case `HasFile(dir, "composer.json")` → `KindPhp`
    (приоритет: go.mod → composer.json → package.json → python)
- [x] `agents/acceptor/detect.go` — реэкспорт константы `KindPhp` + комментарий
  ProjectRoot «go/php/node/python»
- [x] ignore-списки: `vendor` уже был в `isIgnoredDir`
  (`agents/acceptor/detect.go`) и `lspIgnoredDir` (`agents/acceptor/lsp.go`) —
  правок не потребовалось (проверено тестом `TestProjectSourceFiles`)
- [x] тексты ошибок со списком маркеров: `agents/acceptor/accept.go` и
  `agents/acceptor/config.go` — упомянут composer.json

### Ф-2: команды приёмки (`agents/acceptor/detect.go`)

5 switch по Kind; для `KindPhp`:

- [x] `buildCommand`: `phpLintCmd()` — php -l по всем `.php` (исключая vendor/)
- [x] `runCommand`: нет php.ini/серверов — `artisan` есть → `php artisan serve
  --host=127.0.0.1 --port=8080`; `public/index.php` → `php -S 127.0.0.1:8080 -t public`;
  `index.php` → `php -S 127.0.0.1:8080`; иначе `""`
- [x] `formatCommand`: `vendor/bin/php-cs-fixer fix --dry-run --diff`
  (tool `php-cs-fixer`); нет бинаря → `"", ""`
- [x] `analyzeCommand`: `vendor/bin/phpstan analyse --no-progress --error-format=raw`
  (глобальный `phpstan` как фолбэк-бинарь, только при конфиге
  `phpstan.neon`/`phpstan.neon.dist`) → иначе фолбэк `phpLintCmd()` (tool `php -l`)
- [x] `installCommand`: `composer install --no-dev --no-interaction --no-progress`
  (tool `composer install`), только если `composer.json` и `cmdAvailable("composer")`;
  иначе `"", ""` (skip, а не ошибка)

### Ф-3: LSP

- [x] `tools/lspcheck.go` `lspCheckerCommand`: case `KindPhp` — локальный
  `vendor/bin/phpstan` (конфиг-гейт) → глобальный `binCommand("phpstan")` →
  фолбэк `php -l` (по файлам через `printf | xargs -n1 php -l`, иначе
  `phpLintLSPCmd()` find-командой); `parseLSPOutput` case `KindPhp` →
  `parsePhpstanRaw` (формат `file:line:message` без колонки)
- [x] `tools/lspclient/servers.go`:
  - `ServerCommand`: case `KindPhp` → `intelephense --stdio` (фолбэк
    `phpactor`), degrade «…навигация недоступна, используй ReadMap/ReadFiles»
  - `languageFor`: `.php`/`.phtml` → `protocol.LanguageKindPHP`; default-стек
    KindPhp → `protocol.LanguageKindPHP`
- [x] `agents/acceptor/lsp.go` `lspSourceExt`: добавлены `".php"`, `".phtml"`
- [x] `tools/lspnative.go`/`tools/lspnav.go` — проверено: стеки только
  пробрасываются (kind → lspclient), switch-ов по Kind нет — PHP покрывается
  case'ами в lspclient

### Ф-4: монорепозиторий

- [x] `agents/acceptor/detect.go:DetectProjects` работает по маркерам: PHP-подпроект
  по `composer.json` в подкаталоге — отдельный корень (тест
  `TestDetectProjectsPhpSubproject`). Корневой `composer.json` + `package.json`
  (Laravel) → корень = один PHP-проект (приоритет маркеров, тест
  `TestDetectKindPrefersComposerOverPackageJSON`)

### Ф-5: тесты

- [x] `stackdetect/stackdetect_test.go`: composer.json → KindPhp; приоритет
  composer.json над package.json; go.mod всё ещё главный
- [x] `agents/acceptor/accept_test.go`: `TestPhpCommandsByKind`
  (build/run/format/analyze по маркерам), `TestPhpAnalyzeFallbackAndInstall`
  (phpstan без конфига → php -l; composer вне PATH → пропуск install),
  `TestPhpInstallWithComposer` (composer в PATH → composer install через shim);
  `TestDetectProjectsPhpSubproject`, `TestDetectKind` + composer.json
- [x] `tools/lspcheck_test.go`: `TestParsePhpstanRaw` (парсинг + игнор шумных
  строк), `TestLSPCommandSelectionPhpLocal` (local phpstan), `TestLSPCommandSelectionPhpFallback`
  (`php -l` командой и по точечным файлам)
- [x] `tools/lspclient/servers_test.go`: `TestLanguageForPhp` (`.php`/`.phtml`/
  default-стек → LanguageKindPHP)
- [x] `agents/acceptor/lsp_test.go`: `TestProjectSourceFiles` дополнен `.php`,
  `.phtml` и ignore `vendor/x.php`

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