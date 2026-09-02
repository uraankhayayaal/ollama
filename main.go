package main

import (
	"ai/agents"
	"ai/agents/codegenerator"
	"ai/agents/codereviewer"
	// Blank-import регистрирует все встроенные провайдеры систем ревью
	// (init() в forges/github и forges/gitlab) в фабрике forges.New.
	"ai/forges"
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

	// 5. Цикл генерации/само-ревью для агента-генератора: после первого
	// прогона код проверяется локально (LocalForge), найденные замечания
	// передаются модели, и она исправляет код в той же OutputDir.
	if sr, ok := agent.(selfReviewer); ok {
		doSelfReview(ctx, provider, sr, "", defaultPrompt(agentArgs))
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

	// 8. Финальный отчёт агента (например, SUMMARY.md у генератора кода).
	if r, ok := agent.(finalizer); ok {
		r.Finalize()
	}

	fmt.Println("Response Message:", resp.Content)
	fmt.Println("Response Tools:", resp.ToolCalls)
}

// reviewParser — опциональный интерфейс агента, умеющего опубликовать ревью,
// которое модель написала текстом (без вызова инструментов).
type reviewParser interface {
	PublishParsedReview(content string) int
}

// selfReviewer — опциональный интерфейс агента-генератора, поддерживающего
// цикл self-repair: после первой генерации код прогоняется через локальное
// ревью, а найденные замечания передаются модели для исправления.
type selfReviewer interface {
	// SelfReviewDir возвращает путь к сгенерированному коду для ревью.
	SelfReviewDir() string
	// NewReviewAgentFor создаёт агента код-ревью для директории и возвращает
	// и агента, и его forge (чтобы извлечь собранные замечания).
	NewReviewAgentFor(dir string, focus string) (agents.Agent, forges.Forge, error)
	// FixPromptFor собирает текст задания для исправления кода на основе
	// замечаний ревью. Первый аргумент — текущее задание, второй — замечания.
	FixPromptFor(original string, comments []forges.ReviewComment) string
}

// summarizer — опциональный интерфейс агента, умеющего публиковать
// итоговую сводку ревью в тред MR/PR после завершения цикла.
type summarizer interface {
	PostSummaryToPR() error
}

// finalizer — опциональный интерфейс агента, выполняющего финализацию
// после завершения цикла (например, генератор кода пишет SUMMARY.md).
type finalizer interface {
	Finalize()
}

// defaultPrompt возвращает текст задания для генератора. Если промпт не
// передан в командной строке, используется заданное по умолчанию значение.
func defaultPrompt(args []string) string {
	if len(args) >= 1 && args[0] != "" {
		return args[0]
	}
	return "Напиши микросервис для расчета квадратного уровнения, придумай формат аргументов для передачи в код."
}

// doSelfReview запускает цикл self-repair для агента-генератора: генерация →
// локальное ревью сгенерированного кода → исправление по замечаниям ревью.
// focus — цель ревью (может быть "" для общего обзора).
func doSelfReview(ctx context.Context, provider models.LLMProvider, sr selfReviewer, focus string, originalPrompt string) {
	dir := sr.SelfReviewDir()
	log.Printf("Self-review: ревью кода в %s", dir)

	reviewAgent, forge, err := sr.NewReviewAgentFor(dir, focus)
	if err != nil {
		log.Printf("Self-review: не удалось создать агента ревью: %v", err)
		return
	}

	if _, err := provider.Generate(ctx, reviewAgent); err != nil {
		log.Printf("Self-review: ошибка ревью: %v", err)
	}

	lf, ok := forge.(*forges.LocalForge)
	if !ok {
		log.Printf("Self-review: неожиданный тип forge, пропускаю исправление")
		return
	}

	if len(lf.Published) == 0 {
		log.Println("Self-review: замечаний не найдено, исправление не требуется")
		return
	}

	fixPrompt := sr.FixPromptFor(originalPrompt, lf.Published)
	log.Printf("Self-review: найдено замечаний: %d. Запускаю исправление.", len(lf.Published))

	fixAgent, err := codegenerator.NewCodegeneratorInDir(fixPrompt, dir)
	if err != nil {
		log.Printf("Self-review: не удалось создать агента исправления: %v", err)
		return
	}

	if _, err := provider.Generate(ctx, fixAgent); err != nil {
		log.Printf("Self-review: ошибка на этапе исправления: %v", err)
	}
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
