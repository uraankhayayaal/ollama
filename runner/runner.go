package runner

import (
	"ai/agents"
	"ai/tools"
	"context"
	"fmt"
	"os"
	"strconv"
)

// AgentResponse содержит ответ модели и все выполненные вызовы функций.
type AgentResponse struct {
	Content   string
	ToolCalls []tools.ToolCall
	// Truncated указывает, что цикл остановлен по лимиту раундов (maxRounds),
	// а не потому, что модель завершила ответ.
	Truncated bool
}

// Message — нейтральное представление сообщения диалога,
// провайдеры конвертируют его в свой формат.
type Message struct {
	Role       string // "system" | "user" | "assistant" | "tool"
	Content    string
	ToolCalls  []tools.ToolCall // для assistant
	ToolName   string           // для tool
	ToolCallID string           // для tool
}

// ModelReply — ответ модели за один раунд.
type ModelReply struct {
	Content      string
	ToolCalls    []tools.ToolCall
	FinishReason string
}

// ChatProvider — провайдер, умеющий сделать ОДИН запрос к модели.
type ChatProvider interface {
	ChatOnce(ctx context.Context, agent agents.Agent, messages []Message) (*ModelReply, error)
}

// ToolRequiringAgent — необязательный интерфейс агента, который требует,
// чтобы в ПЕРВОМ раунде модель обязательно вызвала определённый инструмент
// (например, WriteFiles у генератора кода). Если модель вместо вызова вернула
// текст — раннер повторит запрос с подсказкой использовать этот инструмент.
type ToolRequiringAgent interface {
	// RequiredToolFirstRound возвращает имя инструмента, обязательного к
	// вызову в первом раунде, и true, если требование активно.
	RequiredToolFirstRound() (string, bool)
}

// requiredRetries — сколько раз переспрашиваем модель, если она не вызвала
// обязательный инструмент первого раунда и ответила текстом.
const requiredRetries = 2

// maxRoundsLimit ограничивает количество итераций выполнения инструментов,
// чтобы защититься от бесконечного цикла "модель -> инструмент".
// Значение по умолчанию можно переопределить переменной REVIEW_MAX_ROUNDS.
const defaultMaxRounds = 12

func maxRounds() int {
	if v := os.Getenv("REVIEW_MAX_ROUNDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxRounds
}

// nudgeMessage формулирует подсказку модели, если она не вызвала
// обязательный инструмент в первом раунде и ответила текстом.
func nudgeMessage(toolName string) string {
	return fmt.Sprintf("Ты ответил текстом, но по заданию обязан вызвать инструмент %q для создания файлов. Немедленно вызови %q с нужными аргументами (файлы/код проекта). Не отвечай текстом.", toolName, toolName)
}

// Generate выполняет агентский цикл: отправляет диалог модели, исполняет
// запрошенные инструменты, возвращает результат модели обратно в историю
// и повторяет, пока модель не завершит ответ (нет tool_calls).
func Generate(ctx context.Context, provider ChatProvider, agent agents.Agent) (*AgentResponse, error) {
	// Собираем user-сообщения (например, дифф для ревью), чтобы передать
	// их контекст в метод системных сообщений (GetAgentMemoryMessages).
	userMessages := agent.GetMessages()

	// Системные сообщения размещаем в начале диалога, как это принято,
	// а user-сообщения — следом. Контекст (дифф) передаётся в метод
	// системных сообщений через параметр.
	systemMessages := agent.GetAgentMemoryMessages(userMessages)

	messages := []Message{}
	for _, m := range systemMessages {
		messages = append(messages, Message{Role: "system", Content: m.Message})
	}
	for _, m := range userMessages {
		messages = append(messages, Message{Role: "user", Content: m.Message})
	}

	var allToolCalls []tools.ToolCall
	content := ""
	mx := maxRounds()

	// Если агент требует обязательный инструмент в первом раунде — узнаём его.
	requiredTool := ""
	if req, ok := agent.(ToolRequiringAgent); ok {
		if n, yes := req.RequiredToolFirstRound(); yes {
			requiredTool = n
		}
	}
	requiredAttempts := 0

	for round := 0; round < mx; round++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("контекст отменён до раунда %d: %w", round+1, err)
		}

		reply, err := provider.ChatOnce(ctx, agent, messages)
		if err != nil {
			return nil, err
		}

		if len(reply.ToolCalls) == 0 {
			// Если обязательный инструмент ещё ни разу не вызван (конец = первый
			// раунд генерации), а модель ответила текстом — подскажем и повторим
			// (с ограничением), чтобы не завершить цикл без действия.
			if requiredTool != "" && len(allToolCalls) == 0 && requiredAttempts < requiredRetries {
				requiredAttempts++
				Debugf("RUNNER: раунд %d: требуемый инструмент %q не вызван, подсказываю (%d/%d) и повторяю", round+1, requiredTool, requiredAttempts, requiredRetries)
				messages = append(messages, Message{
					Role:    "user",
					Content: nudgeMessage(requiredTool),
				})
				continue
			}

			content = reply.Content
			Debugf("RUNNER: раунд %d: модель завершила (finish_reason=%q), content=%q",
				round+1, reply.FinishReason, Truncate(content, 300))
			return &AgentResponse{Content: content, ToolCalls: allToolCalls}, nil
		}

		Debugf("RUNNER: раунд %d: модель запросила %d вызова(ов), finish_reason=%q",
			round+1, len(reply.ToolCalls), reply.FinishReason)

		messages = append(messages, Message{Role: "assistant", Content: reply.Content, ToolCalls: reply.ToolCalls})
		allToolCalls = append(allToolCalls, reply.ToolCalls...)

		for _, tc := range reply.ToolCalls {
			Debugf("RUNNER: выполняю инструмент %q args=%s", tc.Name, Truncate(tc.Arguments, 500))

			args, err := tools.ParseArguments(tc.Arguments)
			if err != nil {
				return nil, fmt.Errorf("разбор аргументов инструмента %s: %w", tc.Name, err)
			}

			result, err := agent.CallFunction(tc.Name, args)
			if err != nil {
				Debugf("RUNNER: инструмент %q вернул ошибку: %v", tc.Name, err)
				return nil, fmt.Errorf("выполнение инструмента %s: %w", tc.Name, err)
			}

			Debugf("RUNNER: результат инструмента %q: %s", tc.Name, Truncate(string(result), 500))
			messages = append(messages, Message{
				Role:       "tool",
				ToolName:   tc.Name,
				ToolCallID: tc.ID,
				Content:    string(result),
			})
		}
	}

	Debugf("RUNNER: достигнут лимит раундов (%d), возвращаю частичный результат", mx)
	return &AgentResponse{Content: content, ToolCalls: allToolCalls, Truncated: true}, nil
}
