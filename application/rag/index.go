// Индексация RAG проекта: сбор файлов рабочей копии и полная индексация
// (срез F3k Этапа 6). Оркестрация фоновых прогонов (single-flight, отчёты в
// чат) остаётся у вызывающего адаптера.
package rag

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"ai/domain"
)

// WalkerFn — функция обхода проекта (обычно infrastructure/rag.WalkProject;
// инъекция для hermetic-тестов, чтобы не создавать реальные файлы).
type WalkerFn func(dir string) ([]string, error)

// projectWalker — обходчик рабочей копии проекта для IndexProjectRAG. Задаётся
// в composition root (main.go) через SetProjectWalker — application-слой не
// импортирует infrastructure/rag (Этап 8, срез H3b).
var projectWalker WalkerFn

// SetProjectWalker подключает обходчик проекта (обычно infrastructure/rag.WalkProject).
func SetProjectWalker(w WalkerFn) { projectWalker = w }

// CollectIndexItems обходит проект (walk) и собирает файлы в IndexItem'ы:
// путь, область (ScopeForPath) и содержимое. Нечитаемые файлы пропускаются,
// прерывание контекста распространяется наружу.
func CollectIndexItems(ctx context.Context, dir string, walk WalkerFn) ([]domain.IndexItem, error) {
	files, err := walk(dir)
	if err != nil {
		return nil, err
	}
	items := make([]domain.IndexItem, 0, len(files))
	for _, rel := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		content, rerr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if rerr != nil {
			continue
		}
		items = append(items, domain.IndexItem{
			Path:    rel,
			Scope:   domain.ScopeForPath(rel),
			Content: string(content),
		})
	}
	return items, nil
}

// IndexProjectRAG индексирует проект целиком: сначала коллекция (create-if-
// not-exists), затем сбор файлов и IndexProject с веткой/коммитом (полная
// переиндексация ветки — прежние версии чанков помечаются устаревшими,
// прогон идемпотентен).
func IndexProjectRAG(ctx context.Context, indexer Indexer, project, dir string, opts domain.IndexOptions) (*domain.IndexResult, error) {
	if _, err := indexer.EnsureCollection(ctx); err != nil {
		return nil, fmt.Errorf("подготовка коллекции: %w", err)
	}
	if projectWalker == nil {
		return nil, fmt.Errorf("обходчик проекта не настроен (SetProjectWalker)")
	}
	items, err := CollectIndexItems(ctx, dir, projectWalker)
	if err != nil {
		return nil, fmt.Errorf("обход %s: %w", dir, err)
	}
	return indexer.IndexProject(ctx, project, items, opts)
}
