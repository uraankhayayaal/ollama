package hitl

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"ai/application/planner"
)

// fakeHost — заглушка GateHost: собирает статусы и события затвора.
type fakeHost struct {
	mu      sync.Mutex
	stopped bool
	running bool
	status  []string
	gates   []GateEvent
}

func (h *fakeHost) Stopped() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stopped
}

func (h *fakeHost) Running() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.running
}

func (h *fakeHost) BroadcastStatus(status, detail string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status = append(h.status, status+":"+detail)
}

func (h *fakeHost) PublishGate(ev GateEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.gates = append(h.gates, ev)
}

func (h *fakeHost) statuses() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.status...)
}

func (h *fakeHost) gateEvents() []GateEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]GateEvent(nil), h.gates...)
}

func waitGating(t *testing.T, g *Gate) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if gating, _, _ := g.State(); gating {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("затвор не встал")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestGateWaitPublishesBlocksThenApprove(t *testing.T) {
	host := &fakeHost{running: true}
	g := NewGate(host)

	type result struct {
		d   planner.GateDecision
		err error
	}
	done := make(chan result, 1)
	go func() {
		d, err := g.Wait(context.Background(), "epics", "подтвердите эпики", []string{"EP-1", "EP-2"})
		done <- result{d, err}
	}()
	waitGating(t, g)

	if gating, typ, msg := g.State(); !gating || typ != "epics" || msg == nil || len(msg.IDs) != 2 || msg.IDs[0] != "EP-1" {
		t.Fatalf("state = %v %q %+v", gating, typ, msg)
	}
	evs := host.gateEvents()
	if len(evs) != 1 || evs[0].Gate != "epics" || evs[0].Summary != "подтвердите эпики" {
		t.Fatalf("gate events = %+v", evs)
	}
	if got := host.statuses(); len(got) != 1 || got[0] != "waiting:epics" {
		t.Fatalf("statuses = %v", got)
	}

	if err := g.Approve("epics", true, "ок"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("wait: %v", r.err)
		}
		if !r.d.Approved || r.d.Reason != "ок" {
			t.Fatalf("decision = %+v", r.d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait не завершился после approve")
	}
	if gating, _, msg := g.State(); gating || msg != nil {
		t.Fatalf("после approve state = %v %+v, want false/nil", gating, msg)
	}
	if got := host.statuses(); got[len(got)-1] != "running:" {
		t.Fatalf("последний статус = %q, want running:", got[len(got)-1])
	}
}

func TestGateWaitReject(t *testing.T) {
	host := &fakeHost{running: true}
	g := NewGate(host)
	done := make(chan planner.GateDecision, 1)
	go func() {
		d, _ := g.Wait(context.Background(), "tasks", "s", nil)
		done <- d
	}()
	waitGating(t, g)
	if err := g.Approve("tasks", false, "мало данных"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	d := <-done
	if d.Approved || d.Reason != "мало данных" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestGateWaitContextCancelResets(t *testing.T) {
	host := &fakeHost{running: true}
	g := NewGate(host)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := g.Wait(ctx, "epics", "s", nil)
		done <- err
	}()
	waitGating(t, g)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait не завершился после отмены")
	}
	if gating, _, msg := g.State(); gating || msg != nil {
		t.Fatalf("после отмены state = %v %+v, want false/nil", gating, msg)
	}
}

func TestGateStoppedCancelsImmediately(t *testing.T) {
	host := &fakeHost{stopped: true, running: true}
	g := NewGate(host)
	d, err := g.Wait(context.Background(), "epics", "s", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d.Approved {
		t.Fatalf("decision = %+v, want zero", d)
	}
	if len(host.gateEvents()) != 0 {
		t.Fatal("остановленная сессия не должна публиковать затвор")
	}
	if gating, _, _ := g.State(); gating {
		t.Fatal("затвор не должен вставать у остановленной сессии")
	}
}

func TestGateApproveNoGate(t *testing.T) {
	host := &fakeHost{running: true}
	g := NewGate(host)
	if err := g.Approve("epics", true, ""); err == nil {
		t.Fatal("approve без затвора должен падать")
	}
}

func TestGateApproveNotRunning(t *testing.T) {
	host := &fakeHost{}
	g := NewGate(host)
	g.mu.Lock()
	g.gating, g.typ = true, "epics"
	g.mu.Unlock()
	if err := g.Approve("epics", true, ""); err == nil {
		t.Fatal("approve без оркестрации должен падать")
	}
}

func TestGateApproveMismatch(t *testing.T) {
	host := &fakeHost{running: true}
	g := NewGate(host)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = g.Wait(context.Background(), "epics", "s", nil)
	}()
	waitGating(t, g)
	if err := g.Approve("tasks", true, ""); err == nil {
		t.Fatal("approve не того затвора должен падать")
	}
	// Настоящий затвор всё ещё жив и разрешается корректным типом.
	if err := g.Approve("epics", true, ""); err != nil {
		t.Fatalf("approve epics: %v", err)
	}
	<-done
}

func TestGateDrainsStaleDecision(t *testing.T) {
	host := &fakeHost{running: true}
	g := NewGate(host)
	// Решение, пришедшее ДО затвора, не должно завершить будущий затвор.
	g.decide <- planner.GateDecision{Approved: true, Reason: "старое"}

	done := make(chan planner.GateDecision, 1)
	go func() {
		d, _ := g.Wait(context.Background(), "epics", "s", nil)
		done <- d
	}()
	waitGating(t, g)
	if err := g.Approve("epics", false, "новое"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	select {
	case d := <-done:
		if d.Approved || d.Reason != "новое" {
			t.Fatalf("decision = %+v, want свежее отклонение", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait не завершился")
	}
}

func TestGateReset(t *testing.T) {
	host := &fakeHost{running: true}
	g := NewGate(host)
	g.mu.Lock()
	g.gating, g.typ, g.msg = true, "tasks", &GateEvent{Gate: "tasks"}
	g.mu.Unlock()
	g.Reset()
	if gating, typ, msg := g.State(); gating || typ != "" || msg != nil {
		t.Fatalf("reset state = %v %q %+v, want zero", gating, typ, msg)
	}
}
