// Пакет runctx инжектит в контекст сервисы расширенного сжатия истории:
// RAG-вытеснение (эпизоды), семантическое ранжирование, LSP-оглавления.
// Сами флаги CODEGEN_HISTORY_* читаются в application/usecases.

package runctx

import (
	"context"
	"strings"

	"ai/application/usecases"
	"ai/infrastructure/rag"
	"ai/infrastructure/stackdetect"
	"ai/tools"
	"ai/tools/lspclient"
)

// CompressionClient — сервисы расширенного сжатия истории диалога.
// Пустые поля = соответствующая фича недоступна.
type CompressionClient struct {
	Project string
	Evict   func(ctx context.Context, project string, items []usecases.EvictionItem) error
	Rank    func(ctx context.Context, query string, texts []string) ([]float32, error)
	Outline func(ctx context.Context, project string, rels []string) (string, error)
}

type compressionClientKey struct{}

// WithCompressionClient кладёт сервисы сжатия в контекст.
func WithCompressionClient(ctx context.Context, c *CompressionClient) context.Context {
	return context.WithValue(ctx, compressionClientKey{}, c)
}

// CompressionClientFromContext достаёт сервисы сжатия из контекста (nil, если
// не подключены).
func CompressionClientFromContext(ctx context.Context) *CompressionClient {
	c, _ := ctx.Value(compressionClientKey{}).(*CompressionClient)
	return c
}

// WithCompression кладёт в контекст сервисы расширенного сжатия для проекта.
func WithCompression(ctx context.Context, project, dir string, ragc *rag.Client) context.Context {
	if strings.TrimSpace(project) == "" || strings.TrimSpace(dir) == "" {
		return ctx
	}

	cc := &CompressionClient{
		Project: project,
		Outline: func(ctx context.Context, p string, rels []string) (string, error) {
			o, err := lspclient.Shared().Outliner(ctx, dir, stackdetect.DetectKind(dir))
			if err != nil {
				return "", err
			}
			return tools.OutlineFromOutliner(o)(ctx, p, rels)
		},
	}
	if ragc != nil {
		cc.Evict = func(ctx context.Context, p string, items []usecases.EvictionItem) error {
			texts := EvictionItemsText(items)
		var text string
		for i, t := range texts {
			if i > 0 {
				text += "\n"
			}
			text += t
		}
		return ragc.EvictionEpisodes(ctx, p, "dialog", text)
		}
		cc.Rank = ragc.Similarity
	}
	return WithCompressionClient(ctx, cc)
}

// EvictionItemsText — тексты кандидатов на выброс.
func EvictionItemsText(items []usecases.EvictionItem) []string {
	texts := make([]string, len(items))
	for i, it := range items {
		texts[i] = it.Text
	}
	return texts
}
