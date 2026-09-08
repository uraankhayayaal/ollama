package planner

import (
	"ai/agents"
	"ai/agents/acceptor"
	"ai/checkpoint"
	"ai/projects"
	"ai/runner"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
			{ID: "s1", Agent: AgentBackendDev, Prompt: "сделай", Description: "генерация"},
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
			{ID: "s1", Agent: AgentBackendDev, Prompt: "создай файлы", Description: "генерация"},
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

// topLevelScopeDir выделяет общий старший подкаталог всех записей scope.
func TestTopLevelScopeDir(t *testing.T) {
	cases := []struct {
		scope []string
		want  string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"frontend/"}, "frontend"},
		{[]string{"frontend/src/App.tsx", "frontend/package.json"}, "frontend"},
		{[]string{"server/main.go", "server/internal/"}, "server"},
		{[]string{"./frontend/src/App.tsx"}, "frontend"},
		{[]string{"main.go"}, "main.go"},
		{[]string{"frontend/", "server/"}, ""},
		{[]string{"frontend/src/App.tsx", "server/main.go"}, ""},
	}
	for _, tc := range cases {
		if got := topLevelScopeDir(tc.scope); got != tc.want {
			t.Errorf("topLevelScopeDir(%v) = %q, want %q", tc.scope, got, tc.want)
		}
	}
}

// acceptanceDir выбирает подкаталог приёмки из scope: существующая директория
// — её и принимаем, иначе корень.
func TestAcceptanceDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "server"), 0755); err != nil {
		t.Fatal(err)
	}

	if got := acceptanceDir(root, []string{"server/"}); got != filepath.Join(root, "server") {
		t.Fatalf("scope server/: got %s", got)
	}
	if got := acceptanceDir(root, []string{"server/main.go"}); got != filepath.Join(root, "server") {
		t.Fatalf("scope server/main.go: got %s", got)
	}
	if got := acceptanceDir(root, nil); got != root {
		t.Fatalf("пустой scope: got %s, want корень", got)
	}
	if got := acceptanceDir(root, []string{"nonexistent/"}); got != root {
		t.Fatalf("несуществующий подкаталог: got %s, want корень", got)
	}
	if got := acceptanceDir(root, []string{"frontend/", "server/"}); got != root {
		t.Fatalf("два подкаталога: got %s, want корень", got)
	}
}

