package ai

import (
	"ai/agents/codereviewer"
	"ai/models"
	"context"
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"
)

func main() {
	fmt.Println("Hello bro!")

	ctx := context.Background()

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

	codereviewer := codereviewer.NewCodereviewer()

	resp, err := provider.Generate(ctx, codereviewer)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	fmt.Printf("[Response] %s\n", resp.Content)
}
