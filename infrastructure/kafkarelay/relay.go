package kafkarelay

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/segmentio/kafka-go"

	"ai/infrastructure/runevents"
	"ai/shared/logging"
	"ai/shared/tokens"
)

// runCacheTTL — окно кэша scope → run_id: задача редко меняет эпик, а частый
// резолв держал бы мьютекс сессии на Redis-чтении.
const runCacheTTL = 5 * time.Minute

// publishFunc отправляет валидный шаг в топик (в тестах — фейк).
type publishFunc func(ctx context.Context, step AgentStep) error

// Relay — общий продюсер шагов для всех сессий сервера: один writer Kafka,
// один потребитель ограниченной очереди, общий счётчик номеров шагов.
// Потокобезопасен; nil-рельс допустим (интеграция выключена) — методы
// nil-safe.
type Relay struct {
	cfg     Config
	writer  *kafka.Writer // nil в тестах
	pricing tokens.Pricing
	counter *stepCounter
	publish publishFunc

	queue chan queuedStep
	done  chan struct{}
	wg    sync.WaitGroup

	closed    atomic.Bool
	written   atomic.Int64
	dropped   atomic.Int64
	writeErrs atomic.Int64

	sinksMu sync.Mutex
	sinks   []*sessionSink
}

// New создаёт рлей и запускает фоновые горутины (потребитель очереди и
// дебаунс message-only шагов). Ошибка — интеграция не поднимается, сервер
// работает без Kafka.
func New(cfg Config) (*Relay, error) {
	if len(cfg.Brokers) == 0 {
		return nil, fmt.Errorf("kafkarelay: не заданы брокеры (KAFKA_BROKERS)")
	}
	normalize(&cfg)
	w := &kafka.Writer{
		Addr:         kafka.TCP(cfg.Brokers...),
		Topic:        cfg.Topic,
		Balancer:     &kafka.Hash{}, // ключ run_id → одна партиция на ран
		RequiredAcks: requiredAcks(cfg.Acks),
		Compression:  compressionCodec(cfg.Compression),
		BatchSize:    cfg.BatchSize,
		BatchTimeout: cfg.Linger,
		WriteTimeout: cfg.WriteTimeout,
		MaxAttempts:  3,
	}
	relay := newRelay(cfg, func(ctx context.Context, step AgentStep) error {
		payload, err := marshalStep(step)
		if err != nil {
			return err
		}
		return w.WriteMessages(ctx, kafka.Message{
			Key:   []byte(step.RunID),
			Value: payload,
		})
	})
	relay.writer = w
	return relay, nil
}

func newRelay(cfg Config, publish publishFunc) *Relay {
	normalize(&cfg)
	relay := &Relay{
		cfg:     cfg,
		pricing: tokens.PricingFromEnv(),
		counter: newStepCounter(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB),
		publish: publish,
		queue:   make(chan queuedStep, cfg.QueueSize),
		done:    make(chan struct{}),
	}
	relay.wg.Add(2)
	go relay.writeLoop()
	go relay.flushLoop()
	return relay
}

func normalize(cfg *Config) {
	if cfg.Topic == "" {
		cfg.Topic = defaultTopic
	}
	if cfg.Acks == "" {
		cfg.Acks = defaultAcks
	}
	if cfg.Compression == "" {
		cfg.Compression = defaultComp
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultBatchSize
	}
	if cfg.Linger <= 0 {
		cfg.Linger = defaultLinger
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = defaultWriteTimeout
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = defaultFlushInterval
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = defaultQueueSize
	}
	if cfg.HarnessName == "" {
		cfg.HarnessName = "ollama-harness"
	}
	if cfg.AgentVersion == "" {
		cfg.AgentVersion = "dev"
	}
}

// SessionSink возвращает sink событий агентного цикла для одной сессии.
// mode определяет семантику run_id: оркестрация (привязка к эпику через
// epicOf) или ассистент (стабильный run_id проекта). Nil-рельс → nil sink.
func (r *Relay) SessionSink(project string, mode Mode, epicOf TaskEpicFunc) runevents.Sink {
	if r == nil {
		return nil
	}
	s := &sessionSink{
		relay:    r,
		log:      logging.For(project),
		project:  project,
		mode:     mode,
		epicOf:   epicOf,
		states:   make(map[string]*runState),
		runCache: make(map[string]cacheEntry),
	}
	r.sinksMu.Lock()
	r.sinks = append(r.sinks, s)
	r.sinksMu.Unlock()
	return s.handle
}

// Close останавливает рлей: дренаж очереди, закрытие writer'а, итоги в лог.
// Nil-safe, повторный вызов — no-op.
func (r *Relay) Close() {
	if r == nil || !r.closed.CompareAndSwap(false, true) {
		return
	}
	close(r.done)
	r.wg.Wait()
	r.sinksMu.Lock()
	r.sinks = nil
	r.sinksMu.Unlock()
	if r.writer != nil {
		if err := r.writer.Close(); err != nil {
			logging.Warnf("kafkarelay: закрытие writer: %v", err)
		}
	}
	_ = r.counter.close()
	if w, d, e := r.written.Load(), r.dropped.Load(), r.writeErrs.Load(); w+d+e > 0 {
		logging.Infof("kafkarelay: остановлено: отправлено шагов %d, потеряно %d, ошибок записи %d", w, d, e)
	}
}

