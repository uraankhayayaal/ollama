package models

import (
	"ai/agents"
	"context"
	"encoding/json"
	"log"
	"os"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

type AlisaProvider struct {
	client openai.Client
	model  string // например, "yandexgpt/latest"
}

func NewAlisaProvider() *AlisaProvider {
	yandexAPIKey := os.Getenv("YANDEX_API_KEY")
	yandexFolderID := os.Getenv("YANDEX_FOLDER_ID")
	yandexModel := os.Getenv("YANDEX_MODEL")

	client := openai.NewClient(
		option.WithAPIKey(yandexAPIKey),
		option.WithBaseURL("https://ai.api.cloud.yandex.net/v1"),
		option.WithHeader("OpenAI-Project", yandexFolderID),
	)
	return &AlisaProvider{client: client, model: yandexModel}
}

func (y *AlisaProvider) Generate(ctx context.Context, agent agents.Agent) (*AgentResponse, error) {
	// Перевод tools
	var oaTools []openai.ChatCompletionToolParam
	for _, t := range agent.GetTools() {
		oaTools = append(oaTools, openai.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        t.Name,
				Description: openai.String(t.Description),
				Parameters:  t.Parameters,
				Strict:      openai.Bool(true), // Рекомендуется включать для точного следования схеме
			},
		})
	}

	// Перевод prompt
	var oaMessages []openai.ChatCompletionMessageParamUnion
	var userMessage string
	for _, m := range agent.GetMessages() {
		switch m.Type {
		case agents.MessageTypeAI, agents.MessageTypeFunction, agents.MessageTypeTool:
			oaMessages = append(oaMessages, openai.AssistantMessage(m.Message))
		case agents.MessageTypeGeneric:
			oaMessages = append(oaMessages, openai.DeveloperMessage(m.Message))
		case agents.MessageTypeSystem:
			oaMessages = append(oaMessages, openai.SystemMessage(m.Message))
		case agents.MessageTypeHuman:
			oaMessages = append(oaMessages, openai.UserMessage(m.Message))
			userMessage = userMessage + "; " + m.Message
		default:
			oaMessages = append(oaMessages, openai.UserMessage(m.Message))
		}
	}

	// Выполнение запроса
	response, err := y.client.Chat.Completions.New(
		context.Background(),
		openai.ChatCompletionNewParams{
			Model:    y.model,
			Messages: oaMessages,
			Tools:    oaTools,
			ToolChoice: openai.ChatCompletionToolChoiceOptionUnionParam{
				OfAuto: openai.String("auto"),
			},
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
			openai.UserMessage(userMessage),
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

			resultJSON, err := agent.CallFunction(functionName, functionArgs)
			if err != nil {
				log.Fatalf("Ошибка при выполнении инструмента: %v", err)
			}

			newMessages = append(newMessages, openai.ToolMessage(string(resultJSON), toolCall.ID))
		}

		// Второй запрос с результатами функций
		secondResponse, err := y.client.Chat.Completions.New(
			context.Background(),
			openai.ChatCompletionNewParams{
				Model:    y.model,
				Messages: newMessages,
				Tools:    oaTools,
			},
		)
		if err != nil {
			log.Fatalf("Ошибка при выполнении второго запроса: %v", err)
		}

		// Ответ модели с учетом вызова функций
		return &AgentResponse{Content: secondResponse.Choices[0].Message.Content}, nil

		// agentResp := &AgentResponse{
		// 	Content: response.Choices[0].Message.Content,
		// }
		// for _, tc := range response.Choices[0].Message.ToolCalls {
		// 	agentResp.ToolCalls = append(agentResp.ToolCalls, tools.ToolCall{
		// 		ID:        tc.ID,
		// 		Name:      tc.Function.Name,
		// 		Arguments: tc.Function.Arguments,
		// 	})
		// }

		// return agentResp, nil
	}

	return &AgentResponse{Content: message.Content}, nil
}
