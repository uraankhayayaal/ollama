// Сборка обучающей выборки прогноза из доски проекта (Ф-5
// PLAN-2026-09-19-done-epic-task-token.md).
//
// В выборку попадают только завершённые (done) задачи и эпики с записанным
// фактом расхода — незавершённые единицы и записи без факта прогнозу не
// учат. Признаки (роль, длины заголовка/описания, число задач) берутся из
// сущностей доски, а факт — из board.TokenUsage.
package tokens

import (
	"context"

	"ai/board"
)

// HistoryStore — минимальный доступ к доске для сборки выборки (реализуется
// board.Store).
type HistoryStore interface {
	ListEpics(ctx context.Context) ([]*board.Epic, error)
	ListTasks(ctx context.Context) ([]*board.Task, error)
}

// SampleFromTask строит признаки задачи по её сущности доски.
func SampleFromTask(t *board.Task) Sample {
	if t == nil {
		return Sample{}
	}
	return Sample{
		IsEpic:   false,
		Role:     t.Assignee,
		TitleLen: len([]rune(t.Title)),
		DescLen:  len([]rune(t.Description)),
		Spent:    t.TokensTotal,
	}
}

// SampleFromEpic строит признаки эпика. numTasks — число его задач (передаётся
// отдельно: на момент создания эпика список задач пуст).
func SampleFromEpic(e *board.Epic, numTasks int) Sample {
	if e == nil {
		return Sample{}
	}
	role := e.AssignedRole
	if role == "" && numTasks == 0 {
		role = "architecture"
	}
	return Sample{
		IsEpic:   true,
		Role:     role,
		TitleLen: len([]rune(e.Title)),
		DescLen:  len([]rune(e.Description)),
		NumTasks: numTasks,
		Spent:    e.TokensTotal,
	}
}

// HistoryFromBoard собирает обучающую выборку из доски проекта: завершённые
// задачи и завершённые эпики с записанным фактом расхода.
func HistoryFromBoard(ctx context.Context, st HistoryStore) ([]Sample, error) {
	if st == nil {
		return nil, nil
	}
	tasks, err := st.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	epics, err := st.ListEpics(ctx)
	if err != nil {
		return nil, err
	}
	samples := make([]Sample, 0, len(tasks)+len(epics))
	for _, t := range tasks {
		if t.Status != board.StatusDone || t.TokensTotal <= 0 {
			continue
		}
		samples = append(samples, SampleFromTask(t))
	}
	for _, e := range epics {
		if e.Status != board.StatusDone || e.TokensTotal <= 0 {
			continue
		}
		samples = append(samples, SampleFromEpic(e, len(e.Tasks)))
	}
	return samples, nil
}
