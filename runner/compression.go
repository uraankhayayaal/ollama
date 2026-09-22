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
	"ai/agents"
	"ai/tools"
	"context"
	"fmt"
	"strings"

	"github.com/ollama/ollama/api"
)

// CompressionClient — сервисы расширенного сжатия истории диалога
// (обычно не все сразу). Пустые поля = соответствующая фича недоступна.
type CompressionClient struct {
	Project string
	// Evict — вытеснение выброшенных юнитов в векторную память (Ф-7).
	Evict func(ctx context.Context, project string, items []EvictionItem) error
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
func (c *CompressionClient) options() CompressOptions {
	opts := CompressOptions{Project: c.Project}
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

// compactSystemPrompt — системный промпт компакции (Ф-10).
const compactSystemPrompt = "Ты — модуль компакции контекста агента-разработчика. " +
	"Ниже дан фрагмент истории диалога (сообщения ассистента и результаты его инструментов), " +
	"который вытесняется из контекстного окна. Сожми его в компактную «памятку» для продолжения " +
	"работы: только факты, принятые решения, затронутые файлы и ничем не закрытые задачи. " +
	"Не рассуждай, не пересказывай код целиком и не добавляй вымышленных деталей. Максимум 150 слов."

// compactAgent — нейтральный агент для вызова ChatOnce в компакции: не является
// ToolRequiringAgent и не отдаёт инструменты, поэтому провайдеры не форсируют
// tool_choice (только текст резюме).
type compactAgent struct{}

func (compactAgent) GetSystemMessages([]agents.Message) []agents.Message { return nil }
func (compactAgent) GetUserMessages() []agents.Message                   { return nil }
func (compactAgent) GetTools() []tools.ToolDefinition                    { return nil }
func (compactAgent) GetToolsForOllama() []api.Tool                       { return nil }
func (compactAgent) CallFunction(string, map[string]any) ([]byte, error) {
	return nil, fmt.Errorf("компакция: вызовы инструментов запрещены")
}

// providerCompactor — абстрактивная компакция (Ф-10) через ChatOnce циклового
// провайдера. Нейтральный агент исключает форсирование инструментов.
type providerCompactor struct {
	p ChatProvider
}

func (c *providerCompactor) Compact(ctx context.Context, text string) (string, error) {
	if len(text) > 256*1024 {
		text = truncateLines(text, 256*1024)
	}
	msgs := []Message{
		{Role: "system", Content: compactSystemPrompt},
		{Role: "user", Content: text},
	}
	reply, err := c.p.ChatOnce(ctx, compactAgent{}, msgs)
	if err != nil {
		return "", err
	}
	if reply == nil || len(reply.ToolCalls) > 0 || strings.TrimSpace(reply.Content) == "" {
		return "", fmt.Errorf("компактор вернул пустой ответ")
	}
	// Компакция не должна сама раздувать контекст памятки.
	return truncateLines(strings.TrimSpace(reply.Content), maxNoticeChars), nil
}

// compactEnabled включена ли компакция (Ф-10).
func compactEnabled() bool { return envFlag("CODEGEN_HISTORY_COMPACT") }

// compressHistoryForRound — единая точка сжатия в цикле Generate:
// считает бюджет (история + Ф-11 по фактическому usage), собирает опции из
// контекстного клиента и провайдера и выполняет пайплайн. Возвращает новые
// сообщения и отчёт (могут совпадать с входом, если сжатие не нужно/выключено).
func compressHistoryForRound(ctx context.Context, provider ChatProvider, messages []Message, lastInputTokens int) ([]Message, *CompressionReport) {
	opts := CompressOptions{}
	if cc := CompressionClientFromContext(ctx); cc != nil {
		opts = cc.options()
	}

	// Бюджет: символьный из окружения; Ф-11 — если провайдер сообщил фактический
	// вход больше токен-лимита, ужесточаем сжатие до него (лимит×4 символов).
	opts.Budget = historyBudget()
	if tokenBudget := historyTokens(); tokenBudget > 0 && lastInputTokens > tokenBudget {
		charBudget := tokenBudget * 4
		if opts.Budget <= 0 || charBudget < opts.Budget {
			Debugf("COMPRESS: фактический вход %d > лимита %d токенов, ужесточаю бюджет до %d символов",
				lastInputTokens, tokenBudget, charBudget)
			opts.Budget = charBudget
		}
	}

	// Ф-10: компактор всегда от провайдера цикла (в отличие от RAG/LSP — это
	// не внешний сервис, а вызов самой модели).
	if opts.Budget > 0 && compactEnabled() && provider != nil {
		opts.Compact = true
		opts.CompactFn = (&providerCompactor{p: provider}).Compact
	}

	out, rep := CompressContext(ctx, messages, opts)
	// Отчёт пишем только когда сжатие реально что-то сделало.
	if len(out) != len(messages) {
		for _, e := range rep.Errors {
			Debugf("COMPRESS: %s", e)
		}
		Debugf("COMPRESS: раунд сжатия: %d → %d сообщений (бюджет %d, выброшено %d, компакция=%d, оглавления=%d)",
			len(messages), len(out), opts.Budget, rep.Dropped, len(rep.Summary), len(rep.Outlines))
	}
	return out, rep
}