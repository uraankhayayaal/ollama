package main

import (
	"ai/agents"
	"ai/agents/codegenerator"
	"ai/agents/codereviewer"
	"ai/models"
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Load the .env file
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	providerType := os.Getenv("LLM_PROVIDER") // "ollama" или "yandex"

	var provider models.LLMProvider

	switch providerType {
	case "ollama":
		model := os.Getenv("OLLAMA_MODEL") // например, "llama3"
		if model == "" {
			model = "llama3"
		}
		provider, err = models.NewOllamaProvider(model)

	case "yandex":
		provider = models.NewAlisaProvider()

	default:
		log.Fatalf("Unknown provider: %s. Use 'ollama' or 'yandex'", providerType)
	}

	if err != nil {
		log.Fatalf("Failed to init provider: %v", err)
	}

	// Выбор агента по первому аргументу: go run . generate <промпт> | review <URL>
	agentName, agentArgs := agentCommand(os.Args)

	var agent agents.Agent
	switch agentName {
	case "generate":
		prompt := defaultPrompt(agentArgs)
		agent = codegenerator.NewCodegenerator(prompt)
	case "review":
		agent = codereviewer.NewCodereviewer(agentArgs)
	default:
		log.Fatalf("Неизвестный агент %q. Используйте 'go run . generate <промпт>' или 'go run . review <URL>'", agentName)
	}

	resp, err := provider.Generate(ctx, agent)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	fmt.Println("Response Message:", resp.Content)
	fmt.Println("Response Tools:", resp.ToolCalls)
}

// defaultPrompt возвращает текст задания для генератора. Если промпт не
// передан в командной строке, используется заданное по умолчанию значение.
func defaultPrompt(args []string) string {
	if len(args) >= 1 && args[0] != "" {
		return args[0]
	}
	return "Напиши микросервис для расчета квадратного уровнения, придумай формат аргументов для передачи в код."
}

// agentCommand возвращает имя агента (первый аргумент) и оставшиеся
// аргументы, переданные ему.
func agentCommand(args []string) (name string, rest []string) {
	if len(args) < 2 {
		return "", nil
	}
	return args[1], args[2:]
}
