package runevents

import (
	"context"
	"encoding/json"
	"testing"
)

// collect — тестовый sink, собирающий события в канал.
func collect(t *testing.T, n int) (chan Event, Sink) {
	t.Helper()
	ch := make(chan Event, n)
	return ch, func(ev Event) { ch <- ev }
}

func TestRouterEventsWithAgent(t *testing.T) {
	ch, sink := collect(t, 4)
	r := NewRouter(sink).WithAgent("architect")

	r.OnMessage("assistant", "план готов", true)
	r.OnToolStart("List", `{"path":"."}`)
	r.OnToolResult("List", `["go.mod"]`, true)

	for i := 0; i < 3; i++ {
		ev := <-ch
		if ev.Agent != "architect" {
			t.Fatalf("event %d: Agent = %q, want architect", i, ev.Agent)
		}
		if ev.Time.IsZero() {
			t.Fatalf("event %d: пустое время", i)
		}
		switch ev.Type {
		case TypeMessage:
			if ev.Role != "assistant" || ev.Content != "план готов" || !ev.Truncated {
				t.Fatalf("message event = %+v", ev)
			}
		case TypeToolStart:
			if ev.Tool != "List" || ev.Arguments != `{"path":"."}` {
				t.Fatalf("tool_start event = %+v", ev)
			}
		case TypeToolResult:
			if ev.Tool != "List" || ev.Result != `["go.mod"]` || !ev.OK {
				t.Fatalf("tool_result event = %+v", ev)
			}
		default:
			t.Fatalf("неожиданный тип %q", ev.Type)
		}
	}
}

func TestWithAgentKeepsSink(t *testing.T) {
	ch, sink := collect(t, 1)
	r := NewRouter(sink)
	r2 := r.WithAgent("devopslead")
	r2.OnMessage("", "ok", false)
	ev := <-ch
	if ev.Agent != "devopslead" || ev.Role != "assistant" {
		t.Fatalf("ev = %+v", ev)
	}
	// Исходный Router не «испорчен» агентом.
	r.OnToolStart("ReadFiles", "")
	if ev := <-ch; ev.Agent != "" || ev.Type != TypeToolStart {
		t.Fatalf("ev = %+v", ev)
	}
}

func TestNilSinkNoPanic(t *testing.T) {
	r := NewRouter(nil).WithAgent("qa")
	r.OnMessage("assistant", "x", false)
	r.OnToolStart("t", "a")
	r.OnToolResult("t", "r", true)
}

func TestEventJSON(t *testing.T) {
	ev := Event{
		Type: TypeToolStart, Agent: "developer", Tool: "Write", Arguments: `{"f":"a"}`,
	}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var back Event
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Type != TypeToolStart || back.Agent != "developer" || back.Tool != "Write" {
		t.Fatalf("roundtrip = %+v", back)
	}
}

func TestContextRoundtrip(t *testing.T) {
	r := NewRouter(nil)
	ctx := WithReporter(context.Background(), r)
	if got := ReporterFromContext(ctx); got != r {
		t.Fatalf("ReporterFromContext = %p, want %p", got, r)
	}
	if ReporterFromContext(context.Background()) != nil {
		t.Fatal("без WithReporter должен возвращаться nil")
	}
}

// TestOnMessageDelta проверяет потоковый фрагмент (Ф-3): тип message_delta,
// накопленный текст и StreamID, помечающий поток.
func TestOnMessageDelta(t *testing.T) {
	ch, sink := collect(t, 2)
	r := NewRouter(sink).WithAgent("developer")

	r.OnMessageDelta("stream-1", "Привет")
	r.OnMessageDelta("stream-1", "Привет, мир!")

	ev := <-ch
	if ev.Type != TypeMessageDelta || ev.Content != "Привет" || ev.StreamID != "stream-1" || ev.Agent != "developer" {
		t.Fatalf("первый фрагмент = %+v", ev)
	}
	ev = <-ch
	if ev.Type != TypeMessageDelta || ev.Content != "Привет, мир!" || ev.StreamID != "stream-1" {
		t.Fatalf("второй фрагмент = %+v", ev)
	}
}

// TestOnTokens проверяет событие потребления токенов: тип tokens, поля
// входа/выхода, реальную скорость генерации и имя агента из WithAgent.
func TestOnTokens(t *testing.T) {
	ch, sink := collect(t, 1)
	r := NewRouter(sink).WithAgent("backendlead")

	r.OnTokens(1234, 56, 42.5)

	ev := <-ch
	if ev.Type != TypeTokenCount {
		t.Fatalf("тип = %q, want tokens", ev.Type)
	}
	if ev.In != 1234 || ev.Out != 56 {
		t.Fatalf("in/out = %d/%d, want 1234/56", ev.In, ev.Out)
	}
	if ev.TPS != 42.5 {
		t.Fatalf("tps = %v, want 42.5", ev.TPS)
	}
	if ev.Agent != "backendlead" {
		t.Fatalf("agent = %q, want backendlead", ev.Agent)
	}
}

// TestOnTokensWithScope проверяет атрибуцию расхода токенов единице работы
// (Ф-2): scope из WithScope попадает в событие, исходный Router остаётся
// без scope (счётчик проекта).
func TestOnTokensWithScope(t *testing.T) {
	ch, sink := collect(t, 2)
	r := NewRouter(sink).WithAgent("developer")

	scoped := r.WithScope("task:T-1")
	scoped.OnTokens(10, 2, 0)
	ev := <-ch
	if ev.Scope != "task:T-1" {
		t.Fatalf("scope = %q, want task:T-1", ev.Scope)
	}
	if ev.Agent != "developer" {
		t.Fatalf("agent = %q, want developer (WithScope сохраняет имя агента)", ev.Agent)
	}

	r.OnTokens(5, 1, 0)
	if ev = <-ch; ev.Scope != "" {
		t.Fatalf("scope исходного Router = %q, want пусто", ev.Scope)
	}
}

// TestWithScopeInContext проверяет проброс scope'а через контекст вызова
// Generate: с репортёром в контексте рождается клон с атрибуцией, без
// репортёра (консольный режим) контекст меняется без паники.
func TestWithScopeInContext(t *testing.T) {
	ch, sink := collect(t, 2)
	ctx := WithReporter(context.Background(), NewRouter(sink))
	scopedCtx := WithScope(ctx, "epic:ARC-01")
	if scopedCtx == ctx {
		t.Fatal("WithScope должен вернуть новый контекст")
	}
	if ReporterFromContext(scopedCtx) == ReporterFromContext(ctx) {
		t.Fatal("WithScope должен вернуть клон репортёра")
	}
	if ReporterFromContext(ctx) == nil {
		t.Fatal("исходный контекст потерял репортёра")
	}

	ReporterFromContext(scopedCtx).OnTokens(3, 1, 0)
	if ev := <-ch; ev.Scope != "epic:ARC-01" {
		t.Fatalf("scope = %q, want epic:ARC-01", ev.Scope)
	}

	// Без репортёра в контексте — тот же контекст, без scope.
	bare := context.Background()
	if got := WithScope(bare, "task:T-1"); got != bare {
		t.Fatalf("без репортёра WithScope = %v, want исходный контекст", got)
	}
	// Вложенный scope перекрывает внешний.
	nested := WithScope(WithScope(ctx, "architecture"), "bugs")
	ReporterFromContext(nested).OnTokens(1, 1, 0)
	if ev := <-ch; ev.Scope != "bugs" {
		t.Fatalf("вложенный scope = %q, want bugs", ev.Scope)
	}
}
