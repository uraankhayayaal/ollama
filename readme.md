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