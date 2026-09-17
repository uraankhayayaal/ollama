// Package runevents — события агентного цикла в реальном времени для Web UI.
// runner.Generate по контексту получает репортёр (WithReporter) и информирует
// его: текст модели, начало инструмента, результат инструмента. Сервер
// транслирует события в сокет сессии.
//
// Имя агента в событие проставляет не runner (в интерфейсе agents.Agent нет
// метода имени), а обёртка WithAgent, которую оркестратор применяет при
// запуске конкретной фазы и кладёт результат в контекст вызова.
package runevents

import (
	"context"
	"sync"
	"time"
)

// EventType — тип события.
type EventType string

const (
	// TypeMessage — текст модели (роль assistant).
	TypeMessage EventType = "message"
	// TypeMessageDelta — фрагмент потокового ответа модели (стриминг, Ф-3).
	// Сервер транслирует его в WS как type=chat_delta; итоговое сообщение
	// приходит обычным TypeMessage, поэтому дельты в историю чата не пишутся.
	TypeMessageDelta EventType = "message_delta"
	// TypeToolStart — начало выполнения инструмента.
	TypeToolStart EventType = "tool_start"
	// TypeToolResult — результат выполнения инструмента.
	TypeToolResult EventType = "tool_result"
)

// Event — событие агентного цикла для трансляции в Web UI.
type Event struct {
	Type      EventType `json:"type"`
	Agent     string    `json:"agent,omitempty"`      // имя агента (через WithAgent)
	Role      string    `json:"role,omitempty"`       // assistant
	Content   string    `json:"content,omitempty"`    // текст ответа модели / потоковый фрагмент
	StreamID  string    `json:"stream_id,omitempty"`  // идентификатор потока (для message_delta)
	Tool      string    `json:"tool,omitempty"`       // имя инструмента
	Arguments string    `json:"arguments,omitempty"`  // аргументы вызова (обрезаны)
	Result    string    `json:"result,omitempty"`     // результат (обрезан)
	OK        bool      `json:"ok"`                   // успешен ли результат инструмента
	Truncated bool      `json:"truncated,omitempty"`  // текст/результат обрезаны по лимиту
	Time      time.Time `json:"time"`                 // момент события (UTC)
}

// Reporter — назначение событий от runner.Generate. Небезопасен для вызовов
// из нескольких горутин — runner вызывает последовательно.
type Reporter interface {
	// OnMessage — полный текст ответа модели (финальный).
	OnMessage(role, content string, truncated bool)
	// OnMessageDelta — потоковый фрагмент ответа модели (стриминг).
	// streamID помечает поток, чтобы фронтенд связывал фрагменты с одним
	// «плавающим» сообщением до прихода финального OnMessage.
	OnMessageDelta(streamID, content string)
	OnToolStart(tool, args string)
	OnToolResult(tool, result string, ok bool)
}

// Sink — получатель событий. Может вызываться из нескольких горутин.
type Sink func(Event)

// Router — реализация Reporter: рассеивает события в sink. Создаётся
// сервером на каждую сессию; WithAgent возвращает «клон» с именем агента,
// который оркестратор кладёт в контекст перед запуском конкретной фазы.
type Router struct {
	mu    sync.Mutex
	agent string
	sink  Sink
}

// NewRouter создаёт Router. nil-sink допустим — события просто отбрасываются.
func NewRouter(sink Sink) *Router {
	return &Router{sink: sink}
}

// WithAgent возвращает клон с установленным именем агента (общий sink).
func (r *Router) WithAgent(name string) *Router {
	r.mu.Lock()
	defer r.mu.Unlock()
	return &Router{agent: name, sink: r.sink}
}

// OnMessage сообщает текст модели в диалоге.
func (r *Router) OnMessage(role, content string, truncated bool) {
	if role == "" {
		role = "assistant"
	}
	r.emit(Event{
		Type:      TypeMessage,
		Role:      role,
		Content:   content,
		Truncated: truncated,
	})
}

// OnMessageDelta сообщает потоковый фрагмент ответа модели (стриминг).
func (r *Router) OnMessageDelta(streamID, content string) {
	r.emit(Event{Type: TypeMessageDelta, StreamID: streamID, Content: content})
}

// OnToolStart сообщает начало вызова инструмента.
func (r *Router) OnToolStart(tool, args string) {
	r.emit(Event{Type: TypeToolStart, Tool: tool, Arguments: args})
}

// OnToolResult сообщает результат инструмента.
func (r *Router) OnToolResult(tool, result string, ok bool) {
	r.emit(Event{Type: TypeToolResult, Tool: tool, Result: result, OK: ok})
}

func (r *Router) emit(ev Event) {
	r.mu.Lock()
	agent, sink := r.agent, r.sink
	r.mu.Unlock()
	ev.Agent = agent
	ev.Time = time.Now().UTC().Truncate(time.Millisecond)
	if sink != nil {
		sink(ev)
	}
}

// reporterKey — тип ключа контекста для передачи Router.
type reporterKey struct{}

// WithReporter помещает Router в контекст (по образцу runner.WithResumeState).
func WithReporter(ctx context.Context, r *Router) context.Context {
	return context.WithValue(ctx, reporterKey{}, r)
}

// ReporterFromContext извлекает Router из контекста. nil, если не установлен.
func ReporterFromContext(ctx context.Context) *Router {
	if r, ok := ctx.Value(reporterKey{}).(*Router); ok {
		return r
	}
	return nil
}