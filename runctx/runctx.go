// Пакет runctx инжектит в контекст агентского цикла сервисы расширенного
// сжатия истории (runner.CompressionClient, Ф-6..Ф-11): адаптеры RAG
// (вытеснение в эпизоды Ф-7, семантическое ранжирование Ф-8) и LSP-оглавлений
// (Ф-9) собраны здесь один раз и переиспользуются сервером (chatassist),
// standalone-режимом main.go и планировщиком (planner/executor).

package runctx

import (
	"context"
	"strings"

	"ai/rag"
	"ai/runner"
	"ai/stackdetect"
	"ai/tools"
	"ai/tools/lspclient"
)

// WithCompression кладёт в контекст сервисы расширенного сжатия для проекта
// (dir — корень проекта, ragc — векторная память; nil — только LSP-шапки, но
// вытеснение/ранжирование будут недоступны и соответствующие фичи отключатся).
// Возвращает исходный ctx, если сжимать нечего. Сами флаги
// CODEGEN_HISTORY_* читаются в цикле runner (runner/compression.go).
func WithCompression(ctx context.Context, project, dir string, ragc *rag.Client) context.Context {
	if strings.TrimSpace(project) == "" || strings.TrimSpace(dir) == "" {
		return ctx
	}

	cc := &runner.CompressionClient{
		Project: project,
		Outline: func(ctx context.Context, p string, rels []string) (string, error) {
			o, err := lspclient.Shared().Outliner(ctx, dir, stackdetect.DetectKind(dir))
			if err != nil {
				// Языковой сервер не установлен/не стартует — оглавления
				// недоступны (песня деградации в сжатии, не fatal).
				return "", err
			}
			return tools.OutlineFromOutliner(o)(ctx, p, rels)
		},
	}
	if ragc != nil {
		cc.Evict = func(ctx context.Context, p string, items []runner.EvictionItem) error {
			return ragc.EvictionEpisodes(ctx, p, "dialog", runner.EvictionItemsText(items))
		}
		cc.Rank = ragc.Similarity
	}
	return runner.WithCompressionClient(ctx, cc)
}