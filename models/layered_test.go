package models

import (
	"ai/agents"
	"ai/runner"
	"ai/tools"
	"context"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

// layerStub — минимальный провайдер-слой для тестов LayeredProvider.
type layerStub struct {
	name     string
	genErr   error
	trunc    bool
	looped   bool
	loopWhy  string
	genCalls int
	// resumeSeen — resume-состояние, которое видел слой (проверяем, что
	// эскалация начинает цикл заново, а не продолжает застрявший диалог).
	resumeSeen *runner.ResumeState
}

func (s *layerStub) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	s.genCalls++
	s.resumeSeen = runner.ResumeStateFromContext(ctx)
	if s.genErr != nil {
		return nil, s.genErr
	}
	if s.looped {
		why := s.loopWhy
		if why == "" {
			why = "повтор одного и того же вызова"
		}
		return &runner.AgentResponse{Looped: true, LoopReason: why, Rounds: 5}, nil
	}
	if s.trunc {
		return &runner.AgentResponse{Content: "частично", Truncated: true, Rounds: 5}, nil
	}
	return &runner.AgentResponse{Content: "ответ_" + s.name}, nil
}

func (s *layerStub) ChatOnce(context.Context, agents.Agent, []runner.Message) (*runner.ModelReply, error) {
	return &runner.ModelReply{}, nil
}

// testAgent — минимальный agents.Agent для тестов слоёв.
type testAgent struct{ name string }

func (a *testAgent) GetUserMessages() []agents.Message {
	return []agents.Message{{Type: agents.MessageTypeHuman, Message: "задача"}}
}
func (a *testAgent) GetSystemMessages(text []agents.Message) []agents.Message {
	return []agents.Message{{Type: agents.MessageTypeSystem, Message: "ты агент"}}
}
func (a *testAgent) GetTools() []tools.ToolDefinition { return nil }
func (a *testAgent) GetToolsForOllama() []api.Tool     { return nil }
func (a *testAgent) CallFunction(string, map[string]any) ([]byte, error) {
	return []byte("ok"), nil
}

