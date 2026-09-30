# LLM-провайдеры и выбор модели

Провайдер, ключи, base URL, список моделей и лимиты описаны в
**`providers.json`** (создаётся из `providers.json.example`). Переменные
окружения задают только *выбор по умолчанию*:

```bash
LLM_PROVIDER=ollama    # ollama | yandex | trim | reg
MODEL=qwen3-coder:30b   # пусто → default_model из providers.json
MODEL_LARGE=qwen3.6:35b-a3b   # пусто → large_model из providers.json
```

Два способа сменить модель, оба без правки кода:

1. **Web UI** — переключатель «Модель» в шапке (провайдер, основная и крупная
   модели + «сброс» к переменным окружения). Переключение применяется сразу, без
   перезапуска процесса; уже идущая оркестрация дорабатывает на прежней модели.
2. **Переменные окружения** — значение по умолчанию при старте процесса
   (`go run . serve`, CLI-агенты).

В обоих случаях фактически выбранная модель пишется в лог — см.
[Отладка](#отладка).

Путь к конфигурации: `PROVIDERS_CONFIG` (не задаётся — ищем `providers.json` в
текущем каталоге и выше до корня). Правка файла подхватывается без перезапуска:
конфиг кэшируется по пути и отпечатку (mtime + размер).

## Выбор модели в Web UI

Переключатель «Модель» в шапке Web UI (`go run . serve`) — два селекта и
кнопка «сброс»:

| Элемент | Смысл |
|---|---|
| провайдер + модель | Модель для рядовых агентов (разработчики, приёмка, QA) |
| крупная модель | Модель для лидов, архитектора, ревьюера. Пусто — берётся `large_model` из конфига |
| сброс | Вернуть выбор из переменных окружения (`GET` → `override: false`) |

Особенности:

- список моделей берётся из `providers.json`; модель, которой там нет,
  выбрать нельзя (400 с перечнем доступных);
- выбор применяется **сразу**, без перезапуска процесса, и проверяется на
  сервере: если провайдер не собрался (битый `base_url`, пустая модель), UI
  показывает ошибку и возвращает прежний выбор;
- уже идущая оркестрация продолжает на прежней модели — UI об этом
  предупредит, новый выбор применится со следующего запуска;
- для Ollama модель должна быть скачана: `ollama pull qwen3-coder:30b`.

REST-контракт — [rest-api.md](../30-reference/rest-api.md#модель-и-провайдеры).

## providers.json

```json
{
  "providers": {
    "ollama": {
      "base_url": "http://localhost:11434",
      "api_key": "",
      "models": ["qwen3-coder:30b", "llama3.2", "qwen2.5-coder:7b"],
      "default_model": "qwen3-coder:30b",
      "large_model": "",
      "settings": { "think_tokens": 0, "input_tokens": 32000, "output_tokens": 16384 }
    },
    "yandex": {
      "base_url": "https://ai.api.cloud.yandex.net/v1",
      "api_key": "your-yandex-api-key",
      "folder_id": "your-folder-id",
      "model_prefix": "gpt://your-folder-id/",
      "models": ["qwen3.6-35b-a3b/latest", "yandexgpt/latest"],
      "default_model": "qwen3.6-35b-a3b/latest",
      "settings": { "think_tokens": 0, "input_tokens": 16000, "output_tokens": 8000 }
    }
  }
}
```

| Поле | Смысл |
|---|---|
| `base_url` | Базовый URL API. Для Ollama — `http://localhost:11434` |
| `api_key` | Ключ доступа (для Ollama пусто) |
| `folder_id`, `model_prefix` | Только Yandex: каталог и префикс имени модели |
| `models` | Модели, доступные для выбора в UI |
| `default_model` | Модель по умолчанию, если не задана `MODEL` |
| `large_model` | Крупная модель для «тяжёлых» агентов, если не задана `MODEL_LARGE` |
| `settings` | Лимиты: входной контекст, выход, бюджет thinking |

Модель, которой нет в `models`, выбрать нельзя: UI покажет её в списке только
если она прописана в `models`, `default_model` или `large_model`. Само наличие
в конфиге не проверяет, что модель скачана в Ollama — для локальных моделей
нужен `ollama pull <модель>`.

## Сравнение провайдеров

| | Ollama | YandexGPT | Trim | Reg Cloud |
|---|---|---|---|---|
| Где работает | Локально (в т.ч. в Docker) | Облако Yandex Cloud | Облако Trim | Облако Reg Cloud |
| Ключ | Не нужен | `api_key` + `folder_id` | `api_key` | `api_key` |
| Выход по умолчанию | 16384 | 8000 (16000 на первом `WriteFiles`) | 4000 | 32768 |
| Входной контекст | `settings.input_tokens` | `settings.input_tokens` | `settings.input_tokens` | `settings.input_tokens` (262144) |
| Reasoning | `settings.think_tokens` → `think` | → `reasoning_effort` | → `reasoning_effort` | → `reasoning_effort` |
| Нюанс | Нужен запуск `ollama serve` | — | Дифф ревью идёт целиком (`noChunk`) | Совместим с OpenAI |

## Двухуровневая маршрутизация (Ollama)

Если задана крупная модель (`MODEL_LARGE` или `large_model`), включается
`LayeredProvider` (`models/layered.go`):

- **основная модель** — рядовые шаги: разработчики, приёмка, инфраструктура.
- **крупная модель** — лиды, архитектор, ревьюер, эксперт по багам.

«Тяжёлый шаг» определяется ролью агента (`NeedsHeavyModel`) **или** эскалацией
после ошибки/короткого ответа. В UI это два независимых селекта; если крупная
не выбрана, берётся `large_model` из конфига.

`LLM_ALWAYS_HEAVY=1` — всё гнать через крупную модель (дороже, но стабильнее).

## Лимиты модели

Задаются в `providers.json` → `settings`:

```json
"settings": { "think_tokens": 0, "input_tokens": 32000, "output_tokens": 16384 }
```

**Почему вход 32000:** история nudge-цикла раздувается до десятков тысяч
токенов; меньшее окно даёт `400 exceeded_context_size`.

**Почему выход 16384:** на меньшем окне Qwen3 обрезает JSON tool-вызовов на
полуслове — модель отвечает, но вызов инструмента не парсится.

`runner.ModelLimits{InputTokens, OutputTokens, ThinkTokens}` реализуют все
провайдеры; `0` означает «провайдер не сообщает лимит», и тогда сжатие идёт
только по явным настройкам.

## Thinking (рассуждение)

`settings.think_tokens` — числовой бюджет, который переводится в уровень:
≤2k → `low`, ≤8k → `medium`, ≤12k → `high`, больше → `max`
(`models/ModelSettings.go`, `thinkLevelFromTokens`). У Ollama уровень задаётся
ключом `think`, у OpenAI-совместимых — `reasoning_effort`.

Дополнительно у Ollama есть флаг `OLLAMA_THINK` (0/1) — принудительное
выключение/включение thinking поверх конфига.

> **Рекомендация: держите `think_tokens: 0`.** Рассуждение удваивает время и
> токены на каждом раунде инструментов при той же точности вызовов, а у qwen3
> включённое thinking обрубает tool-вызовы (пустые ответы с
> `done_reason=length`).

## Авто-ретрай оборванных tool-call (Ollama)

llama-server иногда не дочитывает JSON аргументов (модель оборвала вывод на
полуслове) и отвечает `invalid tool call arguments ... unexpected end of JSON
input`. Ошибка стохастична — повтор того же запроса почти всегда корректен.

```bash
OLLAMA_TOOL_RETRIES=2        # число повторов (всего попыток = повторы + 1)
OLLAMA_TOOL_RETRY_DELAY=1000 # пауза между ними, мс
```

При пустых ответах с `done_reason=length` поднимайте до 5. Ещё есть
`OLLAMA_STREAM_IDLE` — таймаут простоя стрима.

## Как выбирается модель

```
LLM_PROVIDER / MODEL / MODEL_LARGE        выбор из Web UI (server/resolve.go)
      │                                              │
      └──────────────────┬───────────────────────────┘
                         ▼
              models.ResolveSelection
                         │
        ┌────────────────┴─────────────────┐
        ▼                                  ▼
providers.json                        providers.json
(поиск вверх от cwd                  (default_model / large_model,
 или PROVIDERS_CONFIG)               пусто → из MODEL / MODEL_LARGE)
        └────────────────┬─────────────────┘
                         ▼
        ollama  → OllamaProvider (+ LayeredProvider при крупной модели)
        yandex/trim/reg → OpenAIProvider
                         │
                         ▼
        Session.start → runner (цикл раундов)
```

## Отладка

```bash
LLM_DEBUG=1     # подробный вывод запросов/ответов
LOG_DIR=logs/    # каталог логов
```

Логи пишутся в `logs/<проект>.log` (имя проекта — из аргументов команды,
`main.go:projectFromArgs`) и в консоль процесса. Какая модель работает, видно
всегда:

| Где | Что |
|---|---|
| `logs/server.log`, консоль | `server: LLM ollama/qwen3-coder:30b (large=qwen3.6:35b-a3b)` — при старте и при каждой смене выбора |
| `logs/<проект>.log` | `[llm] модель запуска: ollama/qwen3-coder:30b` — на каждый старт оркестрации |
| `logs/<проект>.log` | `[chat] модель: ...` — на каждое сообщение в чат |
| Web UI | Переключатель «Модель» в шапке; ошибка текущего выбора — подсказкой на нём |

## Связанное

- [Web UI](../20-features/web-ui.md) — где стоит переключатель модели
- [REST API](../30-reference/rest-api.md) — `/api/providers`, `/api/providers/select`
- [Переменные окружения](../30-reference/environment-variables.md)
- [Диагностика](../40-operations/troubleshooting.md) — провайдер не настроен, выбор не применился
- [Сжатие контекста](../20-features/context-compression.md) — зачем нужны лимиты модели
- [Токены и метрики](../20-features/tokens-and-metrics.md) — сколько стоит запуск
