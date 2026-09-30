package models

import (
	"ai/agents"
	"ai/runner"
	"ai/tools"
	"context"
	"fmt"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/shared"
	"github.com/openai/openai-go/shared/constant"
)

const bigWriteTokens = 16000

type OpenAIProvider struct {
	client    openai.Client
	model     string
	settings  ModelSettings
	folderID  string
	forceTool bool
}

func NewOpenAIProvider(name ProviderName, model string, cfg ProviderConfig) (*OpenAIProvider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("api_key для провайдера %s не задан в providers.json", name)
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(baseURL),
	}

	if cfg.FolderID != "" {
		opts = append(opts, option.WithHeader("OpenAI-Project", cfg.FolderID))
	}

	fullModel := model
	if cfg.ModelPrefix != "" && !strings.Contains(model, "://") {
		fullModel = cfg.ModelPrefix + model
	}

	return &OpenAIProvider{
		client:   openai.NewClient(opts...),
		model:    fullModel,
		folderID: cfg.FolderID,
		settings: ModelSettings{
			ThinkTokens:  cfg.Settings.ThinkTokens,
			InputTokens:  cfg.Settings.InputTokens,
			OutputTokens: cfg.Settings.OutputTokens,
		},
		forceTool: name == ProviderTrim || name == ProviderReg,
	}, nil
}

func (p *OpenAIProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	return runner.Generate(ctx, p, agent)
}

func (p *OpenAIProvider) ModelLimits() runner.ModelLimits {
	return runner.ModelLimits{
		InputTokens:  p.settings.InputTokens,
		OutputTokens: p.settings.OutputTokens,
		ThinkTokens:  p.settings.ThinkTokens,
	}
}

func (p *OpenAIProvider) ChatOnce(ctx context.Context, agent agents.Agent, msgs []runner.Message) (*runner.ModelReply, error) {
	oaTools := make([]openai.ChatCompletionToolParam, 0, len(agent.GetTools()))
	for _, t := range agent.GetTools() {
		oaTools = append(oaTools, openai.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        t.Name,
				Description: openai.String(t.Description),
				Parameters:  t.Parameters,
				Strict:      openai.Bool(true),
			},
		})
	}

	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "system":
			messages = append(messages, openai.SystemMessage(m.Content))
		case "user":
			messages = append(messages, openai.UserMessage(m.Content))
		case "tool":
			messages = append(messages, openai.ToolMessage(m.Content, m.ToolCallID))
		case "assistant":
			am := openai.ChatCompletionAssistantMessageParam{
				Role: constant.Assistant("assistant"),
				Content: openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: param.NewOpt(m.Content),
				},
			}
			for _, tc := range m.ToolCalls {
				am.ToolCalls = append(am.ToolCalls, openai.ChatCompletionMessageToolCallParam{
					ID:   tc.ID,
					Type: constant.Function("function"),
					Function: openai.ChatCompletionMessageToolCallFunctionParam{
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				})
			}
			messages = append(messages, openai.ChatCompletionMessageParamUnion{OfAssistant: &am})
		}
	}

	toolChoice := openai.ChatCompletionToolChoiceOptionUnionParam{
		OfAuto: openai.String("auto"),
	}

	if p.forceTool {
		if req, ok := agent.(runner.ToolRequiringAgent); ok {
			if name, yes := req.RequiredToolFirstRound(); yes && name != "" && !hasToolResult(msgs) {
				toolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{
					OfChatCompletionNamedToolChoice: &openai.ChatCompletionNamedToolChoiceParam{
						Type: constant.Function("function"),
						Function: openai.ChatCompletionNamedToolChoiceFunctionParam{
							Name: name,
						},
					},
				}
			}
		}
	}

	maxTokens := p.settings.OutputTokens
	if maxTokens <= 0 {
		maxTokens = 8000
	}
	if req, ok := agent.(runner.ToolRequiringAgent); ok {
		if name, yes := req.RequiredToolFirstRound(); yes && name != "" && !hasToolResult(msgs) {
			if maxTokens < bigWriteTokens {
				maxTokens = bigWriteTokens
			}
		}
	}

	reasoning := openai.ReasoningEffort("none")
	if level := thinkLevelFromTokens(p.settings.ThinkTokens); level != "" {
		reasoning = openai.ReasoningEffort(level)
	}

	response, err := p.client.Chat.Completions.New(
		ctx,
		openai.ChatCompletionNewParams{
			Model:               p.model,
			Messages:            messages,
			Tools:               oaTools,
			ToolChoice:          toolChoice,
			MaxCompletionTokens: openai.Int(int64(maxTokens)),
			ReasoningEffort:     reasoning,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("Ошибка при выполнении запроса: %w", err)
	}

	if len(response.Choices) == 0 {
		return nil, fmt.Errorf("Модель не вернула ни одного ответа")
	}

	message := response.Choices[0].Message

	var toolCalls []tools.ToolCall
	for _, toolCall := range message.ToolCalls {
		toolCalls = append(toolCalls, tools.ToolCall{
			ID:        toolCall.ID,
			Name:      toolCall.Function.Name,
			Arguments: toolCall.Function.Arguments,
		})
	}

	if len(toolCalls) == 0 && message.FunctionCall.Name != "" {
		toolCalls = append(toolCalls, tools.ToolCall{
			ID:        fmt.Sprintf("call_%s", message.FunctionCall.Name),
			Name:      message.FunctionCall.Name,
			Arguments: message.FunctionCall.Arguments,
		})
	}

	var usage *runner.Usage
	if response.Usage.JSON.PromptTokens.Valid() && response.Usage.JSON.CompletionTokens.Valid() {
		usage = &runner.Usage{
			InputTokens:  int(response.Usage.PromptTokens),
			OutputTokens: int(response.Usage.CompletionTokens),
		}
	}

	return &runner.ModelReply{
		Content:      message.Content,
		ToolCalls:    toolCalls,
		FinishReason: string(response.Choices[0].FinishReason),
		Usage:        usage,
	}, nil
}

func (p *OpenAIProvider) GetModelName(ctx context.Context) string {
	return p.model
}

func hasToolResult(msgs []runner.Message) bool {
	for _, m := range msgs {
		if m.Role == "tool" {
			return true
		}
	}
	return false
}
