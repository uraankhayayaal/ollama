package planner

import (
	"ai/agents"
	"ai/agents/backendlead"
	"ai/board"
	"ai/projects"
	"ai/runner"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// leadFailProvider — на «Декомпозируй эпик» провайдер падает: 5.4 должен
// перевести эпик в human_help и вернуть ошибку, а не крутить цикл заново.
type leadFailProvider struct{}

func (p *leadFailProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}
	if strings.HasPrefix(umsg, "Декомпозируй эпик") {
		return nil, errors.New("провайдер декомпозиции недоступен")
	}
	return &runner.AgentResponse{Content: "Задача выполнена."}, nil
}

func (p *leadFailProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

// leadCaptureProvider — фиксирует агента-лида, которому отдаётся эпик, и
// отвечает корректной JSON-декомпозицией (5.2: проверка резолвера worktree).
type leadCaptureProvider struct {
	lead agents.Agent
}

func (p *leadCaptureProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}
	if strings.HasPrefix(umsg, "Декомпозируй эпик") {
		p.lead = agent
		return &runner.AgentResponse{Content: leadDecompositionJSON}, nil
	}
	return &runner.AgentResponse{Content: "Задача выполнена."}, nil
}

func (p *leadCaptureProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

// leadTestBoard поднимает доску для тестов лид-фазы и убирает за собой
// выходную директорию проекта.
func leadTestBoard(t *testing.T, project string) *board.Store {
	t.Helper()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: project})
	t.Cleanup(func() { _ = os.RemoveAll(projects.ProjectDir(project)) })
	return store
}

// TestPhaseLeadsFailureSetsEpicHumanHelp — 5.4 сквозь phaseLeads: падение
// провайдера на декомпозиции не крутит цикл заново, а переводит эпик в
// human_help и запоминает причину остановки.
func TestPhaseLeadsFailureSetsEpicHumanHelp(t *testing.T) {
	ctx := context.Background()
	store := leadTestBoard(t, "kanban-leadfail")
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "ARCH-01", Title: "Бэкенд-сервис", AssignedRole: "Backend Lead", SequenceOrder: 1},
	}); err != nil {
		t.Fatal(err)
	}

	kr := NewKanbanRunner(&leadFailProvider{}, store)
	progress, err := kr.phaseLeads(ctx)
	if err == nil {
		t.Fatal("phaseLeads должна вернуть ошибку при падении провайдера декомпозиции")
	}
	if progress {
		t.Error("при падении лид-фазы не должно быть прогресса")
	}
	if !strings.Contains(err.Error(), "провайдер декомпозиции недоступен") {
		t.Errorf("ошибка должна нести исходную причину, got: %v", err)
	}
	epic, gerr := store.GetEpic(ctx, "ARCH-01")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if epic.Status != board.StatusHumanHelp {
		t.Errorf("эпик должен уйти в human_help, got: %s", epic.Status)
	}
	if reason := kr.needHuman(); !strings.Contains(reason, "декомпозиция эпика ARCH-01") {
		t.Errorf("стоп-причина должна называть эпик, got: %q", reason)
	}
}

// TestLeadFailCascadesToEpicTasks — 5.4: leadFail переводит эпик в human_help
// и каскадом ставит на паузу незакрытые задачи эпика (pauseEpicTasks), причина
// оседает в стоп-причине запуска.
func TestLeadFailCascadesToEpicTasks(t *testing.T) {
	ctx := context.Background()
	store := leadTestBoard(t, "kanban-leadfail2")
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "ARCH-01", Title: "Бэкенд-сервис", AssignedRole: "Backend Lead", SequenceOrder: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "T-01", Title: "Сервис", AssignedRole: "Go Developer", SequenceOrder: 1},
		EpicID:   "ARCH-01",
		Status:   board.StatusReady,
	}); err != nil {
		t.Fatal(err)
	}

	epic, err := store.GetEpic(ctx, "ARCH-01")
	if err != nil {
		t.Fatal(err)
	}
	kr := NewKanbanRunner(&kanbanProvider{}, store)
	inErr := errors.New("декомпозиция сорвалась")
	if got := kr.leadFail(ctx, epic, inErr); !errors.Is(got, inErr) {
		t.Fatalf("leadFail должна вернуть исходную ошибку, got: %v", got)
	}
	epic, err = store.GetEpic(ctx, "ARCH-01")
	if err != nil {
		t.Fatal(err)
	}
	if epic.Status != board.StatusHumanHelp {
		t.Errorf("эпик должен уйти в human_help, got: %s", epic.Status)
	}
	task, err := store.GetTask(ctx, "T-01")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != board.StatusHumanHelp {
		t.Errorf("незакрытая задача эпика должна встать на паузу, got: %s", task.Status)
	}
	if task.ResumeStatus != board.StatusReady {
		t.Errorf("пауза должна запомнить исходный статус ready, got: %q", task.ResumeStatus)
	}
	if reason := kr.needHuman(); !strings.Contains(reason, "декомпозиция эпика ARCH-01") ||
		!strings.Contains(reason, "декомпозиция сорвалась") {
		t.Errorf("стоп-причина должна называть эпик и причину, got: %q", reason)
	}
}

