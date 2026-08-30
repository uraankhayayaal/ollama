# Начало работы:
1. Запустить контейнеры `docker compose up`
1. Загрузить модель `docker compose exec -it ollama ollama pull qwen3-coder:30b`
1. Загрузить модель `docker compose exec -it ollama ollama pull qwen2.5-coder:7b`

# Запуск команд
1. Кодревью использовать агента `codereviewer := codereviewer.NewCodereviewer()` и выполнить команду `go run . --mr=https://gitlab.com/it-yakutia/botsad.ru/-/merge_requests/2`
1. Генератор кода использовать агента `codereviewer := codegenerator.NewCodegenerator()` и выполнить команду `go run .`

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