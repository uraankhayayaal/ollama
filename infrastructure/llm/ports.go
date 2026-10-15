package llm

import "ai/domain/services"

// Compile-time проверки: пакет `infrastructure/llm` (бывший `models`) —
// реализация порта domain/services.LLMClient. Порт объявлен в домене
// (Этап 1), провайдеры ниже реализуют его без правок сигнатур.
//
// OpenAIProvider пока не покрыт: у него нет ChatStream (стриминг реализован
// только для Ollama). Добьём при переводе OpenAI-пути на порт.
var (
	_ services.LLMClient = (*OllamaProvider)(nil)
)