// TestLeadFailCancelledContextPassthrough — 5.4: отмена контекста (остановка
// сессии) не форсмажорит: ошибка пробрасывается как есть, статус эпика не
// меняется.
func TestLeadFailCancelledContextPassthrough(t *testing.T) {
	ctx := context.Background()
	store := leadTestBoard(t, "kanban-leadfail3")
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "ARCH-01", Title: "Бэкенд-сервис", AssignedRole: "Backend Lead", SequenceOrder: 1},
	}); err != nil {
		t.Fatal(err)
	}
	epic, err := store.GetEpic(ctx, "ARCH-01")
	if err != nil {
		t.Fatal(err)
	}

	kr := NewKanbanRunner(&kanbanProvider{}, store)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	inErr := errors.New("сборка прервана")
	if got := kr.leadFail(cctx, epic, inErr); !errors.Is(got, inErr) {
		t.Fatalf("leadFail при отмене должна вернуть исходную ошибку, got: %v", got)
	}
	epic, err = store.GetEpic(ctx, "ARCH-01")
	if err != nil {
		t.Fatal(err)
	}
	if epic.Status == board.StatusHumanHelp {
		t.Error("отмена контекста не должна переводить эпик в human_help")
	}
	if kr.needHuman() != "" {
		t.Errorf("отмена контекста не должна ставить стоп-причину, got: %q", kr.needHuman())
	}
}

// TestPhaseLeadsWiresEpicWorktreeIntoLead — 5.2: phaseLeads подключает лиду
// worktree ветки эпика через резолвер epicOutputDir: резолвер вызывается с
// проектом и ID эпика, у лида меняется OutputDir (скелетон пойдёт в ветку).
func TestPhaseLeadsWiresEpicWorktreeIntoLead(t *testing.T) {
	ctx := context.Background()
	store := leadTestBoard(t, "kanban-leadwt")
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "ARCH-01", Title: "Бэкенд-сервис", AssignedRole: "Backend Lead", SequenceOrder: 1},
	}); err != nil {
		t.Fatal(err)
	}

	prov := &leadCaptureProvider{}
	kr := NewKanbanRunner(prov, store)
	wt := filepath.Join(t.TempDir(), "wt-ARCH-01")
	if err := os.MkdirAll(wt, 0755); err != nil {
		t.Fatal(err)
	}
	var gotProject, gotEpic string
	kr.SetEpicOutputDir(func(project, epicID string) string {
		gotProject, gotEpic = project, epicID
		return wt
	})

	if _, err := kr.phaseLeads(ctx); err != nil {
		t.Fatalf("phaseLeads: %v", err)
	}
	if gotProject != "kanban-leadwt" || gotEpic != "ARCH-01" {
		t.Errorf("резолвер должен получить проект/эпик канбан-leadwt/ARCH-01, got %q/%q", gotProject, gotEpic)
	}
	if prov.lead == nil {
		t.Fatal("провайдер не получил агента-лида")
	}
	lead, ok := prov.lead.(*backendlead.BackendLead)
	if !ok {
		t.Fatalf("ожидался Backend Lead, got %T", prov.lead)
	}
	if lead.OutputDir != wt {
		t.Errorf("OutputDir лида должен стать worktree %q, got %q", wt, lead.OutputDir)
	}
}

// TestLeadPromptSkeletonAndContractRefs — 5.3: промпт декомпозиции ведёт
// скелетон и отсылки к файлам для лидов направлений, но сохраняет старый
// формат «полный контракт в description» для QA-лида (без скелетона).
func TestLeadPromptSkeletonAndContractRefs(t *testing.T) {
	store := leadTestBoard(t, "kanban-leadprompt")
	kr := NewKanbanRunner(&kanbanProvider{}, store)

	dev := kr.leadPrompt(&board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "ARCH-01", Title: "Бэкенд-сервис", AssignedRole: "Backend Lead"},
	})
	for _, want := range []string{
		"Декомпозируй эпик", // префикс узнают сквозные тесты провайдера
		"СКЕЛЕТОН В ВЕТКЕ ЭПИКА",
		"отсылки к файлам скелетона",
		"ДЕТАЛИЗАЦИЯ ЗАДАЧ",
	} {
		if !strings.Contains(dev, want) {
			t.Errorf("промпт бэкенд-лида не содержит %q", want)
		}
	}
	for _, bad := range []string{"Ты НЕ пишешь код", "ПОЛНЫЙ контракт"} {
		if strings.Contains(dev, bad) {
			t.Errorf("промпт бэкенд-лида содержит устаревшее %q", bad)
		}
	}

	qa := kr.leadPrompt(&board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "ARCH-02", Title: "Автотесты", AssignedRole: "QA Lead"},
	})
	for _, want := range []string{"ПОЛНЫЙ контракт", "Ты НЕ пишешь код"} {
		if !strings.Contains(qa, want) {
			t.Errorf("промпт QA-лида не содержит %q", want)
		}
	}
	if strings.Contains(qa, "СКЕЛЕТОН В ВЕТКЕ ЭПИКА") {
		t.Error("QA-лид не пишет скелетон — секции не должно быть в промпте")
	}
}
