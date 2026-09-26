// Тесты учёта расхода токенов эпиками и задачами (Ф-1
// PLAN-2026-09-19-done-epic-task-token.md).
package board

import (
	"context"
	"encoding/json"
	"testing"
)

func TestTokenUsageJSONRoundTrip(t *testing.T) {
	e := Epic{TaskSpec: TaskSpec{TaskID: "ARC-01", Title: "Backend"}}
	e.Set(1200, 340)
	e.TokenEstimate = 2000
	data, err := json.Marshal(&e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got Epic
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.TokensInput != 1200 || got.TokensOutput != 340 || got.TokensTotal != 1540 {
		t.Errorf("факт не сохранился: %+v", got.TokenUsage)
	}
	if got.TokenEstimate != 2000 {
		t.Errorf("оценка не сохранилась: %d", got.TokenEstimate)
	}
	// Поля лежат на верхнем уровне объекта эпика (встроенная структура).
	fields := map[string]any{}
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("Unmarshal в map: %v", err)
	}
	for _, k := range []string{"tokens_in", "tokens_out", "tokens_total", "token_estimate"} {
		if _, ok := fields[k]; !ok {
			t.Errorf("ключ %q отсутствует в JSON эпика", k)
		}
	}
	// Нулевой факт не сериализуется — старая доска читается как есть.
	fresh := Task{TaskSpec: TaskSpec{TaskID: "T-1"}, EpicID: "ARC-01"}
	data, err = json.Marshal(&fresh)
	if err != nil {
		t.Fatalf("Marshal задачи: %v", err)
	}
	fields = map[string]any{}
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("Unmarshal в map: %v", err)
	}
	for _, k := range []string{"tokens_in", "tokens_out", "tokens_total", "token_estimate"} {
		if _, ok := fields[k]; ok {
			t.Errorf("ключ %q не должен сериализоваться при нулевом учёте", k)
		}
	}
}

func TestTokenUsageError(t *testing.T) {
	u := TokenUsage{}
	if _, ok := u.Error(); ok {
		t.Errorf("без оценки ошибка не считается")
	}
	u.TokenEstimate = 1000
	u.Set(1200, 300) // факт 1500 против оценки 1000 = 50%
	pct, ok := u.Error()
	if !ok || pct != 50 {
		t.Errorf("ошибка прогноза = %v (ok=%v), ожидалось 50", pct, ok)
	}
	u.Set(800, 200) // факт 1000 = оценка → 0%
	if pct, ok := u.Error(); !ok || pct != 0 {
		t.Errorf("точная оценка должна давать 0%%, получено %v (ok=%v)", pct, ok)
	}
}

func TestStoreTaskTokenFinalize(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "ARC-01", Title: "Backend"}}); err != nil {
		t.Fatalf("CreateEpic: %v", err)
	}
	if err := s.CreateTask(ctx, &Task{TaskSpec: TaskSpec{TaskID: "T-1"}, EpicID: "ARC-01"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Живой факт: накопление порциями.
	if _, err := s.AddTaskTokens(ctx, "T-1", 100, 20); err != nil {
		t.Fatalf("AddTaskTokens: %v", err)
	}
	if _, err := s.AddTaskTokens(ctx, "T-1", 50, 10); err != nil {
		t.Fatalf("AddTaskTokens: %v", err)
	}
	task, err := s.GetTask(ctx, "T-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.TokensTotal != 180 {
		t.Fatalf("накопленный факт задачи = %d, ожидалось 180", task.TokensTotal)
	}

	// Финализация перезаписывает итогом и идемпотентна.
	if err := s.FinalizeTaskTokens(ctx, "T-1", 200, 40); err != nil {
		t.Fatalf("FinalizeTaskTokens: %v", err)
	}
	if err := s.FinalizeTaskTokens(ctx, "T-1", 200, 40); err != nil {
		t.Fatalf("повторный FinalizeTaskTokens: %v", err)
	}
	task, err = s.GetTask(ctx, "T-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.TokensInput != 200 || task.TokensOutput != 40 || task.TokensTotal != 240 {
		t.Errorf("итог задачи = %+v, ожидалось in=200 out=40 total=240", task.TokenUsage)
	}

	// Отсутствующая задача — ErrNotFound, а не паника/тихая потеря.
	if err := s.FinalizeTaskTokens(ctx, "T-none", 1, 1); err != ErrNotFound {
		t.Errorf("FinalizeTaskTokens для несуществующей задачи: %v, ожидалось ErrNotFound", err)
	}
}

func TestStoreEpicTokenAccumulate(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "ARC-01"}}); err != nil {
		t.Fatalf("CreateEpic: %v", err)
	}
	if _, err := s.AddEpicTokens(ctx, "ARC-01", 10, 4); err != nil {
		t.Fatalf("AddEpicTokens: %v", err)
	}
	if err := s.FinalizeEpicTokens(ctx, "ARC-01", 12, 6); err != nil {
		t.Fatalf("FinalizeEpicTokens: %v", err)
	}
	e, err := s.GetEpic(ctx, "ARC-01")
	if err != nil {
		t.Fatalf("GetEpic: %v", err)
	}
	if e.TokensTotal != 18 {
		t.Errorf("итог эпика = %d, ожидалось 18", e.TokensTotal)
	}
}

