package kafkarelay

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"ai/infrastructure/runevents"
)

func testRelay(t *testing.T, queueSize int, publish publishFunc) *Relay {
	t.Helper()
	relay := newRelay(Config{
		QueueSize:     queueSize,
		FlushInterval: 30 * time.Millisecond,
		WriteTimeout:  time.Second,
		HarnessName:   "test-harness",
		AgentVersion:  "test-1",
	}, publish)
	t.Cleanup(relay.Close)
	return relay
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("условие не выполнилось за 2 секунды")
}

type capture struct {
	mu   sync.Mutex
	got  []AgentStep
	stop chan struct{}
}

func newCapture() *capture { return &capture{stop: make(chan struct{})} }

func (c *capture) publish(ctx context.Context, s AgentStep) error {
	c.mu.Lock()
	c.got = append(c.got, s)
	c.mu.Unlock()
	return nil
}

func (c *capture) steps() []AgentStep {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]AgentStep(nil), c.got...)
}

// toolRound эмитит полный раунд с инструментом через sink.
func toolRound(sink runevents.Sink, scope string, t0 time.Time) {
	sink(runevents.Event{Type: runevents.TypeTokenCount, In: 10, Out: 5, Scope: scope, Time: t0})
	sink(runevents.Event{Type: runevents.TypeMessage, Role: "assistant", Content: "нужен bash", Scope: scope, Time: t0.Add(time.Millisecond)})
	sink(runevents.Event{Type: runevents.TypeToolStart, Tool: "bash", Scope: scope, Time: t0.Add(2 * time.Millisecond)})
	sink(runevents.Event{Type: runevents.TypeToolResult, Tool: "bash", OK: true, Scope: scope, Time: t0.Add(102 * time.Millisecond)})
}

func TestRelay_PublishesToolStepsWithEpicRunID(t *testing.T) {
	rec := newCapture()
	relay := testRelay(t, 16, rec.publish)
	sink := relay.SessionSink("proj", ModeOrchestration, nil)

	t0 := time.Now().UTC()
	toolRound(sink, "epic:EP-1", t0)
	toolRound(sink, "epic:EP-1", t0.Add(time.Second))

	waitFor(t, func() bool { return len(rec.steps()) == 2 })
	got := rec.steps()

	wantRun := scopeRunID(context.Background(), "proj", "epic:EP-1", nil)
	for i, s := range got {
		if s.RunID != wantRun {
			t.Fatalf("шаг %d: run_id=%q, want %q", i, s.RunID, wantRun)
		}
		if s.StepNumber != i+1 {
			t.Fatalf("шаг %d: step_number=%d", i, s.StepNumber)
		}
		if s.ToolCalled != "bash" || s.Status != StepStatusSuccess {
			t.Fatalf("шаг %d: tool=%q status=%q", i, s.ToolCalled, s.Status)
		}
		if s.TokensUsed != 15 {
			t.Fatalf("шаг %d: tokens_used=%d, want 15", i, s.TokensUsed)
		}
		if s.HarnessName != "test-harness" || s.AgentVersion != "test-1" {
			t.Fatalf("шаг %d: harness=%q version=%q", i, s.HarnessName, s.AgentVersion)
		}
		if s.LatencyMs < 90 {
			t.Fatalf("шаг %d: latency=%d", i, s.LatencyMs)
		}
		if err := s.Validate(); err != nil {
			t.Fatalf("шаг %d невалиден по контракту приёмщика: %v", i, err)
		}
	}
}

func TestRelay_AssistantHasParallelRunID(t *testing.T) {
	rec := newCapture()
	relay := testRelay(t, 16, rec.publish)
	orch := relay.SessionSink("proj", ModeOrchestration, nil)
	asst := relay.SessionSink("proj", ModeAssistant, nil)

	t0 := time.Now().UTC()
	toolRound(orch, "", t0)
	toolRound(asst, "", t0)

	waitFor(t, func() bool { return len(rec.steps()) == 2 })
	runs := make(map[string]int)
	for _, s := range rec.steps() {
		runs[s.RunID]++
	}
	orchRun := scopeRunID(context.Background(), "proj", "", nil)
	if runs[orchRun] != 1 {
		t.Fatalf("оркестрация: ран %q получил %d шагов, want 1 (все: %v)", orchRun, runs[orchRun], runs)
	}
	asstRun := assistantRunID("proj")
	if runs[asstRun] != 1 {
		t.Fatalf("ассистент: ран %q получил %d шагов, want 1", asstRun, runs[asstRun])
	}
	if asstRun == orchRun {
		t.Fatalf("оркестрация и ассистент должны жить в разных ранах")
	}
}

