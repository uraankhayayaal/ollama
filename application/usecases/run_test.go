package usecases

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"ai/domain"
)

// fakeAgent — минимальная реализация порта Agent для проверки цикла RunTask.
type fakeAgent struct {
	system []domain.AgentMessage
	user   []domain.AgentMessage
	tools  []domain.ToolDefinition
	calls  []string
	reply  func(name string, args map[string]any) ([]byte, error)
}

func (a *fakeAgent) GetSystemMessages([]domain.AgentMessage) []domain.AgentMessage { return a.system }
func (a *fakeAgent) GetUserMessages() []domain.AgentMessage                        { return a.user }
func (a *fakeAgent) GetTools() []domain.ToolDefinition                             { return a.tools }

func (a *fakeAgent) CallFunction(name string, args map[string]any) ([]byte, error) {
	a.calls = append(a.calls, name)
	if a.reply != nil {
		return a.reply(name, args)
	}
	return []byte("ok"), nil
}

// funcProvider — провайдер с построчным сценарием ответов (реализует
// ChatProvider). Запросы сохраняются для проверки инъекций/истории.
type funcProvider struct {
	next     func(n int) *domain.ModelReply
	n        int
	requests []domain.ChatRequest
}

func (p *funcProvider) ChatOnce(_ context.Context, req domain.ChatRequest) (*domain.ModelReply, error) {
	p.requests = append(p.requests, req)
	if p.next == nil {
		return &domain.ModelReply{Content: "готово"}, nil
	}
	r := p.next(p.n)
	p.n++
	return r, nil
}

// fakeReporter — реализация порта Reporter: фиксирует события цикла.
type fakeReporter struct {
	messages []string
	tools    []string
	tokens   int
}

func (r *fakeReporter) OnMessage(role, content string, _ bool) {
	r.messages = append(r.messages, role+":"+content)
}
func (r *fakeReporter) OnMessageDelta(string, string) {}
func (r *fakeReporter) OnToolStart(tool, _ string)    { r.tools = append(r.tools, "start:"+tool) }
func (r *fakeReporter) OnToolResult(tool, _ string, _ bool) {
	r.tools = append(r.tools, "result:"+tool)
}
func (r *fakeReporter) OnTokens(in, out int64, _ float64) { r.tokens += int(in + out) }

// fakeInjections — реализация InjectionApplier: добавляет видимый маркер.
type fakeInjections struct{ turns int }

func (f *fakeInjections) Apply(history []domain.Message, _ int) []domain.Message {
	f.turns++
	return append(history, domain.Message{Role: "user", Content: "INJECT"})
}

func testRunConfig(max int) RunConfig {
	return RunConfig{
		MaxRounds: max,
		ParseArguments: func(raw string) (map[string]any, error) {
			if raw == "" {
				return map[string]any{}, nil
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(raw), &m); err != nil {
				return nil, err
			}
			return m, nil
		},
		IsParallelSafe: func(string) bool { return false },
	}
}

func TestRunTaskCompletesOnTextReply(t *testing.T) {
	agent := &fakeAgent{
		system: []domain.AgentMessage{{Message: "ты агент"}},
		user:   []domain.AgentMessage{{Message: "сделай"}},
	}
	prov := &funcProvider{next: func(int) *domain.ModelReply { return &domain.ModelReply{Content: "готово"} }}
	rep := &fakeReporter{}

	resp, err := RunTask(context.Background(), RunPorts{
		Provider: prov, Agent: agent, Reporter: rep, Config: testRunConfig(3),
	})
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if resp.Content != "готово" {
		t.Errorf("content = %q, ожидалось «готово»", resp.Content)
	}
	if resp.Rounds != 1 || resp.Truncated || resp.Looped {
		t.Errorf("ожидался один успешный раунд: %+v", resp)
	}
	if len(prov.requests) != 1 {
		t.Fatalf("запросов к модели = %d, ожидался 1", len(prov.requests))
	}
	if got := prov.requests[0].Messages[1]; got.Role != "user" || got.Content != "сделай" {
		t.Errorf("история запроса не содержит пользовательского сообщения: %+v", prov.requests[0].Messages)
	}
	if len(rep.messages) == 0 || rep.messages[0] != "assistant:готово" {
		t.Errorf("репортёр не получил финальное сообщение: %v", rep.messages)
	}
}

