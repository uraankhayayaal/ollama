# Переменные окружения

Полный справочник. Значения по умолчанию — **из кода**, а не из
`.env.example`; расхождения отмечены явно.

## Правила разбора

| Тип | Как разбирается | Пример |
|---|---|---|
| `bool` | `0`/`false`/`off`/`no` → выкл; всё остальное непустое → вкл | `ACCEPT_LSP=0` |
| `int` | `strconv.Atoi`; ошибка или ≤ 0 → дефолт | `RAG_MAX_RESULTS=5` |
| `duration` | `time.ParseDuration`; также голое число = секунды (LSP) | `LSP_TIMEOUT=20s` |
| `float` | `strconv.ParseFloat`; нечисловое/отрицательное → «не задана» | `LLM_PRICE_IN=3.0` |
| флаг-по-пустоту | непустое значение = включено | `CODEGEN_CONTRACT_ENFORCE=1` |

**Пустое значение почти всегда означает «дефолт», а не «ноль».** Например,
`LSP_MAX_DIAGS=` не отключает лимит, а возвращает 30.

## Переменные, отсутствующие в `.env.example`

Их **нет** в шаблоне, хотя код их читает. Часть активна по умолчанию —
выключается явным `0`/`false`/`off`/`no`.

### LSP (весь контур)

`LSP_SERVER`, `LSP_NATIVE`, `LSP_TIMEOUT`, `LSP_DIAG_WAIT`, `LSP_IDLE_TTL`,
`LSP_MAX_DIAGS`, `LSP_MAX_LOCATIONS`, `LSP_MAX_OUTPUT`, `LSP_BIN_PATH`,
`LSP_STEP_GATE`, `LSP_AUTO_FIX`, `LSP_MAX_FIX_ROUNDS` — 12 переменных.

Плюс `ACCEPT_LSP` в приёмке.

### Сжатие и инструменты

`CODEGEN_PARALLEL`, `CODEGEN_ROLLBACK_ON_FAIL`, `CODEGEN_CONTRACT_ENFORCE`,
`CODEGEN_RUN_MAX_OUTPUT`, `CODEGEN_SKELETON_CACHE`.

### RAG-контекст по ролям

`RAG_ARCHITECT_CONTEXT`, `RAG_ASSISTANT_CONTEXT` (активны по умолчанию);
`RAG_PLANNER_CONTEXT` — в `.env.example`.

### Приёмка

`ACCEPT_AUTOFORMAT`, `ACCEPT_LSP`, `ACCEPT_TEST_CMD`.

### Прочее

`LLM_PRICE_IN`, `LLM_PRICE_OUT`, `LLM_CURRENCY`, `LLM_ALWAYS_HEAVY`,
`LOG_DIR`, `AI_WORKSPACES`, `PORT`, `PARALLEL_TOOL_CALLS`,
`WEB_SEARCH_TIMEOUT`, `WEB_SEARCH_MAX_RESULTS`, `OLLAMA_STREAM_IDLE`.

> **Рекомендация:** добавить эти переменные в `.env.example` с
> комментарием-значением по умолчанию — иначе «убрать из `.env`» нельзя,
> нужно помнить про значение.

---

## LLM: выбор провайдера и модели

