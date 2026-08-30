package main

import (
	"ai/agents/codegenerator"
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

	// codereviewer := codereviewer.NewCodereviewer() // Кодревью
	codereviewer := codegenerator.NewCodegenerator() // Генератор кода

	resp, err := provider.Generate(ctx, codereviewer)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	fmt.Println("Response Message:", resp.Content)
	fmt.Println("Response Tools:", resp.ToolCalls)
}
