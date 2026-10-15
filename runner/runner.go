package runner

import (
	"ai/agents"
	"ai/application/usecases"
	"ai/domain"
	"ai/infrastructure/runevents"
	"ai/runner/adapters"
	"ai/tools"
	"context"
	"os"
	"strconv"
	"strings"
)

// AgentResponse содержит ответ модели и все выполненные вызовы функций.
// Тип принадлежит домену (domain.AgentResponse), здесь — алиас.
type AgentResponse = domain.AgentResponse

// Message — нейтральное представление сообщения диалога,
// провайдеры конвертируют его в свой формат.
type Message = domain.Message

// ModelReply — ответ модели за один раунд.
type ModelReply = domain.ModelReply

// Usage — фактическое потребление токенов одного запроса к модели.
type Usage = domain.Usage

// ChatProvider, StreamChatProvider, ModelLimitsProvider — порты LLM,
// определённые в application/usecases (см. usecases/ports.go); здесь алиасы
// для обратной совместимости пакета runner.
type ChatProvider = usecases.ChatProvider

// StreamChunk — фрагмент потокового ответа модели (принадлежит домену).
type StreamChunk = domain.StreamChunk

// StreamChatProvider — опциональное потоковое расширение ChatProvider.
type StreamChatProvider = usecases.StreamChatProvider

// ModelLimits — алиас domain.ModelLimits.
type ModelLimits = domain.ModelLimits

// ModelLimitsProvider — опциональный порт лимитов модели (usecases).
type ModelLimitsProvider = usecases.ModelLimitsProvider

// EstimateTextTokens — алиас domain.EstimateTextTokens (см. domain/tokens.go).
func EstimateTextTokens(s string) int { return domain.EstimateTextTokens(s) }

// EstimateUsage — алиас domain.EstimateUsage (см. domain/tokens.go).
func EstimateUsage(messages []Message, reply *ModelReply) (in, out int) {
	return domain.EstimateUsage(messages, reply)
}

// ToolRequiringAgent — порт агента с обязательным инструментом за цикл.
type ToolRequiringAgent = usecases.ToolRequiringAgent

// RequiredToolGroupsAgent — порт агента с обязательными группами инструментов.
type RequiredToolGroupsAgent = usecases.RequiredToolGroupsAgent

// ResumeState — алиас usecases.ResumeState: точка возобновления цикла
// (полная история и число потраченных раундов).
type ResumeState = usecases.ResumeState

// WithResumeState помещает состояние возобновления агентского цикла в контекст.
// Провайдеры сами не меняются: runner.Generate читает состояние из контекста.
// Ключ контекста принадлежит application/usecases, поэтому runner и models
// используют один и тот же resume-канал.
func WithResumeState(ctx context.Context, state *ResumeState) context.Context {
	return usecases.WithResumeState(ctx, state)
}

// ResumeStateFromContext извлекает состояние возобновления из контекста.
func ResumeStateFromContext(ctx context.Context) *ResumeState {
	st, _ := usecases.ResumeStateFromContext(ctx)
	return st
}

