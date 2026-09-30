# LLM-провайдеры

Провайдер выбирается одной переменной `LLM_PROVIDER`. Поддерживаются
Ollama, YandexGPT и Trim.

```bash
LLM_PROVIDER=ollama    # ollama | yandex | trim
```

Без неё запуск падает с «unknown provider» (`models/resolve.go:24,57`).
Пустое значение не подставляет провайдера по умолчанию.

## Сравнение

| | Ollama | YandexGPT | Trim |
|---|---|---|---|
| Где работает | Локально (в т.ч. в Docker) | Облако Yandex Cloud | Облако Trim |
| Ключ | Не нужен | `YANDEX_API_KEY` + `YANDEX_FOLDER_ID` | `TRIM_API_KEY` |
| Выход по умолчанию | Не задан (рекомендуется 16384) | 8000 (16000 на первом `WriteFiles`) | 4000 |
| Входной контекст | Задаётся в модели | Только как конфигурация | Только как конфигурация |
| Reasoning | `OLLAMA_THINK`, `OLLAMA_THINK_TOKENS` | `YANDEX_THINK_TOKENS` → `reasoning_effort` | `TRIM_THINK_TOKENS` → `reasoning_effort` |
| Нюанс | Нужен запуск `ollama serve` | Ревью диффов у Trim идёт целиком (`noChunk`) | Совместим с OpenAI |

## Ollama

```bash
LLM_PROVIDER=ollama
OLLAMA_MODEL=qwen3-coder:30b
OLLAMA_MODEL_LARGE=qwen3.6:35b-a3b      # для лидов/архитектора/ревьюера
OLLAMA_INPUT_TOKENS=32000
OLLAMA_OUTPUT_TOKENS=16384
OLLAMA_THINK=0
```

### Двухуровневая маршрутизация

Если задана `OLLAMA_MODEL_LARGE`, включается `LayeredProvider`
(`models/layered.go`):

- **Лёгкая модель** (`OLLAMA_MODEL`) — рядовые шаги: разработчики, приёмка.
- **Тяжёлая модель** (`OLLAMA_MODEL_LARGE`) — лиды, архитектор, ревьюер,
  эксперт по багам, планировщик.

«Тяжёлый шаг» определяется эвристикой: роль лида **или** эскалация после
ошибки/короткого ответа.

`LLM_ALWAYS_HEAVY=1` — всё гнать через тяжёлую модель (дороже, но стабильнее).

### Лимиты модели

```bash
OLLAMA_INPUT_TOKENS=32000   # num_ctx. Legacy: OLLAMA_NUM_CTX
OLLAMA_OUTPUT_TOKENS=16384  # num_predict. Legacy: OLLAMA_MAX_TOKENS
```

**Почему 32000, а не меньше:** история nudge-цикла раздувается до десятков
тысяч токенов; меньшее окно даёт `400 exceeded_context_size`.

**Почему 16384 на выход:** на меньшем окне Qwen3 обрезает JSON tool-вызовов
на полуслове — модель отвечает, но вызов инструмента не парсится.

### Thinking (рассуждение)

```bash
OLLAMA_THINK=0           # 0/false/off — выключить; 1 — принудительно включить
OLLAMA_THINK_TOKENS=4000 # числовой бюджет → уровень low/medium/high/max
```

`OLLAMA_THINK_TOKENS` имеет приоритет над `OLLAMA_THINK`. Провайдер
принимает только уровень, поэтому число переводится: ≤2k → `low`, ≤8k →
`medium`, ≤12k → `high`, больше → `max`
(`models/ModelSettings.go`, `thinkLevelFromTokens`).

> **Рекомендация: держите `OLLAMA_THINK=0`.** Рассуждение удваивает время и
> токены на каждом раунде инструментов при той же точности вызовов, а у
> qwen3 включённое thinking обрубает tool-вызовы (пустые ответы с
> `done_reason=length`).

### Авто-ретрай оборванных tool-call

llama-server иногда не дочитывает JSON аргументов (модель оборвала вывод на
полуслове) и отвечает `invalid tool call arguments ... unexpected end of JSON
input`. Ошибка стохастична — повтор того же запроса почти всегда корректен.

```bash
OLLAMA_TOOL_RETRIES=2        # число повторов (всего попыток = повторы + 1)
OLLAMA_TOOL_RETRY_DELAY=1000 # пауза между ними, мс
```

При пустых ответах с `done_reason=length` поднимайте до 5.

## YandexGPT

```bash
LLM_PROVIDER=yandex
YANDEX_API_KEY=...
YANDEX_FOLDER_ID=...
YANDEX_MODEL=qwen3.6-35b-a3b/latest
YANDEX_OUTPUT_TOKENS=8000    # Legacy: YANDEX_MAX_TOKENS
YANDEX_INPUT_TOKENS=16000    # конфигурация модели, API не принимает
YANDEX_THINK_TOKENS=4000     # включает reasoning_effort
```

**Особенность первого раунда:** бюджет выхода автоматически повышается до
16000 токенов, когда модель генерирует `WriteFiles` (JSON со всеми файлами
проекта). Это позволяет выдать проект с десятками файлов за один вызов без
обрезания.

Формат конфигурации модели (для справки):

```json
{
  "baseURL": "https://ai.api.cloud.yandex.net/v1",
  "apiKey": "Ваш_API_Ключ",
  "models": {
    "qwen3.6-35b-a3b": "gpt://<folder>/qwen3.6-35b-a3b/latest"
  }
}
```

## Trim

```bash
LLM_PROVIDER=trim
TRIM_API_KEY=...
TRIM_HOST=...
TRIM_MODEL=...
TRIM_OUTPUT_TOKENS=4000     # Legacy: TRIM_MAX_TOKENS
TRIM_INPUT_TOKENS=16000
TRIM_THINK_TOKENS=4000
```

API совместим с OpenAI. Нюанс: дифф для ревью передаётся **целиком**, без
разбиения на части (`noChunk`), поэтому на Trim стоит держать
`REVIEW_MAX_DIFF_SIZE` небольшим.

## Как выбирается модель

```
LLM_PROVIDER
      │
      ▼
models/resolve.go
      │
      ├── ollama → OLLAMA_MODEL (+ OLLAMA_MODEL_LARGE) → LayeredProvider
      ├── yandex → AlisaDefinition (YANDEX_MODEL)
      └── trim   → TrimProvider (TRIM_MODEL)
              │
              ▼
      runner.Runner  (цикл раундов)
              │
              ▼
      ModelLimits → используется для сжатия истории
```

`runner.ModelLimits{InputTokens, OutputTokens, ThinkTokens}` реализуют все
провайдеры; `0` означает «провайдер не сообщает лимит», и тогда сжатие идёт
только по явным настройкам.

## Отладка

```bash
LLM_DEBUG=1     # подробный вывод запросов/ответов
LOG_DIR=logs/    # каталог логов
```

Логи пишутся в `logs/<проект>.log` — имя проекта берётся из аргументов
команды (`main.go:projectFromArgs`).

## Связанное

- [Конфигурация](configuration.md)
- [Переменные окружения](../30-reference/environment-variables.md)
- [Сжатие контекста](../20-features/context-compression.md) — зачем нужны лимиты модели
- [Токены и метрики](../20-features/tokens-and-metrics.md) — сколько стоит запуск