func TestRelay_TaskScopeResolvesToEpicRun(t *testing.T) {
	rec := newCapture()
	relay := testRelay(t, 16, rec.publish)
	epicOf := func(context.Context, string) (string, error) { return "EP-7", nil }
	sink := relay.SessionSink("proj", ModeOrchestration, epicOf)

	toolRound(sink, "task:T-1", time.Now().UTC())
	waitFor(t, func() bool { return len(rec.steps()) == 1 })

	want := scopeRunID(context.Background(), "proj", "epic:EP-7", nil)
	if got := rec.steps()[0].RunID; got != want {
		t.Fatalf("task-скоуп должен уходить в ран эпика: got=%q want=%q", got, want)
	}
}

func TestRelay_MessageOnlyStepViaFlush(t *testing.T) {
	rec := newCapture()
	relay := testRelay(t, 16, rec.publish)
	sink := relay.SessionSink("proj", ModeOrchestration, nil)

	t0 := time.Now().UTC()
	sink(runevents.Event{Type: runevents.TypeTokenCount, In: 3, Out: 4, Scope: "epic:EP-1", Time: t0})
	sink(runevents.Event{Type: runevents.TypeMessage, Role: "assistant", Content: "готово", Scope: "epic:EP-1", Time: t0.Add(time.Millisecond)})

	waitFor(t, func() bool { return len(rec.steps()) == 1 })
	s := rec.steps()[0]
	if s.ToolCalled != "" || s.TokensUsed != 7 || s.Thought != "готово" {
		t.Fatalf("message-only шаг неверен: %+v", s)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("невалидный шаг: %v", err)
	}
}

func TestRelay_DropsWhenQueueFull(t *testing.T) {
	blocked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var published int
	blocking := func(ctx context.Context, s AgentStep) error {
		mu.Lock()
		published++
		mu.Unlock()
		once.Do(func() { close(blocked) })
		select {
		case <-release:
		case <-ctx.Done():
		}
		return ctx.Err()
	}
	relay := testRelay(t, 1, blocking)
	sink := relay.SessionSink("proj", ModeOrchestration, nil)

	t0 := time.Now().UTC()
	toolRound(sink, "epic:EP-1", t0) // уходит в publish и блокируется
	waitFor(t, func() bool {
		select {
		case <-blocked:
			return true
		default:
			return false
		}
	})
	toolRound(sink, "epic:EP-1", t0.Add(time.Second))   // занимает очередь (cap=1)
	toolRound(sink, "epic:EP-1", t0.Add(2*time.Second)) // дропается

	waitFor(t, func() bool { return relay.dropped.Load() >= 1 })
	close(release)
	waitFor(t, func() bool { return relay.written.Load()+relay.writeErrs.Load() >= 1 })
	mu.Lock()
	defer mu.Unlock()
	if published > 2 {
		t.Fatalf("опубликовано %d — дроп не сработал", published)
	}
}

func TestRelay_CloseDrainsQueue(t *testing.T) {
	rec := newCapture()
	relay := testRelay(t, 16, rec.publish)
	sink := relay.SessionSink("proj", ModeOrchestration, nil)
	toolRound(sink, "epic:EP-1", time.Now().UTC())
	relay.Close()
	if n := len(rec.steps()); n != 1 {
		t.Fatalf("после Close отправлено %d шагов, want 1", n)
	}
}

func TestRelay_FromEnvDisabledWithoutBrokers(t *testing.T) {
	t.Setenv("KAFKA_BROKERS", "")
	if r := FromEnv(); r != nil {
		t.Fatalf("без KAFKA_BROKERS рлей должен быть выключен")
	}
}

func TestStepCounter_RedisAndFallback(t *testing.T) {
	mr := miniredis.RunT(t)
	c := newStepCounter(mr.Addr(), "", 0)
	defer c.close()
	ctx := context.Background()

	if n := c.next(ctx, "run-a"); n != 1 {
		t.Fatalf("первый номер: %d", n)
	}
	if n := c.next(ctx, "run-a"); n != 2 {
		t.Fatalf("второй номер: %d", n)
	}
	if n := c.next(ctx, "run-b"); n != 1 {
		t.Fatalf("независимый ран: %d", n)
	}
	if ttl := mr.TTL("obs:step:run-a"); ttl <= 0 {
		t.Fatalf("у ключа нет TTL: %v", ttl)
	}

	// Фолбэк: без Redis номера остаются локальными, но монотонными.
	offline := newStepCounter("127.0.0.1:1", "", 0)
	defer offline.close()
	if n := offline.next(ctx, "run-c"); n != 1 {
		t.Fatalf("фолбэк первый: %d", n)
	}
	if n := offline.next(ctx, "run-c"); n != 2 {
		t.Fatalf("фолбэк второй: %d", n)
	}
}
