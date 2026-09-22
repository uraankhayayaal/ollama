package models

import (
	"ai/agents"
	"ai/logging"
	"ai/runner"
	"ai/tools"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ollama/ollama/api"
)

type OllamaProvider struct {
	client *api.Client
	model  string
	// settings — ключевые лимиты модели: входящий контекст (num_ctx), выход
	// (num_predict) и бюджет thinking. Вход задаётся OLLAMA_INPUT_TOKENS/
	// OLLAMA_NUM_CTX (по умолчанию 32000: Ollama использует 4096 токенов, а
	// история nudge-цикла может раздуваться до десятков тысяч, из-за чего
	// модель возвращает 400 exceeded_context_size), выход —
	// OLLAMA_OUTPUT_TOKENS/OLLAMA_MAX_TOKENS, thinking — OLLAMA_THINK_TOKENS.
	// См. ModelSettings.
	settings ModelSettings
}

func NewOllamaProvider(model string) (*OllamaProvider, error) {
	// 1. Создаем клиент Ollama (по умолчанию подключается к http://127.0.0.1:11434)
	client, err := api.ClientFromEnvironment()
	if err != nil {
		logging.Fatalf("Ошибка инициализации клиента: %v", err)
	}

	return &OllamaProvider{
		client: client,
		model:  model,
		settings: resolveSettings("OLLAMA", ModelSettings{
			InputTokens: 32000,
		}),
	}, nil
}

func (o *OllamaProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	return runner.Generate(ctx, o, agent)
}

// ChatOnce выполняет один запрос к модели Ollama. Реализуется через ChatStream
// с убранным потоковым колбэком — поведение сохранено (полный текст ответа).
func (o *OllamaProvider) ChatOnce(ctx context.Context, agent agents.Agent, msgs []runner.Message) (*runner.ModelReply, error) {
	return o.ChatStream(ctx, agent, msgs, nil)
}

// toolCallRetriesDefault — сколько раз повторяем запрос к модели, если
// llama-server вернул невалидный JSON аргументов tool-вызова (модель обрезала
// вывод; см. retryableToolCallErr). 0 (OLLAMA_TOOL_RETRIES=0) — повторов нет.
const toolCallRetriesDefault = 2

// toolCallRetryDelayDefault — пауза между повторами, она даёт llama-server
// время вернуться в рабочее состояние. Настраивается OLLAMA_TOOL_RETRY_DELAY
// (мс; 0 — без паузы).
const toolCallRetryDelayDefault = time.Second

// retryableToolCallErr распознаёт «обрубленный» tool-call llama-server —
// ошибку, которую безопасно повторять с тем же запросом: это не сетевая/
// контекстная проблема, а качество генерации (модель недописала JSON вызова),
// ответ стохастичен, поэтому следующий запрос с той же историей почти наверняка
// вернёт корректные аргументы. Ошибка формируется накопителем аргументов
// llama-server ("llama-server returned invalid tool call arguments for %q: %w").
func retryableToolCallErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "invalid tool call arguments")
}

