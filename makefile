# Загрузка модели qwen
docker compose exec -it ollama ollama pull qwen3-coder:8b

# Запуск модели qwen
docker compose run -it ollama ollama run qwen3-coder