package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"
	"github.com/tmc/langchaingo/agents"
	"github.com/tmc/langchaingo/llms/ollama"
	"github.com/tmc/langchaingo/tools"
)

// 1. Создаем инструмент (Tool), чтобы агент мог физически записывать код в файлы
type FileWriterTool struct{}

func (f FileWriterTool) Name() string { return "FileWriter" }
func (f FileWriterTool) Description() string {
	return "Используй этот инструмент для сохранения написанного кода в файл. " +
		"Входной формат — JSON-строка строго в виде: {\"filename\":\"main.go\", \"content\":\"код внутри\"}"
}

func (f FileWriterTool) Call(ctx context.Context, input string) (string, error) {
	var params map[string]string
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("ошибка парсинга JSON: %v. Убедись, что передаешь валидный JSON", err)
	}

	filename := params["filename"]
	content := params["content"]

	if filename == "" || content == "" {
		return "Ошибка: имя файла или контент пусты", nil
	}

	// Записываем код на диск
	err := os.WriteFile(filename, []byte(content), 0644)
	if err != nil {
		return fmt.Sprintf("Не удалось записать файл: %v", err), nil
	}

	return fmt.Sprintf("Файл %s успешно создан и в него записан код.", filename), nil
}

func main() {
	ctx := context.Background()

	// Load the .env file
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	llmModelName := os.Getenv("LLM")

	// 2. Подключаемся к Ollama внутри Docker.
	// Если запускаете код на хосте (вне Docker), укажите адрес вашей Ollama: http://localhost:11434
	llm, err := ollama.New(
		ollama.WithServerURL("http://localhost:11434"),
		ollama.WithModel(llmModelName),
	)
	if err != nil {
		log.Fatalf("Ошибка инициализации Ollama: %v", err)
	}

	// 3. Передаем агенту доступные инструменты
	myTools := []tools.Tool{
		FileWriterTool{},
	}

	// 4. Инициализируем структурированного чат-агента
	agent := agents.NewOneShotAgent(
		llm,
		myTools,
		agents.WithMaxIterations(5), // Ограничиваем цепочку размышлений агента
	)

	// 5. Create the Executor loop to handle the agent lifecycle
	executor := agents.NewExecutor(agent)

	// 6. Ставим задачу агенту
	prompt := "Напиши простой HTTP-сервер на Go, который на порту 8080 отдает фразу 'Hello from AI Agent!'. " +
		"Сохрани этот рабочий код в файл с именем server.go."

	fmt.Printf("[Пользователь]: %s\n\n", prompt)
	fmt.Println("[Агент начинает думать...]")

	result, err := executor.Call(ctx, map[string]any{
		"input": prompt,
	})
	if err != nil {
		log.Fatalf("Ошибка выполнения задачи агентом: %v", err)
	}

	// 6. Print the final answer derived by the agent
	fmt.Printf("\n[Итог работы агента]:\n%s\n", result["output"])
}
