package planner

// Тесты автономии (Ф-6, этап 4): watchdog по heartbeat вместо «сбросить всё»,
// персистентная эскалация модели на уровне задачи и конечный бюджет.

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ai/agents"
	"ai/agents/architect"
	"ai/board"
	"ai/models"
	"ai/projects"
	"ai/runner"

	"github.com/alicebob/miniredis/v2"
)

// --- watchdog по heartbeat ---

// autonomyStore поднимает доску с эпиком и задачей в заданном состоянии.
func autonomyStore(t *testing.T, project string) *board.Store {
	t.Helper()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: project})
	ctx := context.Background()
	if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{TaskID: "E-1", Title: "epic"}}); err != nil {
		t.Fatal(err)
	}
	return store
}

func putTask(t *testing.T, store *board.Store, id string, status board.Status, heartbeat time.Time) {
	t.Helper()
	task := &board.Task{
		TaskSpec: board.TaskSpec{TaskID: id, Title: "задача " + id, SequenceOrder: 1},
		EpicID:   "E-1",
		Status:   status,
	}
	if !heartbeat.IsZero() {
		task.HeartbeatAt = heartbeat.Format(time.RFC3339)
	}
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask %s: %v", id, err)
	}
}

func taskStatus(t *testing.T, store *board.Store, id string) board.Status {
	t.Helper()
	task, err := store.GetTask(context.Background(), id)
	if err != nil {
		t.Fatalf("GetTask %s: %v", id, err)
	}
	return task.Status
}

// TestRecoverStuckTasksUsesHeartbeat — crux этапа 4.1: брошенной считается
// только задача с мёртвым пульсом. Свежий пульс (живой чужой прогон) и задача
// этого же раннера остаются «в работе» — прежний код сбрасывал всё подряд.
func TestRecoverStuckTasksUsesHeartbeat(t *testing.T) {
	store := autonomyStore(t, "watchdog-hb")
	putTask(t, store, "FRESH", board.StatusInProgress, time.Now().Add(-time.Minute))
	putTask(t, store, "STALE", board.StatusInProgress, time.Now().Add(-90*time.Minute))
	putTask(t, store, "NOBEAT", board.StatusInProgress, time.Time{})
	putTask(t, store, "READY", board.StatusReady, time.Time{})

	kr := NewKanbanRunner(&loopedProvider{}, store)
	kr.markActive("READY")
	t.Cleanup(func() { kr.unmarkActive("READY") })

	n, err := kr.recoverStuckTasks(context.Background())
	if err != nil {
		t.Fatalf("recoverStuckTasks: %v", err)
	}
	if n != 2 {
		t.Fatalf("возвращено в очередь %d задач, ожидалось 2 (STALE + NOBEAT)", n)
	}
	if got := taskStatus(t, store, "FRESH"); got != board.StatusInProgress {
		t.Fatalf("живой пульс: статус %s, ожидался %s (чужой прогон не трогаем)", got, board.StatusInProgress)
	}
	if got := taskStatus(t, store, "STALE"); got != board.StatusReady {
		t.Fatalf("мёртвый пульс: статус %s, ожидался %s", got, board.StatusReady)
	}
	if got := taskStatus(t, store, "NOBEAT"); got != board.StatusReady {
		t.Fatalf("без пульса: статус %s, ожидался %s", got, board.StatusReady)
	}
	if got := taskStatus(t, store, "READY"); got != board.StatusReady {
		t.Fatalf("статус задачи вне зоны отката изменился: %s", got)
	}
}

// TestRecoverStuckTasksKeepsOwnActiveTask — задача, которую выполняет ЭТОТ
// раннер, жива по определению: собственный in_progress между фазами не
// сбрасывается.
func TestRecoverStuckTasksKeepsOwnActiveTask(t *testing.T) {
	store := autonomyStore(t, "watchdog-own")
	putTask(t, store, "MINE", board.StatusInProgress, time.Now().Add(-2*time.Hour))

	kr := NewKanbanRunner(&loopedProvider{}, store)
	kr.markActive("MINE")
	n, err := kr.recoverStuckTasks(context.Background())
	if err != nil {
		t.Fatalf("recoverStuckTasks: %v", err)
	}
	kr.unmarkActive("MINE")
	if n != 0 {
		t.Fatalf("своя задача сброшена: %d", n)
	}
	if got := taskStatus(t, store, "MINE"); got != board.StatusInProgress {
		t.Fatalf("статус своей задачи: %s", got)
	}
}

