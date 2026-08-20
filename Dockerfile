# Этап 1: Загрузка модели во временный контейнер
FROM ollama/ollama:latest AS preloader

# Запускаем сервер Ollama в фоне, ждем инициализации и скачиваем модель
RUN nohup ollama serve > /tmp/ollama.log 2>&1 & \
    sleep 5 && \
    ollama pull qwen3-coder:30b

# Этап 2: Финальный чистый образ
FROM ollama/ollama:latest

# Копируем скачанную модель из первого этапа в постоянный кэш финального образа
COPY --from=preloader /root/.ollama /root/.ollama

# Открываем порт и запускаем стандартный сервер
EXPOSE 11434
ENTRYPOINT ["/bin/ollama"]
CMD ["serve"]
