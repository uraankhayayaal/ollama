package services

import (
	"ai/domain"
	"context"
)

// Searcher — порт семантического поиска RAG (реализуется infrastructure/rag).
type Searcher interface {
	Ping(ctx context.Context) error
	Search(ctx context.Context, p domain.SearchParams) ([]domain.SearchResult, error)
	ProjectInfo(ctx context.Context, projectName string) (domain.ProjectInfo, error)
}

type RAGClient interface {
	Search(ctx context.Context, query string, limit int) ([]RAGResult, error)
	Index(ctx context.Context, path string) error
	BranchIndexed(ctx context.Context, branch string) (bool, error)
}

type RAGResult struct {
	Path     string
	Content  string
	Score    float64
	Metadata map[string]string
}
