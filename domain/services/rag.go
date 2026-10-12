package services

import "context"

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
