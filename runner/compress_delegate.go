package runner

import (
	"ai/application/usecases"
	"context"
)

// Базовое (позиционное) сжатие истории вынесено в application/usecases
// (usecases/compress.go). Здесь — алиасы для совместимости вызовов раннера,
// тестов и runctx, использующих исторические имена runner.*.
type EvictionItem = usecases.EvictionItem

// CompressionReport — алиас usecases.CompressionReport.
type CompressionReport = usecases.CompressionReport

// CompressOptions — алиас usecases.CompressOptions.
type CompressOptions = usecases.CompressOptions

// EvictionItemsText — алиас usecases.EvictionItemsText.
func EvictionItemsText(items []EvictionItem) string { return usecases.EvictionItemsText(items) }

// CompressHistory — алиас usecases.CompressHistory.
func CompressHistory(msgs []Message, budget int) []Message {
	return usecases.CompressHistory(msgs, budget)
}

// CompressContext — алиас usecases.CompressContext.
func CompressContext(ctx context.Context, msgs []Message, opts CompressOptions) ([]Message, *CompressionReport) {
	return usecases.CompressContext(ctx, msgs, opts)
}

// estimateLen/extractFiles — обёртки над usecases-хелперами для внутренних
// тестов раннера (исторически жили в runner/compress.go).
func estimateLen(msgs []Message) int       { return usecases.EstimateLen(msgs) }
func extractFiles(texts []string) []string { return usecases.ExtractFiles(texts) }
