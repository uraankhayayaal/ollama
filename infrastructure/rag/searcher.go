package rag

import "context"

type Searcher interface {
	Ping(ctx context.Context) error
	Search(ctx context.Context, p SearchParams) ([]SearchResult, error)
	ProjectInfo(ctx context.Context, projectName string) (ProjectInfo, error)
}