// Шаг acceptor со scope на подкаталог принимает именно его: отчёт приёмки
// отвечает проекту frontend, а не всему корню.
func TestExecutorAcceptorScopedToSubproject(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go не установлен")
	}
	ctx := context.Background()
	name := "AcceptorScopeTest"
	root := projects.ProjectDir(name)
	defer os.RemoveAll(filepath.Clean(root))
	if err := os.MkdirAll(filepath.Join(root, "frontend"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "frontend", "package.json"),
		[]byte(`{"scripts": {"build": "echo ok"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "server"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "server", "go.mod"),
		[]byte("module server\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "server", "main.go"),
		[]byte("package main\nfunc main() { println(\"hi\") }\n"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ACCEPT_INSTALL_DEPS", "false")
	t.Setenv("ACCEPT_BUILD_TIMEOUT", "2m")

	plan := &Plan{
		ProjectName: name,
		Summary:     "приёмка фронтенда",
		Steps: []Step{
			{ID: "a1", Agent: AgentAcceptor, Prompt: "приёмка фронтенда", Description: "приёмка frontend", Scope: []string{"frontend/"}},
			{ID: "a2", Agent: AgentAcceptor, Prompt: "приёмка сервера", Description: "приёмка server", Scope: []string{"server/"}},
		},
	}

	exec := NewExecutor(&fixPlanProvider{}, plan)
	if err := exec.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rep := exec.acceptReports["a1"]
	if rep == nil {
		t.Fatal("не найден отчёт для шага a1")
	}
	if rep.Project != "frontend" {
		t.Fatalf("отчёт a1: проект %q, ожидали frontend", rep.Project)
	}
	if rep.Verdict != acceptor.VerdictApprove {
		t.Fatalf("отчёт a1: вердикт %s, ожидали approve", rep.Verdict)
	}
	if len(rep.Projects) != 0 {
		t.Fatalf("одиночный подпроект не должен агрегироваться, got %d", len(rep.Projects))
	}

	rep2 := exec.acceptReports["a2"]
	if rep2 == nil || rep2.Project != "server" {
		t.Fatalf("отчёт a2: проект %q, ожидали server", rep2.Project)
	}
}

// roundStubProvider — провайдер, имитирующий лимит раундов агентского цикла:
// первый запуск возвращает Truncated с историей диалога и потраченными
// раундами, повторный (с resume-состоянием в контексте) — успех. Фиксирует,
// видел ли resume-состояние и с какого раунда продолжил цикл.
type roundStubProvider struct {
	calls     int
	sawResume bool
	resumeAt  int
}

func (s *roundStubProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	s.calls++
	if st := runner.ResumeStateFromContext(ctx); st != nil {
		s.sawResume = true
		s.resumeAt = st.Rounds
		return &runner.AgentResponse{Content: "сделано", Rounds: st.Rounds + 1}, nil
	}
	return &runner.AgentResponse{
		Content:   "частично",
		Truncated: true,
		Rounds:    12,
		Messages:  []runner.Message{{Role: "user", Content: "задача"}},
	}, nil
}

func (s *roundStubProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

// Шаг упёрся в лимит раундов агентского цикла: выполнение плана должно
// остановиться с подсказкой --resume, а история диалога и потраченные раунды
// должны сохраниться в чекпоинт (статус шага — failed).
func TestExecutorTruncatedStepPersistsRoundState(t *testing.T) {
	ctx := context.Background()
	store := newExecutorStore(t, "checkpoint:roundpersist")
	defer store.Close()

	plan := &Plan{
		ProjectName: "roundProj",
		Summary:     "лимит раундов",
		Steps: []Step{
			{ID: "s1", Agent: AgentBackendDev, Prompt: "сделай", Description: "генерация"},
		},
	}

	exec := NewExecutor(&roundStubProvider{}, plan).SetCheckpoint(store, false)
	err := exec.Run(ctx)
	if err == nil {
		t.Fatal("ожидали ошибку: шаг упёрся в лимит раундов")
	}
	if !strings.Contains(err.Error(), "--resume") {
		t.Fatalf("ошибка должна подсказывать запуск с --resume, got: %v", err)
	}

	snap, lerr := store.Load(ctx)
	if lerr != nil {
		t.Fatalf("Load: %v", lerr)
	}
	if snap.Statuses["s1"] != checkpoint.StatusFailed {
		t.Fatalf("шаг должен иметь статус failed, got %q", snap.Statuses["s1"])
	}
	if snap.Completed["s1"] {
		t.Fatal("шаг не должен быть помечен completed")
	}
	if snap.Rounds["s1"] != 12 {
		t.Fatalf("сохранённые раунды: %d, ожидали 12", snap.Rounds["s1"])
	}
	if len(snap.Conversations["s1"]) == 0 {
		t.Fatal("история диалога должна быть сохранена в чекпоинт")
	}
}

// Resume продолжает прерванный шаг с потраченных раундов: провайдер должен
// получить resume-состояние через контекст (раунд 13), шаг успешно
// завершиться, а история агентского цикла — очиститься из чекпоинта.
func TestExecutorResumeContinuesTruncatedStep(t *testing.T) {
	ctx := context.Background()
	store := newExecutorStore(t, "checkpoint:roundresume")
	defer store.Close()

	plan := &Plan{
		ProjectName: "roundResumeProj",
		Summary:     "продолжить с раунда 13",
		Steps: []Step{
			{ID: "s1", Agent: AgentBackendDev, Prompt: "сделай", Description: "генерация"},
		},
	}

	planJSON, _ := json.Marshal(plan)
	firstSnap := &checkpoint.Snapshot{
		ProjectName:   plan.ProjectName,
		Summary:       plan.Summary,
		PlanJSON:      planJSON,
		Completed:     map[string]bool{},
		Statuses:      map[string]string{"s1": checkpoint.StatusFailed},
		Rounds:        map[string]int{"s1": 12},
		Conversations: map[string]json.RawMessage{"s1": json.RawMessage(`[{"Role":"user","Content":"задача"}]`)},
	}
	if err := store.Save(ctx, firstSnap); err != nil {
		t.Fatalf("Save: %v", err)
	}

	stub := &roundStubProvider{}
	exec := NewExecutor(stub, plan).SetCheckpoint(store, true)
	if err := exec.Run(ctx); err != nil {
		t.Fatalf("Run с resume: %v", err)
	}

	if !stub.sawResume {
		t.Fatal("провайдер должен был получить resume-состояние через контекст")
	}
	if stub.resumeAt != 12 {
		t.Fatalf("цикл должен продолжиться с раунда 13 (resumeAt=12), got %d", stub.resumeAt)
	}
	if !exec.completed["s1"] {
		t.Fatal("шаг должен успешно завершиться после resume")
	}

	// После успеха история агентского цикла удаляется из чекпоинта.
	nsnap, lerr := store.Load(ctx)
	if lerr != nil {
		t.Fatalf("Load: %v", lerr)
	}
	if _, ok := nsnap.Conversations["s1"]; ok {
		t.Fatal("история должна быть очищена после успешного завершения шага")
	}
	if !nsnap.Completed["s1"] {
		t.Fatal("шаг должен быть помечен completed в чекпоинте")
	}
}
