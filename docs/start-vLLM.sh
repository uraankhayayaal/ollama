#!/bin/bash

# Путь к виртуальному окружению и проекту
VENV_PATH="$HOME/app/models/vllm_env"
PORT=8000

echo "=== [1/3] Проверка портов ==="
if lsof -i :$PORT > /dev/null ; then
    echo "Освобождаем порт $PORT..."
    fuser -k -n tcp $PORT
    sleep 2
fi

echo "=== [2/3] Активация окружения ==="
source "$VENV_PATH/bin/activate"

echo "=== [3/3] Запуск vLLM (Контекст: 96К, Пиковая скорость: 150+ токенов/с) ==="
echo "------------------------------------------------------------------------"

# Запуск с вашими идеальными параметрами
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
   --port $PORT \
   --enable-auto-tool-choice \
   --tool-call-parser qwen3_coder