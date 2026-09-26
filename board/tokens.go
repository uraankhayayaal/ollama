// Учёт расхода LLM-токенов эпиками и задачами (Ф-1
// PLAN-2026-09-19-done-epic-task-token.md).
//
// Пока единица в работе, токены копятся в счётчиках по scope (tokens.Store:
// task:<id>, epic:<id>) — это горутино-безопасные HINCRBY-ключи, доступные с
// любого потока. В доску факт попадает двумя способами:
//
//	AddTaskTokens/AddEpicTokens   — накопить порцию в сущность (для «живого»
//	                                факта в UI, пока единица работает);
//	FinalizeTaskTokens/…          — записать итог (вход/выход) терминальной
//	                                единицы; вызывается оркестратором при
//	                                переводе в done/cancelled.
//
// Записи сущностей — read-modify-write без блокировки (как SetTaskStatus),
// поэтому вызывать их нужно из одного потока — агентского цикла. Именно так и
// устроено: в доску пишет только оркестратор, а не обработчик событий.
package board

import (
	"context"
	"fmt"
)

// AddTaskTokens накапливает порцию токенов в факт задачи и возвращает
// обновлённый учёт. Счётчик (scope task:<id>) при этом не трогается — вызов
// нужен, когда порция уже снята со счётчика и её надо отразить на доске.
func (s *Store) AddTaskTokens(ctx context.Context, id string, in, out int64) (TokenUsage, error) {
	t, err := s.GetTask(ctx, id)
	if err != nil {
		return TokenUsage{}, err
	}
	t.Add(in, out)
	if err := s.SaveTask(ctx, t); err != nil {
		return TokenUsage{}, err
	}
	return t.TokenUsage, nil
}

// AddEpicTokens накапливает порцию токенов в факт эпика.
func (s *Store) AddEpicTokens(ctx context.Context, id string, in, out int64) (TokenUsage, error) {
	e, err := s.GetEpic(ctx, id)
	if err != nil {
		return TokenUsage{}, err
	}
	e.Add(in, out)
	if err := s.SaveEpic(ctx, e); err != nil {
		return TokenUsage{}, err
	}
	return e.TokenUsage, nil
}

// FinalizeTaskTokens записывает итоговый расход токенов задачи (факт на момент
// перевода в терминальный статус). Оценка (TokenEstimate) сохраняется — по
// ней считается ошибка прогноза. Идемпотентно: повторный вызов с теми же
// суммами не меняет запись, а вызов с меньшей суммой (счётчик уже обнулён) не
// затирает записанный итог — факт не убывает.
func (s *Store) FinalizeTaskTokens(ctx context.Context, id string, in, out int64) error {
	t, err := s.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if t.TokensInput == in && t.TokensOutput == out {
		return nil
	}
	if t.TokensTotal > in+out {
		return nil
	}
	t.Set(in, out)
	return s.SaveTask(ctx, t)
}

// FinalizeEpicTokens записывает итоговый расход токенов эпика (факт на момент
// перевода в done/cancelled). Оценка сохраняется. Идемпотентно; факт не
// убывает (см. FinalizeTaskTokens).
func (s *Store) FinalizeEpicTokens(ctx context.Context, id string, in, out int64) error {
	e, err := s.GetEpic(ctx, id)
	if err != nil {
		return err
	}
	if e.TokensInput == in && e.TokensOutput == out {
		return nil
	}
	if e.TokensTotal > in+out {
		return nil
	}
	e.Set(in, out)
	return s.SaveEpic(ctx, e)
}

// TokenTotals — сводка расхода токенов по доске проекта: сумма факта, сумма
// прогноза и доля записей с записанным фактом (для оценки качества прогноза
// в UI). Единицы без факта и без оценки в счётчики не попадают.
type TokenTotals struct {
	// SpentTokens — фактически потрачено токенов (задачи + эпики).
	SpentTokens int64 `json:"spent_tokens"`
	// EstimatedTokens — сумма прогнозов тех же единиц.
	EstimatedTokens int64 `json:"estimated_tokens"`
	// Measured — число единиц с записанным фактом.
	Measured int `json:"measured"`
}

// ErrorPct возвращает среднюю ошибку прогноза в процентах по измеренным
// единицам. ok=false, если измеренных единиц с прогнозом нет.
func (t TokenTotals) ErrorPct() (pct float64, ok bool) {
	if t.EstimatedTokens <= 0 || t.Measured == 0 {
		return 0, false
	}
	diff := float64(t.SpentTokens - t.EstimatedTokens)
	if diff < 0 {
		diff = -diff
	}
	return diff / float64(t.EstimatedTokens) * 100, true
}

// TokenTotals собирает сводку расхода токенов по всем задачам и эпикам доски.
func (s *Store) TokenTotals(ctx context.Context) (TokenTotals, error) {
	var out TokenTotals
	tasks, err := s.ListTasks(ctx)
	if err != nil {
		return out, fmt.Errorf("board: сводка токенов: %w", err)
	}
	for _, t := range tasks {
		out.add(t.TokenUsage)
	}
	epics, err := s.ListEpics(ctx)
	if err != nil {
		return out, fmt.Errorf("board: сводка токенов: %w", err)
	}
	for _, e := range epics {
		out.add(e.TokenUsage)
	}
	return out, nil
}

func (t *TokenTotals) add(u TokenUsage) {
	if u.TokensTotal <= 0 && u.TokenEstimate <= 0 {
		return
	}
	t.SpentTokens += u.TokensTotal
	t.EstimatedTokens += u.TokenEstimate
	if u.TokensTotal > 0 && u.TokenEstimate > 0 {
		t.Measured++
	}
}