// toolCallRetries — число повторов запроса при невалидном JSON аргументов
// tool-call (OLLAMA_TOOL_RETRIES; не настроено — toolCallRetriesDefault).
func toolCallRetries() int {
	if v := strings.TrimSpace(os.Getenv("OLLAMA_TOOL_RETRIES")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return toolCallRetriesDefault
}

// toolCallRetryDelay — пауза между повторами (OLLAMA_TOOL_RETRY_DELAY, мс;
// не настроено — toolCallRetryDelayDefault).
func toolCallRetryDelay() time.Duration {
	if v := strings.TrimSpace(os.Getenv("OLLAMA_TOOL_RETRY_DELAY")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return toolCallRetryDelayDefault
}

// ChatStream выполняет один запрос к модели Ollama в потоковом режиме: фрагменты
// текста отдаются через onChunk (накопленный Partial), полный текст возвращается
// в ModelReply. Реализует runner.StreamChatProvider.
func (o *OllamaProvider) ChatStream(ctx context.Context, agent agents.Agent, msgs []runner.Message, onChunk func(runner.StreamChunk)) (*runner.ModelReply, error) {
	ollamaTools := agent.GetToolsForOllama()

	// Перевод нейтральных сообщений в формат Ollama
	var messages []api.Message
	for _, m := range msgs {
		switch m.Role {
		case "tool":
			messages = append(messages, api.Message{
				Role:       "tool",
				Content:    m.Content,
				ToolName:   m.ToolName,
				ToolCallID: m.ToolCallID,
			})
		default:
			am := api.Message{Role: m.Role, Content: m.Content}
			if m.Role == "assistant" && len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					argsMap, err := tools.ParseArguments(tc.Arguments)
					if err != nil {
						return nil, fmt.Errorf("разбор аргументов истории вызова %s: %w", tc.Name, err)
					}
					args := api.NewToolCallFunctionArguments()
					for k, v := range argsMap {
						args.Set(k, v)
					}
					am.ToolCalls = append(am.ToolCalls, api.ToolCall{
						ID: tc.ID,
						Function: api.ToolCallFunction{
							Name:      tc.Name,
							Arguments: args,
						},
					})
				}
			}
			messages = append(messages, am)
		}
	}

	// Стриминг включён: фрагменты отдаются колбэку клиента, а ChatOnce
	// (делегирующий сюда) просто накапливает полный текст.
	stream := true

	// num_ctx — размер окна контекста (вход), num_predict — лимит выходных
	// токенов. Оба берутся из settings (ModelSettings).
	options := map[string]any{"num_ctx": o.settings.InputTokens}
	if o.settings.OutputTokens > 0 {
		options["num_predict"] = o.settings.OutputTokens
	}

	req := &api.ChatRequest{
		Model:    o.model,
		Messages: messages,
		Tools:    ollamaTools,
		Stream:   &stream,
		Options:  options,
	}

	// Reasoning-модели (например qwen3 с включённым thinking) перед ответом
	// генерируют цепочку рассуждения — это удваивает время и токены на каждом
	// раунде инструментов при той же точности вызовов. Управление рассуждением:
	//   - OLLAMA_THINK_TOKENS — числовой бюджет thinking: включаем рассуждение
	//     с уровнем, соответствующим бюджету (см. thinkLevelFromTokens);
	//   - legacy OLLAMA_THINK — boolean-переключатель ("0"/"false"/"off" —
	//     выключить, "1" — принудительно включить). По умолчанию параметр не
	//     задаётся — модель работает как настроена. Приоритет у числового
	//     бюджета.
	if level := thinkLevelFromTokens(o.settings.ThinkTokens); level != "" {
		req.Think = &api.ThinkValue{Value: level}
		runner.Debugf("OLLAMA: thinking для модели %q: уровень %q (бюджет %d токенов)", o.model, level, o.settings.ThinkTokens)
	} else if v := strings.TrimSpace(os.Getenv("OLLAMA_THINK")); v != "" {
		enabled := true
		switch strings.ToLower(v) {
		case "0", "false", "off", "no":
			enabled = false
		}
		req.Think = &api.ThinkValue{Value: enabled}
		if !enabled {
			runner.Debugf("OLLAMA: рассуждение (think) отключено для модели %q", o.model)
		}
	}

	runner.Debugf("OLLAMA: запрос к модели %q (сообщений: %d, инструментов: %d)", o.model, len(messages), len(ollamaTools))
	for i, m := range messages {
		runner.Debugf("OLLAMA: messages[%d] role=%q content=%q tool_calls=%d", i, m.Role, runner.Truncate(m.Content, 300), len(m.ToolCalls))
	}
	for _, t := range ollamaTools {
		runner.Debugf("OLLAMA: tool=%q", t.Function.Name)
	}

	var content strings.Builder
	var toolCalls []tools.ToolCall
	var doneReason string
	var usage *runner.Usage

	// Авто-ретрай «обрубленных» tool-call: llama-server иногда не дочитывает
	// JSON-аргументы вызова инструмента (модель оборвала вывод на полуслове,
	// редко) и отвечает "llama-server returned invalid tool call arguments:
	// unexpected end of JSON input". Это не сетевая/контекстная ошибка, а
	// стохастическое качество генерации — повтор того же запроса почти всегда
	// возвращает корректный вызов, и оркестрация не падает на одном раунде.
	// Количество повторов — toolCallRetries(), пауза — toolCallRetryDelay().
	streamFn := func(resp api.ChatResponse) error {
		if resp.Message.Content != "" {
			content.WriteString(resp.Message.Content)
		}
		if resp.DoneReason != "" {
			doneReason = resp.DoneReason
		}

		// Финальный фрагмент стрима несёт фактический подсчёт токенов
		// (prompt_eval_count — весь вход, eval_count — выход модели), а
		// eval_count/eval_duration дают реальную скорость генерации.
		if resp.Done && (resp.PromptEvalCount > 0 || resp.EvalCount > 0) {
			usage = &runner.Usage{
				InputTokens:  resp.PromptEvalCount,
				OutputTokens: resp.EvalCount,
			}
			if resp.EvalCount > 0 && resp.EvalDuration > 0 {
				usage.OutputTPS = float64(resp.EvalCount) / resp.EvalDuration.Seconds()
			}
		}

		if len(resp.Message.ToolCalls) > 0 {
			for i, tc := range resp.Message.ToolCalls {
				argsBytes, err := json.Marshal(tc.Function.Arguments)
				if err != nil {
					return fmt.Errorf("ошибка маршалинга аргументов: %w", err)
				}

				// Некоторые модели не заполняют id — генерируем сами,
				// чтобы tool-результат корректно стыковался с вызовом.
				id := tc.ID
				if id == "" {
					id = fmt.Sprintf("call_%d", i+1)
				}

				toolCalls = append(toolCalls, tools.ToolCall{ID: id, Name: tc.Function.Name, Arguments: string(argsBytes)})
				runner.Debugf("OLLAMA: tool_call %q (id=%q) args=%s", tc.Function.Name, id, string(argsBytes))
			}
		}

		if resp.Done {
			runner.Debugf(
				"OLLAMA: сырой ответ done=%v done_reason=%q content=%q thinking=%q",
				resp.Done, resp.DoneReason, runner.Truncate(resp.Message.Content, 300), runner.Truncate(resp.Message.Thinking, 300),
			)
			runner.DebugCheckEmpty("ollama", resp.DoneReason, content.String(), len(resp.Message.ToolCalls), resp)
		}

		// Потоковый фрагмент: накопленный текст, чтобы клиент просто
		// перезаписывал предпоследнее сообщение. Стрим идёт синхронно.
		if onChunk != nil {
			onChunk(runner.StreamChunk{Partial: content.String(), Done: resp.Done})
		}

		return nil
	}

	var err error
	for attempt := 0; ; attempt++ {
		err = o.client.Chat(ctx, req, streamFn)
		if err == nil {
			break
		}
		if attempt >= toolCallRetries() || !retryableToolCallErr(err) || ctx.Err() != nil {
			break
		}
		runner.Debugf("OLLAMA: попытка %d/%d: невалидные аргументы tool-call (%v), повторяю запрос к модели %q",
			attempt+1, toolCallRetries(), err, o.model)
		// Сбрасываем накопители частичного ответа неудачной попытки.
		content.Reset()
		toolCalls = nil
		doneReason = ""
		usage = nil
		if d := toolCallRetryDelay(); d > 0 {
			timer := time.NewTimer(d)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return nil, fmt.Errorf("Ошибка выполнения Chat: %w", ctx.Err())
			}
		}
	}
	if err != nil {
		// Оборачиваем через %w: отмена контекста (остановка пользователем,
		// graceful shutdown, таймаут шага) должна оставаться различимой для
		// errors.Is(err, context.Canceled) наверху — иначе server/session
		// трактует остановку как «оркестрация прервана ошибкой» и публикует
		// статус error вместо stopped.
		return nil, fmt.Errorf("Ошибка выполнения Chat: %w", err)
	}

	return &runner.ModelReply{Content: content.String(), ToolCalls: toolCalls, FinishReason: doneReason, Usage: usage}, nil
}
