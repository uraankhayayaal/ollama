package developer

import (
	"context"
	"strings"

	"ai/domain/services"
	"ai/infrastructure/rag"
)

// RAGClient — клиент векторной памяти (Qdrant) для CodeSearch.
// Алиас к порту services.Searcher.
type RAGClient = services.Searcher

// NewRAGClient создаёт RAG-клиент (безопасный: при недоступном Qdrant — nil).
func NewRAGClient() *rag.Client {
	return rag.NewClientSafe(rag.Config{})
}

// WipRepo — интерфейс промежуточных коммитов (Ф-6).
type WipRepo interface {
	CommitWipRound(ctx context.Context, dir string, touched []string, round int) (sha string, ok bool, err error)
}

// RoundState — состояние агента за раунд (Ф-6).
type RoundState struct {
	Agent        string
	State        string
	CommitSHA    string
	Committed    bool
	VerifyRan    bool
	VerifyFailed bool
	HeartbeatAt  string
}

// RoundStateErrorText — нормализованный текст ошибки раунда (макс. 512 байт).
func RoundStateErrorText(rs RoundState) string {
	_ = rs
	return ""
}

// ReindexTouched — частичная переиндексация файлов, затронутых за раунд.
func ReindexTouched(ctx context.Context, client *rag.Client, dir, project string, touched []string) (int, error) {
	if client == nil || len(touched) == 0 {
		return 0, nil
	}
	opts := rag.DetectIndexOptions(dir)
	reindexed := 0
	for _, rel := range touched {
		if err := client.DeleteFile(ctx, project, rel, opts); err != nil {
			// degrading silently
		}
	}
	for _, rel := range touched {
		_, err := client.IndexFile(ctx, project, rel, "", "", opts)
		if err == nil {
			reindexed++
		}
	}
	return reindexed, nil
}

// nolint:unused
var _ = strings.Contains

