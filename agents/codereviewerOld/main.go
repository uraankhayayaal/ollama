package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"

	"github.com/joho/godotenv"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/ollama"
)

// GitLabConfig хранит параметры подключения, извлеченные из URL
type GitLabConfig struct {
	BaseURL string
	Token   string
	ProjID  string
	MRIID   string
}

func main() {
	// Load the .env file
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	llmModelName := os.Getenv("LLM")

	if len(os.Args) < 2 {
		log.Fatalf("Использование: go run codereviewer.go <ссылка_на_gitlab_mr>")
	}
	mrURL := os.Args[1]

	token := os.Getenv("GITLAB_TOKEN")
	if token == "" {
		log.Fatal("Ошибка: Переменная окружения GITLAB_TOKEN не установлена")
	}

	ctx := context.Background()

	// Инициализируем модель Qwen через локальный Ollama
	// По умолчанию langchaingo стучится на http://localhost:11434
	llm, err := ollama.New(
		ollama.WithModel(llmModelName), // Укажите тег модели, которую вы скачали в Ollama
	)
	if err != nil {
		log.Fatalf("Ошибка инициализации локального Ollama: %v", err)
	}

	// Парсинг ссылки на MR
	config, err := parseGitLabURL(mrURL, token)
	if err != nil {
		log.Fatalf("Ошибка парсинга URL: %v", err)
	}

	// 1. Создаем инструменты (Tools) для LLM
	tools := []llms.Tool{
		{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        "review_comments",
				Description: "Оставить комментарий к строке кода в Merge Request через GitLab API",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file_path": map[string]any{"type": "string", "description": "Путь к файлу, например 'main.go'"},
						"line":      map[string]any{"type": "integer", "description": "Номер строки в новой версии файла, к которой относится комментарий"},
						"comment":   map[string]any{"type": "string", "description": "Текст рекомендации или описание ошибки и решения"},
					},
					"required": []string{"file_path", "line", "comment"},
				},
				Strict: true,
			},
		},
		{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        "approve_mr",
				Description: "Поставить апрув (approve) к Merge Request, если изменения не критичны и не ломают систему",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"is_approved": map[string]any{"type": "bool", "description": "Флаг true - апрув"},
					},
					"required": []string{"is_approved"},
				},
				Strict: true,
			},
		},
	}

	// Формируем системный промпт для агента
	systemPrompt := "Ты — опытный ведущий разработчик. Твоя задача — провести ревью изменений кода. " +
		"Сначала внимательно изучи diff. Если найдешь баги, проблемы безопасности или архитектурные дефекты, оставь комментарии к конкретным строкам, номер строки вычисли из входных данных, используя иннструмент 'review_comments'. " +
		"Если критических багов и ломающих изменений нет (или все замечания носят характер мелких улучшений), обязательно вызови интсрумент 'approve_mr'. "

	diff, err := getMRDiff(config)

	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt),
		llms.TextParts(llms.ChatMessageTypeHuman, "Начни код-ревью для текущего MR и выбери инструменты."),
		llms.TextParts(llms.ChatMessageTypeHuman, diff),
	}

	fmt.Println("🚀 Инициализация локального ИИ-агента (Qwen)...")

	// Главный цикл работы агента (LLM + Инструменты)
	for {
		fmt.Println("Цикл обработки...")

		resp, err := llm.GenerateContent(ctx, messages, llms.WithTools(tools), llms.WithToolChoice("required"))
		if err != nil {
			log.Fatalf("Ошибка генерации контента моделью Qwen: %v", err)
		}

		choice := resp.Choices[0]

		// Если модель решила продолжить текстом и больше не вызывает функции, завершаем работу
		if len(choice.ToolCalls) == 0 {
			fmt.Printf("\n🤖 Финальный ответ агента:\n%s\n", choice.Content)
			break
		}

		// Добавляем ответ модели в историю сообщений
		messages = append(messages, llms.MessageContent{
			Role:  llms.ChatMessageTypeAI,
			Parts: []llms.ContentPart{llms.TextPart(choice.Content)},
		})

		// Обрабатываем каждый вызов инструмента от LLM
		for _, toolCall := range choice.ToolCalls {
			var resultStr string
			fmt.Printf("🛠️ Агент вызывает инструмент: %s\n", toolCall.FunctionCall.Name)

			switch toolCall.FunctionCall.Name {

			case "review_comments":
				var args struct {
					FilePath string `json:"file_path"`
					Line     int    `json:"line"`
					Comment  string `json:"comment"`
				}
				if err := json.Unmarshal([]byte(toolCall.FunctionCall.Arguments), &args); err != nil {
					resultStr = fmt.Sprintf("Ошибка парсинга аргументов: %v", err)
				} else {
					err := postComment(config, args.FilePath, args.Line, args.Comment)
					if err != nil {
						resultStr = fmt.Sprintf("Ошибка отправки комментария: %v", err)
					} else {
						resultStr = "Комментарий успешно отправлен"
						fmt.Printf("💬 Оставлен комментарий в %s:%d\n", args.FilePath, args.Line)
					}
				}

			case "approve_mr":
				err := approveMR(config)
				if err != nil {
					resultStr = fmt.Sprintf("Ошибка постановки апрува: %v", err)
				} else {
					resultStr = "МР успешно одобрен (Approved)"
					fmt.Println("💚 Выставлен Approve для Merge Request!")
				}
			}

			// Возвращаем результат выполнения инструмента обратно в контекст LLM
			messages = append(messages, llms.MessageContent{
				Role: llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{
					llms.ToolCallResponse{
						ToolCallID: toolCall.ID,
						Name:       toolCall.FunctionCall.Name,
						Content:    resultStr,
					},
				},
			})
		}
	}
}

