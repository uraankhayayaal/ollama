package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ai/agents"
	"ai/board"
	"ai/chat"
	"ai/runner"
	"ai/tools"
)

// fakeActions — hermetic ActionsBackend: подтверждение выставляется вручную,
// исполнение пишется в поля-флаги. Проверяем поведение обёртки actionTool без
// реальной сессии и git.
type fakeActions struct {
	confirmed bool
	started   bool
	merged    string
	released  string
	rejected  bool
}

func (f *fakeActions) KanbanStart(context.Context) error { f.started = true; return nil }
func (f *fakeActions) TaskMerge(_ context.Context, taskID string) (string, error) {
	f.merged = taskID
	return "задача влита", nil
}
func (f *fakeActions) EpicRelease(_ context.Context, epicID string) (string, error) {
	f.released = epicID
	return "эпик в main", nil
}
func (f *fakeActions) BranchReject(context.Context) error { f.rejected = true; return nil }
func (f *fakeActions) ActionConfirmed(context.Context) bool { return f.confirmed }

// harnessStubProvider — LLM-провайдер оркестрации, немедленно останавливающий
// цикл ошибкой (без сети). В отличие от qaStubProvider безопасен для
// многократных вызовов (планер зовёт Generate на каждом шаге) и не паникует.
type harnessStubProvider struct{}

func (harnessStubProvider) Generate(context.Context, agents.Agent) (*runner.AgentResponse, error) {
	return nil, errors.New("стоп: тестовый провайдер")
}

// byName строит map «имя → Tool» из реального набора мостов actionTools.
func byName(t *testing.T, ts []tools.Tool) map[string]tools.Tool {
	t.Helper()
	m := make(map[string]tools.Tool, len(ts))
	for _, x := range ts {
		m[x.Name()] = x
	}
	return m
}

// decodeToolResult разбирает JSON-результат инструмента в map.
func decodeToolResult(t *testing.T, out []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("json результата инструмента: %v (%s)", err, out)
	}
	return m
}

