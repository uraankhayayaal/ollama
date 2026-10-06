# Конфигурация

Вся конфигурация — через переменные окружения, загружаемые из `.env`
библиотекой `godotenv` при старте. Файл `.env` необязателен: пустое значение
почти всегда означает «по умолчанию в коде».

**Полный справочник с умолчаниями** — в
[Переменных окружения](../30-reference/environment-variables.md). Здесь —
как ими пользоваться.

## Файлы конфигурации

| Файл | Назначение |
|---|---|
| `.env` | Рабочая конфигурация. Не коммитится |
| `.env.example` | Шаблон с комментариями. 392 строки, покрывает основные домены |
| `.env.macbook.air` | MacBook Air M1 / 16 ГБ — 8B Q4_K_M, малый контекст, мелкие диффы |
| `.env.macbook.m3` | MacBook M3 / 64 ГБ — 14B–30B, дефолтные лимиты, таймаут 15m |
| `.env.rtx5070ti` | Конфигурация с NVIDIA RTX 5070 Ti |

Переключение между машинами — копированием нужного файла:

```bash
cp .env.macbook.m3 .env
```

## Приоритеты

От высшего к низшему:

1. **Переменная окружения процесса** — `AI_WEB_ADDR=0.0.0.0:8090 go run . serve`
2. **Позиционный аргумент CLI** — `go run . serve 0.0.0.0:8090` (для адреса
   Web UI это переопределяет env, `main.go:88`)
3. **Файл `.env`**
4. **Значение по умолчанию в коде**

Внутри `ACCEPT_*` есть свой, более узкий приоритет (команды приёмки):

```
ACCEPT_*_CMD  →  make-цель из Makefile проекта  →  автодетект по стеку
```

Подробнее — [Makefile-контракт](../20-features/makefile-contract.md).

## Минимальный набор

```bash
# Обязательно
LLM_PROVIDER=ollama          # ollama | yandex | trim | reg

# Модели (ключи, base_url и лимиты — в providers.json)
MODEL=qwen3-coder:30b        # рядовые шаги
MODEL_LARGE=...              # лиды, архитектор, ревьюер (опционально)
```

Всё остальное имеет рабочие умолчания. Модель можно переключить и в Web UI
(`GET/POST /api/providers`) — без перезапуска процесса.

## Часто настраиваемые группы

### Производительность

```bash
CODEGEN_PARALLEL=1           # параллельные волны (вкл по умолчанию)
CODEGEN_HISTORY_BUDGET=200000  # сжатие истории под символный бюджет
LSP_AUTO_FIX=1               # авто-починка по диагностикам (вкл)
```

Слабое железо (8B на 16 ГБ) — уменьшайте:

```bash
# окно контекста модели — в providers.json → settings.input_tokens
REVIEW_CHUNK_SIZE=6000       # размер части диффа для ревью
REVIEW_MAX_COMMENTS=5
REVIEW_TIMEOUT=10m
REVIEW_MAX_ROUNDS=8
```

### Качество и глубина

```bash
REVIEW_CRITICAL_ONLY=0       # не отсекать некритичные замечания
REVIEW_FIX_ROUNDS=3          # циклов «ревью → исправление → ревью»
ACCEPT_MAX_ROUNDS=3          # циклов «приёмка → исправление → приёмка»
CODEGEN_HISTORY_EVICT=1      # вытеснение истории в RAG (Ф-7)
```

### Интеграции

```bash
# RAG
QDRANT_ADDR=localhost:56334
EMBEDDING_MODEL=nomic-embed-text
RAG_AUTO_REINDEX=1           # авто-обновление индекса после правок

# Git
GITHUB_TOKEN=... / GITLAB_TOKEN=...

# Web UI
AI_WEB_PASSWORD=...          # пусто — вход без пароля
```

### Безопасность выполнения

```bash
CODEGEN_SANDBOX=container    # container | session | auto | "" (хост)
CODEGEN_SANDBOX_NETWORK=none # без сети (для session — по умолчанию)
CODEGEN_SANDBOX_ALLOW_DOMAINS=proxy.golang.org,*.npmjs.org  # белый список (session)
CODEGEN_SANDBOX_RO=1         # read-only корень контейнера
```

## Частые ошибки в конфигурации

