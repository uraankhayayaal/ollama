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

| | Ollama | vLLM | YandexGPT | Trim | Reg Cloud |
|---|---|---|---|---|---|
| Где работает | Локально (в т.ч. в Docker) | Локально (GPU) | Облако Yandex Cloud | Облако Trim | Облако Reg Cloud |
| Ключ | Не нужен | Не нужен | `api_key` + `folder_id` | `api_key` | `api_key` |
| Выход по умолчанию | 16384 | 16384 | 8000 (16000 на первом `WriteFiles`) | 4000 | 32768 |
| Входной контекст | `settings.input_tokens` | `--max-model-len` | `settings.input_tokens` | `settings.input_tokens` | `settings.input_tokens` (262144) |
| Reasoning | `settings.think_tokens` → `think` | → `reasoning_effort` | → `reasoning_effort` | → `reasoning_effort` | → `reasoning_effort` |
| API | Ollama API | OpenAI-compatible | OpenAI-compatible | OpenAI-compatible | OpenAI-compatible |
| Производительность | ~10-30 tok/s (CPU/GPU) | ~50-800 tok/s (GPU cluster) | — | — | — |
| GPU | Опционально | Обязательно | — | — | — |
| Нюанс | Нужен запуск `ollama serve` | Нужен `pip install vllm` + GPU | — | Дифф ревью идёт целиком (`noChunk`) | Совместим с OpenAI |

## vLLM — локальный inference-движок

Альтернатива Ollama для систем с мощными GPU. vLLM работает как OpenAI-compatible
API-сервер, поэтому подключается к системе через `OpenAIProvider` без правок кода.

### Установка

```bash
pip install vllm
```

Требования: NVIDIA GPU с поддержкой FP8 (Hopper/Ada, RTX 50-серия), минимум
24 ГБ VRAM на GPU.

### Запуск

Базовая команда (2× RTX 5070 Ti):

```bash
python3 -m vllm.entrypoints.openai.api_server \
  --model unsloth/Qwen3.6-27B-NVFP4 \
  --tensor-parallel-size 2 \
  --max-model-len 98304 \
  --kv-cache-dtype fp8 \
  --gpu-memory-utilization 0.93 \
  --max-num-seqs 4 \
  --enable-chunked-prefill \
  --limit-mm-per-prompt '{"image":0,"video":0}' \
  --trust-remote-code \
  --port 8000 \
  --enable-auto-tool-choice \
  --tool-call-parser qwen3_coder
```

### Параметры запуска

| Флаг | Смысл | Рекомендации |
|---|---|---|
| `--model` | Модель с Hugging Face | `unsloth/Qwen3.6-27B-NVFP4` — FP8-версия, меньше VRAM |
| `--tensor-parallel-size` | Число GPU для распределения модели | Кол-во доступных GPU (2 для RTX 5070 Ti x2) |
| `--max-model-len` | Максимальная длина контекста | 98304 — максимальный контекст модели |
| `--kv-cache-dtype` | Точность KV-кэша | `fp8` — экономия VRAM при высокой производительности |
| `--gpu-memory-utilization` | Доля VRAM для inference | 0.93 — оставить ~7% для ОС и фоновых процессов |
| `--max-num-seqs` | Максимум параллельных запросов | 4 — достаточно для оркестрации (не более 2-3 агентов параллельно) |
| `--enable-chunked-prefill` | Chunked prefill | Включить — ускоряет обработку длинных промптов |
| `--limit-mm-per-prompt` | Лимит мультимодальных данных | `{"image":0,"video":0}` — отключить (не нужно для кодогенерации) |
| `--trust-remote-code` | Разрешить код модели | Требуется для Qwen3 |
| `--port` | Порт API | `8000` — дефолт vLLM |
| `--enable-auto-tool-choice` | Auto tool choice API | Включить — нужен для tool-calling |
| `--tool-call-parser` | Парсер вызовов инструментов | `qwen3_coder` — оптимизирован для Qwen-моделей |

### Производительность (2× RTX 5070 Ti)

Замеренные показатели для `unsloth/Qwen3.6-27B-NVFP4`:

| Метрика | Значение |
|---|---|
| Prompt throughput (первый запрос) | ~815 tok/s |
| Generation throughput (стабильный) | ~52 tok/s |
| GPU KV cache usage | ~14% (типичный запрос) |
| Prefix cache hit rate | ~75% |

Сравнение с Ollama: **vLLM в 2-5 раз быстрее** на generation-этапе (52 vs 10-30 tok/s).

### Подключение к системе

Добавить провайдер в `providers.json`:

```json
{
  "providers": {
    "vllm": {
      "base_url": "http://localhost:8000/v1",
      "api_key": "",
      "models": ["unsloth/Qwen3.6-27B-NVFP4"],
      "default_model": "unsloth/Qwen3.6-27B-NVFP4",
      "large_model": "",
      "settings": {
        "think_tokens": 0,
        "input_tokens": 65536,
        "output_tokens": 16384
      }
    }
  }
}
```

> **Важно:** `base_url` указывает на `/v1`-эндпоинт vLLM (`/v1/chat/completions`).

Или через переменные окружения:

```bash
LLM_PROVIDER=vllm
MODEL=unsloth/Qwen3.6-27B-NVFP4
```

### Tuning по оборудованию

| Оборудование | `tensor-parallel-size` | `gpu-memory-utilization` | `max-num-seqs` | Модель |
|---|---|---|---|---|
| 1× RTX 3090/4090 (24 ГБ) | 1 | 0.85 | 2 | 7B-14B |
| 1× RTX 5070 Ti (16 ГБ) | 1 | 0.80 | 2 | 7B-14B |
| 2× RTX 5070 Ti (32 ГБ) | 2 | 0.93 | 4 | 27B-32B |
| 2× A100 (80 ГБ) | 2 | 0.95 | 8 | 70B+ |
| 4× A100/H100 | 4 | 0.95 | 8 | 70B+ |

### Отладка vLLM

Сервер логирует метрики каждые 10 секунд. Ключевые индикаторы:

- **GPU KV cache usage** > 80% — снизить `max-num-seqs` или `gpu-memory-utilization`
- **Prefix cache hit rate** < 50% — проверить, не перегружен ли cache; при низком — поднять `max-num-seqs`
- **Avg generation throughput** падает при `Running: 2+ reqs` — снизить `max-num-seqs`

```bash
# Проверка, что сервер отвечает
curl -s http://localhost:8000/v1/models | python3 -m json.tool

# Быстрый тест генерации
curl http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"unsloth/Qwen3.6-27B-NVFP4","messages":[{"role":"user","content":"Hello"}],"max_tokens":100}'
```

## Двухуровневая маршрутизация (Ollama)

Если задана крупная модель (`MODEL_LARGE` или `large_model`), включается
`LayeredProvider` (`infrastructure/llm/layered.go`):

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
(`infrastructure/llm/ModelSettings.go`, `thinkLevelFromTokens`). У Ollama уровень задаётся
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
              llm.ResolveSelection
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
