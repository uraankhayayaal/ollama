// Тесты прогноза расхода токенов (Ф-5
// PLAN-2026-09-19-done-epic-task-token.md): деградация на малой истории,
// работоспособность модели на синтетике с сильной связью и раздельная сборка
// истории из доски.
package tokens

import (
	"context"
	"errors"
	"testing"

	"ai/board"
)

// fakeHistory — подставная доска для HistoryFromBoard.
type fakeHistory struct {
	epics []*board.Epic
	tasks []*board.Task
	err   error
}

func (f fakeHistory) ListEpics(context.Context) ([]*board.Epic, error) {
	return f.epics, f.err
}

func (f fakeHistory) ListTasks(context.Context) ([]*board.Task, error) {
	return f.tasks, f.err
}

func TestPredictorNoHistory(t *testing.T) {
	p := NewPredictor(nil)
	if p.Trained() {
		t.Errorf("предиктор без истории не должен быть обучен")
	}
	est := p.Predict(Sample{IsEpic: true, Role: "backendlead", DescLen: 100})
	if est.Estimate != 0 || est.Basis != BasisNone {
		t.Fatalf("без истории ожидался отказ, получено %+v", est)
	}
}

func TestPredictorSmallHistoryUsesMedian(t *testing.T) {
	// Три примера — меньше порога обучения (minPredictSamples).
	samples := []Sample{
		{IsEpic: true, Role: "backendlead", DescLen: 100, Spent: 1000},
		{IsEpic: true, Role: "backendlead", DescLen: 120, Spent: 3000},
		{IsEpic: true, Role: "frontendlead", DescLen: 90, Spent: 2000},
	}
	p := NewPredictor(samples)
	if p.Trained() {
		t.Fatalf("на 3 примерах модель обучаться не должна")
	}
	est := p.Predict(Sample{IsEpic: true, Role: "backendlead", DescLen: 110})
	if est.Estimate != 2000 || est.Basis != BasisRoleMedian {
		t.Fatalf("оценка по роли = %+v, ожидалось 2000/%s", est, BasisRoleMedian)
	}
	// Роли без истории — медиана по проекту.
	est = p.Predict(Sample{Role: "qaengineer", DescLen: 50})
	if est.Estimate != 2000 || est.Basis != BasisGlobalMedian {
		t.Fatalf("оценка без роли = %+v, ожидалось 2000/%s", est, BasisGlobalMedian)
	}
	if est.MAE != 0 {
		t.Errorf("у медианы MAE должен быть нулевым, получено %v", est.MAE)
	}
}

func TestPredictorTrainedOnSyntheticSignal(t *testing.T) {
	// Синтетика с сильной связью: расход растёт с длиной описания и числом
	// задач эпика. Модель обязана выучить эту связь.
	var samples []Sample
	for i := 1; i <= 24; i++ {
		samples = append(samples,
			Sample{IsEpic: false, Role: "developer", TitleLen: 20, DescLen: 200 * i, Spent: int64(1000 * i)},
			Sample{IsEpic: true, Role: "backendlead", TitleLen: 20, DescLen: 400 * i, NumTasks: i, Spent: int64(3000 * i)},
		)
	}
	p := NewPredictor(samples)
	if !p.Trained() {
		t.Fatalf("на 48 примерах модель должна обучиться")
	}
	// Задача с описанием вдвое меньше последней (i=12) — расход ~ в 2 раза ниже.
	got := p.Predict(Sample{Role: "developer", TitleLen: 20, DescLen: 2400})
	if got.Basis != BasisModel {
		t.Fatalf("basis = %q, want %s", got.Basis, BasisModel)
	}
	if got.Estimate < 2000 || got.Estimate > 30000 {
		t.Errorf("прогноз задачи = %d, вне разумного диапазона 2000..30000", got.Estimate)
	}
	// Эпик с 6 задачами должен оцениваться выше задачи-«одиночки».
	epic := p.Predict(Sample{IsEpic: true, Role: "backendlead", TitleLen: 20, DescLen: 2400, NumTasks: 6})
	if epic.Estimate <= got.Estimate {
		t.Errorf("эпик с задачами (%d) должен стоить больше задачи (%d)", epic.Estimate, got.Estimate)
	}
	if got.MAE <= 0 || got.MAE > 100 {
		t.Errorf("MAE модели = %v%%, ожидалось осмысленное значение 0..100", got.MAE)
	}
}

