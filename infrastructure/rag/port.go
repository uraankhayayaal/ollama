// Адаптер RAG-клиента к порту домена (Этап 4, срез E2).
//
// `rag.Client` — богатый инфраструктурный API: каждый метод принимает имя
// проекта явным аргументом, а поиск/индексация оперируют веткой и коммитом.
// Порт `domain/services.RAGClient` — узкий и потребительский: он предполагает
// одного потребителя, привязанного к проекту и его рабочему каталогу.
// `ProjectAdapter` сводит одно к другому, чтобы `application`/`tools` могли
// зависеть от `services.RAGClient`, а не от конкретного `*rag.Client`.

package rag

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ai/domain/services"
)

// ProjectAdapter — реализация `services.RAGClient` в рамках одного проекта.
// project — имя проекта (payload project_name), dir — рабочий каталог проекта
// (используется для определения ветки/коммита, если не задан явно).
type ProjectAdapter struct {
	client  *Client
	project string
	dir     string
}

// NewProjectAdapter создаёт адаптер поверх клиента RAG. dir может быть пустым —
// тогда ветка/коммит не определяются (проекты без Git).
func NewProjectAdapter(c *Client, project, dir string) *ProjectAdapter {
	return &ProjectAdapter{client: c, project: strings.TrimSpace(project), dir: strings.TrimSpace(dir)}
}

// Project — имя проекта, к которому привязан адаптер.
func (p *ProjectAdapter) Project() string { return p.project }

// Search реализует `services.RAGClient`: семантический поиск по индексу
// проекта (ветка по умолчанию — main). Score приводится к float64, а
// версионные маркеры (ветка/коммит/chunk_id/строки) уходят в Metadata.
func (p *ProjectAdapter) Search(ctx context.Context, query string, limit int) ([]services.RAGResult, error) {
	res, err := p.client.Search(ctx, SearchParams{Project: p.project, Query: query, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]services.RAGResult, 0, len(res))
	for _, r := range res {
		out = append(out, services.RAGResult{
			Path:    r.File,
			Content: r.Snippet,
			Score:   float64(r.Score),
			Metadata: map[string]string{
				"branch":   r.Branch,
				"commit":   r.CommitSHA,
				"chunk_id": r.ChunkID,
				"lines":    strconv.Itoa(r.StartLine) + "-" + strconv.Itoa(r.EndLine),
			},
		})
	}
	return out, nil
}

// Index реализует `services.RAGClient`: полная индексация дерева файлов в path
// (пусто — рабочий каталог адаптера). Ветка/коммит определяются по состоянию
// git этого каталога (`DetectIndexOptions`), коллекция создаётся лениво.
func (p *ProjectAdapter) Index(ctx context.Context, path string) error {
	dir := strings.TrimSpace(path)
	if dir == "" {
		dir = p.dir
	}
	if dir == "" {
		return fmt.Errorf("rag: не задан каталог для индексации проекта %s", p.project)
	}
	if _, err := p.client.EnsureCollection(ctx); err != nil {
		return err
	}
	items, err := collectIndexItems(ctx, dir)
	if err != nil {
		return fmt.Errorf("rag: обход %s: %w", dir, err)
	}
	_, err = p.client.IndexProject(ctx, p.project, items, DetectIndexOptions(dir))
	return err
}

// BranchIndexed реализует `services.RAGClient`: проиндексирована ли ветка на
// текущем коммите рабочего каталога. Проект без Git (коммит не определился)
// даёт false — сравнение версий невозможно.
func (p *ProjectAdapter) BranchIndexed(ctx context.Context, branch string) (bool, error) {
	commit, _ := DetectCommit(p.dir)
	return p.client.BranchIndexed(ctx, p.project, branch, commit)
}

// collectIndexItems обходит каталог (WalkProject) и собирает файлы в IndexItem:
// путь, область (ScopeForPath) и содержимое. Нечитаемые файлы пропускаются,
// отмена контекста прерывает обход.
func collectIndexItems(ctx context.Context, dir string) ([]IndexItem, error) {
	files, err := WalkProject(dir)
	if err != nil {
		return nil, err
	}
	items := make([]IndexItem, 0, len(files))
	for _, rel := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		content, rerr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if rerr != nil {
			continue
		}
		items = append(items, IndexItem{
			Path:    rel,
			Scope:   ScopeForPath(rel),
			Content: string(content),
		})
	}
	return items, nil
}

// Compile-time проверка: адаптер реализует порт `domain/services.RAGClient`.
var _ services.RAGClient = (*ProjectAdapter)(nil)