// TestActionToolDestructiveRequiresConfirm — деструктивный мост без явного «да»
// из чата не выполняется: возвращается status=confirm (модель должна спросить
// пользователя), backend не мутируется (Р-3).
func TestActionToolDestructiveRequiresConfirm(t *testing.T) {
	b := &fakeActions{}
	tools := byName(t, actionTools(b))

	out, err := tools[actionTaskMerge].Execute(map[string]any{"task_id": "task-1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m := decodeToolResult(t, out)
	if m["status"] != "confirm" {
		t.Fatalf("status = %v, want confirm (body: %s)", m["status"], out)
	}
	if m["action"] != actionTaskMerge {
		t.Fatalf("action = %v, want %s", m["action"], actionTaskMerge)
	}
	if b.merged != "" {
		t.Fatalf("без подтверждения действие не должно выполняться, merged=%q", b.merged)
	}
}

// TestActionToolDestructiveRunsAfterConfirm — после явного согласия пользователя
// (да/подтверждаю в чате) деструктивный мост выполняется и возвращает success.
func TestActionToolDestructiveRunsAfterConfirm(t *testing.T) {
	b := &fakeActions{confirmed: true}
	tools := byName(t, actionTools(b))

	out, err := tools[actionTaskMerge].Execute(map[string]any{"task_id": "task-1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m := decodeToolResult(t, out)
	if m["status"] != "success" {
		t.Fatalf("status = %v, want success (body: %s)", m["status"], out)
	}
	if b.merged != "task-1" {
		t.Fatalf("действие не выполнено после подтверждения, merged=%q", b.merged)
	}
}

// TestActionToolConfirmGate — по каждому реальному мосту: деструктивные
// возвращают status=confirm и НЕ трогают backend без «да», безопасный
// KanbanStart выполняется сразу.
func TestActionToolConfirmGate(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(b *fakeActions) map[string]any
	}{
		{"TaskMerge blocked", func(b *fakeActions) map[string]any {
			out, err := byName(t, actionTools(b))[actionTaskMerge].Execute(map[string]any{"task_id": "t"})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			return decodeToolResult(t, out)
		}},
		{"EpicRelease blocked", func(b *fakeActions) map[string]any {
			out, err := byName(t, actionTools(b))[actionEpicRelease].Execute(map[string]any{"epic_id": "e"})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			return decodeToolResult(t, out)
		}},
		{"BranchReject blocked", func(b *fakeActions) map[string]any {
			out, err := byName(t, actionTools(b))[actionBranchReject].Execute(map[string]any{})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			return decodeToolResult(t, out)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &fakeActions{}
			m := tc.run(b)
			if m["status"] != "confirm" {
				t.Fatalf("status = %v, want confirm (без «да» в чате)", m["status"])
			}
		})
	}

	// Безопасный мост (KanbanStart) без подтверждения — успешен и выполняет
	// backend.
	b := &fakeActions{}
	out, err := byName(t, actionTools(b))[actionKanbanStart].Execute(map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if m := decodeToolResult(t, out); m["status"] != "success" {
		t.Fatalf("KanbanStart без подтверждения: status = %v, want success", m["status"])
	}
	if !b.started {
		t.Fatal("KanbanStart должен запускать оркестрацию без подтверждения")
	}
}

// TestConfirmAffirmative — слова-согласия распознаются, отрицания/вопросы — нет.
func TestConfirmAffirmative(t *testing.T) {
	yes := []string{"да", "да, подтверждаю", "Подтверждаю.", "делай", "ок", "окей", "согласен", "разрешаю", "давай", "YES", "конечно, откатывай"}
	for _, s := range yes {
		if !confirmAffirmative(s) {
			t.Errorf("confirmAffirmative(%q) = false, want true", s)
		}
	}
	no := []string{"нет", "не надо", "подожди", "а что это значит?", "кто ты", "отмени"}
	for _, s := range no {
		if confirmAffirmative(s) {
			t.Errorf("confirmAffirmative(%q) = true, want false", s)
		}
	}
}

// waitChatAssistantContains ждёт появления НЕПУСТОГО сообщения ассистента,
// содержащего подстроку want. В отличие от waitChatAssistant (первое попавшееся),
// ищет финальный ответ второго прогона, когда в истории уже есть более ранние
// сообщения ассистента.
func waitChatAssistantContains(t *testing.T, sess *Session, within time.Duration, want string) chat.Message {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		hist, err := sess.chat.History(context.Background(), 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range hist {
			if m.Role == chat.RoleAssistant && strings.TrimSpace(m.Content) != "" && strings.Contains(m.Content, want) {
				return m
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("нет непустого сообщения ассистента с %q за %v", want, within)
	return chat.Message{}
}

// scriptedConfirmFlow гоняет две реплики ассистента через runChatAssistant:
//  1. первый ответ — результат вызова деструктивного моста (status=confirm),
//     затем текст-вопрос «Подтвердите ...? (да/нет)»;
//  2. в чат дописывается пользовательское «да»;
//  3. второй прогон — тот же мост выполняется (подтверждение получено),
//     затем текст-результат done.
//
// Возвращает первый (вопрос-подтверждение) и финальный ответы ассистента.
func scriptedConfirmFlow(t *testing.T, sess *Session, question string, tool tools.ToolCall, ask, done string) (chat.Message, chat.Message) {
	t.Helper()
	m1 := runScriptedChatAssistant(t, sess, question,
		&runner.ModelReply{ToolCalls: []tools.ToolCall{tool}, FinishReason: "tool_calls"},
		&runner.ModelReply{Content: ask, FinishReason: "stop"},
	)
	if !strings.Contains(m1.Content, "Подтвердите") {
		t.Fatalf("ассистент не запросил подтверждение: %q", m1.Content)
	}
	sess.append(chat.RoleUser, "да", "user", "", nil)

	prov := &scriptedGenerateProvider{chat: &scriptedChatProvider{replies: []*runner.ModelReply{
		{ToolCalls: []tools.ToolCall{tool}, FinishReason: "tool_calls"},
		{Content: done, FinishReason: "stop"},
	}}}
	sess.runChatAssistant(context.Background(), "да", prov)
	return m1, waitChatAssistantContains(t, sess, 3*time.Second, done)
}

// TestChatAssistantRejectBranchAfterConfirm — «откати ветку»: деструктивный
// мост BranchReject сначала возвращает confirm (git не мутируется), после «да»
// выполняется (reset --hard базы + удаление ветки).
func TestChatAssistantRejectBranchAfterConfirm(t *testing.T) {
	git := &fakeGit{}
	srv, _, mr := setupGitflow(t, git)
	seedGitflowBoard(t, mr, srv)
	sess, _, err := srv.getOrCreate("myrepo")
	if err != nil {
		t.Fatal(err)
	}

	_, m2 := scriptedConfirmFlow(t, sess, "откати ветку",
		tools.ToolCall{Name: actionBranchReject, Arguments: "{}"},
		"Подтвердите откат ветки ai/myrepo и возврат на main? (да/нет)",
		"Ветка отклонена.",
	)
	if !git.saw("git reset --hard main") || !git.saw("git branch -D ai/myrepo") {
		t.Fatalf("после «да» ветка не отклонена, вызовы: %v", git.callsList())
	}
	if !strings.Contains(m2.Content, "Ветка отклонена") {
		t.Fatalf("финальный ответ = %q", m2.Content)
	}
}

// TestChatAssistantReleaseEpicAfterConfirm — «залей в main»: до «да» релиз
// не трогает git, после «да» релизная ветка эпика вливается в main и пушится.
func TestChatAssistantReleaseEpicAfterConfirm(t *testing.T) {
	git := mockReleaseGit("")
	srv, _, mr := setupGitflow(t, git)
	seedReleaseBoard(t, mr, srv)
	sess, _, err := srv.getOrCreate("myrepo")
	if err != nil {
		t.Fatal(err)
	}

	_, _ = scriptedConfirmFlow(t, sess, "залей в main",
		tools.ToolCall{Name: actionEpicRelease, Arguments: `{"epic_id":"epic-1"}`},
		"Подтвердите релиз эпика epic-1 в main? (да/нет)",
		"Эпик epic-1 влит в main.",
	)
	if !git.saw("git merge --no-ff -m эпик epic-1: релиз в main из ai/epic/e1 ai/epic/e1") ||
		!git.saw("git push git@gitlab.com:g/myrepo.git main") {
		t.Fatalf("релиз не выполнен после подтверждения, вызовы: %v", git.callsList())
	}
}

// TestChatAssistantMergeTaskAfterConfirm — «замерджь задачу»: до «да» git не
// мутируется, после «да» ветка задачи вливается в релизную ветку эпика и
// пушится.
func TestChatAssistantMergeTaskAfterConfirm(t *testing.T) {
	git := mockMergeGit("")
	srv, _, mr := setupGitflow(t, git)
	seedGitflowBoard(t, mr, srv)
	sess, _, err := srv.getOrCreate("myrepo")
	if err != nil {
		t.Fatal(err)
	}

	_, _ = scriptedConfirmFlow(t, sess, "замерджь задачу task-1",
		tools.ToolCall{Name: actionTaskMerge, Arguments: `{"task_id":"task-1"}`},
		"Подтвердите мёрдж ветки ai/task/t1 в релизную ветку ai/epic/e1? (да/нет)",
		"Задача task-1 влита в релизную ветку ai/epic/e1.",
	)
	if !git.saw("git worktree add ") ||
		!git.saw("git merge --no-ff -m задача task-1: влитие в релиз эпика epic-1 ai/task/t1") {
		t.Fatalf("мёрдж не выполнен после подтверждения, вызовы: %v", git.callsList())
	}
}

// TestChatAssistantDeleteTaskAfterConfirm — «удали задачу #12»: ассистент
// сначала спрашивает подтверждение, по «да» вызывает BoardDeleteTask и задача
// исчезает с доски.
func TestChatAssistantDeleteTaskAfterConfirm(t *testing.T) {
	srv, _, mr := newTestServer(t)
	ctx := context.Background()
	registerTestDir(t, srv, "proj-del-task")

	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "proj-del-task"})
	defer store.Close()
	if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Релиз"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "task-12", Title: "Лишняя задача"},
		EpicID:   "epic-1",
	}); err != nil {
		t.Fatal(err)
	}

	sess, _, err := srv.getOrCreate("proj-del-task")
	if err != nil {
		t.Fatal(err)
	}

	// Раунд 1: модель следует правилу промпта и спрашивает подтверждение
	// (BoardDeleteTask — инструмент доски без жёсткого гейта, защита — промпт).
	m1 := runScriptedChatAssistant(t, sess, "удали задачу task-12",
		&runner.ModelReply{Content: "Подтвердите удаление задачи task-12? (да/нет)", FinishReason: "stop"},
	)
	if !strings.Contains(m1.Content, "Подтвердите") {
		t.Fatalf("ассистент не запросил подтверждение: %q", m1.Content)
	}

	// Задача до «да» ещё на доске.
	if _, err := sess.board.GetTask(ctx, "task-12"); err != nil {
		t.Fatalf("до подтверждения задача не должна быть удалена: %v", err)
	}

	// Пользователь подтверждает → модель удаляет задачу инструментом доски.
	sess.append(chat.RoleUser, "да", "user", "", nil)
	sess.runChatAssistant(context.Background(), "да", &scriptedGenerateProvider{chat: &scriptedChatProvider{
		replies: []*runner.ModelReply{
			{ToolCalls: []tools.ToolCall{{
				Name:      tools.BoardDeleteTask,
				Arguments: `{"task_id":"task-12"}`,
			}}, FinishReason: "tool_calls"},
			{Content: "Задача task-12 удалена.", FinishReason: "stop"},
		},
	}})
	m2 := waitChatAssistantContains(t, sess, 3*time.Second, "удалена")
	if !strings.Contains(m2.Content, "удалена") {
		t.Fatalf("финал = %q", m2.Content)
	}
	if _, err := sess.board.GetTask(ctx, "task-12"); err == nil {
		t.Fatal("задача не удалена с доски после подтверждения")
	}
}

// TestChatAssistantKanbanStartLaunchesOrchestration — «запусти канбан по
// эпику»: безопасный мост KanbanStart выполняется БЕЗ подтверждения и
// запускает оркестрацию (общая механика continue). Провайдер оркестрации —
// stub, останавливающий цикл ошибкой (чтобы раннер не ушёл в циклы).
func TestChatAssistantKanbanStartLaunchesOrchestration(t *testing.T) {
	srv, _, mr := newTestServer(t)
	ctx := context.Background()
	registerTestDir(t, srv, "proj-kb-act")

	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "proj-kb-act"})
	defer store.Close()
	if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{TaskID: "epic-1", Title: "Канбан"}}); err != nil {
		t.Fatal(err)
	}

	sess, _, err := srv.getOrCreate("proj-kb-act")
	if err != nil {
		t.Fatal(err)
	}
	// Оркестрация резолвит провайдер через srv.prov: подменяем на stub,
	// который немедленно останавливает цикл ошибкой (без сети).
	srv.prov = providerResolve{prov: harnessStubProvider{}, done: true}

	m := runScriptedChatAssistant(t, sess, "запусти канбан по эпику",
		&runner.ModelReply{ToolCalls: []tools.ToolCall{{Name: actionKanbanStart, Arguments: "{}"}}, FinishReason: "tool_calls"},
		&runner.ModelReply{Content: "Запускаю канбан по доске.", FinishReason: "stop"},
	)
	if !strings.Contains(m.Content, "Запускаю канбан") {
		t.Fatalf("финал = %q", m.Content)
	}

	// Оркестрация действительно стартовала: в истории роль status с текстом
	// «Оркестрация запущена» (start() пишет его синхронно).
	waitChatRole(t, sess, chat.RoleStatus, 3*time.Second)
	hist, err := sess.chat.History(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range hist {
		if strings.Contains(h.Content, "Оркестрация запущена") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("оркестрация не запущена, история: %+v", hist)
	}
	sess.stop()
}