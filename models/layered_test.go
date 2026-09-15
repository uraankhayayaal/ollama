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
	genCalls int
}

func (s *layerStub) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	s.genCalls++
	if s.genErr != nil {
		return nil, s.genErr
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