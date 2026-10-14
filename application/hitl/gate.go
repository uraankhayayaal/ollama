// Package hitl содержит HITL-затвор (human-in-the-loop): блокирующее
// подтверждение человеком публикации эпиков/задач. Вынесено из HTTP-слоя
// (`server/session.go`, Этап 6): политика «дождаться решения человека, только
// потом продолжать оркестрацию» — правило, а не серверная обвязка. Серверные
// эффекты (публикация в шину, статус сессии) инвертированы портом GateHost.
package hitl

import (
	"context"
	"fmt"
	"sync"

	"ai/application/planner"
)

// GateEvent — событие «нужно подтверждение человека» (шина → Web UI, баннер).
type GateEvent struct {
	Gate    string   `json:"gate"` // epics | tasks
	Summary string   `json:"summary"`
	IDs     []string `json:"ids"`
}

// GateHost — сессионные эффекты, необходимые затвору.
type GateHost interface {
	// Stopped сообщает, остановлена ли сессия.
	Stopped() bool
	// Running сообщает, идёт ли сейчас оркестрация.
	Running() bool
	// BroadcastStatus публикует статус сессии в шину (type=status).
	BroadcastStatus(status, detail string)
	// PublishGate публикует событие затвора в шину (type=gate).
	PublishGate(ev GateEvent)
}

// Gate — потокобезопасный HITL-затвор. Хранит состояние активного затвора
// (тип + payload для переигрывания баннера при переподключении клиента) и канал
// решения (буфер 1: решение, присланное до затвора, не теряется).
type Gate struct {
	host   GateHost
	mu     sync.Mutex
	decide chan planner.GateDecision
	gating bool
	typ    string
	msg    *GateEvent
}

// NewGate создаёт затвор с указанным хостом эффектов.
func NewGate(host GateHost) *Gate {
	return &Gate{host: host, decide: make(chan planner.GateDecision, 1)}
}

// State возвращает текущее состояние затвора: идёт ли ожидание, тип и payload
// активного затвора (nil — затвора нет). Одно снятие состояния для консистентной
// выдачи трёх полей (snapshot/status).
func (g *Gate) State() (gating bool, typ string, msg *GateEvent) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gating, g.typ, g.msg
}

// Reset сбрасывает состояние затвора (завершение/останов оркестрации).
func (g *Gate) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gating, g.typ, g.msg = false, "", nil
}

// Wait блокирует runner до решения (Approve) или отмены контекста. Публикует
// статус «ждёт подтверждения» и событие затвора (payload запоминается для
// переигрывания баннера новому клиенту).
func (g *Gate) Wait(ctx context.Context, typ, summary string, ids []string) (planner.GateDecision, error) {
	if g.host.Stopped() {
		return planner.GateDecision{}, context.Canceled
	}
	ev := GateEvent{Gate: typ, Summary: summary, IDs: ids}
	g.mu.Lock()
	g.gating = true
	g.typ = typ
	g.msg = &ev
	g.mu.Unlock()

	// Снимаем возможное решение, присланное ДО затвора (буфер 1).
	select {
	case <-g.decide:
	default:
	}
	g.host.PublishGate(ev)
	g.host.BroadcastStatus("waiting", typ)

	defer g.Reset()

	select {
	case d := <-g.decide:
		// Approve уже сбросил gating/typ/msg и положил решение в канал.
		// Возвращаем статус «running»: затвор разрешён, оркестрация идёт.
		g.host.BroadcastStatus("running", "")
		return d, nil
	case <-ctx.Done():
		return planner.GateDecision{}, ctx.Err()
	}
}

// Approve разрешает текущий затвор: approved + reason → решение в канал.
func (g *Gate) Approve(typ string, approved bool, reason string) error {
	if !g.host.Running() {
		return fmt.Errorf("нет активного затвора (%s)", typ)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.gating {
		return fmt.Errorf("нет активного затвора (%s)", typ)
	}
	if g.typ != typ {
		return fmt.Errorf("ожидается затвор «%s», пришло «%s»", g.typ, typ)
	}
	g.decide <- planner.GateDecision{Approved: approved, Reason: reason}
	g.gating, g.typ, g.msg = false, "", nil
	return nil
}
