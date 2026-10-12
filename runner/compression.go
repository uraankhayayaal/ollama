package runner

// Клиент/оркестрация расширенного сжатия (Ф-6..Ф-11).
//
// CompressionClient — набор опциональных сервисов (RAG-вытеснение,
// ранжировщик, LSP-оглавления), передаваемый вызывающим кодом через контекст
// по образцу runevents.Reporter (server/chatassist.go, main.go, planner).
// Поля — функции (адаптеры живут в пакетах rag/lsp без импорта runner):
// так remove циклы импортов, а hermetic-тесты подставляют fake без сети.
// Сервисы, потому что это позволяет не плодить копии.

import (
	"ai/application/usecases"
	"context"
	"strings"
)

// CompressionClient — сервисы расширенного сжатия истории диалога
// (обычно не все сразу). Пустые поля = соответствующая фича недоступна.
type CompressionClient struct {
	Project string
	// Evict — вытеснение выброшенных юнитов в векторную память (Ф-7).
	Evict func(ctx context.Context, project string, items []usecases.EvictionItem) error
	// Rank — семантическое ранжирование кандидатов на выброс (Ф-8).
	Rank func(ctx context.Context, query string, texts []string) ([]float32, error)
	// Outline — LSP-оглавления убранных файлов (Ф-9).
	Outline func(ctx context.Context, project string, rels []string) (string, error)
}

type compressionClientKey struct{}

// WithCompressionClient кладёт сервисы сжатия в контекст.
func WithCompressionClient(ctx context.Context, c *CompressionClient) context.Context {
	return context.WithValue(ctx, compressionClientKey{}, c)
}

// CompressionClientFromContext достаёт сервисы сжатия из контекста (nil, если
// не подключены — все побочные фичи деградируют молча).
func CompressionClientFromContext(ctx context.Context) *CompressionClient {
	c, _ := ctx.Value(compressionClientKey{}).(*CompressionClient)
	return c
}

// options собирает CompressOptions из клиента и флагов окружения. Бюджет не
// трогается — его считает цикл (история → пополнение по usage, см. runner.go).
func (c *CompressionClient) options() usecases.CompressOptions {
	opts := usecases.CompressOptions{Project: c.Project}
	if c.Evict != nil {
		opts.Evict = envFlag("CODEGEN_HISTORY_EVICT")
		opts.EvictFn = c.Evict
	}
	if c.Rank != nil {
		opts.Rank = envFlag("CODEGEN_HISTORY_RANK")
		opts.RankFn = c.Rank
		opts.RankLimit = 6
	}
	if c.Outline != nil {
		opts.Outline = envFlag("CODEGEN_HISTORY_OUTLINE")
		opts.OutlineFn = c.Outline
	}
	opts.Notice = envFlag("CODEGEN_HISTORY_NOTICE")
	return opts
}

// envFlag — включает флаг по 1/true/yes/on (для CODEGEN_HISTORY_*).
func envFlag(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// compressHistoryForRound — единая точка сжатия в цикле Generate:
// считает бюджет (история + Ф-11 по фактическому usage + окно провайдера по
// умолчанию), собирает опции из контекстного клиента и провайдера и выполняет
// пайплайн. Возвращает новые сообщения и отчёт (могут совпадать с входом, если
// сжатие не нужно/выключено).
//
// Важно: реализация сжатия теперь находится в usecases.
func compressHistoryForRound(ctx context.Context, provider usecases.ChatProvider, messages []Message, lastInputTokens int) ([]Message, *usecases.CompressionReport) {
	// This is a compatibility wrapper that delegates to usecases version
	return usecases.CompressHistoryForRound(ctx, provider, messages, lastInputTokens)
}