# Логи и события

Четыре независимых механизма: логи процесса агента, логи рантайма
приложения, поток событий доски и WebSocket-рассылка.

## Настройка

```bash
LOG_DIR=                        # каталог лог-файлов сервера
APP_LOG_AUTO_FEED=1             # автоподмешивание логов в промпт (вкл по умолчанию)
APP_LOG_MAX_FEED_ROUNDS=2       # сколько раз за цикл логи подмешиваются заново
```

## 1. Логи процесса агента

`server/logs.go`. `GET /api/projects/{id}/logs` отдаёт файлы `*.log` из двух
каталогов: глобального (`LOG_DIR`) и внутри каталога проекта.

```json
{
  "dir": "...",
  "files": [{"name": "run.log", "size": 1024, "modified": "...", "content": "..."}],
  "selected": "run.log",
  "truncated": true
}
```

Содержимое обрезается до **1 МБ** (`logMaxBytes`, `logs.go:27`); факт
обрезания отдаётся полем `truncated`, а не флагом внутри файла.

`logsDir()` (`logs.go:125`) и `collectLogFiles` (`:151`) собирают список;
`readLogTail` (`:180`) читает хвост.

## 2. Логи рантайма приложения

Два независимых пути.

### Инструмент `ReadAppLogs`

`tools/applogs.go:119`. Агент сам запускает приложение и читает его вывод.

| Параметр | По умолчанию | Предел |
|---|---|---|
| `source` | `auto` | `auto` / `local` / `docker` |
| `command` | определяется автоматически | — |
| `service` | — | только для `source=docker` |
| `lines` | 200 | 2000 |
| `wait` | 3 с | 30 с |

Автоопределение команды (`resolveRunCommand`, `applogs.go:500`):

```
Makefile с целью run → make run
package.json → npm-раннер (npm run dev / yarn / pnpm)
иначе → run-скрипт по стеку, для статики — node one-liner HTTP-сервер на :8080
```

Для `source=docker` ищется compose-файл (`findComposeFile:634`) и читаются
логи сервиса (`readDockerLogs:381`).

**Корректное завершение:** `SIGTERM`, пауза 500 мс (`appLogsKillGrace`), затем
`SIGKILL` — приложение успевает дописать буферизованные строки
(`stopProcess:473`).

### Автоподмешивание в промпт

```bash
APP_LOG_AUTO_FEED=1
APP_LOG_MAX_FEED_ROUNDS=2
```

Строки логов **подмешиваются в промпт модели**, чтобы агент сам починил
`panic` или «порт занят», не вызывая инструмент вручную.

Повторяется до `APP_LOG_MAX_FEED_ROUNDS` раз за цикл — 2 хватает, чтобы
увидеть эффект правки, и не засоряет контекст.

## 3. Буфер логов и WebSocket

`server/applog.go`:

| Параметр | Значение |
|---|---|
| `appLogBufferLines` | 2000 строк в кольцевом буфере |
| `appLogDefaultTail` | 300 строк по умолчанию |
| `appLogMaxTail` | 2000 строк максимум |
| `appLogIdleFlush` | сброс накопленных строк в лог проекта раз в 2 с |

Писать в файл на каждую строку значило бы захлёбывать диск логами от рантайма
— копим и пишем пачками (`appLogFlusherLoop`, `applog.go:110`).

Поле `Time` — момент **получения** строки, не время приложения: логи
сторонних приложений часто вообще без таймстемпов (`applog.go:47-51`).

## 4. Поток событий

`server/events.go` — события доски, публикуемые в WebSocket:

| Тип | Когда |
|---|---|
| `board_changed` | Изменилась доска |
| `chat_updated` | Обновился чат |
| `git_status_changed` | Изменился git-статус |
| `tokens_updated` | Обновился расход токенов |
| `session_status` | Сменился статус сессии |

## 5. События агентского цикла

`runevents/reporter.go:50` — единая структура события:

```go
type Event struct {
    Type      EventType
    Agent     string   // через WithAgent
    Role      string
    Content   string   // ответ модели / потоковый фрагмент
    StreamID  string   // связывает фрагменты с одним «плавающим» сообщением
    Tool      string
    Arguments string   // обрезаны
    Result    string   // обрезан
    OK        bool
    Truncated bool
    In, Out   int64    // токены раунда
    TPS       float64  // реальная скорость генерации
    Scope     string   // task:<id> | epic:<id> | architecture | bugs
    Source    string   // local | docker, для логов рантайма
    Time      time.Time
}
```

Интерфейс `Reporter` (`reporter.go:76-98`):

| Метод | Когда |
|---|---|
| `OnMessage(role, content, truncated)` | Финальный ответ модели |
| `OnMessageDelta(streamID, content)` | Потоковый фрагмент |
| `OnToolStart(tool, args)` | Начало вызова инструмента |
| `OnToolResult(tool, result, ok)` | Результат вызова |
| `OnTokens(in, out, tps)` | Расход раунда |
| `OnAppLog(source, content)` | Строка лога рантайма |

> **Потокобезопасность:** `OnAppLog` может вызываться из горуниц инструмента
> `ReadAppLogs`, поэтому реализация обязана быть потокобезопасной, в отличие
> от остальных методов (раннер вызывает их последовательно).

`Scope` определяет, на что потрачены токены раунда; пусто — расход на проект
целиком.

## Рассылка в WebSocket

`server/logbroker.go` — отдельный брокер, следящий за файлами логов
(**не** через `ReadAppLogs`):

| Параметр | Значение |
|---|---|
| `scanEvery` | 10 (интервал сканирования каталогов) |
| `headLen` | 64 байта — «голова» файла для обнаружения ротации |

Механика: подписка на каталоги и файлы (`Subscribe:116`), хвост каждого
открыт с конца (`openAtEnd:235`), чтение новых строк (`readNew:314`),
детект ротации по смене `head` (`rotated:279`).

Ротация обрабатывается корректно: файл переоткрывается, а не теряется.

## Что где смотреть

| Что | Где |
|---|---|
| Ошибка агентского цикла | Логи процесса (Web UI → «Логи») |
| `panic` в приложении | `ReadAppLogs` + автоподмешивание |
| Логи compose-сервисов | `ReadAppLogs(source=docker)` |
| Изменения доски | События `board_changed` |
| Расход токенов | `tokens_updated` + [метрики](tokens-and-metrics.md) |

## Проверить

```bash
ls -la $(go run . serve 2>&1 | head -1 >/dev/null; echo logs) 2>/dev/null || ls -la logs/
```

Проверить буфер логов рантайма:

```http
GET /api/projects/{id}/applog?lines=300
```

## Связанное

- [Web UI](web-ui.md) — панели логов и таймлайн инструментов
- [Инструменты](tools.md) — `ReadAppLogs`
- [Токены и метрики](tokens-and-metrics.md) — `Scope` и расход
- [REST API](../30-reference/rest-api.md)
