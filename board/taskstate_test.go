package board

import (
	"context"
	"testing"
)

// Ф-6 (этап 2): State Tracking. Проверяем, что состояние раунда переживает
// запись в Redis, PatchTask дёргает хук, а попытки считаются по взятию задачи
// в работу (а не по числу раундов агента).

// readyTestTask доводит задачу до «готова к работе» по честной цепочке статусов
// (FSM требует соседних переходов, минуть статус нельзя).
func readyTestTask(t *testing.T, s *Store, ctx context.Context, id string) {
	t.Helper()
	for _, st := range []Status{StatusAnalysis, StatusReady} {
		if err := s.SetTaskStatus(ctx, id, st); err != nil {
			t.Fatalf("%s -> %s: %v", id, st, err)
		}
	}
}

// stateTestTask создаёт эпик и задачу — задача на доске обязана ссылаться на
// эпик, иначе CreateTask её не примет.
func stateTestTask(t *testing.T, s *Store, ctx context.Context, id string) {
	t.Helper()
	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "E-1", Title: "epic"}}); err != nil {
		t.Fatalf("CreateEpic: %v", err)
	}
	if err := s.CreateTask(ctx, &Task{TaskSpec: TaskSpec{TaskID: id, Title: id, SequenceOrder: 1}, EpicID: "E-1"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
}

func TestTaskStateFieldsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	stateTestTask(t, s, ctx, "T-1")
	err := s.PatchTask(ctx, "T-1", func(t *Task) error {
		t.AgentState = AgentStateFixingErrors
		t.ActiveAgent = "backend-разработчик"
		t.HeartbeatAt = "2026-10-05T10:00:00Z"
		t.LastError = "go test — FAIL"
		t.Checkpoint = &TaskCheckpoint{BaseSHA: "aaa", LastSHA: "bbb", LastGoodSHA: "aaa"}
		return nil
	})
	if err != nil {
		t.Fatalf("PatchTask: %v", err)
	}
	got, err := s.GetTask(ctx, "T-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.AgentState != AgentStateFixingErrors || got.ActiveAgent != "backend-разработчик" {
		t.Fatalf("состояние не сохранилось: %+v", got)
	}
	if got.Checkpoint == nil || got.Checkpoint.BaseSHA != "aaa" || got.Checkpoint.LastSHA != "bbb" || got.Checkpoint.LastGoodSHA != "aaa" {
		t.Fatalf("чекпойнт не сохранился: %+v", got.Checkpoint)
	}
	if got.LastError != "go test — FAIL" || got.HeartbeatAt == "" {
		t.Fatalf("ошибка/пульс не сохранились: %+v", got)
	}
}

func TestPatchTaskFiresStateHookAndKeepsOtherFields(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	stateTestTask(t, s, ctx, "T-1")
	readyTestTask(t, s, ctx, "T-1")
	var hooked []string
	s.TaskStateHook = func(_ context.Context, t *Task) { hooked = append(hooked, t.TaskID) }
	if err := s.PatchTask(ctx, "T-1", func(t *Task) error { t.AgentState = AgentStateWritingCode; return nil }); err != nil {
		t.Fatalf("PatchTask: %v", err)
	}
	if len(hooked) != 1 || hooked[0] != "T-1" {
		t.Fatalf("хук состояния не вызван: %v", hooked)
	}
	got, _ := s.GetTask(ctx, "T-1")
	// Точечное обновление не имеет права затереть соседние поля: ими живут
	// статус, инъекции и учёт токенов.
	if got.Status != StatusReady || got.EpicID != "E-1" || got.SequenceOrder != 1 {
		t.Fatalf("PatchTask затёр соседние поля: %+v", got)
	}
	if err := s.PatchTask(ctx, "T-2", func(*Task) error { return nil }); err == nil {
		t.Fatal("PatchTask несуществующей задачи должен вернуть ошибку")
	}
}

func TestTaskAttemptsCountOnTakeToWork(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	stateTestTask(t, s, ctx, "T-1")
	readyTestTask(t, s, ctx, "T-1")
	if err := s.SetTaskStatus(ctx, "T-1", StatusInProgress); err != nil {
		t.Fatalf("ready -> in_progress: %v", err)
	}
	got, _ := s.GetTask(ctx, "T-1")
	if got.Attempts != 1 {
		t.Fatalf("попыток после первого взятия: %d, ожидалась 1", got.Attempts)
	}
	// Повторная запись того же статуса попыткой не считается: бюджет автономии
	// должен считать ВЗЯТИЯ задачи, а не дёрганья доски.
	if err := s.SetTaskStatus(ctx, "T-1", StatusInProgress); err == nil {
		// Повторный переход в тот же статус невалиден (ValidateTransition), но
		// даже если бы прошёл — счётчик не должен расти.
		t.Log("повторный переход отклонён ValidateTransition")
	}
	if err := s.SetTaskStatus(ctx, "T-1", StatusDone); err != nil {
		t.Fatalf("in_progress -> done: %v", err)
	}
	got, _ = s.GetTask(ctx, "T-1")
	if got.Attempts != 1 {
		t.Fatalf("попыток после выполнения: %d, ожидалась 1", got.Attempts)
	}
}
