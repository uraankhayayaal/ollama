// Конкретные фоновые прогоны индексации RAG (срез F3t Этапа 6): содержимое
// ветки по ref, изменения ветки относительно базы и произвольный каталог.
// Раньше тела прогонов (загрузка снапшота, пропуск актуального индекса, отчёт)
// жили в HTTP-слое (`server/ragref.go`, `server/ragindex.go`); теперь они здесь,
// а сервер оставляет адаптеры (лог проекта, фабрику индексатора, каталог).
package rag

import (
	"context"
	"fmt"
	"strings"

	"ai/infrastructure/git"
	infrarag "ai/infrastructure/rag"
)

// RefLoader — загрузчик снапшота ветки по ref (обычно LoadRefItems; инъекция
// для hermetic-тестов).
type RefLoader func(ctx context.Context, ex git.Executor, dir, ref string) (RefSnapshot, error)

// DiffLoader — загрузчик изменений ветки относительно базы (обычно LoadRefDiff).
type DiffLoader func(ctx context.Context, ex git.Executor, dir, ref, base string) (RefSnapshot, error)

// RefJob индексирует содержимое ветки проекта из git (по ref). Основной
// сценарий — главная ветка после merge эпика. Если активные точки ветки на этом
// коммите уже есть — прогон пропускается (skip), чтобы не платить эмбеддинги
// всех файлов проекта на каждый старт задачи.
type RefJob struct {
	Project string
	Dir     string
	Exec    git.Executor
	Loader  RefLoader
	Sink    IndexSink
}

// Run выполняет прогон (сигнатура совпадает с IndexRun при фиксированном ref).
func (j RefJob) Run(ctx context.Context, indexer Indexer, ref string) (*infrarag.IndexResult, string, bool, error) {
	snap, err := j.Loader(ctx, j.Exec, j.Dir, ref)
	if err != nil {
		return nil, ref, false, err
	}
	skip, err := BranchIndexUpToDate(ctx, indexer, j.Project, snap.Branch, snap.CommitSHA)
	if err != nil || skip {
		return &infrarag.IndexResult{}, snap.Branch, skip, err
	}
	j.Sink.Info(fmt.Sprintf("rag: фоновая индексация %s: ветка %s из git (%s), файлов %d, коммит %s",
		j.Project, ref, j.Dir, len(snap.Items), orDash(snap.CommitSHA)))
	if _, err := indexer.EnsureCollection(ctx); err != nil {
		return nil, ref, false, fmt.Errorf("подготовка коллекции: %w", err)
	}
	res, err := indexer.IndexProject(ctx, j.Project, snap.Items, infrarag.IndexOptions{
		Branch:    snap.Branch,
		CommitSHA: snap.CommitSHA,
	})
	return res, snap.Branch, false, err
}

// RefDiffJob индексирует в ветке ref только файлы, которых нет в базовой ветке
// base (`git diff base...ref`) — изменения самой ветки. Основной сценарий —
// старт задачи; остальное покрывает индекс main.
type RefDiffJob struct {
	Project string
	Dir     string
	Base    string
	Exec    git.Executor
	Loader  DiffLoader
	Sink    IndexSink
}

// Run выполняет прогон (сигнатура совпадает с IndexRun при фиксированном ref).
func (j RefDiffJob) Run(ctx context.Context, indexer Indexer, ref string) (*infrarag.IndexResult, string, bool, error) {
	snap, err := j.Loader(ctx, j.Exec, j.Dir, ref, j.Base)
	if err != nil {
		return nil, ref, false, err
	}
	skip, err := BranchIndexUpToDate(ctx, indexer, j.Project, snap.Branch, snap.CommitSHA)
	if err != nil || skip {
		return &infrarag.IndexResult{}, snap.Branch, skip, err
	}
	j.Sink.Info(fmt.Sprintf("rag: фоновая индексация %s: изменения ветки %s относительно %s (%s), файлов %d, коммит %s",
		j.Project, ref, j.Base, j.Dir, len(snap.Items), orDash(snap.CommitSHA)))
	if _, err := indexer.EnsureCollection(ctx); err != nil {
		return nil, ref, false, fmt.Errorf("подготовка коллекции: %w", err)
	}
	res, err := indexer.IndexProject(ctx, j.Project, snap.Items, infrarag.IndexOptions{
		Branch:    snap.Branch,
		CommitSHA: snap.CommitSHA,
	})
	return res, snap.Branch, false, err
}

// DirJob индексирует произвольный каталог проекта (temp/<имя> или worktree
// задачи) под веткой, определённой ДО запуска (opts вычисляет адаптер — каталог
// мог исчезнуть уже после). what — подпись источника для логов.
type DirJob struct {
	Project string
	Dir     string
	What    string
	Opts    infrarag.IndexOptions
	Sink    IndexSink
}

// Run выполняет прогон (сигнатура совпадает с IndexRun).
func (j DirJob) Run(ctx context.Context, indexer Indexer) (*infrarag.IndexResult, string, bool, error) {
	j.Sink.Info(fmt.Sprintf("rag: фоновая индексация %s (%s): обход %s (ветка %s, коммит %s)",
		j.Project, j.What, j.Dir, j.Opts.Branch, orDash(j.Opts.CommitSHA)))
	res, err := IndexProjectRAG(ctx, indexer, j.Project, j.Dir, j.Opts)
	return res, j.Opts.Branch, false, err
}

// orDash — замена пустого значения в логах.
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
