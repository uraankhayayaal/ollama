# Токены и метрики

Учёт расхода токенов по задачам и эпикам, денежная оценка и метрики
агентского цикла (Prometheus).

## Настройка

```bash
# Денежная оценка (не заданы в .env.example — читаются кодом)
LLM_PRICE_IN=0        # цена 1M входных токенов
LLM_PRICE_OUT=0       # цена 1M выходных токенов
LLM_CURRENCY=у.е.     # подпись валюты (по умолчанию «у.е.»)
```

Цены задаёт пользователь: у провайдеров они разные и меняются
(`tokens/pricing.go:5-12`).

**Без обеих переменных цена неизвестна:** `Pricing.Known() == false`, и
интерфейс показывает токены **без** денежной оценки, а не выдуманный ноль.
Это тот же обязательный degrade, что и для Qdrant/LSP.

Любой нечисловой или отрицательный элемент игнорируется: дефолт — «не
задана», а не «нулевая» (`tokens/pricing.go:80-90`).

## Учёт по доске

`board/tokens.go`:

| Функция | Что делает |
|---|---|
| `AddTaskTokens(ctx, id, in, out)` | Прибавить к расходу задачи |
| `AddEpicTokens(ctx, id, in, out)` | Прибавить к расходу эпика |
| `FinalizeTaskTokens(ctx, id, in, out)` | **Присвоить** (не прибавить) |
| `FinalizeEpicTokens(ctx, id, in, out)` | **Присвоить** |
| `TokenTotals(ctx)` | Суммы по проекту |
| `TokenTotals.ErrorPct()` | Доля токенов, потраченных на неуспешные запуски |

Разница между `Add` и `Finalize` важна: `Finalize` перезаписывает значение,
поэтому повторный прогон не удваивает расход.

`ErrorPct()` возвращает `(pct, ok)` — `ok == false`, если в проекте не было
ошибок; UI в этом случае не показывает процент.

## Где считается

Токены провайдера суммируются в раннере и пишутся в реестр метрик:

```
модель ответила (usage)
      │
      ├── runmetrics.Registry.RecordTokens(in, out, tps, scope, agent)
      └── доска: AddTaskTokens / AddEpicTokens
```

Расход привязан к **scope** (эпик) и **агенту**, поэтому в UI видно, какая
роль сколько стоит.

## Метрики агентского цикла

`runmetrics.Registry` собирает события через `runevents.Reporter`
(`runmetrics/registry.go:153-158`) и отдаёт снимок `Snapshot`
(`:490`).

### Счётчики

| Метрика | Смысл |
|---|---|
| `run_tokens_total` | Потреблённые токены, `direction=input\|output` |
| `run_rounds_total` | Раунды модели (вызовы LLM) |
| `run_tool_calls_total` | Вызовы инструментов |
| `run_tool_errors_total` | Неуспешные вызовы |
| `run_tool_results_unpaired_total` | Результаты без парного начала (дедуп-корзина) |
| `run_app_log_lines_total` | Строки логов, прочитанные `ReadAppLogs` |
| `run_cost_units_total` | Накопленная стоимость, `kind=input\|output\|total` |

### Гейджи

| Метрика | Смысл |
|---|---|
| `run_token_price_per_million` | Цена 1M токенов, `direction=input\|output` |
| `run_uptime_seconds` | Время наблюдения текущего запуска |
| `run_output_tps` | Последняя измеренная скорость генерации (вых. ток/с) |
| `run_series_overflow` | Единицы, схлопнутые в `scope=other` из-за лимита кардинальности |

### Гистограммы

`run_tool_duration_seconds` (по инструментам), `run_step_duration_seconds`
(по scope и step) — `runmetrics/prom.go:78-93`.

> **Лимит кардинальности:** при большом числе scope/агентов единицы
> схлопываются в `scope=other`, а счётчик увеличивает `run_series_overflow`.
> Метрика остаётся полной — но имена теряются. Это осознанный компромисс
> против взрыва памяти Prometheus.

## HTTP

| Метод | Путь | Ответ |
|---|---|---|
| `GET` | `/api/metrics` | JSON-снимок по всем проектам |
| `GET` | `/api/projects/{id}/metrics` | JSON-снимок проекта |
| `POST` | `/api/projects/{id}/metrics/reset` | Сброс реестра |
| `GET` | `/api/metrics/prom` | Prometheus exposition, `text 0.0.4` |

Обработчики — `server/metrics.go:84-137`; экспозиция — `runmetrics/prom.go:19-26`.

`Snapshot` сглаживает значения до трёх знаков (`round3`,
`server/metrics.go:142`).

## Денежная оценка в UI

`web/src/Components/TokensCounter` и панель токенов на доске показывают:

- Вход / выход токенов за задачу, эпик и проект.
- Стоимость — **только если** `Pricing.Known()`.
- Долю ошибок — только если были ошибки.

## Оценка без токенизатора

Когда провайдер не отдаёт usage (некоторые локальные модели), расход
оценивается: максимум из `len/4` и `число_слов × 1.25`
(`runner/runner.go:111-119`). Кириллица учитывается.

Оценка попадает в те же счётчики, но помечается как приблизительная в UI.

## Очистка

```bash
# Через API
curl -X POST localhost:8090/api/projects/<id>/metrics/reset
```

Сброс реестра не трогает накопленные в доске токены — это разные хранилища
для разных задач (диагностика цикла vs. учёт расходов).

## Связанное

- [Планировщик](planner.md) — где считается расход раундов
- [Web UI](web-ui.md) — панель метрик
- [Провайдеры](../10-getting-started/providers.md) — откуда берётся usage
