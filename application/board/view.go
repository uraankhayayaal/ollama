// Package board содержит read-model Kanban-доски: сборку упорядоченного снимка
// (эпики в эффективном порядке очереди лидов, задачи, баги, meta) и пагинацию
// снимка. Вынесено из HTTP-слоя (`server/session.go`, Этап 6): порядок строк
// доски совпадает с порядком, в котором эпики берёт оркестратор — это правило
// предметной области, а не серверная обвязка. Git-статус проекта в снимок не
// входит (его добавляет сервер: это презентация git-flow, завязанная на реестр
// проектов).
package board

import (
	"context"

	iboard "ai/infrastructure/board"
)

// Store — источник данных доски (реализуется *infrastructure/board.Store).
type Store interface {
	ListEpics(ctx context.Context) ([]*iboard.Epic, error)
	ListTasks(ctx context.Context) ([]*iboard.Task, error)
	ListBugReports(ctx context.Context) ([]*iboard.BugReport, error)
	GetMeta(ctx context.Context) (*iboard.Meta, error)
}

// Snapshot — полный снимок доски для REST/SSE.
type Snapshot struct {
	Meta  *iboard.Meta        `json:"meta,omitempty"`
	Epics []*iboard.Epic      `json:"epics"`
	Tasks []*iboard.Task      `json:"tasks"`
	Bugs  []*iboard.BugReport `json:"bugs"`
	Total *Total              `json:"total,omitempty"` // счётчики всех элементов (Ф-3: пагинация)
}

// Total — полные счётчики доски (когда снимок ограничен limit/offset).
type Total struct {
	Epics int `json:"epics"`
	Tasks int `json:"tasks"`
	Bugs  int `json:"bugs"`
}

// View собирает снимок доски: эпики, задачи, баги и meta. Порядок эпиков
// приводится к эффективному порядку очереди лидов (фаза → приоритет →
// sequence_order → task_id, см. infra SortEpics) — так строки доски идут ровно
// в том порядке, в каком эпики берёт оркестратор, и перетаскивание строк меняет
// реальную очередь.
func View(ctx context.Context, store Store) (Snapshot, error) {
	epics, err := store.ListEpics(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	iboard.SortEpics(epics)
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	bugs, err := store.ListBugReports(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	meta, _ := store.GetMeta(ctx)
	return Snapshot{Meta: meta, Epics: epics, Tasks: tasks, Bugs: bugs}, nil
}

// ViewPage ограничивает снимок доски страницей (limit/limit+offset=0 — полный
// снимок) и дополняет его полными счётчиками.
func ViewPage(ctx context.Context, store Store, limit, offset int) (Snapshot, error) {
	v, err := View(ctx, store)
	if err != nil {
		return Snapshot{}, err
	}
	total := &Total{Epics: len(v.Epics), Tasks: len(v.Tasks), Bugs: len(v.Bugs)}
	if limit <= 0 {
		v.Total = total
		return v, nil
	}
	if offset < 0 {
		offset = 0
	}
	v.Epics = SlicePage(v.Epics, offset, limit)
	v.Tasks = SlicePage(v.Tasks, offset, limit)
	v.Bugs = SlicePage(v.Bugs, offset, limit)
	v.Total = total
	return v, nil
}

// SlicePage возвращает подмножество [offset, offset+limit) из слайса любого типа.
func SlicePage[T any](in []T, offset, limit int) []T {
	if offset >= len(in) {
		return []T{}
	}
	end := offset + limit
	if end > len(in) {
		end = len(in)
	}
	return in[offset:end]
}

// TruncateText обрезает длинный текст для статусных сообщений.
func TruncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
