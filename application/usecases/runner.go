package usecases

import (
	"context"
	"os"
	"strconv"

	"ai/domain"
)

// NopReporter — пустой репортёр для CLI-режима (события цикла не транслируются).
type NopReporter struct{}

func (NopReporter) OnMessage(_, _ string, _ bool)            {}
func (NopReporter) OnMessageDelta(_, _ string)                {}
func (NopReporter) OnThinking(_, _ string)                    {}
func (NopReporter) OnToolStart(_, _ string)                   {}
func (NopReporter) OnToolResult(_, _ string, _ bool)          {}
func (NopReporter) OnTokens(_, _ int64, _ float64)            {}

// BasicRunner — базовая реализация AgentRunner для CLI и простых сценариев.
// Обёртывает RunTask с конфигурацией из окружения. Без сжатия истории,
// без инъекций, без resume — только цикл «модель → инструменты».
type BasicRunner struct {
	config RunConfig
	rep    Reporter
}

// NewBasicRunner создаёт базовый раннер. Параметры:
// maxRounds — лимит раундов (0 → из REVIEW_MAX_ROUNDS, дефолт 3);
// reporter — репортёр событий (nil → NopReporter);
// isParallelSafe — проверка безопасности параллельного выполнения (nil → выключено);
// parseArguments — парсер аргументов инструментов (nil → domain.ParseArguments).
func NewBasicRunner(maxRounds int, reporter Reporter, isParallelSafe func(string) bool, parseArguments func(string) (map[string]any, error)) *BasicRunner {
	if maxRounds <= 0 {
		maxRounds = defaultMaxRounds()
	}
	if reporter == nil {
		reporter = NopReporter{}
	}
	if parseArguments == nil {
		parseArguments = domain.ParseArguments
	}
	return &BasicRunner{
		config: RunConfig{
			MaxRounds:      maxRounds,
			ParallelEnabled: isParallelSafe != nil,
			ParallelMax:    4,
			IsParallelSafe: isParallelSafe,
			ParseArguments: parseArguments,
		},
		rep: reporter,
	}
}

func (r *BasicRunner) Generate(ctx context.Context, provider ChatProvider, agent Agent) (*domain.AgentResponse, error) {
	ports := RunPorts{
		Provider: provider,
		Agent:    agent,
		Reporter: r.rep,
		Config:   r.config,
		Compressor: HistoryCompressorFunc(func(ctx context.Context, _ ChatProvider, msgs []domain.Message, _ int) ([]domain.Message, *CompressionReport) {
			return ChatCompress(ctx, msgs, ChatCompressBudget)
		}),
	}
	return RunTask(ctx, ports)
}

// ChatCompressBudget — символьный бюджет истории для чат-ассистента.
// ~80% от 98304 токенов × 3.5 символов/токен ≈ 275K, с запасом → 250K.
const ChatCompressBudget = 250_000

func defaultMaxRounds() int {
	v := os.Getenv("REVIEW_MAX_ROUNDS")
	if v == "" {
		return 3
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 3
	}
	return n
}