func TestStoreTokenEstimateHook(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	s.TokenEstimate = func(_ context.Context, e *Epic, t *Task) int64 {
		if t != nil {
			return 700 // оценка задачи
		}
		return 900 // оценка эпика
	}
	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "ARC-01"}}); err != nil {
		t.Fatalf("CreateEpic: %v", err)
	}
	// Явная оценка в сущности не переоценивается.
	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "ARC-02"}, TokenUsage: TokenUsage{TokenEstimate: 10}}); err != nil {
		t.Fatalf("CreateEpic с оценкой: %v", err)
	}
	if err := s.CreateTask(ctx, &Task{TaskSpec: TaskSpec{TaskID: "T-1"}, EpicID: "ARC-01"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	e, _ := s.GetEpic(ctx, "ARC-01")
	if e.TokenEstimate != 900 {
		t.Errorf("оценка эпика = %d, ожидалось 900", e.TokenEstimate)
	}
	e2, _ := s.GetEpic(ctx, "ARC-02")
	if e2.TokenEstimate != 10 {
		t.Errorf("явная оценка эпика перезаписана: %d, ожидалось 10", e2.TokenEstimate)
	}
	task, _ := s.GetTask(ctx, "T-1")
	if task.TokenEstimate != 700 {
		t.Errorf("оценка задачи = %d, ожидалось 700", task.TokenEstimate)
	}
}

func TestStoreTokenTotals(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateEpic(ctx, &Epic{
		TaskSpec:   TaskSpec{TaskID: "ARC-01"},
		TokenUsage: TokenUsage{TokensInput: 300, TokensOutput: 100, TokensTotal: 400, TokenEstimate: 500},
	}); err != nil {
		t.Fatalf("CreateEpic: %v", err)
	}
	if err := s.CreateTask(ctx, &Task{
		TaskSpec:   TaskSpec{TaskID: "T-1"},
		EpicID:     "ARC-01",
		TokenUsage: TokenUsage{TokensInput: 90, TokensOutput: 10, TokensTotal: 100, TokenEstimate: 200},
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	// Задача без факта и без оценки в сводку не попадает.
	if err := s.CreateTask(ctx, &Task{TaskSpec: TaskSpec{TaskID: "T-2"}, EpicID: "ARC-01"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	tot, err := s.TokenTotals(ctx)
	if err != nil {
		t.Fatalf("TokenTotals: %v", err)
	}
	if tot.SpentTokens != 500 || tot.EstimatedTokens != 700 || tot.Measured != 2 {
		t.Fatalf("сводка = %+v, ожидалось spent=500 estimated=700 measured=2", tot)
	}
	pct, ok := tot.ErrorPct()
	if !ok || pct < 28.5 || pct > 28.6 {
		t.Errorf("ошибка прогноза = %v%% (ok=%v), ожидалось ~28.57%%", pct, ok)
	}

	// Пустая доска: ошибку считать нельзя.
	s2 := newTestStore(t)
	tot, err = s2.TokenTotals(ctx)
	if err != nil {
		t.Fatalf("TokenTotals (пустая доска): %v", err)
	}
	if _, ok := tot.ErrorPct(); ok {
		t.Errorf("на пустой доске ошибка прогноза не считается")
	}
}

func TestFinalizeTokensNeverShrink(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "ARC-01"}}); err != nil {
		t.Fatalf("CreateEpic: %v", err)
	}
	if err := s.CreateTask(ctx, &Task{TaskSpec: TaskSpec{TaskID: "T-1"}, EpicID: "ARC-01"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	// Итог записан (задача + лид + архитектор).
	if err := s.FinalizeTaskTokens(ctx, "T-1", 1000, 500); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeEpicTokens(ctx, "ARC-01", 3000, 1500); err != nil {
		t.Fatal(err)
	}
	// Повторная финализация после обнуления счётчиков (суммы меньше) не затирает факт.
	if err := s.FinalizeTaskTokens(ctx, "T-1", 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeEpicTokens(ctx, "ARC-01", 1500, 900); err != nil {
		t.Fatal(err)
	}
	task, _ := s.GetTask(ctx, "T-1")
	epic, _ := s.GetEpic(ctx, "ARC-01")
	if task.TokensTotal != 1500 {
		t.Errorf("факт задачи уменьшился: %d, ожидалось 1500", task.TokensTotal)
	}
	if epic.TokensTotal != 4500 {
		t.Errorf("факт эпика уменьшился: %d, ожидалось 4500", epic.TokensTotal)
	}
}