// TestRecoverStuckTasksZeroThresholdDisablesHeartbeat — KANBAN_STALE_TASK_MIN=0
// отключает порог (тогда брошенной считается любая задача вне active).
func TestRecoverStuckTasksZeroThresholdDisablesHeartbeat(t *testing.T) {
	t.Setenv("KANBAN_STALE_TASK_MIN", "0")
	store := autonomyStore(t, "watchdog-zero")
	putTask(t, store, "FRESH", board.StatusInProgress, time.Now())

	kr := NewKanbanRunner(&loopedProvider{}, store)
	if n, err := kr.recoverStuckTasks(context.Background()); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v, ожидался возврат свежепульсовой задачи в очередь", n, err)
	}
}

// TestRecoverStuckTasksUnparsableHeartbeat — испорченный пульс не должен
// оставлять задачу висеть навсегда: нечитаемое значение считаем мёртвым.
func TestRecoverStuckTasksUnparsableHeartbeat(t *testing.T) {
	store := autonomyStore(t, "watchdog-bad")
	putTask(t, store, "BAD", board.StatusInProgress, time.Time{})
	if err := store.PatchTask(context.Background(), "BAD", func(task *board.Task) error {
		task.HeartbeatAt = "вчера вечером"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	kr := NewKanbanRunner(&loopedProvider{}, store)
	n, err := kr.recoverStuckTasks(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v, нечитаемый пульс должен считаться мёртвым", n, err)
	}
}

// TestRecoverStuckTasksOnServerRestart — сценарий 4.2: задача была «в работе»
// (сервер упал посреди прогона) — новый запуск возвращает её в очередь, и она
// снова берётся в работу (attempts растёт).
func TestRecoverStuckTasksOnServerRestart(t *testing.T) {
	store := autonomyStore(t, "watchdog-restart")
	putTask(t, store, "T-1", board.StatusReady, time.Time{})
	ctx := context.Background()
	// Прогон «до падения»: задача ушла в работу, агент отчитался пульсом.
	if err := store.SetTaskStatus(ctx, "T-1", board.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	if err := store.PatchTask(ctx, "T-1", func(task *board.Task) error {
		task.HeartbeatAt = time.Now().Format(time.RFC3339)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	kr := NewKanbanRunner(&loopedProvider{}, store)
	kr.recoverStuckTasks(ctx)
	kr.recoverStuckTasks(ctx) // порог по умолчанию 30 мин: пульс свежий
	if got := taskStatus(t, store, "T-1"); got != board.StatusInProgress {
		t.Fatalf("свежий пульс после рестарта: статус %s (задача ещё считается живой)", got)
	}

	// Пульс «протух» (перезапуск был час назад) — watchdog возвращает задачу.
	if err := store.PatchTask(ctx, "T-1", func(task *board.Task) error {
		task.HeartbeatAt = time.Now().Add(-time.Hour).Format(time.RFC3339)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := kr.recoverStuckTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if got := taskStatus(t, store, "T-1"); got != board.StatusReady {
		t.Fatalf("протухший пульс: статус %s, ожидался %s", got, board.StatusReady)
	}
	if err := store.SetTaskStatus(ctx, "T-1", board.StatusInProgress); err != nil {
		t.Fatalf("повторный старт задачи: %v", err)
	}
	task, _ := store.GetTask(ctx, "T-1")
	if task.Attempts < 2 {
		t.Fatalf("attempts=%d: повторный прогон должен учитываться", task.Attempts)
	}
}

// --- персистентная эскалация модели ---

// heavyProbeProvider: первый заход специалиста зацикливается, второй (с
// требованием сильной модели из контекста) выполняет задачу. Заодно
// фиксирует, на каком слое шёл каждый вызов.
type heavyProbeProvider struct {
	mu    sync.Mutex
	calls []string // "small" / "large" по порядку вызовов специалиста
}

func (p *heavyProbeProvider) note(layer string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, layer)
}

func (p *heavyProbeProvider) layers() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

func (p *heavyProbeProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}
	switch {
	case strings.HasPrefix(umsg, "Декомпозируй эпик"):
		return &runner.AgentResponse{Content: leadDecompositionJSON}, nil
	case strings.HasPrefix(umsg, "Ты — специалист"):
		if models.HeavyModelFor(ctx) {
			p.note("large")
			// Сильная модель доводит задачу до конца.
			return &runner.AgentResponse{Content: "готово"}, nil
		}
		p.note("small")
		return &runner.AgentResponse{
			Content:    "пробую снова",
			Looped:     true,
			LoopReason: "вызов WriteFiles повторён 3 раз с одними и теми же аргументами",
			Rounds:     6,
		}, nil
	default:
		if a, ok := agent.(*architect.Architect); ok {
			if _, err := a.CallFunction(architect.SubmitBacklogToolName, architectBacklogArgs); err != nil {
				return nil, err
			}
		}
		return &runner.AgentResponse{Content: ""}, nil
	}
}

func (p *heavyProbeProvider) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

// TestLoopEscalationSwitchesModelOnRetry — 4.3/4.4: первая попытка идёт на
// дешёвой модели, после петли задача возвращается в очередь с требованием
// большой модели, вторая попытка идёт уже на ней и завершает работу.
func TestLoopEscalationSwitchesModelOnRetry(t *testing.T) {
	srv := miniredis.RunT(t)
	const project = "escalate-model"
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: project})
	dir := projects.ProjectDir(project)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	prov := &heavyProbeProvider{}
	kr := NewKanbanRunner(prov, store)
	if err := kr.Run(context.Background(), project, "Сделай todo-приложение"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	layers := prov.layers()
	if len(layers) < 2 {
		t.Fatalf("вызовов специалиста: %v — ждём эскалацию со второго", layers)
	}
	if layers[0] != "small" {
		t.Fatalf("первая попытка должна быть на дешёвой модели, got %q", layers[0])
	}
	if layers[1] != "large" {
		t.Fatalf("после петли работа должна продолжиться на большой модели, got %q", layers[1])
	}

	tasks, err := store.ListTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var done int
	for _, task := range tasks {
		if task.Status == board.StatusDone {
			done++
			if task.ModelTier != board.ModelTierLarge {
				t.Fatalf("выполненная после эскалации задача %s: model_tier=%q, ожидался %q (требование переживает прогон)",
					task.TaskID, task.ModelTier, board.ModelTierLarge)
			}
			if task.Escalations < 1 {
				t.Fatalf("задача %s: эскалаций %d, ожидалась >= 1", task.TaskID, task.Escalations)
			}
		}
	}
	if done == 0 {
		t.Fatalf("после эскалации задача должна быть выполнена: %+v", tasks)
	}
}

// TestEscalatedRequirementSurvivesNextRun — требование модели живёт в записи
// задачи, а не в одном вызове: если задача снова попадёт в работу, она пойдёт
// на большой модели сразу (эскалация не «теряется» между прогонами).
func TestEscalatedRequirementSurvivesNextRun(t *testing.T) {
	store := autonomyStore(t, "escalate-persist")
	putTask(t, store, "T-1", board.StatusReady, time.Time{})
	ctx := context.Background()
	kr := NewKanbanRunner(&loopedProvider{}, store)
	task, _ := store.GetTask(ctx, "T-1")

	if _, err := kr.escalateLoop(ctx, task, "петля"); err != nil {
		t.Fatalf("escalateLoop: %v", err)
	}
	got, _ := store.GetTask(ctx, "T-1")
	if got.ModelTier != board.ModelTierLarge {
		t.Fatalf("model_tier=%q после эскалации", got.ModelTier)
	}
	if got.Status != board.StatusReady {
		t.Fatalf("после эскалации задача должна вернуться в очередь, got %s", got.Status)
	}

	// Следующий прогон: требование на месте даже без повторной петли.
	if _, err := kr.escalateLoop(ctx, got, "вторая петля"); err != nil {
		t.Fatalf("escalateLoop (вторая): %v", err)
	}
	again, _ := store.GetTask(ctx, "T-1")
	if again.Escalations != 2 {
		t.Fatalf("эскалаций %d, ожидалось 2 (счётчик в записи задачи)", again.Escalations)
	}
	if len(again.Injections) < 2 {
		t.Fatalf("инъекций %d: на каждый прогон нужен свой разбор петли", len(again.Injections))
	}
}

// TestEscalationBudgetStopsAndAsksHuman — исчерпание бюджета: работа задачи
// останавливается («помощь человека»), счётчик не растёт дальше, причина
// доходит до чата.
func TestEscalationBudgetStopsAndAsksHuman(t *testing.T) {
	t.Setenv("KANBAN_MAX_ESCALATIONS", "1")
	store := autonomyStore(t, "escalate-budget")
	putTask(t, store, "T-1", board.StatusInProgress, time.Now())
	ctx := context.Background()
	kr := NewKanbanRunner(&loopedProvider{}, store)

	var audit []string
	kr.SetStatusNotifier(func(msg string) { audit = append(audit, msg) })

	// Первая петля: бюджет ещё есть — эскалация.
	task, _ := store.GetTask(ctx, "T-1")
	if _, err := kr.escalateLoop(ctx, task, "петля раз"); err != nil {
		t.Fatalf("escalateLoop 1: %v", err)
	}
	// Вторая: бюджет исчерпан.
	task, _ = store.GetTask(ctx, "T-1")
	if err := store.SetTaskStatus(ctx, "T-1", board.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	if _, err := kr.escalateLoop(ctx, task, "петля два"); err != nil {
		t.Fatalf("escalateLoop 2: %v", err)
	}
	got, _ := store.GetTask(ctx, "T-1")
	if got.Status != board.StatusHumanHelp {
		t.Fatalf("после исчерпания бюджета статус %s, ожидалась «помощь человека»", got.Status)
	}
	if got.Escalations != 1 {
		t.Fatalf("эскалаций %d, ожидался бюджет 1", got.Escalations)
	}
	if kr.needHuman() == "" {
		t.Fatal("причина остановки не запомнена: запуск сообщил бы «нет прогресса»")
	}
	joined := strings.Join(audit, "\n")
	if !strings.Contains(joined, "нужен человек") {
		t.Fatalf("в аудите нет сообщения о нужде в человеке: %v", audit)
	}
}

// TestStaleTaskAfterEnvParsing — разбор KANBAN_STALE_TASK_MIN: мусор и
// отрицательные значения не ломают watchdog (используется умолчание).
func TestStaleTaskAfterEnvParsing(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", staleTaskMinDefault * time.Minute},
		{"15", 15 * time.Minute},
		{"0", 0},
		{"мусор", staleTaskMinDefault * time.Minute},
		{"-5", staleTaskMinDefault * time.Minute},
	}
	for _, c := range cases {
		t.Setenv("KANBAN_STALE_TASK_MIN", c.env)
		if got := staleTaskAfter(); got != c.want {
			t.Fatalf("KANBAN_STALE_TASK_MIN=%q → %v, ожидалось %v", c.env, got, c.want)
		}
	}
}

// TestMaxEscalationsEnvParsing — разбор KANBAN_MAX_ESCALATIONS.
func TestMaxEscalationsEnvParsing(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"", maxEscalationsDefault},
		{"5", 5},
		{"0", 0},
		{"-1", maxEscalationsDefault},
		{"nope", maxEscalationsDefault},
	}
	for _, c := range cases {
		t.Setenv("KANBAN_MAX_ESCALATIONS", c.env)
		if got := maxEscalations(); got != c.want {
			t.Fatalf("KANBAN_MAX_ESCALATIONS=%q → %d, ожидалось %d", c.env, got, c.want)
		}
	}
}