// WithoutResumeState убирает состояние возобновления из контекста: следующий
// цикл начнётся заново (свежая история, полный бюджет раундов).
//
// Нужен там, где задачу берут «с другой стороны»: продолжение того же
// застрявшего диалога после срыва воспроизводит ту же петлю.
func WithoutResumeState(ctx context.Context) context.Context {
	return usecases.WithoutResumeState(ctx)
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

// nudgeMessage — алиас usecases.NudgeMessage (политика цикла в application).
func nudgeMessage(toolName string) string { return usecases.NudgeMessage(toolName) }

// retryMessage — алиас usecases.RetryMessage.
func retryMessage(toolName string) string { return usecases.RetryMessage(toolName) }

// Пороговые константы политики цикла (реализация — application/usecases/policy.go).
const (
	maxRepeatedToolCalls      = usecases.MaxRepeatedToolCalls
	maxRepeatedToolFails      = usecases.MaxRepeatedToolFails
	maxNeedRefills            = usecases.MaxNeedRefills
	requiredProgressAfter     = usecases.RequiredProgressAfter
	requiredProgressEvery     = usecases.RequiredProgressEvery
	maxRequiredProgressNudges = usecases.MaxRequiredProgressNudges
)

// ContextSupplier — алиас usecases.ContextFetcher.
type ContextSupplier = usecases.ContextFetcher

// NeedContextTargets — алиас usecases.NeedContextTargets.
func NeedContextTargets(content string) []string { return usecases.NeedContextTargets(content) }

// needContextPull — алиас usecases.NeedContextPull.
func needContextPull(agent agents.Agent, content string) (map[string]string, bool) {
	return usecases.NeedContextPull(agent, content)
}

// contextRefillMessage — алиас usecases.ContextRefillMessage.
func contextRefillMessage(pulled map[string]string) string {
	return usecases.ContextRefillMessage(pulled)
}

// loopMessage — алиас usecases.LoopMessage.
func loopMessage(toolName string, n int) string { return usecases.LoopMessage(toolName, n) }

// progressNudgeMessage — алиас usecases.ProgressNudgeMessage.
func progressNudgeMessage(toolName string, round, total, nudge int) string {
	return usecases.ProgressNudgeMessage(toolName, round, total, nudge)
}

// toolFailMessage — алиас usecases.ToolFailMessage.
func toolFailMessage(toolName string, n int, required string) string {
	return usecases.ToolFailMessage(toolName, n, required)
}

// Параллельное выполнение инструментов (parallel tool calling).
//
// Модель может запросить НЕСКОЛЬКО вызовов за один раунд. Read-only
// инструменты (List/ReadFiles/ReadMap и чтение доски) независимы и безопасны
// для одновременного запуска — это заметно ускоряет цикл: главный боттлнек
// агента-разработчика/лида — последовательное чтение многих файлов. Все
// остальные инструменты выполняются строго последовательно в порядке вызова,
// сохраняя прежнюю семантику (см. tools.IsParallelSafe и tools/parallel.go).
//
// Фича включается по умолчанию, отключается PARALLEL_TOOL_CALLS=0. Ширина
// волны ограничивается PARALLEL_TOOL_MAX (0 — без ограничения).
const (
	defaultParallelMax     = 8
	parallelToolCallsOnEnv = "PARALLEL_TOOL_CALLS"
	parallelToolMaxOnEnv   = "PARALLEL_TOOL_MAX"
)

// parallelToolCallsEnabled сообщает, включено ли параллельное выполнение
// независимых (read-only) инструментов одного раунда. По умолчанию — да;
// выключается PARALLEL_TOOL_CALLS=0/false/off.
func parallelToolCallsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(parallelToolCallsOnEnv))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// parallelToolMax — максимальная ширина параллельной волны read-only
// инструментов (PARALLEL_TOOL_MAX). 0 или отрицательное значение — без
// ограничения.
func parallelToolMax() int {
	if v := strings.TrimSpace(os.Getenv(parallelToolMaxOnEnv)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultParallelMax
}

// toolCallJob — подготовленный вызов инструмента раунда: аргументы уже
// разобраны из JSON строки, к вызову готов.
type toolCallJob struct {
	tc   tools.ToolCall
	args map[string]any
}

// toolOutcome — результат выполнения одного вызова инструмента.
type toolOutcome struct {
	result []byte
	err    error
}

// toolResultFailed — алиас usecases.ToolResultFailed (политика в application).
func toolResultFailed(toolName string, result []byte) bool {
	return usecases.ToolResultFailed(toolName, result)
}

// Generate выполняет агентский цикл (application/usecases.RunTask), собирая
// порты и конфигурацию из провайдера, агента, контекста и окружения.
//
// Если в контексте передано состояние возобновления (WithResumeState), цикл
// не начинает заново, а продолжает сохранённый диалог с раунда Rounds+1,
// имея снова полный бюджет maxRounds.
func Generate(ctx context.Context, provider ChatProvider, agent agents.Agent) (*AgentResponse, error) {
	return generate(ctx, provider, agent, ResumeStateFromContext(ctx))
}

func generate(ctx context.Context, provider ChatProvider, agent agents.Agent, resume *ResumeState) (*AgentResponse, error) {
	var reporter usecases.Reporter
	if rep := runevents.ReporterFromContext(ctx); rep != nil {
		reporter = rep
	}
	return usecases.RunTask(ctx, usecases.RunPorts{
		Provider:   provider,
		Agent:      agent,
		Role:       domain.AgentRole(adapters.AgentRole(agent)),
		Reporter:   reporter,
		Injections: adapters.NewInjectionApplier(ctx, agent, provider),
		Compressor: usecases.HistoryCompressorFunc(func(ctx context.Context, provider usecases.ChatProvider, messages []domain.Message, lastInputTokens int) ([]domain.Message, *usecases.CompressionReport) {
			// Convert messages to runner.Message for compatibility
			runnerMessages := make([]Message, len(messages))
			for i, msg := range messages {
				runnerMessages[i] = Message{
					Role:      msg.Role,
					Content:   msg.Content,
					ToolCalls: msg.ToolCalls,
				}
			}

			// Call the original function with type conversion
			compressedMessages, report := compressHistoryForRound(ctx, provider, runnerMessages, lastInputTokens)

			// Convert back to domain.Message
			domainMessages := make([]domain.Message, len(compressedMessages))
			for i, msg := range compressedMessages {
				domainMessages[i] = domain.Message{
					Role:      msg.Role,
					Content:   msg.Content,
					ToolCalls: msg.ToolCalls,
				}
			}

			return domainMessages, report
		}),
		Resume: resume,
		Config: usecases.RunConfig{
			MaxRounds:           maxRounds(),
			ParallelEnabled:     parallelToolCallsEnabled(),
			ParallelMax:         parallelToolMax(),
			IsParallelSafe:      tools.IsParallelSafe,
			ParseArguments:      tools.ParseArguments,
			AutoFixEnabled:      adapters.AutoFixEnabled(),
			AutoFixMaxRounds:    adapters.AutoFixMaxRounds(),
			ReindexEnabled:      adapters.ReindexEnabled(),
			WIPEnabled:          adapters.WIPCommitEnabled(),
			AppLogFeedEnabled:   adapters.AppLogFeedEnabled(),
			AppLogMaxFeedRounds: adapters.AppLogMaxFeedRounds(),
			BuildRoundState:     adapters.NewRoundState,
		},
	})
}
