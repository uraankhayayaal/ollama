// Индексация RAG проекта: сбор файлов рабочей копии и полная индексация
// (срез F3k Этапа 6). Оркестрация фоновых прогонов (single-flight, отчёты в
// чат) остаётся у вызывающего адаптера.
package rag

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	infrarag "ai/infrastructure/rag"
)

// WalkerFn — функция обхода проекта (обычно infrarag.WalkProject; инъекция для
// hermetic-тестов, чтобы не создавать реальные файлы).
type WalkerFn func(dir string) ([]string, error)

// CollectIndexItems обходит проект (walk) и собирает файлы в IndexItem'ы:
// путь, область (ScopeForPath) и содержимое. Нечитаемые файлы пропускаются,
// прерывание контекста распространяется наружу.
func CollectIndexItems(ctx context.Context, dir string, walk WalkerFn) ([]infrarag.IndexItem, error) {
	files, err := walk(dir)
	if err != nil {
		return nil, err
	}
	items := make([]infrarag.IndexItem, 0, len(files))
	for _, rel := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		content, rerr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if rerr != nil {
			continue
		}
		items = append(items, infrarag.IndexItem{
			Path:    rel,
			Scope:   infrarag.ScopeForPath(rel),
			Content: string(content),
		})
	}
	return items, nil
}

// IndexProjectRAG индексирует проект целиком: сначала коллекция (create-if-
// not-exists), затем сбор файлов и IndexProject с веткой/коммитом (полная
// переиндексация ветки — прежние версии чанков помечаются устаревшими,
// прогон идемпотентен).
func IndexProjectRAG(ctx context.Context, indexer Indexer, project, dir string, opts infrarag.IndexOptions) (*infrarag.IndexResult, error) {
	if _, err := indexer.EnsureCollection(ctx); err != nil {
		return nil, fmt.Errorf("подготовка коллекции: %w", err)
	}
	items, err := CollectIndexItems(ctx, dir, infrarag.WalkProject)
	if err != nil {
		return nil, fmt.Errorf("обход %s: %w", dir, err)
	}
	return indexer.IndexProject(ctx, project, items, opts)
}
