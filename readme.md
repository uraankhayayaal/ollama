# ai

Мультиагентная система автоматической разработки: агенты проектируют,
генерируют, собирают, тестируют и ревьюят приложения, ведут Kanban-доску и
работают с Merge Request'ами.

**Полная документация — [`docs/README.md`](docs/README.md).**

## Быстрый старт

```bash
# 1. Инфраструктура: Qdrant + Redis
docker compose up -d

# 2. Ollama ставится отдельно (в compose её нет)
brew install ollama && ollama serve      # macOS
ollama pull qwen3-coder:30b

# 3. Конфигурация
cp .env.example .env

# 4. Работать
go run . serve                             # Web UI на :8090
```

Подробно — [Быстрый старт](docs/10-getting-started/quickstart.md).

## Команды

| Команда | Что делает |
|---|---|
| `go run . serve` | Web UI: доска, чат, диффы, логи |
| `go run . plan <проект> <промпт>` | Декомпозиция и исполнение по волнам |
| `go run . kanban <проект> <промпт>` | Оркестрация на доске |
| `go run . backend\|frontend\|devops\|qa <проект> <промпт>` | Отдельный специалист |
| `go run . *lead <проект> <промпт>` | Декомпозиция под направление |
| `go run . accept <проект>` | Детерминированная приёмка без LLM |
| `go run . index <проект>` | Индексация проекта в Qdrant |
| `go run . review <URL_MR>` | Ревью Merge Request |
| `go run . listen` | Слушатель новых MR (GitLab) |

Полный список — [CLI](docs/30-reference/cli.md).

## Документация

| Раздел | Что внутри |
|---|---|
| [00-overview](docs/00-overview/) | Что это, архитектура, сквозной путь задачи, глоссарий |
| [10-getting-started](docs/10-getting-started/) | Быстрый старт, установка, конфигурация, провайдеры |
| [20-features](docs/20-features/) | Домены: как работают и как настраиваются |
| [30-reference](docs/30-reference/) | env, CLI, REST/WS, инструменты, инфраструктура |
| [40-operations](docs/40-operations/) | Разработка, тесты, добавление фичи, решение проблем |
| [plans](docs/plans/) | Архив планов реализации |

## Разработка

```bash
go build . ./agents/... ./tools/ ./board/
go vet   . ./agents/... ./tools/ ./board/
go test  . ./agents/... ./tools/ ./board/
npm --prefix web run build
```

> `go build ./...` падает на артефактах в `temp/` — там хранятся проекты,
> сгенерированные агентами, а не исходный код.

Подробнее — [Разработка](docs/40-operations/development.md).

## Про `temp/`

Каталог `temp/` — **рабочее хранилище проектов агентов**, а не исходный код.
Он игнорируется git. Не коммитьте его содержимое и не правьте файлы в нём как
в исходниках.

## Правила для агентов

Подробности — [`AGENTS.md`](AGENTS.md).


## Команды для запуска локальных моделей
Сценарий А (Самый стабильный и предсказуемый)
```bash
./build/bin/llama-server \
  --hf-repo unsloth/Qwen3.8-27B-GGUF \
  --hf-file Qwen3.8-27B-UD-Q4_K_M.gguf \
  -ngl all \
  --split-mode layer \
  -c 131072 \
  -fa on \
  --cache-type-k q4_0 \
  --cache-type-v q4_0 \
  -b 2048 \
  -ub 512 \
  -t 8 \
  -tb 8 \
  --port 8080 \
  --jinja \
  --image-min-tokens 1024 \
  --reasoning-preserve
```


Сценарий Б (Экспериментальный параллельный)
```bash
./build/bin/llama-server \
  --hf-repo unsloth/Qwen3.8-27B-GGUF \
  --hf-file Qwen3.8-27B-UD-Q4_K_M.gguf \
  -ngl all \
  --split-mode tensor \
  -c 131072 \
  -fa on \
  --cache-type-k q4_0 \
  --cache-type-v q4_0 \
  -b 2048 \
  -ub 1024 \
  -t 8 \
  -tb 8 \
  --port 8080 \
  --jinja \
  --image-min-tokens 1024 \
  --reasoning-preserve
```

### Шаг 3. Контроль работы и логирование

Теперь ваш ИИ-сервер работает незаметно в фоновом режиме как системный демон.
- Чтобы быстро проверить статус службы (активна ли она, сколько памяти потребляет) `sudo systemctl status llama-server.service` Используйте код с осторожностью.
- Чтобы открыть живой поток логов (вы увидите точно такие же строчки обработки задач и токенов, как в терминале) `journalctl -u llama-server.service -f -n 50` Используйте код с осторожностью.
- Если вам потребуется временно остановить или перезапустить сервер (например, для обновления исполняемого файла) `sudo systemctl stop llama-server.service` и`sudo systemctl restart llama-server.service` Используйте код с осторожностью. Фоновый демон настроен и автоматически поднимется при следующем включении вашей рабочей станции.
- Готовы ли вы перейти к настройке плагина в вашей IDE (VS Code, Cursor, JetBrains)? Напишите, какое именно расширение вы планируете использовать (например, Continue), и мы пропишем его конфигурацию для работы с вашим сервером на порту 8080.