// === Реализация API-инструментов GitLab ===

func getMRDiff(c *GitLabConfig) (string, error) {
	urlStr := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s/diffs", c.BaseURL, c.ProjID, c.MRIID)
	req, _ := http.NewRequest("GET", urlStr, nil)
	req.Header.Set("PRIVATE-TOKEN", c.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("статус %d: %s", resp.StatusCode, string(body))
	}

	return string(body), nil
}

func postComment(c *GitLabConfig, filePath string, line int, comment string) error {
	urlStr := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s/discussions", c.BaseURL, c.ProjID, c.MRIID)

	baseSHA, startSHA, headSHA, err := getMRVersions(c)
	if err != nil {
		return fmt.Errorf("не удалось получить SHA коммитов: %v", err)
	}

	payload := map[string]any{
		"body": comment,
		"position": map[string]any{
			"base_sha":      baseSHA,
			"start_sha":     startSHA,
			"head_sha":      headSHA,
			"position_type": "text",
			"new_path":      filePath,
			"new_line":      line,
		},
	}

	jsonValue, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", urlStr, bytes.NewBuffer(jsonValue))
	req.Header.Set("PRIVATE-TOKEN", c.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("статус %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func approveMR(c *GitLabConfig) error {
	urlStr := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s/approve", c.BaseURL, c.ProjID, c.MRIID)
	req, _ := http.NewRequest("POST", urlStr, nil)
	req.Header.Set("PRIVATE-TOKEN", c.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("статус %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func getMRVersions(c *GitLabConfig) (string, string, string, error) {
	urlStr := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s", c.BaseURL, c.ProjID, c.MRIID)
	req, _ := http.NewRequest("GET", urlStr, nil)
	req.Header.Set("PRIVATE-TOKEN", c.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()

	var data struct {
		DiffRefs struct {
			BaseSha  string `json:"base_sha"`
			StartSha string `json:"start_sha"`
			HeadSha  string `json:"head_sha"`
		} `json:"diff_refs"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", "", "", err
	}
	return data.DiffRefs.BaseSha, data.DiffRefs.StartSha, data.DiffRefs.HeadSha, nil
}

func parseGitLabURL(mrURL string, token string) (*GitLabConfig, error) {
	u, err := url.Parse(mrURL)
	if err != nil {
		return nil, err
	}

	baseURL := fmt.Sprintf("%s://%s", u.Scheme, u.Host)

	re := regexp.MustCompile(`/(.+)/-/merge_requests/(\d+)`)
	matches := re.FindStringSubmatch(u.Path)
	if len(matches) < 3 {
		return nil, fmt.Errorf("неверный формат URL GitLab Merge Request")
	}

	projectPath := matches[1]
	mrIID := matches[2]
	encodedProjID := url.QueryEscape(projectPath)

	return &GitLabConfig{
		BaseURL: baseURL,
		Token:   token,
		ProjID:  encodedProjID,
		MRIID:   mrIID,
	}, nil
}
