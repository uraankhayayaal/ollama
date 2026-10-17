package kafkarelay

import (
	"testing"
	"time"

	"ai/infrastructure/runevents"
)

func mustNow() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

func TestAssembler_TokensThenMessage(t *testing.T) {
	r := &runState{runID: "r1", pending: make(map[string][]time.Time)}
	t0 := mustNow()
	if _, ok := r.handle(runevents.Event{Type: runevents.TypeTokenCount, In: 10, Out: 20, Time: t0}); ok {
		t.Fatalf("граница раунда без текста не даёт шаг")
	}
	t1 := t0.Add(50 * time.Millisecond)
	if _, ok := r.handle(runevents.Event{Type: runevents.TypeMessage, Role: "assistant", Content: "done", Time: t1}); ok {
		t.Fatalf("message-only шаг не публикуется сразу: за текстом может идти инструмент")
	}
	// Раунд закрылся приходом токенов следующего: шаг уходит со старыми токенами.
	qs, ok := r.handle(runevents.Event{Type: runevents.TypeTokenCount, In: 1, Out: 1, Time: t1.Add(time.Second)})
	if !ok {
		t.Fatalf("ожидали message-only шаг на границе раунда")
	}
	if qs.tokensIn != 10 || qs.tokensOut != 20 {
		t.Fatalf("токены закрытого раунда: in=%d out=%d", qs.tokensIn, qs.tokensOut)
	}
	if qs.tool != "" || qs.status != StepStatusSuccess || qs.thought != "done" {
		t.Fatalf("неверный шаг: %+v", qs)
	}
}

func TestAssembler_ToolStep(t *testing.T) {
	r := newRunState("r1")
	t0 := mustNow()
	r.handle(runevents.Event{Type: runevents.TypeTokenCount, In: 5, Out: 5, Time: t0})
	r.handle(runevents.Event{Type: runevents.TypeMessage, Role: "assistant", Content: "thought", Time: t0.Add(10 * time.Millisecond)})
	r.handle(runevents.Event{Type: runevents.TypeToolStart, Tool: "bash", Time: t0.Add(20 * time.Millisecond)})
	res, ok := r.handle(runevents.Event{Type: runevents.TypeToolResult, Tool: "bash", OK: true, Time: t0.Add(120 * time.Millisecond)})
	if !ok {
		t.Fatalf("ожидали шаг после tool_result")
	}
	if res.tool != "bash" || res.status != StepStatusSuccess {
		t.Fatalf("неверный шаг: tool=%q status=%q", res.tool, res.status)
	}
	if res.tokensIn != 5 || res.tokensOut != 5 {
		t.Fatalf("токены первого шага должны сохраниться: %d/%d", res.tokensIn, res.tokensOut)
	}
	if res.latencyMs < 90 { // ~100ms
		t.Fatalf("латентность: %d", res.latencyMs)
	}
}

func TestAssembler_ParallelTools(t *testing.T) {
	r := newRunState("r1")
	t0 := mustNow()
	r.handle(runevents.Event{Type: runevents.TypeTokenCount, In: 3, Out: 3, Time: t0})
	r.handle(runevents.Event{Type: runevents.TypeMessage, Role: "assistant", Content: "t", Time: t0.Add(1 * time.Millisecond)})
	r.handle(runevents.Event{Type: runevents.TypeToolStart, Tool: "edit", Time: t0.Add(5 * time.Millisecond)})
	r.handle(runevents.Event{Type: runevents.TypeToolStart, Tool: "read", Time: t0.Add(6 * time.Millisecond)})
	s1, ok := r.handle(runevents.Event{Type: runevents.TypeToolResult, Tool: "edit", OK: true, Time: t0.Add(25 * time.Millisecond)})
	if !ok || s1.tool != "edit" || s1.tokensIn != 3 || s1.tokensOut != 3 {
		t.Fatalf("первый шаг параллельной волны неверен: %+v ok=%t", s1, ok)
	}
	s2, ok := r.handle(runevents.Event{Type: runevents.TypeToolResult, Tool: "read", OK: false, Time: t0.Add(16 * time.Millisecond)}) // раньше, но FIFO по pending
	if !ok {
		t.Fatalf("ожидали второй шаг")
	}
	if s2.tool != "read" || s2.status != StepStatusError {
		t.Fatalf("второй шаг: tool=%q status=%q", s2.tool, s2.status)
	}
	if s2.tokensIn != 0 || s2.tokensOut != 0 {
		t.Fatalf("токены не должны дублироваться: %d/%d", s2.tokensIn, s2.tokensOut)
	}
}

func TestAssembler_FlushMessageOnly(t *testing.T) {
	r := newRunState("r1")
	t0 := mustNow()
	r.handle(runevents.Event{Type: runevents.TypeTokenCount, In: 1, Out: 2, Time: t0})
	r.handle(runevents.Event{Type: runevents.TypeMessage, Role: "assistant", Content: "final", Time: t0.Add(10 * time.Millisecond)})
	// раунд не получил инструментов, следующий токен не пришёл
	qs, ok := r.flushStale(t0.Add(6*time.Second), 5*time.Second)
	if !ok {
		t.Fatalf("ожидали flush message-only шага")
	}
	if qs.tool != "" || qs.tokensIn != 1 || qs.tokensOut != 2 {
		t.Fatalf("flush неверный: %+v", qs)
	}
}

func TestAssembler_UserMessageIgnored(t *testing.T) {
	r := newRunState("r1")
	t0 := mustNow()
	r.handle(runevents.Event{Type: runevents.TypeTokenCount, In: 0, Out: 1, Time: t0})
	qs, ok := r.handle(runevents.Event{Type: runevents.TypeMessage, Role: "user", Content: "injection", Time: t0.Add(time.Millisecond)})
	if ok {
		t.Fatalf("user-сообщения не должны формировать шаг: %+v", qs)
	}
	if _, ok = r.handle(runevents.Event{Type: runevents.TypeMessage, Role: "assistant", Content: "answer", Time: t0.Add(2 * time.Millisecond)}); ok {
		t.Fatalf("текст ассистента не должен закрывать шаг сразу")
	}
	qs, ok = r.flushStale(t0.Add(6*time.Second), 5*time.Second)
	if !ok || qs.tool != "" || qs.thought != "answer" || qs.tokensIn != 0 || qs.tokensOut != 1 {
		t.Fatalf("ожидали финальный message-only шаг: %+v ok=%t", qs, ok)
	}
}