Ключи, base URL, список моделей и лимиты — в `providers.json`. Переменные ниже
задают только выбор по умолчанию; в Web UI модель переключается без перезапуска
(см. [Провайдеры](../10-getting-started/providers.md#выбор-модели-в-web-ui)).

| Переменная | Дефолт | Смысл |
|---|---|---|
| `LLM_PROVIDER` | авто | `ollama` / `yandex` / `trim` / `reg` |
| `MODEL` | `default_model` из providers.json | Основная модель выбранного провайдера |
| `MODEL_LARGE` | `large_model` из providers.json | Крупная модель для «тяжёлых» агентов |
| `PROVIDERS_CONFIG` | поиск вверх от cwd | Путь к providers.json |
| `LLM_DEBUG` | выкл | Лог запросов и ответов провайдера |
| `LLM_ALWAYS_HEAVY` | выкл | Всегда слать в крупную модель (для тестов маршрутизации) |
| `PARALLEL_TOOL_CALLS` | **вкл** | Параллельное выполнение read-only инструментов в раунде |
| `PARALLEL_TOOL_MAX` | 8 | Ширина параллельной волны (0 = без лимита) |

Двухуровневая маршрутизация: обычные запросы идут в основную модель, тяжёлые
задачи (архитектура, ревью) — в крупную. Рабочая модель всегда видна в логах:
`server: LLM <провайдер>/<модель> (large=<модель>)` в `logs/server.log` и
`[llm] модель запуска: …` в `logs/<проект>.log`.

## Ollama

| Переменная | Дефолт | Смысл |
|---|---|---|
| `OLLAMA_THINK` | из `settings.think_tokens` | 0/1 — принудительное выключение/включение thinking |
| `OLLAMA_STREAM_IDLE` | — | Таймаут простоя стрима |
| `OLLAMA_TOOL_RETRIES` | 2 | Ретраи оборванного JSON tool-вызова |
| `OLLAMA_TOOL_RETRY_DELAY` | 1000 мс | Пауза между ретраями |

> Модель, контекстное окно, лимит выхода и бюджет thinking задаются в
> `providers.json` → `models.<провайдер>`: `default_model`, `large_model`,
> `settings.input_tokens`, `settings.output_tokens`, `settings.think_tokens`.
> Переменные вида `OLLAMA_MODEL` / `*_INPUT_TOKENS` / `*_THINK_TOKENS` больше не
> читаются — после перехода на providers.json они молча игнорировались.
> `OLLAMA_HOST` / `OLLAMA_KEEP_ALIVE` — параметры самого Ollama, не приложения.
> `OLLAMA_NUM_CTX` / `OLLAMA_MAX_TOKENS` больше не читаются.

## YandexGPT, Trim, Reg Cloud

У всех трёх провайдеров настройки живут в `providers.json` (`base_url`,
`api_key`, `models`, `default_model`, `settings`), а переменные окружения
`YANDEX_API_KEY` / `YANDEX_FOLDER_ID` / `YANDEX_MODEL` / `TRIM_*` / `REG_*`
больше **не читаются** — они остались от конфигурации до providers.json и
молча ничего не делают.

| Провайдер | Обязательное в конфиге | Типичные лимиты |
|---|---|---|
| `yandex` | `base_url`, `api_key`, `folder_id`, `model_prefix` | вход 16000, выход 8000 |
| `trim` | `base_url`, `api_key` | вход 16000, выход 4000 |
| `reg` | `base_url`, `api_key` | вход 262144, выход 32768 |

Лимиты — в `settings` (`input_tokens`, `output_tokens`, `think_tokens`).
Бюджет thinking переводится в `reasoning_effort`: ≤2k → `low`, ≤8k → `medium`,
≤12k → `high`, больше → `max`.

Нюансы реализации:

- **Yandex** — на первом раунде с `WriteFiles` бюджет выхода автоматически
  повышается до 16000 токенов (иначе обрезается JSON со всеми файлами);
- **Trim** — не поддерживает разбиение диффа на части (`noChunk`), держите
  `REVIEW_MAX_DIFF_SIZE` небольшим;
- **Reg** — модель по умолчанию `qwen-3.8-27b`.

## Цены токенов

| Переменная | Дефолт | Смысл |
|---|---|---|
| `LLM_PRICE_IN` | не задана | Цена 1M входных токенов |
| `LLM_PRICE_OUT` | не задана | Цена 1M выходных токенов |
| `LLM_CURRENCY` | `у.е.` | Подпись валюты |

Без цен `Pricing.Known() == false` — UI показывает токены без денежной
оценки. Подробнее — [Токены и метрики](../20-features/tokens-and-metrics.md).

---

## Агенты: общие

| Переменная | Дефолт | Смысл |
|---|---|---|
| `CODEGEN_LANG` | `Go` | Целевой язык генерации |
| `CODEGEN_MODULE` | пусто (модель придумывает) | Имя модуля `go.mod` |
| `CODEGEN_MAX_FILES` | 0 (без лимита) | Макс. файлов за запуск |
| `CODEGEN_NO_OVERWRITE` | `false` | Запрет перезаписи существующих файлов |
| `CODEGEN_SUMMARY_FILE` | `SUMMARY.md` | Файл отчёта после генерации |
| `CODEGEN_PARALLEL` | **вкл** | Параллельное выполнение шагов в волне |
| `CODEGEN_ROLLBACK_ON_FAIL` | **вкл** | Откат скоупа шага при ошибке |
| `CODEGEN_CONTRACT_ENFORCE` | снимок берётся | `=1` повышает уровень до предупреждения |
| `CODEGEN_RUN_TIMEOUT` | `60s` | Таймаут команды `Run` |
| `CODEGEN_RUN_MAX_OUTPUT` | 20000 | Лимит символов вывода `Run` |
| `CODEGEN_READ_MAX_FILE` | 100000 | Лимит символов на файл |
| `CODEGEN_READ_MAX_TOTAL` | 800000 | Лимит символов за `ReadFiles` |
| `CODEGEN_SKELETON_CACHE` | вкл | Кеш сигнатур без тел |

### По ролям

| Переменная | Смысл |
|---|---|
| `BACKEND_PLAN_FILE` | Файл плана backend-лида |
| `BACKEND_MAX_FILES` | Лимит файлов для backend |
| `BACKEND_NO_OVERWRITE` | Запрет перезаписи (backend) |
| `FRONTEND_PLAN_FILE` | Файл плана frontend-лида |
| `FRONTEND_MAX_FILES` | Лимит файлов (frontend) |
| `FRONTEND_NO_OVERWRITE` | Запрет перезаписи (frontend) |
| `DEVOPS_MAX_FILES` | Лимит файлов (devops) |
| `DEVOPS_NO_OVERWRITE` | Запрет перезаписи (devops) |
| `QA_REPORT_FILE` | `TEST_REPORT.md` |
| `QA_MAX_FILES` | Лимит файлов (QA) |
| `QA_NO_OVERWRITE` | Запрет перезаписи (QA) |

## Сжатие контекста

| Переменная | Дефолт | Смысл |
|---|---|---|
| `CODEGEN_HISTORY_BUDGET` | 0 (выкл) | Бюджет истории в символах |
| `CODEGEN_HISTORY_TOKENS` | 0 (выкл) | Бюджет по фактическому usage (Ф-11) |
| `CODEGEN_HISTORY_NOTICE` | выкл | Ф-6: памятка модели о сжатии |
| `CODEGEN_HISTORY_EVICT` | выкл | Ф-7: вытеснение в RAG-эпизоды |
| `CODEGEN_HISTORY_RANK` | выкл | Ф-8: семантический отбор кандидатов |
| `CODEGEN_HISTORY_OUTLINE` | выкл | Ф-9: LSP-оглавления убранных файлов |
| `CODEGEN_HISTORY_COMPACT` | выкл | Ф-10: компакция моделью |

## Песочница

| Переменная | Дефолт | Смысл |
|---|---|---|
| `CODEGEN_SANDBOX` | пусто (хост) | `container` / `auto` / пусто |
| `CODEGEN_SANDBOX_IMAGE` | по стеку | Образ |
| `CODEGEN_IMAGE` | — | Legacy-алиас образа |
| `CODEGEN_SANDBOX_NETWORK` | `default` | `default` / `none` / `bridge` |
| `CODEGEN_SANDBOX_MEMORY` | `2g` | `--memory` |
| `CODEGEN_SANDBOX_CPUS` | `2` | `--cpus` |
| `CODEGEN_SANDBOX_RO` | `false` | Read-only корень контейнера |
| `CODEGEN_SANDBOX_ALLOW_WRITE` | `true` | Монтирование проекта `:rw` |
| `CODEGEN_SANDBOX_DOCKER` | из `PATH` | Путь к docker |
| `CODEGEN_SANDBOX_NET` | — | Legacy-алиас сети |
| `CODEGEN_MEMORY` / `CODEGEN_CPUS` | — | Legacy-алиасы лимитов |
| `CODEGEN_DOCKER_HOST` | `DOCKER_HOST` | Сокет демона |
| `CODEGEN_DOCKER_TLS` | `DOCKER_TLS_VERIFY` | TLS |
| `CODEGEN_DOCKER_CERT` | `DOCKER_CERT_PATH` | Сертификаты |
| `CODEGEN_SANDBOX_BIN` | `PATH_SANDBOX` | `PATH` для дочернего compose |
| `CODEGEN_SANDBOX_USER` | `SANDBOX_UID` | UID для сборки образа |

---

## Приёмка (`ACCEPT_*`)

| Переменная | Дефолт | Смысл |
|---|---|---|
| `ACCEPT_BUILD_CMD` | авто | Команда сборки |
| `ACCEPT_RUN_CMD` | авто | Команда запуска |
| `ACCEPT_TEST_CMD` | авто | Команда тестов |
| `ACCEPT_FORMAT_CMD` | авто | Проверка стилизатора |
| `ACCEPT_ANALYZE_CMD` | авто | Проверка анализатора |
| `ACCEPT_INSTALL_CMD` | авто | Установка зависимостей |
| `ACCEPT_INSTALL_DEPS` | `true` | Устанавливать зависимости |
| `ACCEPT_INSTALL_TIMEOUT` | `5m` | Таймаут установки |
| `ACCEPT_FORMAT` | `true` | Проверять формат (предупреждения) |
| `ACCEPT_AUTOFORMAT` | `true` | Авто-починка `gofmt -w` |
| `ACCEPT_ANALYZE` | `true` | Проверять анализатор (ошибки) |
| `ACCEPT_LSP` | `true` | LSP-стадия |
| `ACCEPT_BUILD_TIMEOUT` | `2m` | Таймаут сборки |
| `ACCEPT_RUN_TIMEOUT` | `10s` | Время работы приложения |
| `ACCEPT_MAX_ROUNDS` | 3 | Циклов «приёмка → исправление» (≤ 0 — без цикла) |
| `ACCEPT_MAX_LOG` | 6000 | Лимит символов лога в отчёте |

Приоритет: env `ACCEPT_*_CMD` → make-цель → автодетект по стеку. Подробнее —
[Makefile-контракт](../20-features/makefile-contract.md).

## Планировщик

| Переменная | Дефолт | Смысл |
|---|---|---|
| `REDIS_ADDR` | `localhost:56379` | Адрес Redis для чекпоинтов |
| `REDIS_PASSWORD` | — | Пароль |
| `REDIS_DB` | 0 | Номер БД |
| `PLAN_CHECKPOINT` | выкл | Явное включение чекпоинтов |
| `PLAN_CHECKPOINT_TTL` | 0 (бессрочно) | TTL чекпоинтов |
| `PLAN_RESUME` | выкл | Продолжить с последнего чекпоинта |
| `BOARD_REDIS_ADDR` | — | Отдельный Redis доски |
| `BOARD_REDIS_DB` | 0 | БД доски |
| `BOARD_REDIS_PASSWORD` | — | Пароль доски |
| `BOARD_TTL` | 0 (бессрочно) | TTL записей доски |

> **Чекпоинты включаются автоматически**, если `REDIS_ADDR` задан явно, либо
> `PLAN_CHECKPOINT=1`, либо передан `--resume` / `PLAN_RESUME=1`
> (`main.go:683-684`).

---

## RAG

| Переменная | Дефолт | Смысл |
|---|---|---|
| `QDRANT_ADDR` | — | **gRPC**-адрес Qdrant |
| `QDRANT_COLLECTION_NAME` | `project_code_base` | Коллекция |
| `EMBEDDING_MODEL` | — | Модель эмбеддингов в Ollama |
| `RAG_MAX_RESULTS` | 3 | Чанков в выдаче |
| `RAG_READ_MAX_TOTAL` | 150000 | Суммарных символов сниппетов |
| `RAG_SEARCH_TIMEOUT` | `30s` | Таймаут цикла Ping+embed+query |
| `RAG_PLANNER_CONTEXT` | вкл | RAG-контекст планировщика |
| `RAG_ARCHITECT_CONTEXT` | вкл | RAG-контекст архитектора |
| `RAG_ASSISTANT_CONTEXT` | вкл | RAG-контекст ассистента |
| `RAG_AUTO_REINDEX` | выкл | Автопереиндексация после мутаций |

## LSP

| Переменная | Дефолт | Смысл |
|---|---|---|
| `LSP_SERVER` | авто по стеку | Готовая команда запуска сервера |
| `LSP_NATIVE` | вкл | Нативные `publishDiagnostics` |
| `LSP_TIMEOUT` | 15 с | Таймаут запроса (число = секунды) |
| `LSP_DIAG_WAIT` | `3s` | Верхняя граница ожидания диагностик |
| `LSP_IDLE_TTL` | 10 мин | Эвикция простаивающего сервера |
| `LSP_MAX_DIAGS` | 30 | Строк диагностик |
| `LSP_MAX_LOCATIONS` | 50 | Позиций definition/references |
| `LSP_MAX_OUTPUT` | 4000 | Символов вывода |
| `LSP_BIN_PATH` | — | Доп. каталоги поиска бинарников |
| `LSP_STEP_GATE` | вкл | Scope-гейт по шагу плана |
| `LSP_AUTO_FIX` | вкл | Авто-починка по диагностикам |
| `LSP_MAX_FIX_ROUNDS` | 3 | Раундов автопочинки |

> `GO_LSP_HELPER` — **не production**: флаг фейкового сервера в тестах.

## Web-поиск

| Переменная | Дефолт | Смысл |
|---|---|---|
| `WEB_SEARCH_TIMEOUT` | — | Таймаут запроса |
| `WEB_SEARCH_MAX_RESULTS` | — | Макс. результатов |

## Код-ревью

| Переменная | Дефолт | Смысл |
|---|---|---|
| `REVIEW_MAX_COMMENTS` | 10 | Замечаний за ревью (0 = без лимита) |
| `REVIEW_CRITICAL_ONLY` | `true` | Только критические |
| `REVIEW_BLOCK_ON_CRITICAL` | `true` | Блокировать апрув |
| `REVIEW_SKIP_GENERATED` | `true` | Отсекать сгенерированные/бинарные |
| `REVIEW_MAX_DIFF_SIZE` | 300000 | Порог обрезания диффа, байт |
| `REVIEW_CHUNK_SIZE` | 14000 | Порог разбиения на части |
| `REVIEW_MAX_ROUNDS` | 3 | Раундов модель→инструменты |
| `REVIEW_FIX_ROUNDS` | 3 | Циклов «ревью → исправление» |
| `REVIEW_TIMEOUT` | 10m | Таймаут всего цикла |

> **Расхождение с ранними планами:** там `REVIEW_MAX_ROUNDS=12`. В коде
> **3** (`agents/codereviewer/config.go:48`). Задайте явно, если нужен
> длинный разбор.

> `REVIEW_TIMEOUT` по умолчанию заведомо больше таймаута одного
> HTTP-запроса провайдера (5 минут) — для большого ревью ставьте 15m.

## Слушатель MR

| Переменная | Дефолт | Смысл |
|---|---|---|
| `GITLAB_URL` | — | Адрес GitLab (без слэша) |
| `GITLAB_TOKEN` | — | Токен, scope `api` |
| `GITLAB_PROJECTS` | — | ID проектов через запятую |
| `GITHUB_TOKEN` | — | Токен, scope `repo` |
| `DB_FILENAME` | `discovered_mrs.txt` | Файл-база известных MR |
| `POLL_INTERVAL` | 30s | Период опроса |

## Git

| Переменная | Дефолт | Смысл |
|---|---|---|
| `GITOPS_SUBMODULES` | вкл (кроме `0`) | Инициализация сабмодулей |
| `GITOPS_SUBMODULE_DEPTH` | полная история | Shallow-глубина (положительное целое) |

> `GIT_ALLOW_PROTOCOL` в Go-коде **не читается** — это переменная самого git.

## Web UI и логи

| Переменная | Дефолт | Смысл |
|---|---|---|
| `AI_WEB_ADDR` | `127.0.0.1:8090` | Адрес HTTP-сервера |
| `AI_WEB_PASSWORD` | пусто (без защиты) | Пароль входа |
| `AI_WORKSPACES` | `$HOME/.ai-workspaces.json` | Реестр проектов |
| `LOG_DIR` | — | Каталог лог-файлов |
| `APP_LOG_AUTO_FEED` | вкл | Подмешивание логов в промпт |
| `APP_LOG_MAX_FEED_ROUNDS` | 2 | Повторов подмешивания за цикл |
| `PORT` | — | Порт (альтернатива `AI_WEB_ADDR`) |

> **Лимиты заданы константами, а не переменными** (`server/server.go:116-118`):
> 5 попыток входа в минуту, 120 запросов `/api/*` в минуту, 30 сообщений
> в чат в минуту.

## Служебные

| Переменная | Дефолт | Смысл |
|---|---|---|
| `GOBIN`, `GOPATH` | — | Поиск бинарников LSP |
| `PATH` | — | Поиск бинарников и команд |

---

## Минимальный рабочий набор

```bash
# Провайдер и модель (ключи/base_url/лимиты — в providers.json)
LLM_PROVIDER=ollama
MODEL=qwen2.5-coder:14b
MODEL_LARGE=qwen2.5-coder:32b

# Инфраструктура
QDRANT_ADDR=localhost:56334
EMBEDDING_MODEL=nomic-embed-text

# Web UI
AI_WEB_ADDR=127.0.0.1:8090
AI_WEB_PASSWORD=...
```

Остальное работает с дефолтами.

## Проверить, что переменная читается

```bash
grep -rn --include="*.go" "Getenv(\"ИМЯ\"\|env.*(\"ИМЯ\"" .
```

## Добавить переменную

1. Объявить имя константой рядом с местом чтения (как `reindexOnEnv`).
2. Разобрать значение **с дефолтом** — пустое значение не должно ломать
   поведение.
3. Задокументировать в `.env.example` с комментарием-дефолтом.
4. Добавить в [этот справочник](environment-variables.md) и в доменный
   документ.
5. Покрыть разбор тестом (паттерн `config_test.go`).

## Связанное

- [Конфигурация](../10-getting-started/configuration.md) — приоритеты, примеры
- [Провайдеры](../10-getting-started/providers.md)
- [Фичи и домены](../20-features/README.md) — карта «переменная → домен»
- [Инфраструктура](infrastructure.md)