// enqueue кладёт шаг в очередь; не блокируется: при переполнении шаг
// отбрасывается (политика drop) с троттлингом в лог.
func (r *Relay) enqueue(step queuedStep, log *logging.Logger) {
	if r.closed.Load() {
		return
	}
	select {
	case r.queue <- step:
	default:
		if n := r.dropped.Add(1); n == 1 || n%100 == 0 {
			log.Warnf("kafkarelay: очередь переполнена — шаг рана %s потерян (всего потерь: %d)", step.runID, n)
		}
	}
}

// writeLoop — единственный потребитель очереди: номер шага, валидация
// контракта, запись в брокер. По done — дренаж остатка с общим дедлайном.
func (r *Relay) writeLoop() {
	defer r.wg.Done()
	for {
		select {
		case step := <-r.queue:
			r.writeOne(context.Background(), step)
		case <-r.done:
			r.drain()
			return
		}
	}
}

// drain досылает остаток очереди после Close с общим дедлайном.
func (r *Relay) drain() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		select {
		case step := <-r.queue:
			r.writeOne(ctx, step)
		case <-ctx.Done():
			return
		default:
			return
		}
	}
}

// flushLoop периодически закрывает застрявшие message-only раунды (финальный
// ответ модели, после которого запуск закончился и OnTokens уже не придёт).
func (r *Relay) flushLoop() {
	defer r.wg.Done()
	ticker := time.NewTicker(r.cfg.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			r.sinksMu.Lock()
			sinks := append([]*sessionSink(nil), r.sinks...)
			r.sinksMu.Unlock()
			for _, s := range sinks {
				s.flushStale(now)
			}
		case <-r.done:
			return
		}
	}
}

// writeOne собирает AgentStep, присваивает номер и публикует. Ошибки записи
// не фатальны: счётчик + троттлинг warn, поток продолжается (drop-политика).
func (r *Relay) writeOne(parent context.Context, st queuedStep) {
	ctx, cancel := context.WithTimeout(parent, r.cfg.WriteTimeout)
	defer cancel()
	step := AgentStep{
		RunID:        st.runID,
		HarnessName:  r.cfg.HarnessName,
		AgentVersion: r.cfg.AgentVersion,
		StepNumber:   int(r.counter.next(ctx, st.runID)),
		Thought:      st.thought,
		ToolCalled:   st.tool,
		Status:       st.status,
		LatencyMs:    int(st.latencyMs),
		TokensUsed:   int(st.tokensIn + st.tokensOut),
		CostUSD:      round6(r.pricing.Cost(st.tokensIn, st.tokensOut)),
	}
	if err := step.Validate(); err != nil {
		r.noteWriteErr(fmt.Errorf("шаг рана %s (номер %d): %w", st.runID, step.StepNumber, err))
		return
	}
	if err := r.publish(ctx, step); err != nil {
		r.noteWriteErr(err)
		return
	}
	r.written.Add(1)
}

func (r *Relay) noteWriteErr(err error) {
	if n := r.writeErrs.Add(1); n == 1 || n%100 == 0 {
		logging.Warnf("kafkarelay: не удалось отправить шаг в Kafka (поток не блокируется): %v", err)
	}
}

// sessionSink — sink одной сессии: фильтрует события, ведёт конечные автоматы
// раундов (ключ — scope оркестрации либо единый поток ассистента), резолвит
// run_id через кэш и передаёт закрытые шаги в рлей.
type sessionSink struct {
	relay   *Relay
	log     *logging.Logger
	project string
	mode    Mode
	epicOf  TaskEpicFunc

	mu       sync.Mutex
	states   map[string]*runState
	runCache map[string]cacheEntry
}

type cacheEntry struct {
	val string
	at  time.Time
}

func (s *sessionSink) handle(ev runevents.Event) {
	switch ev.Type {
	case runevents.TypeMessageDelta, runevents.TypeAppLog:
		return
	case runevents.TypeMessage:
		if ev.Role == "user" {
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stateForLocked(ev)
	if step, ok := st.handle(ev); ok {
		s.relay.enqueue(step, s.log)
	}
}

func (s *sessionSink) flushStale(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.states {
		if step, ok := st.flushStale(now, s.relay.cfg.FlushInterval); ok {
			s.relay.enqueue(step, s.log)
		}
	}
}

func (s *sessionSink) stateForLocked(ev runevents.Event) *runState {
	key := "orch:" + ev.Scope
	if s.mode == ModeAssistant {
		key = "assistant"
	}
	if st, ok := s.states[key]; ok {
		return st
	}
	st := newRunState(s.runIDForScopeLocked(ev.Scope))
	s.states[key] = st
	return st
}

func (s *sessionSink) runIDForScopeLocked(scope string) string {
	if s.mode == ModeAssistant {
		return assistantRunID(s.project)
	}
	if e, ok := s.runCache[scope]; ok && time.Since(e.at) < runCacheTTL {
		return e.val
	}
	id := scopeRunID(context.Background(), s.project, scope, s.epicOf)
	s.runCache[scope] = cacheEntry{val: id, at: time.Now()}
	return id
}

func requiredAcks(v string) kafka.RequiredAcks {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "none":
		return kafka.RequireNone
	case "one":
		return kafka.RequireOne
	default:
		return kafka.RequireAll
	}
}

func compressionCodec(v string) kafka.Compression {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "gzip":
		return kafka.Gzip
	case "snappy":
		return kafka.Snappy
	case "lz4":
		return kafka.Lz4
	case "zstd":
		return kafka.Zstd
	default:
		return 0
	}
}

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }
