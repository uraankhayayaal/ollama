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
	"strings"

	"github.com/joho/godotenv"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

func runConversation(client openai.Client, userInput string, yandexFolderID string, yandexModel string, mrURL string, token string) string {
	// Парсинг ссылки на MR
	config, err := parseGitLabURL(mrURL, token)
	if err != nil {
		log.Fatalf("Ошибка парсинга URL: %v", err)
	}

	diff, err := getMRDiff(config)
	if err != nil {
		log.Fatalf("Ошибка при получении изменений кода: %v", err)
	}

	selectedModel := fmt.Sprintf("gpt://%s/%s", yandexFolderID, yandexModel)

	// Задание функций
	tools := []openai.ChatCompletionToolParam{
		{
			Function: shared.FunctionDefinitionParam{
				Name:        "post_comments",
				Description: openai.String("Оставить рекомендации и замечания по результатам код-ревью"),
				Parameters: shared.FunctionParameters{
					"type": "object", // Корень параметров ВСЕГДА должен быть object
					"properties": map[string]any{
						"comments": map[string]any{ // Наш параметр-массив
							"type":        "array",
							"description": "Список замечаний к коду",
							"items": map[string]any{ // Описание элементов внутри массива (объекты)
								"type": "object",
								"properties": map[string]any{
									"file_path": map[string]any{
										"type":        "string",
										"description": "Путь к файлу, например, 'main.go'",
									},
									"line": map[string]any{
										"type":        "integer",
										"description": "Номер строки, к которой относится комментарий",
									},
									"text": map[string]any{
										"type":        "string",
										"description": "Текст замечания или рекомендации по улучшению",
									},
								},
								// Если включен Strict Mode, все поля в items.properties должны быть в required
								"required":             []string{"file_path", "line", "text"},
								"additionalProperties": false,
							},
						},
					},
					"required":             []string{"comments"}, // Делаем массив обязательным аргументом
					"additionalProperties": false,                // Обязательно для Structured Outputs / Strict Mode
				},
				Strict: openai.Bool(true), // Рекомендуется включать для точного следования схеме
			},
		},
		{
			Function: shared.FunctionDefinitionParam{
				Name:        "approve_merge_request",
				Description: openai.String("Одобрение изменений кода"),
			},
		},
	}

	// Выполнение запроса
	response, err := client.Chat.Completions.New(
		context.Background(),
		openai.ChatCompletionNewParams{
			Model: selectedModel,
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.SystemMessage("Будь аккуратен при выборе номера строки, используй Git diff hunk header" +
					"Разделяй комментарии на две категории: критичные и не критичные, в начале каждого комменатрия используй ключевые слова 'крит:' и 'для заметки:'" +
					"Если нет критических ответов, можно оставить комментарии и сделать апрув"),
				openai.UserMessage(diff + userInput),
			},
			ToolChoice: openai.ChatCompletionToolChoiceOptionUnionParam{
				OfAuto: openai.String("auto"),
			},
			Tools: tools,
		},
	)
	if err != nil {
		log.Fatalf("Ошибка при выполнении запроса: %v", err)
	}

	// Ответ модели
	message := response.Choices[0].Message

	// Вызов запрошенных моделью функций
	if len(message.ToolCalls) > 0 {
		// Массив сообщений для отправки результатов выполнения
		newMessages := []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(userInput),
			message.ToParam(),
		}

		// Заполнение результата для каждой вызванной функции
		for _, toolCall := range message.ToolCalls {
			functionName := toolCall.Function.Name
			var functionArgs map[string]any
			if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &functionArgs); err != nil {
				log.Fatalf("Ошибка при разборе аргументов: %v", err)
			}

			var resultJSON []byte

			if functionName == "post_comments" {
				// 1. Создаем переменную нужного типа
				var comments []ReviewComment

				// 2. Сериализуем сырые данные обратно в JSON-байты
				bytes, err := json.Marshal(functionArgs["comments"])
				if err == nil {
					// 3. Десериализуем байты напрямую в вашу структуру []ReviewComment
					_ = json.Unmarshal(bytes, &comments)
				}

				fmt.Println("post_comments_func_args", functionArgs)

				// 3. Вызываем вашу бизнес-логику (функцию, которая обрабатывает комментарии)
				// Внутри args.Comments уже лежит готовый срез (slice) []ReviewComment
				result := []map[string]string{}
				for _, comment := range comments {
					err := postCommentOnLine(config, comment)
					if err != nil {
						result = append(result, map[string]string{"status": "error", "message": err.Error()})
					}
				}

				fmt.Println("post_comments_results", result)

				// 4. Кодируем результат работы вашей функции обратно в JSON,
				// чтобы отправить его обратно в OpenAI (как Tool message)
				resultJSON, _ = json.Marshal(result)
			}

			if functionName == "approve_merge_request" {
				result := map[string]string{}
				fmt.Println("approve_merge_request", result)
				err := approveMR(config)
				if err != nil {
					result = map[string]string{"status": "error", "message": err.Error()}
				}
				resultJSON, _ = json.Marshal(result)
			}

			newMessages = append(newMessages, openai.ToolMessage(string(resultJSON), toolCall.ID))
		}

		// Второй запрос с результатами функций
		secondResponse, err := client.Chat.Completions.New(
			context.Background(),
			openai.ChatCompletionNewParams{
				Model:    selectedModel,
				Messages: newMessages,
				Tools:    tools,
			},
		)
		if err != nil {
			log.Fatalf("Ошибка при выполнении второго запроса: %v", err)
		}

		// Ответ модели с учетом вызова функций
		return secondResponse.Choices[0].Message.Content
	}

	// Функции не были вызваны, возвращаем исходный ответ
	return message.Content
}

