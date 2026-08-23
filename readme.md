# Начало работы:
1. Запустить контейнеры `docker compose up`
1. Загрузить модель `docker compose exec -it ollama ollama pull qwen3-coder:30b`
1. Загрузить модель `docker compose exec -it ollama ollama pull qwen2.5-coder:7b`

# Запуск команд
1. Кодревью использовать агента `codereviewer := codereviewer.NewCodereviewer()` и выполнить команду `go run . --mr=https://gitlab.com/it-yakutia/botsad.ru/-/merge_requests/2`
1. Генератор кода использовать агента `codereviewer := codegenerator.NewCodegenerator()` и выполнить команду `go run .`