func TestRunTaskExecutesToolAndFeedsResultBack(t *testing.T) {
	agent := &fakeAgent{
		user:  []domain.AgentMessage{{Message: "прочитай"}},
		tools: []domain.ToolDefinition{{Name: "Echo"}},
		reply: func(_ string, args map[string]any) ([]byte, error) {
			return []byte(fmt.Sprintf("echo:%v", args["x"])), nil
		},
	}
	prov := &funcProvider{next: func(n int) *domain.ModelReply {
		switch n {
		case 0:
			return &domain.ModelReply{ToolCalls: []domain.ToolCall{
				{ID: "1", Name: "Echo", Arguments: `{"x":"hi"}`},
			}}
		default:
			return &domain.ModelReply{Content: "готово"}
		}
	}}
	rep := &fakeReporter{}

	resp, err := RunTask(context.Background(), RunPorts{
		Provider: prov, Agent: agent, Reporter: rep, Config: testRunConfig(5),
	})
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "Echo" {
		t.Fatalf("ожидался один вызов Echo: %+v", resp.ToolCalls)
	}
	if len(agent.calls) != 1 || agent.calls[0] != "Echo" {
		t.Errorf("инструмент не выполнен: %v", agent.calls)
	}
	if resp.Rounds != 2 {
		t.Errorf("раундов = %d, ожидалось 2", resp.Rounds)
	}
	// Второй запрос к модели должен содержать результат инструмента (роль tool).
	found := false
	for _, m := range prov.requests[1].Messages {
		if m.Role == "tool" && m.ToolName == "Echo" && m.Content == "echo:hi" {
			found = true
		}
	}
	if !found {
		t.Errorf("результат инструмента не вернулся в историю: %+v", prov.requests[1].Messages)
	}
	if len(rep.tools) < 2 {
		t.Errorf("репортёр не получил события инструмента: %v", rep.tools)
	}
}

func TestRunTaskStopsAtRoundLimit(t *testing.T) {
	agent := &fakeAgent{
		user: []domain.AgentMessage{{Message: "работай"}},
		reply: func(_ string, args map[string]any) ([]byte, error) {
			return []byte(fmt.Sprintf("r=%v", args["i"])), nil
		},
	}
	prov := &funcProvider{next: func(n int) *domain.ModelReply {
		// Уникальные аргументы каждый раунд: повторный вызов одного и того же
		// не должен ложно срабатывать как петля.
		return &domain.ModelReply{ToolCalls: []domain.ToolCall{
			{ID: fmt.Sprintf("%d", n), Name: "Echo", Arguments: fmt.Sprintf(`{"i":%d}`, n)},
		}}
	}}

	resp, err := RunTask(context.Background(), RunPorts{
		Provider: prov, Agent: agent, Config: testRunConfig(3),
	})
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if !resp.Truncated {
		t.Fatalf("ожидался Truncated по лимиту раундов: %+v", resp)
	}
	if resp.Rounds != 3 {
		t.Errorf("раундов = %d, ожидалось 3", resp.Rounds)
	}
	if len(prov.requests) != 3 {
		t.Errorf("запросов к модели = %d, ожидалось 3", len(prov.requests))
	}
}

// TestRunTaskShortHistoryNoSystem — регресс: история короче минимального
// «хвоста» разрыва петли (агент без системных сообщений) не должна паниковать
// на срезе messages[:loopPrefix].
func TestRunTaskShortHistoryNoSystem(t *testing.T) {
	agent := &fakeAgent{user: []domain.AgentMessage{{Message: "сделай"}}}
	prov := &funcProvider{next: func(int) *domain.ModelReply { return &domain.ModelReply{Content: "готово"} }}

	resp, err := RunTask(context.Background(), RunPorts{
		Provider: prov, Agent: agent, Config: testRunConfig(2),
	})
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if resp.Content != "готово" {
		t.Errorf("content = %q, ожидалось «готово»", resp.Content)
	}
}

func TestRunTaskAppliesInjections(t *testing.T) {
	agent := &fakeAgent{
		system: []domain.AgentMessage{{Message: "ты агент"}},
		user:   []domain.AgentMessage{{Message: "сделай"}},
	}
	prov := &funcProvider{next: func(int) *domain.ModelReply { return &domain.ModelReply{Content: "готово"} }}
	inj := &fakeInjections{}

	if _, err := RunTask(context.Background(), RunPorts{
		Provider: prov, Agent: agent, Injections: inj, Config: testRunConfig(2),
	}); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if inj.turns != 1 {
		t.Errorf("InjectionApplier вызван %d раз, ожидался 1", inj.turns)
	}
	last := prov.requests[0].Messages[len(prov.requests[0].Messages)-1]
	if last.Content != "INJECT" {
		t.Errorf("инъекция не попала в запрос: %+v", prov.requests[0].Messages)
	}
}
