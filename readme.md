# Начало работы:
1. Запустить контейнеры `docker compose up`
1. Загрузить модель `docker compose exec -it ollama ollama pull qwen3-coder:30b`
1. Загрузить модель `docker compose exec -it ollama ollama pull qwen2.5-coder:7b`

# Запуск команд

Агент выбирается через первый аргумент командной строки.

1. **Генератор кода** — каждое выполнение создаёт новый каталог `temp/gen_<дата>_<время>_<номер>/`. Текст задания передаётся первым аргументом (в кавычках):
   ```bash
   go run . generate "Напиши микросервис для расчета квадратного уровнения, придумай формат аргументов для передачи в код."
   ```
   Если промпт не указан, используется задание по умолчанию: `go run . generate`

   Возможности генератора:
   - **Инструменты** `WriteFile`/`WriteFiles` (запись файлов), `ReadFiles` (чтение), `DeleteFiles` (удаление) и `Run` (запуск `go build`/`go vet`/`go test` в OutputDir).
   - **Принудительный вызов инструмента**: в первом раунде модель обязана вызвать `WriteFiles` (а не ответить текстом). Если она всё же отвечает текстом, раннер подсказывает ей и повторяет запрос; для Yandex на первом шаге дополнительно форсируется `tool_choice` на этот инструмент.
   - **Самопроверка кода**: после записи агент сам запускает сборку и проверки, а если есть ошибки — исправляет код до «зелёного» состояния.
   - **Self-review (цикл repair)**: написанный код прогоняется через агента кодревью в локальном режиме (без сети), найденные замечания передаются модели для исправления, после чего SUMMARY записывается в файл.
   - В конце пишет `readme.md` с инструкцией запуска.
1. **Кодревью** MR — ссылку на Merge Request/Pull Request передаём напрямую. Второй необязательный аргумент — цель ревью (например «безопасность», «производительность»):
   ```bash
   go run . review <URL_MR>
   # пример:
   go run . review https://gitlab.com/it-yakutia/botsad.ru/-/merge_requests/2
   # с фокусом на безопасность:
   go run . review https://github.com/user/repo/pull/10 "безопасность"
   ```
   Репозиторий можно ревьюить и без сети, передав локальную директорию: `go run . review local:///путь/к/коду`.

Вспомогательные флаги:
- `LLM_DEBUG=1` — подробный лог запросов/ответов моделей: `LLM_DEBUG=1 go run . generate`
- `YANDEX_MAX_TOKENS=<число>` — лимит токенов ответа Yandex (по умолчанию 4000)
- `REVIEW_TIMEOUT=<длительность>` — таймаут всего цикла агента (например `15m`), по умолчанию `5m`
- `REVIEW_MAX_COMMENTS=<число>` — лимит замечаний за ревью (по умолчанию 10)
- `REVIEW_BLOCK_ON_CRITICAL=<true|false>` — блокировать апрув при критичных замечаниях (по умолчанию true)
- `REVIEW_SKIP_GENERATED=<true|false>` — отсекать сгенерированные/бинарные файлы (по умолчанию true)
- `REVIEW_CHUNK_SIZE=<число>` — дифф длиннее этого значения (в символах) ревьюится по частям, чтобы не переполнять контекст модели (по умолчанию 14000, 0 — без разбиения)
- `REVIEW_MAX_ROUNDS=<число>` — максимум циклов «модель→инструмент» за ревью (по умолчанию 12; для ревью больших диффов по частям может потребоваться больше)
- `CODEGEN_LANG=<язык>` — целевой язык программирования (по умолчанию `Go`)
- `CODEGEN_MODULE=<модуль>` — имя модуля Go для `go.mod` (пусто — модель придумывает сама)
- `CODEGEN_MAX_FILES=<число>` — максимум записываемых файлов за запуск (0 — без лимита)
- `CODEGEN_NO_OVERWRITE=<true|false>` — запрещать перезапись существующих файлов (по умолчанию false)
- `CODEGEN_SUMMARY_FILE=<имя>` — имя файла-отчёта после генерации (по умолчанию `SUMMARY.md`, пусто — отключить)

Автоматическая защита от галлюцинаций: перед публикацией каждое замечание проверяется по актуальному диффу — комментарии к файлам или номерам строк, которых нет в диффе, отсекаются и не публикуются (при этом не блокируют апрув и не считаются ошибками). Дубликаты замечаний к одной и той же строке публикуются один раз.

Если модель (например, YandexGPT) вместо вызова инструментов пишет ревью текстом, замечания автоматически распарсиваются из ответа и публикуются в MR/PR, с применением той же проверки по диффу.

Провайдер (ollama/yandex) и ключи задаются в файле `.env` (переменная `LLM_PROVIDER`).

----
Test tools call
```bash
curl http://localhost:11434/api/chat -d '{
  "model": "qwen3:8b",
  "stream": false,
  "messages": [
    { "role": "user", "content": "Какая погода в Москве?" }
  ],
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "get_current_weather",
        "description": "Получить текущую погоду для локации",
        "parameters": {
          "type": "object",
          "properties": {
            "location": { "type": "string", "description": "Город" }
          },
          "required": ["location"]
        }
      }
    }
  ]
}'
```

В Go для работы с Qdrant используется официальный клиент https://github.com/qdrant/go-client:
```bash
go get github.com/qdrant/go-client
```
Для RAG-систем всегда используйте gRPC порт (6334) вместо HTTP — это снижает задержки (latency) при поиске векторов в разы.
```go
package main

import (
	"context"
	"log"
	"time"

	"github.com/qdrant/go-client"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Подключаемся по gRPC
	client, err := qdrant.NewClient(&qdrant.Config{
		Host: "localhost",
		Port: 6334,
	})
	if err != nil {
		log.Fatalf("Ошибка подключения к Qdrant: %v", err)
	}
	defer client.Close()

	collectionName := "knowledge_base"

	// Создаем коллекцию (например, под эмбеддинги размерностью 1024)
	err = client.CreateCollection(ctx, &qdrant.CreateCollection{
		CollectionName: collectionName,
		VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{
			Size:     1024,                   // Укажите размерность вашей модели эмбеддингов
			Distance: qdrant.Distance_Cosine, // Косинусное сходство — стандарт для RAG
		}),
	})
	if err != nil {
		log.Printf("Коллекция уже существует или ошибка: %v", err)
	}
}

```