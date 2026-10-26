package usecases

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ai/domain"
)

// fakeEscalationStore — in-memory EscalationStore для проверки политики
// эскалации без доски/Redis.
type fakeEscalationStore struct {
	task      *domain.Task
	statuses  []domain.Status
	comments  []domain.Comment
	getErr    error
	patchErr  error
	statusErr error
	missing   bool
}

func (f *fakeEscalationStore) GetTask(_ context.Context, id string) (*domain.Task, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.missing || f.task == nil || f.task.TaskID != id {
		return nil, nil
	}
	return f.task, nil
}

func (f *fakeEscalationStore) PatchTask(_ context.Context, _ string, mutate func(*domain.Task) error) error {
	if f.patchErr != nil {
		return f.patchErr
	}
	return mutate(f.task)
}

func (f *fakeEscalationStore) SetTaskStatus(_ context.Context, _ string, status domain.Status) error {
	if f.statusErr != nil {
		return f.statusErr
	}
	f.statuses = append(f.statuses, status)
	f.task.Status = status
	return nil
}

func (f *fakeEscalationStore) AddTaskComment(_ context.Context, _ string, c domain.Comment) ([]domain.Comment, error) {
	f.comments = append(f.comments, c)
	return f.comments, nil
}

func TestEscalateRetriesWithinBudget(t *testing.T) {
	store := &fakeEscalationStore{task: &domain.Task{
		TaskSpec:    domain.TaskSpec{TaskID: "T-1"},
		Escalations: 1,
	}}
	var statusCalls, stopReason string
	ok, err := Escalate(context.Background(), "T-1", EscalationDiag{
		Event: "зациклилась", Detail: "повтор WriteFiles", Action: "смени подход",
	}, EscalationPorts{
		Tasks:          store,
		MaxEscalations: 3,
		Status:         func(format string, args ...any) { statusCalls = format },
		SetStopReason:  func(reason string) { stopReason = reason },
	})
	if err != nil {
		t.Fatalf("Escalate: %v", err)
	}
	if !ok {
		t.Fatal("в пределах бюджета эскалация должна вернуть прогресс")
	}
	if store.task.Escalations != 2 {
		t.Errorf("счётчик эскалаций = %d, ожидалось 2", store.task.Escalations)
	}
	if store.task.ModelTier != domain.ModelTierLarge {
		t.Errorf("model_tier = %q, ожидалось %q", store.task.ModelTier, domain.ModelTierLarge)
	}
	if len(store.task.Injections) != 1 {
		t.Fatalf("инъекций = %d, ожидалась 1", len(store.task.Injections))
	}
	inj := store.task.Injections[0]
	if inj.Scope != domain.InjectionScopeRuntime {
		t.Errorf("scope инъекции = %q, ожидался runtime (task→runtime нормализация)", inj.Scope)
	}
	if inj.Target != domain.InjectionTargetUserLast {
		t.Errorf("target инъекции = %q, ожидался user_last", inj.Target)
	}
	if !strings.Contains(inj.Content, "повтор WriteFiles") || !strings.Contains(inj.Content, "смени подход") {
		t.Errorf("инъекция не содержит диагноз: %q", inj.Content)
	}
	if store.task.PauseReason != "" {
		t.Errorf("в пределах бюджета причина остановки не пишется, получено %q", store.task.PauseReason)
	}
	if stopReason != "" {
		t.Errorf("в пределах бюджета stopReason не ставится, получено %q", stopReason)
	}
	if len(store.statuses) != 0 {
		t.Errorf("в пределах бюджета SetTaskStatus не вызывается: %+v", store.statuses)
	}
	if statusCalls == "" {
		t.Error("аудит эскалации не отправлен в чат/лог")
	}
}

func TestEscalateStopsAndAsksHumanWhenBudgetExhausted(t *testing.T) {
	store := &fakeEscalationStore{task: &domain.Task{
		TaskSpec:    domain.TaskSpec{TaskID: "T-1"},
		Escalations: 3,
	}}
	var stopReason string
	ok, err := Escalate(context.Background(), "T-1", EscalationDiag{
		Event: "работа не сделана", Detail: "гард отклонил done", Action: "внеси правки",
	}, EscalationPorts{
		Tasks:          store,
		MaxEscalations: 3,
		SetStopReason:  func(reason string) { stopReason = reason },
	})
	if err != nil {
		t.Fatalf("Escalate: %v", err)
	}
	if !ok {
		t.Fatal("остановка задачи — тоже «прогресс» раунда (иначе цикл упадёт)")
	}
	if store.task.Status != domain.StatusCancelled {
		t.Errorf("статус = %q, ожидался cancelled", store.task.Status)
	}
	if !strings.Contains(store.task.PauseReason, "Бюджет автономии исчерпан") {
		t.Errorf("причина остановки не записана: %q", store.task.PauseReason)
	}
	if len(store.comments) != 1 || store.comments[0].Type != domain.CommentTypeSystem {
		t.Errorf("ожидался системный комментарий о форсмажоре, получено %+v", store.comments)
	}
	if !strings.Contains(stopReason, "бюджет автономии исчерпан") && !strings.Contains(stopReason, "Бюджет автономии исчерпан") {
		t.Errorf("stopReason не выставлен: %q", stopReason)
	}
	if len(store.task.Injections) != 0 {
		t.Errorf("при исчерпании бюджета инъекция не пишется, получено %d", len(store.task.Injections))
	}
}

func TestEscalateErrors(t *testing.T) {
	if _, err := Escalate(context.Background(), "T-1", EscalationDiag{}, EscalationPorts{}); err == nil {
		t.Error("эскалация без хранилища должна вернуть ошибку")
	}
	if _, err := Escalate(context.Background(), "T-1", EscalationDiag{}, EscalationPorts{
		Tasks: &fakeEscalationStore{getErr: errors.New("redis down")},
	}); err == nil {
		t.Error("ошибка чтения задачи должна вернуться наружу")
	}
	if _, err := Escalate(context.Background(), "T-1", EscalationDiag{}, EscalationPorts{
		Tasks: &fakeEscalationStore{missing: true},
	}); err == nil {
		t.Error("отсутствующая задача должна вернуть ошибку")
	}
}
