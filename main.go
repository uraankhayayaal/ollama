package main

import (
	"ai/agents"
	"ai/agents/codegenerator"
	"ai/agents/codereviewer"
	// Blank-import регистрирует все встроенные провайдеры систем ревью
	// (init() в forges/github и forges/gitlab) в фабрике forges.New.
	_ "ai/forges/all"
	"ai/models"
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	// Load the .env file
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	// Таймаут цикла агента берётся из окружения REVIEW_TIMEOUT, иначе 5 минут.
	// Длительное ревью большого PR может требовать большего лимита.
	timeout := parseTimeout(os.Getenv("REVIEW_TIMEOUT"))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

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

	if resp.Truncated {
		log.Println("Внимание: цикл агента остановлен по лимиту раундов, результат может быть неполным")
	}

	// 6b. Запасной путь: если модель вернула ревью текстом, а не вызовами
	// инструментов (характерно для YandexGPT), а замечаний ещё не
	// опубликовано — пытаемся распарсить текст в комментарии и опубликовать.
	if r, ok := agent.(reviewParser); ok {
		n := r.PublishParsedReview(resp.Content)
		if n > 0 {
			log.Printf("Опубликовано замечаний из текстового ответа модели: %d", n)
		}
	}

	// 7. Итоговый отчёт-сводка в тред MR/PR, если агент его поддерживает.
	if r, ok := agent.(summarizer); ok {
		if serr := r.PostSummaryToPR(); serr != nil {
			log.Printf("Не удалось опубликовать итоговую сводку: %v", serr)
		}
	}

	fmt.Println("Response Message:", resp.Content)
	fmt.Println("Response Tools:", resp.ToolCalls)
}

// reviewParser — опциональный интерфейс агента, умеющего опубликовать ревью,
// которое модель написала текстом (без вызова инструментов).
type reviewParser interface {
	PublishParsedReview(content string) int
}

// summarizer — опциональный интерфейс агента, умеющего публиковать
// итоговую сводку ревью в тред MR/PR после завершения цикла.
type summarizer interface {
	PostSummaryToPR() error
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

// parseTimeout разбирает длительность таймаута из строки (например "10m").
// При пустой строке или ошибке разбора возвращается значение по умолчанию.
func parseTimeout(raw string) time.Duration {
	const def = 5 * time.Minute
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("Неверный REVIEW_TIMEOUT=%q, использую %v", raw, def)
		return def
	}
	return d
}
