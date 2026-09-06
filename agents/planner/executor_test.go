package planner

import (
	"ai/agents"
	"ai/checkpoint"
	"ai/runner"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// stubProvider — фейковый провайдер, который падает при вызове. Используется,
// чтобы доказать, что при resume шаги НЕ выполняются.
// lengthEmpty — если установлен, Generate возвращает «обрезку по лимиту»
// с пустым содержанием (модель не создала код).
type stubProvider struct {
	errors      bool
	lengthEmpty bool
}

func (s *stubProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	if s.errors {
		return nil, genErr("Generate вызван, а не должен был")
	}
	if s.lengthEmpty {
		return &runner.AgentResponse{Content: "", Truncated: true}, nil
	}
	return &runner.AgentResponse{}, nil
}

func (s *stubProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

// newExecutorStore создаёт чекпоинт-хранилище поверх in-memory Redis.
func newExecutorStore(t *testing.T, key string) *checkpoint.Store {
	t.Helper()
	srv := miniredis.RunT(t)
	store, err := checkpoint.NewStore(context.Background(), checkpoint.StoreConfig{
		Addr: srv.Addr(),
		Key:  key,
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func TestExecutorResumeSkipsCompleted(t *testing.T) {
	ctx := context.Background()
	store := newExecutorStore(t, "checkpoint:resume")
	defer store.Close()

	plan := &Plan{
		ProjectName: "resumeProj",
		Summary:     "тест resume",
		Steps: []Step{
			{ID: "s1", Agent: AgentCodeGenerator, Prompt: "сделай", Description: "генерация"},
			{ID: "s2", Agent: AgentCodeReviewer, Prompt: "проверь", DependsOn: []string{"s1"}, Description: "ревью"},
		},
	}

	// Первый запуск завершил оба шага (сохранили чекпоинт). Resume должен
	// пропустить оба шага, не выполняя их.
	planJSON, _ := json.Marshal(plan)
	firstSnap := &checkpoint.Snapshot{
		ProjectName: plan.ProjectName,
		Summary:     plan.Summary,
		PlanJSON:    planJSON,
		Completed:   map[string]bool{"s1": true, "s2": true},
		Statuses: map[string]string{
			"s1": checkpoint.StatusDone,
			"s2": checkpoint.StatusDone,
		},
	}
	if err := store.Save(ctx, firstSnap); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Провайдер настроен на ошибку: если шаг реально выполнится — тест упадёт.
	exec := NewExecutor(&stubProvider{errors: true}, plan).SetCheckpoint(store, true)
	if err := exec.Run(ctx); err != nil {
		t.Fatalf("Run с resume: %v", err)
	}

	if !exec.completed["s1"] || !exec.completed["s2"] {
		t.Fatalf("оба шага должны быть отмечены завершёнными, completed=%v", exec.completed)
	}

	// Чекпоинт не должен быть перезаписан с нуля: оба шага по-прежнему done.
	snap, lerr := store.Load(ctx)
	if lerr != nil {
		t.Fatalf("Load: %v", lerr)
	}
	if !snap.Completed["s1"] || !snap.Completed["s2"] {
		t.Fatalf("чекпоинт потерял завершённые шаги: %v", snap.Completed)
	}
}

func TestExecutorFreshRunSavesSnapshot(t *testing.T) {
	ctx := context.Background()
	store := newExecutorStore(t, "checkpoint:fresh")
	defer store.Close()

	plan := &Plan{
		ProjectName: "freshProj",
		Summary:     "тест свежего старта",
		Steps: []Step{
			{ID: "s1", Agent: AgentCodeReviewer, Prompt: "проверь", Description: "ревью"},
		},
	}

	// s2 (зависящий от s1) помечен done в сохранённом чекпоинте, но в новом
	// плане его нет — свежий запуск не должен учитывать старый чекпоинт при
	// resume=false: snapshot перезаписывается с нуля.
	exec := NewExecutor(&stubProvider{errors: true}, plan).SetCheckpoint(store, false)

	// При resume=false initCheckpoint сохраняет свежий снапшот, а затем
	// выполнение шага упадёт (провайдер с ошибкой) — это ожидаемо.
	err := exec.Run(ctx)
	if err == nil {
		t.Fatal("ожидали ошибку при выполнении шага")
	}

	snap, lerr := store.Load(ctx)
	if lerr != nil {
		t.Fatalf("Load: %v", lerr)
	}
	if snap.Completed["s1"] {
		t.Fatal("свежий снапшот не должен содержать завершённых шагов")
	}
	if snap.Statuses["s1"] != checkpoint.StatusFailed {
		t.Fatalf("статус s1 после падения: %q, ожидали failed", snap.Statuses["s1"])
	}
	var stored Plan
	if err := json.Unmarshal(snap.PlanJSON, &stored); err != nil {
		t.Fatalf("разбор PlanJSON из чекпоинта: %v", err)
	}
	if stored.Summary != plan.Summary {
		t.Fatal("PlanJSON в чекпоинте не совпадает с планом")
	}
}

func TestExecutorResumeNoSnapshotIsFresh(t *testing.T) {
	ctx := context.Background()
	store := newExecutorStore(t, "checkpoint:nosnap")
	defer store.Close()

	plan := &Plan{
		ProjectName: "nosnapProj",
		Summary:     "нет чекпоинта",
		Steps: []Step{
			{ID: "s1", Agent: AgentCodeReviewer, Prompt: "проверь", Description: "ревью"},
		},
	}

	// Resume запрошен, но чекпоинта нет → стартуем с нуля (выполнение шага
	// упадёт из-за провайдера, что доказывает: шаг НЕ был пропущен).
	exec := NewExecutor(&stubProvider{errors: true}, plan).SetCheckpoint(store, true)
	if err := exec.Run(ctx); err == nil {
		t.Fatal("ожидали выполнение шага (нет чекпоинта для пропуска)")
	}
}

// Модель вернула пустой ответ с обрезанием по лимиту токенов (finish_reason=
// "length") и не вызвала ни одного инструмента — кодгенер не создал файлы.
// Шаг должен упасть и получить статус failed, а не «успешно завершиться».
func TestExecutorEmptyTruncatedCodingStepFails(t *testing.T) {
	ctx := context.Background()
	store := newExecutorStore(t, "checkpoint:emptylength")
	defer store.Close()

	plan := &Plan{
		ProjectName: "emptyProj",
		Summary:     "модель обрезалась",
		Steps: []Step{
			{ID: "s1", Agent: AgentCodeGenerator, Prompt: "создай файлы", Description: "генерация"},
		},
	}

	exec := NewExecutor(&stubProvider{lengthEmpty: true}, plan).SetCheckpoint(store, true)
	err := exec.Run(ctx)
	if err == nil {
		t.Fatal("ожидали ошибку: пустой ответ с length не должен считаться успехом")
	}
	if !strings.Contains(err.Error(), "не создал код") {
		t.Fatalf("ошибка должна говорить о пустом ответе, got: %v", err)
	}

	snap, lerr := store.Load(ctx)
	if lerr != nil {
		t.Fatalf("Load: %v", lerr)
	}
	if snap.Completed["s1"] {
		t.Fatal("шаг без созданного кода не должен быть помечен completed")
	}
	if snap.Statuses["s1"] != checkpoint.StatusFailed {
		t.Fatalf("статус s1: %q, ожидали failed", snap.Statuses["s1"])
	}
}

// genErr — небольшая помощь для описания ошибок в тестах.
type genErr string

func (e genErr) Error() string { return string(e) }