| Симптом | Причина | Решение |
|---|---|---|
| `unknown provider` при старте | Не задан `LLM_PROVIDER` | Задать `LLM_PROVIDER=ollama` |
| `400 exceeded_context_size` | Окно модели меньше разросшейся истории | Поднять `settings.input_tokens` в `providers.json` или включить `CODEGEN_HISTORY_*` |
| Пустые ответы модели, `finish_reason=length` | Мал `num_predict` — Qwen3 обрезает JSON tool-вызовов | Поднять `settings.output_tokens` (рекомендуется 16384) |
| `CodeSearch` всегда `skipped` | Нет Qdrant или не построен индекс | `docker compose up -d` + `go run . index <проект>` |
| `LspCheck` всегда `skipped` | Не установлен LSP-сервер | Поставить сервер, проверить `LSP_BIN_PATH` |
| `plan` не восстанавливается | Не задан `REDIS_ADDR` или нет `--resume` | Проверить Redis и `PLAN_RESUME=1` |
| `kanban` не стартует | Нет Redis для доски | `docker compose up -d`, проверить `BOARD_REDIS_ADDR` |

## Расхождения между `.env.example` и кодом

Обнаружены при сверке. Учитывайте при настройке:

**Читается кодом, но не описано в `.env.example`:**

| Переменная | Умолчание в коде |
|---|---|
| `LSP_SERVER`, `LSP_NATIVE`, `LSP_TIMEOUT`, `LSP_DIAG_WAIT`, `LSP_IDLE_TTL` | `""`, вкл, `15s`, `3s`, `10m` |
| `LSP_MAX_DIAGS`, `LSP_MAX_LOCATIONS`, `LSP_MAX_OUTPUT` | `30`, `50`, `4000` |
| `LSP_BIN_PATH`, `LSP_STEP_GATE`, `LSP_AUTO_FIX`, `LSP_MAX_FIX_ROUNDS` | `""`, вкл, вкл, `3` |
| `ACCEPT_TEST_CMD`, `ACCEPT_AUTOFORMAT`, `ACCEPT_LSP` | `""`, `true`, `true` |
| `CODEGEN_PARALLEL`, `CODEGEN_ROLLBACK_ON_FAIL`, `CODEGEN_CONTRACT_ENFORCE` | все **включены** |
| `CODEGEN_RUN_MAX_OUTPUT`, `CODEGEN_SKELETON_CACHE` | `20000`, вкл |
| `RAG_ARCHITECT_CONTEXT`, `RAG_ASSISTANT_CONTEXT` | оба **включены** |
| `LLM_PRICE_IN`, `LLM_PRICE_OUT`, `LLM_CURRENCY` | `0`, `0`, `у.е.` |
| `LOG_DIR`, `AI_WORKSPACES`, `PARALLEL_TOOL_CALLS`, `OLLAMA_STREAM_IDLE` | — |
| `WEB_SEARCH_MAX_RESULTS`, `WEB_SEARCH_TIMEOUT` | — |

**Практическое следствие:** эти флаги активны по умолчанию, но не видны в
шаблоне — их нельзя «выключить, убрав из `.env`», нужно явно задать
`0`/`false`/`off`.

**Описано в `.env.example`, но кодом не читается:**

| Переменная | Комментарий |
|---|---|
| `OLLAMA_HOST` | Адрес сервера; в этом коде не читается — клиент Ollama берёт его сам |
| `OLLAMA_KV_CACHE_TYPE`, `OLLAMA_KEEP_ALIVE` | Параметры **сервера** `ollama serve`, не клиента |
| `CODEGEN_SANDBOX_BIN`, `CODEGEN_SANDBOX_USER` | Только для `docker compose -f sandbox/compose.yaml build` |
| `CODEGEN_DOCKER_TLS`, `CODEGEN_DOCKER_CERT` | Прокидываются в окружение демона при наличии хоста/сертификата |

**Есть в коде, но не документировано нигде:** `PORT` — читается только
архивными проектами в `archive/`, к основному коду отношения не имеет.

## Проверка конфигурации

```bash
# Какие переменные реально видны процессу
env | grep -E '^(LLM|OLLAMA|QDRANT|REDIS|BOARD|CODEGEN|ACCEPT|LSP|RAG|REVIEW)_' | sort

# Проверить конкретную
grep -n '^CODEGEN_PARALLEL' .env
```

## Связанное

- [Переменные окружения](../30-reference/environment-variables.md) — полный справочник
- [LLM-провайдеры](providers.md)
- [Решение проблем](../40-operations/troubleshooting.md)
