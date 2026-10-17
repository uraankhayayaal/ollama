package kafkarelay

import (
	"strings"
	"time"

	"ai/infrastructure/runevents"
)

// queuedStep — собранный шаг до присвоения номера и публикации.
type queuedStep struct {
	runID     string
	thought   string
	tool      string
	status    StepStatus
	latencyMs int64
	tokensIn  int64
	tokensOut int64
	at        time.Time // момент события (диагностика в логах)
}

// runState — конечный автомат раундов одного потока событий (один scope
// оркестрации либо весь поток ассистента). Потокобезопасность — на стороне
// sessionSink (один мьютекс на sink).
//
// Раунд модели устроен так (см. application/usecases/run.go): токены раунда
// (OnTokens), затем финальный текст (OnMessage), затем — возможно — вызовы
// инструментов (OnToolStart/OnToolResult, в том числе параллельно). Шаг
// публикации: при первом закрытии инструмента раунда (пара start→result) либо
// по дебаунсу/границе раунда, если инструментов не было (message-only шаг —
// финальный ответ модели). Токены и текст раунда достаются только первому
// шагу: параллельные вызовы того же раунда идут с нулевыми токенами.
type runState struct {
	runID string

	started   time.Time
	thoughts  []string
	tokensIn  int64
	tokensOut int64
	published bool
	// pending — открытые старты инструментов по имени (FIFO, как в
	// runmetrics): параллельные вызовы одного имени разносятся по порядку.
	pending map[string][]time.Time
}

func newRunState(runID string) *runState {
	return &runState{runID: runID, pending: make(map[string][]time.Time)}
}

// handle обрабатывает событие и, если раунд закрылся, возвращает шаг.
func (st *runState) handle(ev runevents.Event) (queuedStep, bool) {
	switch ev.Type {
	case runevents.TypeTokenCount:
		// Граница раунда: незакрытая реплика без тулов уходит message-only
		// шагом, затем состояние переходит к новому раунду и его токенам.
		step, flushed := st.messageStep(ev.Time)
		st.beginRound(ev.Time)
		st.tokensIn += ev.In
		st.tokensOut += ev.Out
		return step, flushed
	case runevents.TypeMessage:
		if ev.Role == "user" {
			return queuedStep{}, false
		}
		// Прежняя реплика без инструментов закрывается на границе нового
		// текста (usage предыдущего раунда мог не прийти — OnTokens не звался).
		// Закрытие возможно только когда раунд не получил тулов.
		if len(st.thoughts) > 0 && len(st.pending) == 0 {
			step, flushed := st.messageStep(ev.Time)
			st.beginRound(ev.Time)
			st.appendThought(ev.Content)
			return step, flushed
		}
		if st.started.IsZero() {
			st.started = ev.Time
		}
		st.appendThought(ev.Content)
		return queuedStep{}, false
	case runevents.TypeToolStart:
		if st.started.IsZero() {
			st.started = ev.Time
		}
		st.pending[ev.Tool] = append(st.pending[ev.Tool], ev.Time)
		return queuedStep{}, false
	case runevents.TypeToolResult:
		if st.started.IsZero() {
			st.started = ev.Time
		}
		lat := st.takeLatency(ev)
		sts := StepStatusSuccess
		if !ev.OK {
			sts = StepStatusError
		}
		step := st.build(ev.Time, ev.Tool, sts, lat)
		st.published = true
		return step, true
	default:
		return queuedStep{}, false
	}
}

// flushStale публикует message-only шаг, если раунд с текстом не получил ни
// инструмента, ни продолжения (финальный ответ модели: OnTokens следующего
// раунда уже не придёт).
func (st *runState) flushStale(now time.Time, minAge time.Duration) (queuedStep, bool) {
	if st.started.IsZero() || st.published || len(st.thoughts) == 0 || len(st.pending) > 0 {
		return queuedStep{}, false
	}
	if now.Sub(st.started) < minAge {
		return queuedStep{}, false
	}
	lat := now.Sub(st.started).Milliseconds()
	if lat < 0 {
		lat = 0
	}
	step := st.build(now, "", StepStatusSuccess, lat)
	st.published = true
	return step, true
}

// beginRound сбрасывает состояние на границе раунда.
func (st *runState) beginRound(t time.Time) {
	st.started = t
	st.thoughts = nil
	st.tokensIn, st.tokensOut = 0, 0
	st.published = false
	clear(st.pending)
}

// messageStep закрывает незакрытую реплику без инструментов (message-only
// шаг). Состояние не трогает — границу раунда делает вызывающий (beginRound).
func (st *runState) messageStep(now time.Time) (queuedStep, bool) {
	if st.published || len(st.thoughts) == 0 || len(st.pending) > 0 {
		return queuedStep{}, false
	}
	step := st.build(now, "", StepStatusSuccess, latencySince(st.started, now))
	st.published = true
	return step, true
}

// appendThought добавляет текст модели в текущий раунд (пустые реплики не
// копятся: финальный ответ может быть пустым, шага из него не получится).
func (st *runState) appendThought(content string) {
	if c := strings.TrimSpace(content); c != "" {
		st.thoughts = append(st.thoughts, content)
	}
}

// takeLatency снимает старте для закрытого вызова: по имени инструмента
// (FIFO), иначе — от начала раунда (результат без старта, как unpaired в
// runmetrics).
func (st *runState) takeLatency(ev runevents.Event) int64 {
	base := st.started
	if q := st.pending[ev.Tool]; len(q) > 0 {
		base = q[0]
		if len(q) == 1 {
			delete(st.pending, ev.Tool)
		} else {
			st.pending[ev.Tool] = q[1:]
		}
	}
	if base.IsZero() {
		return 0
	}
	lat := ev.Time.Sub(base).Milliseconds()
	if lat < 0 {
		lat = 0
	}
	return lat
}

// build собирает шаг. Токены и текст раунда достаются только первому шагу
// (published == false): параллельные вызовы того же раунда идут с нулём.
func (st *runState) build(at time.Time, tool string, status StepStatus, latencyMs int64) queuedStep {
	step := queuedStep{
		runID:     st.runID,
		thought:   strings.Join(st.thoughts, "\n\n"),
		tool:      tool,
		status:    status,
		latencyMs: latencyMs,
		at:        at.UTC(),
	}
	if !st.published {
		step.tokensIn, step.tokensOut = st.tokensIn, st.tokensOut
	}
	return step
}

func latencySince(start, now time.Time) int64 {
	if start.IsZero() {
		return 0
	}
	d := now.Sub(start).Milliseconds()
	if d < 0 {
		return 0
	}
	return d
}