func main() {
	if len(os.Args) < 2 {
		log.Fatalf("Использование: go run main.go <ссылка_на_gitlab_mr>")
	}
	mrURL := os.Args[1]

	// Load the .env file
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	yandexAPIKey := os.Getenv("YANDEX_API_KEY")
	yandexFolderID := os.Getenv("YANDEX_FOLDER_ID")
	yandexModel := os.Getenv("YANDEX_MODEL")
	token := os.Getenv("GITLAB_TOKEN")

	client := openai.NewClient(
		option.WithAPIKey(yandexAPIKey),
		option.WithBaseURL("https://ai.api.cloud.yandex.net/v1"),
		option.WithHeader("OpenAI-Project", yandexFolderID),
	)

	// Формируем промпт для агента
	prompt := "Ты — опытный ведущий разработчик. Твоя задача — провести ревью изменений кода"

	result := runConversation(client, prompt, yandexFolderID, yandexModel, mrURL, token)
	fmt.Println(result)
}

// Структура для элемента массива
type ReviewComment struct {
	FilePath string `json:"file_path"`
	Line     int    `json:"line"`
	Text     string `json:"text"`
}

// Структура, описывающая ВСЕ аргументы функции post_comments
type PostCommentsArgs struct {
	Comments []ReviewComment `json:"comments"`
}

// GitLabConfig хранит параметры подключения, извлеченные из URL
type GitLabConfig struct {
	BaseURL string
	Token   string
	ProjID  string
	MRIID   string
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

func postCommentOnLine(c *GitLabConfig, comment ReviewComment) error {
	fmt.Println("postCommentOnLine", comment)
	urlStr := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s/discussions", c.BaseURL, c.ProjID, c.MRIID)

	baseSHA, startSHA, headSHA, err := getMRVersions(c)
	if err != nil {
		return fmt.Errorf("не удалось получить SHA коммитов: %v", err)
	}

	payload := map[string]any{
		"body": comment.Text,
		"position": map[string]any{
			"base_sha":      baseSHA,
			"start_sha":     startSHA,
			"head_sha":      headSHA,
			"position_type": "text",
			"new_path":      comment.FilePath,
			"new_line":      comment.Line,
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
		if resp.StatusCode == 400 && strings.Contains(string(body), "must be a valid line code") {
			err := postCommentOnFile(c, comment)
			if err != nil {
				return err
			}
			return nil
		}
		return fmt.Errorf("статус %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func postCommentOnFile(c *GitLabConfig, comment ReviewComment) error {
	fmt.Println("postCommentOnFile", comment)
	urlStr := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s/discussions", c.BaseURL, c.ProjID, c.MRIID)

	baseSHA, startSHA, headSHA, err := getMRVersions(c)
	if err != nil {
		return fmt.Errorf("не удалось получить SHA коммитов: %v", err)
	}

	payload := map[string]any{
		"body": comment.Text,
		"position": map[string]any{
			"base_sha":      baseSHA,
			"start_sha":     startSHA,
			"head_sha":      headSHA,
			"position_type": "file",
			"new_path":      comment.FilePath,
			"old_path":      comment.FilePath,
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