// Имя типа агента содержит "Lead" → эвристика считает его тяжёлым:
// большой слой вызывается сразу, малый — вообще не задействуется.
func TestLayeredRouteHeavyByTypeName(t *testing.T) {
	small := &layerStub{name: "small"}
	large := &layerStub{name: "large"}
	prov := NewLayeredProvider(small, large).(*LayeredProvider)

	resp, err := prov.Generate(context.Background(), &backendLeadAgent{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(resp.Content, "large") {
		t.Fatalf("тяжёлый агент должен получить ответ большой модели, got %q", resp.Content)
	}
	if large.genCalls != 1 {
		t.Fatalf("большая модель должна быть вызвана ровно один раз, got %d", large.genCalls)
	}
	if small.genCalls != 0 {
		t.Fatalf("малая модель не должна вызываться для тяжёлого агента, got %d", small.genCalls)
	}
}

// backendLeadAgent — тип, чьё имя типа эвристически похоже на лида.
type backendLeadAgent struct{ testAgent }

// Агент, объявляющий NeedsHeavyModel, маршрутизируется на большой слой даже
// без эвристики по имени типа.
func TestLayeredRouteHeavyByInterface(t *testing.T) {
	small := &layerStub{name: "small"}
	large := &layerStub{name: "large"}
	prov := NewLayeredProvider(small, large).(*LayeredProvider)

	agent := &heavyTestAgent{testAgent: &testAgent{name: "crawler"}}
	if _, ok := any(agent).(NeedsHeavyModel); !ok {
		t.Fatal("heavyTestAgent должен реализовывать NeedsHeavyModel")
	}
	resp, err := prov.Generate(context.Background(), agent)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(resp.Content, "large") {
		t.Fatalf("NeedsHeavyModel должен вести на большую модель, got %q", resp.Content)
	}
	if small.genCalls != 0 || large.genCalls != 1 {
		t.Fatalf("вызовы: small=%d, large=%d (ожидали 0 и 1)", small.genCalls, large.genCalls)
	}
}

// heavyTestAgent — агент, явно требующий большую модель.
type heavyTestAgent struct{ *testAgent }

func (a *heavyTestAgent) NeedsHeavyModel() bool { return true }

// Обычный (лёгкий) агент работает на малой модели; большой слой не вызывается.
func TestLayeredLightAgentUsesSmall(t *testing.T) {
	small := &layerStub{name: "small"}
	large := &layerStub{name: "large"}
	prov := NewLayeredProvider(small, large).(*LayeredProvider)

	resp, err := prov.Generate(context.Background(), &testAgent{name: "developer"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(resp.Content, "small") {
		t.Fatalf("лёгкий агент должен получить ответ малой модели, got %q", resp.Content)
	}
	if small.genCalls != 1 || large.genCalls != 0 {
		t.Fatalf("вызовы: small=%d (ожидали 1), large=%d (ожидали 0)", small.genCalls, large.genCalls)
	}
}

// Ошибка малого слоя → эскалация на большой, ответ big считается финальным.
func TestLayeredEscalatesOnSmallError(t *testing.T) {
	small := &layerStub{name: "small", genErr: errStub("малая модель упала")}
	large := &layerStub{name: "large"}
	prov := NewLayeredProvider(small, large).(*LayeredProvider)

	resp, err := prov.Generate(context.Background(), &testAgent{name: "developer"})
	if err != nil {
		t.Fatalf("после эскалации ошибки быть не должно: %v", err)
	}
	if !strings.Contains(resp.Content, "large") {
		t.Fatalf("после эскалации ответ приходит от большой модели, got %q", resp.Content)
	}
	if small.genCalls != 1 || large.genCalls != 1 {
		t.Fatalf("вызовы: small=%d, large=%d (ожидали 1 и 1)", small.genCalls, large.genCalls)
	}
}

// Зацикливание малого слоя (Looped) — такой же повод для эскалации, как срыв
// по лимиту раундов: задачу выполняет БОЛЬШАЯ модель. Продолжать тот же
// застрявший диалог нельзя, поэтому resume-история сбрасывается.
func TestLayeredEscalatesOnLoop(t *testing.T) {
	small := &layerStub{name: "small", looped: true, loopWhy: "повтор Run npm test"}
	large := &layerStub{name: "large"}
	prov := NewLayeredProvider(small, large).(*LayeredProvider)

	resume := &runner.ResumeState{Rounds: 120, Messages: []runner.Message{{Role: "system", Content: "s"}}}
	ctx := runner.WithResumeState(context.Background(), resume)

	resp, err := prov.Generate(ctx, &testAgent{name: "developer"})
	if err != nil {
		t.Fatalf("после эскалации ошибки быть не должно: %v", err)
	}
	if !strings.Contains(resp.Content, "large") {
		t.Fatalf("зацикливание должно уводить задачу на большую модель, got %q", resp.Content)
	}
	if small.genCalls != 1 || large.genCalls != 1 {
		t.Fatalf("вызовы: small=%d, large=%d (ожидали 1 и 1)", small.genCalls, large.genCalls)
	}
	if small.resumeSeen == nil || small.resumeSeen.Rounds != 120 {
		t.Fatalf("малый слой должен увидеть сохранённый диалог, got %#v", small.resumeSeen)
	}
	if large.resumeSeen != nil {
		t.Fatalf("большая модель должна начать с чистой истории, а не продолжать петлю: %#v", large.resumeSeen)
	}
}

// Большая модель тоже зациклилась — её вердикт возвращается наружу: вызывающий
// код обязан честно уронить задачу (AgentResponse.Looped), а не считать её
// выполненной.
func TestLayeredReturnsLoopVerdictFromLarge(t *testing.T) {
	small := &layerStub{name: "small", looped: true}
	large := &layerStub{name: "large", looped: true, loopWhy: "петля на большой модели"}
	prov := NewLayeredProvider(small, large).(*LayeredProvider)

	resp, err := prov.Generate(context.Background(), &testAgent{name: "developer"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !resp.Looped || resp.LoopReason != "петля на большой модели" {
		t.Fatalf("наружу должен уйти вердикт большой модели, got %+v", resp)
	}
	if resp.StopReason() != "зацикливание" {
		t.Fatalf("StopReason = %q", resp.StopReason())
	}
	if err := resp.LoopError("задача T-1"); err == nil {
		t.Fatal("вызывающий код должен получить ошибку вместо тихого успеха")
	}
}

// Оба слоя упали — возвращается первопричина малого слоя.
func TestLayeredBothFail(t *testing.T) {
	small := &layerStub{name: "small", genErr: errStub("small boom")}
	large := &layerStub{name: "large", genErr: errStub("large boom")}
	prov := NewLayeredProvider(small, large).(*LayeredProvider)

	_, err := prov.Generate(context.Background(), &testAgent{name: "developer"})
	if err == nil || !strings.Contains(err.Error(), "small boom") {
		t.Fatalf("ожидали первопричину малого слоя, got %v", err)
	}
}

// Принудительный тяжёлый режим (LLM_ALWAYS_HEAVY=1) заставляет даже лёгкого
// агента работать на большой модели.
func TestLayeredAlwaysHeavyEnv(t *testing.T) {
	t.Setenv("LLM_ALWAYS_HEAVY", "1")

	small := &layerStub{name: "small"}
	large := &layerStub{name: "large"}
	prov := NewLayeredProvider(small, large).(*LayeredProvider)

	resp, err := prov.Generate(context.Background(), &testAgent{name: "developer"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(resp.Content, "large") {
		t.Fatalf("LLM_ALWAYS_HEAVY должен гнать на большую модель, got %q", resp.Content)
	}
	if small.genCalls != 0 || large.genCalls != 1 {
		t.Fatalf("вызовы: small=%d, large=%d (ожидали 0 и 1)", small.genCalls, large.genCalls)
	}
}

// Без большой модели LayeredProvider не создаётся — возвращается small.
func TestNewLayeredProviderWithoutLarge(t *testing.T) {
	small := &layerStub{name: "small"}
	if got := NewLayeredProvider(small, nil); got != (LLMProvider)(small) {
		t.Fatal("без большого слоя должен вернуться малый без обёртки")
	}
	var nilP LLMProvider
	if got := NewLayeredProvider(nil, nilP); got != nil {
		t.Fatal("два nil-слоя должны дать nil провайдер")
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }