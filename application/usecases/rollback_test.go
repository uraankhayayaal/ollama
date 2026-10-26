package usecases

import (
	"context"
	"errors"
	"testing"

	"ai/domain"
	"ai/domain/services"
)

// fakeBoard — минимальный BoardStore для отката: реализованы только
// используемые методы (остальное — nil через встраивание интерфейса).
type fakeBoard struct {
	services.BoardStore // встраивание интерфейса: неиспользуемые методы — nil

	patched bool
	cpSHA   string
	lastErr string
	agState string
	status  domain.Status
	setDone bool
}

func (f *fakeBoard) PatchTask(_ context.Context, _ string, mutate func(*domain.Task) error) error {
	t := &domain.Task{}
	if err := mutate(t); err != nil {
		return err
	}
	f.patched = true
	if t.Checkpoint != nil {
		f.cpSHA = t.Checkpoint.LastGoodSHA
	}
	f.lastErr = t.LastError
	f.agState = t.AgentState
	// Rollback использует PatchTask для возврата в очередь — фиксируем статус.
	if t.Status != "" {
		f.status = t.Status
		f.setDone = true
	}
	return nil
}

func (f *fakeBoard) SetTaskStatus(_ context.Context, _ string, s domain.Status) error {
	f.setDone = true
	f.status = s
	return nil
}

type fakeRollbacker struct {
	sha        string
	submodules []string
	err        error
	gotTarget  string
}

func (f *fakeRollbacker) RollbackWorktree(_ context.Context, _, target string) (string, []string, error) {
	f.gotTarget = target
	return f.sha, f.submodules, f.err
}

func TestRollbackTarget(t *testing.T) {
	cp := &domain.TaskCheckpoint{LastGoodSHA: "abc123", BaseSHA: "base456"}
	task := &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "T-1"}, Checkpoint: cp}

	tests := []struct {
		to      string
		want    string
		wantErr bool
	}{
		{"last_good", "abc123", false},
		{"base", "base456", false},
		{"deadbeef", "deadbeef", false}, // произвольный SHA
		{"", "", true},
		{"  ", "", true},
		{"bad target", "", true}, // пробел внутри
	}
	for _, tc := range tests {
		got, err := RollbackTarget(task, tc.to)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: ожидалась ошибка", tc.to)
			} else if !errors.Is(err, ErrBadTarget) {
				t.Errorf("%q: ошибка не ErrBadTarget: %v", tc.to, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: неожиданная ошибка: %v", tc.to, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.to, got, tc.want)
		}
	}

	// Без чекпойнта именованные точки недоступны, SHA — можно.
	empty := &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "T-2"}}
	if _, err := RollbackTarget(empty, "last_good"); !errors.Is(err, ErrBadTarget) {
		t.Errorf("last_good без чекпойнта: ожидался ErrBadTarget, got %v", err)
	}
	if got, err := RollbackTarget(empty, "cafe01"); err != nil || got != "cafe01" {
		t.Errorf("SHA без чекпойнта: got %q, %v", got, err)
	}
}

func TestRollbackTaskReturnsToQueue(t *testing.T) {
	store := &fakeBoard{}
	rb := &fakeRollbacker{sha: "aaaaaaa1", submodules: []string{"sub/mod"}}
	task := &domain.Task{
		TaskSpec:   domain.TaskSpec{TaskID: "T-9"},
		Status:     domain.StatusDevelopment,
		LastError:  "сборка упала",
		AgentState: "working",
		Checkpoint: &domain.TaskCheckpoint{BaseSHA: "b0"},
	}

	out, err := RollbackTask(context.Background(), store, task, "/tmp/wt", "base", rb)
	if err != nil {
		t.Fatalf("RollbackTask: %v", err)
	}
	if rb.gotTarget != "b0" {
		t.Fatalf("цель отката: got %q, want b0", rb.gotTarget)
	}
	if out.SHA != "aaaaaaa1" || out.Status != domain.StatusInfra {
		t.Fatalf("outcome: %#v", out)
	}
	if !store.patched || store.cpSHA != "aaaaaaa1" || store.lastErr != "" || store.agState != "" {
		t.Fatalf("чекпойнт не обновлён: %#v", store)
	}
	if !store.setDone || store.status != domain.StatusInfra {
		t.Fatalf("задача не вернулась в очередь: %#v", store)
	}
}

func TestRollbackTaskKeepsTerminalStatus(t *testing.T) {
	store := &fakeBoard{}
	rb := &fakeRollbacker{sha: "bbbbbbb2"}
	task := &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "T-3"}, Status: domain.StatusCancelled}

	out, err := RollbackTask(context.Background(), store, task, "/tmp/wt", "bbbbbbb2", rb)
	if err != nil {
		t.Fatalf("RollbackTask: %v", err)
	}
	if out.Status != domain.StatusCancelled {
		t.Fatalf("статус отменённой задачи не должен меняться: %q", out.Status)
	}
	if store.setDone {
		t.Fatal("отменённую задачу нельзя возвращать в очередь")
	}
}

func TestRollbackTaskNoWorktree(t *testing.T) {
	_, err := RollbackTask(context.Background(), &fakeBoard{}, &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "T-4"}}, "  ", "base", &fakeRollbacker{})
	if !errors.Is(err, ErrNoWorktree) {
		t.Fatalf("ожидался ErrNoWorktree, got %v", err)
	}
}

func TestRollbackTaskNoSuchCommit(t *testing.T) {
	store := &fakeBoard{}
	rb := &fakeRollbacker{err: ErrNoSuchCommit}
	task := &domain.Task{TaskSpec: domain.TaskSpec{TaskID: "T-5"}, Status: domain.StatusDevelopment}

	_, err := RollbackTask(context.Background(), store, task, "/tmp/wt", "deadbeef", rb)
	if !errors.Is(err, ErrNoSuchCommit) {
		t.Fatalf("ожидался ErrNoSuchCommit, got %v", err)
	}
	if store.patched {
		t.Fatal("чекпойнт не должен трогаться при неудачном откате")
	}
}
