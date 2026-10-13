package runner

// Клиент/оркестрация расширенного сжатия (Ф-6..Ф-11).
//
// CompressionClient — набор опциональных сервисов (RAG-вытеснение,
// ранжировщик, LSP-оглавления), передаваемый вызывающим кодом через контекст
// по образцу runevents.Reporter (server/chatassist.go, main.go, planner).
// Поля — функции (адаптеры живут в пакетах rag/lsp без импорта runner):
// так remove циклы импортов, а hermetic-тесты подставляют fake без сети.
// Сервисы, потому что это позволяет не плодить копии.
//
// Ядро сжатия (пайплайн CompressContext) — в application/usecases
// (usecases/compress.go); здесь — оркестрация раунда: бюджет из окружения и
// фактического usage, страховка по окну провайдера, компакция через
// нейтральный порт ChatProvider.

import (
	"ai/application/usecases"
	"ai/domain"
	"context"
	"fmt"
	"os"
	"strconv"
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

// historyBudget возвращает символьный бюджет истории (CODEGEN_HISTORY_BUDGET,
// 0 = сжатие выключено). Приблизительно: 1 токен ≈ 4 символа.
func historyBudget() int {
	if v := os.Getenv("CODEGEN_HISTORY_BUDGET"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// historyTokens возвращает токен-лимит истории по фактическому usage
// провайдера (CODEGEN_HISTORY_TOKENS, 0 = выключено, Ф-11).
func historyTokens() int {
	if v := os.Getenv("CODEGEN_HISTORY_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// compactSystemPrompt — системный промпт компакции (Ф-10).
const compactSystemPrompt = "Ты — модуль компакции контекста агента-разработчика. " +
	"Ниже дан фрагмент истории диалога (сообщения ассистента и результаты его инструментов), " +
	"который вытесняется из контекстного окна. Сожми его в компактную «памятку» для продолжения " +
	"работы: только факты, принятые решения, затронутые файлы и ничем не закрытые задачи. " +
	"Не рассуждай, не пересказывай код целиком и не добавляй вымышленных деталей. Максимум 150 слов."

// providerCompactor — абстрактивная компакция (Ф-10) через ChatOnce циклового
// провайдера. Запрос нейтральный (domain.ChatRequest) и без инструментов:
// ForceToolName не задан, поэтому провайдеры не форсируют tool_choice —
// только текст резюме.
type providerCompactor struct {
	p ChatProvider
}

func (c *providerCompactor) Compact(ctx context.Context, text string) (string, error) {
	if len(text) > 256*1024 {
		text = usecases.TruncateLines(text, 256*1024)
	}
	reply, err := c.p.ChatOnce(ctx, domain.ChatRequest{Messages: []Message{
		{Role: "system", Content: compactSystemPrompt},
		{Role: "user", Content: text},
	}})
	if err != nil {
		return "", err
	}
	if reply == nil || len(reply.ToolCalls) > 0 || strings.TrimSpace(reply.Content) == "" {
		return "", fmt.Errorf("компактор вернул пустой ответ")
	}
	// Компакция не должна сама раздувать контекст памятки.
	return usecases.TruncateLines(strings.TrimSpace(reply.Content), usecases.MaxNoticeChars), nil
}

// compactEnabled включена ли компакция (Ф-10).
func compactEnabled() bool { return envFlag("CODEGEN_HISTORY_COMPACT") }

// providerInputCap — токен-лимит входного контекста, выведенный из лимитов
// провайдера: окно (n_ctx) минус резерв под вывод (ответ + thinking). Окно
// разделяется между входом и генерацией, поэтому вход не должен занимать его
// целиком. 0 — окно неизвестно или полностью занято резервом.
func providerInputCap(provider ChatProvider) int {
	ml, ok := provider.(ModelLimitsProvider)
	if !ok {
		return 0
	}
	limits := ml.ModelLimits()
	if limits.InputTokens <= 0 {
		return 0
	}
	reserve := limits.OutputTokens + limits.ThinkTokens
	if reserve <= 0 {
		// Провайдер не сообщил лимит вывода — резервируем четверть окна.
		reserve = limits.InputTokens / 4
	}
	if reserve >= limits.InputTokens {
		reserve = limits.InputTokens / 4
	}
	return limits.InputTokens - reserve
}

// compressHistoryForRound — единая точка сжатия в цикле Generate:
// считает бюджет (история + Ф-11 по фактическому usage + окно провайдера по
// умолчанию), собирает опции из контекстного клиента и провайдера и выполняет
// пайплайн (ядро — usecases.CompressContext). Возвращает новые сообщения и
// отчёт (могут совпадать с входом, если сжатие не нужно/выключено).
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

	// Страховка по умолчанию: даже без CODEGEN_HISTORY_* разросшаяся история
	// не должна превышать окно контекста провайдера — иначе цикл (десятки
	// раундов с результатами инструментов) падает с 400 exceed_context_size
	// («request (N tokens) exceeds the available context size»). Лимит входа =
	// окно минус резерв на вывод; срабатывает по фактическому usage прошлого
	// раунда и упреждающе — когда уже собранный в этом раунде вход по оценке
	// превышает лимит. Явный бюджет из окружения имеет приоритет.
	if opts.Budget <= 0 {
		if capTokens := providerInputCap(provider); capTokens > 0 {
			est, _ := EstimateUsage(messages, nil)
			if lastInputTokens > capTokens || est > capTokens {
				opts.Budget = capTokens * 4
				Debugf("COMPRESS: вход выше окна провайдера (usage=%d, оценка=%d > %d) — бюджет по умолчанию %d символов",
					lastInputTokens, est, capTokens, opts.Budget)
			}
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