func TestPredictorDegenerateHistoryFallsBack(t *testing.T) {
	// История из одинаковых записей: матрица признаков вырождена (кроме
	// перехвата) — модель не обучается, работает медиана.
	samples := []Sample{
		{Role: "developer", DescLen: 100, Spent: 5000},
		{Role: "developer", DescLen: 100, Spent: 5000},
		{Role: "developer", DescLen: 100, Spent: 5000},
		{Role: "developer", DescLen: 100, Spent: 5000},
	}
	p := NewPredictor(samples)
	// Регуляризация не даёт модели «уехать» на вырожденной истории: даже при
	// обучении прогноз держится около среднего расхода и не взрывается на
	// невиданных размерах описания.
	est := p.Predict(Sample{Role: "developer", DescLen: 9999})
	if est.Estimate < 500 || est.Estimate > 10000 {
		t.Errorf("на вырожденной истории прогноз = %+v, ожидался порядок факта (5000)", est)
	}
	// Модель без ролей-признаков (все записи одной роли) обязана давать
	// правдоподобный результат, а не NaN/отказ.
	if est.Basis == BasisNone {
		t.Errorf("на вырожденной, но непустой истории ожидался прогноз, получен отказ")
	}
	// Ноль фактов вовсе не считается историей.
	p = NewPredictor([]Sample{{Role: "developer", Spent: 0}, {Role: "developer", Spent: 0}})
	if est := p.Predict(Sample{Role: "developer"}); est.Basis != BasisNone {
		t.Errorf("нулевая история должна давать отказ, получено %+v", est)
	}
}

func TestHistoryFromBoard(t *testing.T) {
	st := fakeHistory{
		epics: []*board.Epic{
			{TaskSpec: board.TaskSpec{TaskID: "e-done", Title: "Готово", Description: "описание эпика", AssignedRole: "backendlead"},
				Status: board.StatusDone, Tasks: []string{"t1", "t2"},
				TokenUsage: board.TokenUsage{TokensTotal: 8000}},
			{TaskSpec: board.TaskSpec{TaskID: "e-new", Title: "В работе"}, Status: board.StatusInProgress,
				TokenUsage: board.TokenUsage{TokensTotal: 400}},
			{TaskSpec: board.TaskSpec{TaskID: "e-nofact"}, Status: board.StatusDone},
		},
		tasks: []*board.Task{
			{TaskSpec: board.TaskSpec{TaskID: "t1", Title: "Задача", Description: "описание задачи"},
				Assignee: "developer", Status: board.StatusDone,
				TokenUsage: board.TokenUsage{TokensTotal: 2500}},
			{TaskSpec: board.TaskSpec{TaskID: "t2"}, Assignee: "developer", Status: board.StatusDone},
			{TaskSpec: board.TaskSpec{TaskID: "t3"}, Assignee: "developer", Status: board.StatusInProgress,
				TokenUsage: board.TokenUsage{TokensTotal: 999}},
		},
	}
	samples, err := HistoryFromBoard(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	// Только завершённые единицы с фактом: t1 и e-done.
	if len(samples) != 2 {
		t.Fatalf("выборка = %d записей, ожидалось 2: %+v", len(samples), samples)
	}
	if samples[0].IsEpic || samples[0].Role != "developer" || samples[0].Spent != 2500 {
		t.Errorf("признаки задачи неверны: %+v", samples[0])
	}
	if !samples[1].IsEpic || samples[1].Role != "backendlead" || samples[1].NumTasks != 2 || samples[1].Spent != 8000 {
		t.Errorf("признаки эпика неверны: %+v", samples[1])
	}

	// nil-доска — пустая выборка без ошибки.
	if s, err := HistoryFromBoard(context.Background(), nil); err != nil || len(s) != 0 {
		t.Errorf("HistoryFromBoard(nil) = %v, %v", s, err)
	}
	// Ошибка доски пробрасывается.
	if _, err := HistoryFromBoard(context.Background(), fakeHistory{err: errors.New("redis упал")}); err == nil {
		t.Errorf("ошибка доски должна пробрасываться")
	}
}

func TestSampleFromEntities(t *testing.T) {
	// Длины считаются в рунах, а не в байтах (кириллица).
	s := SampleFromTask(&board.Task{
		TaskSpec:   board.TaskSpec{TaskID: "t1", Title: "Задача", Description: "Описание"},
		Assignee:   "developer",
		TokenUsage: board.TokenUsage{TokensTotal: 100},
	})
	if s.TitleLen != 6 || s.DescLen != 8 {
		t.Errorf("длины в рунах: title=%d desc=%d, ожидалось 6/8", s.TitleLen, s.DescLen)
	}
	// Эпик без роли и задач — считаем работу архитектора.
	e := SampleFromEpic(&board.Epic{TaskSpec: board.TaskSpec{TaskID: "e1"}}, 0)
	if e.Role != "architecture" {
		t.Errorf("роль эпика без assigned_role = %q, ожидалось architecture", e.Role)
	}
	// nil-сущности не падают.
	if got := SampleFromTask(nil); got != (Sample{}) {
		t.Errorf("SampleFromTask(nil) = %+v, ожидался нулевой Sample", got)
	}
	if got := SampleFromEpic(nil, 0); got != (Sample{}) {
		t.Errorf("SampleFromEpic(nil, 0) = %+v, ожидался нулевой Sample", got)
	}
}
