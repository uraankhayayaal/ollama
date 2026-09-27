// Package runmetrics — единый реестр метрик агентского цикла.
//
// Ф-1 PLAN-2026-09-19-todo-owerview-for-prom.md. Задача этапа: свести разрозненную
// телеметрию в одно место. До этого шага метрики шагов жили только в чекпоинте
// (checkpoint.Store.TrackMetric — экземпляр на консольную оркестрацию и без
// читателя), токены — в Redis-счётчике tokens, а длительности инструментов не
// измерялись вовсе. Реестр закрывает все три источника: он подписан на поток
// событий runevents (runevents.Sink), поэтому знает и агента, и scope единицы
// работы, и точные моменты начала/конца вызова инструмента.
//
// Три источника, один реестр:
//
//	Record        — события агентского цикла: вызовы инструментов, длительности,
//	                исходы, раунды, строки логов рантайма (runevents.Sink);
//	RecordTokens  — расход токенов раунда (тот же путь, что у сервера);
//	SetStepMetric — именованные метрики шага (бывший checkpoint.TrackMetric).
//
// Реестр живёт в памяти процесса и не обязан переживать перезапуск: назначение
// — оперативная телеметрия текущего запуска (дашборд, /metrics), а долговременные
// факты расхода токенов остаются в Redis-счётчике и в сущностях доски.
package runmetrics

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"ai/runevents"
	"ai/tokens"
)

// maxSeries — потолок числа различных scope в реестре. Имена единиц работы
// (task:<id>, epic:<id>) не ограничены, а каждое становится набором лейблов в
// экспорте, поэтому выше лимита хвост схлопывается в псевдо-scope "other".
// Это защита и от роста памяти, и от «взрыва» кардинальности в Prometheus.
const maxSeries = 256

// scopeOther — псевдо-scope для единиц работы сверх maxSeries.
const scopeOther = "other"

// stepBuckets — границы гистограммы длительности шага, секунды.
var stepBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

// histogram — счётчик значений по фиксированным границам (Prometheus-совместимо).
type histogram struct {
	counts []int64
	sum    float64
	count  int64
}

func newHistogram() histogram { return histogram{counts: make([]int64, len(stepBuckets))} }

// observe кладёт значение в корзины «<= границы». Значения вне диапазона
// учитываются только в sum/count — как в Prometheus.
func (h *histogram) observe(v float64) {
	h.sum += v
	h.count++
	for i, b := range stepBuckets {
		if v <= b {
			h.counts[i]++
		}
	}
}

// toolStat — агрегаты по имени инструмента.
type toolStat struct {
	calls  int64
	errors int64
	all    histogram
	failed histogram
}

// scopeStat — агрегаты по единице работы (task:<id>, epic:<id>, architecture…).
type scopeStat struct {
	calls     int64
	errors    int64
	rounds    int64
	tokensIn  int64
	tokensOut int64
	all       histogram
}

// agentStat — агрегаты по имени агента.
type agentStat struct {
	calls     int64
	errors    int64
	tokensIn  int64
	tokensOut int64
}

// pending — начало вызова инструмента, ожидающее результата.
type pending struct {
	agent string
	scope string
	at    time.Time
}

// Registry — счётчики, гистограммы и гейджи по проекту. Потокобезопасен:
// события приходят из горутин агентского цикла и фоновых инструментов.
type Registry struct {
	mu sync.Mutex

	project   string
	pricing   tokens.Pricing
	startedAt time.Time
	// runID — идентификатор запуска (время создания реестра). Позволяет
	// отличать телеметрию текущего процесса от накопленных в Redis счётчиков.
	runID int64

	rounds      int64
	tokensIn    int64
	tokensOut   int64
	lastTPS     float64
	toolCalls   int64
	toolErrors  int64
	appLogLines int64
	// unpaired — результаты инструментов без парного начала. Причина — потерянный
	// или повторно проигранный старт; такие результаты не должны портить
	// агрегаты, поэтому длительность считается нулевой, а сам факт учитывается
	// отдельно (дедуп-корзина).
	unpaired int64
	// overflow — число единиц работы, вытесненных сверх maxSeries.
	overflow int

	tools      map[string]*toolStat
	scopes     map[string]*scopeStat
	agents     map[string]*agentStat
	stepMetric map[string]float64
	pending    map[string][]pending
}

// New создаёт реестр для проекта с заданными ценами токенов.
func New(project string, pricing tokens.Pricing) *Registry {
	now := time.Now()
	return &Registry{
		project:    project,
		pricing:    pricing,
		startedAt:  now,
		runID:      now.UnixNano(),
		tools:      map[string]*toolStat{},
		scopes:     map[string]*scopeStat{},
		agents:     map[string]*agentStat{},
		stepMetric: map[string]float64{},
		pending:    map[string][]pending{},
	}
}

// Reporter возвращает репортёр runevents, пишущий события прямо в реестр.
// Одна строка подключения там, где доступен только Reporter (консольная
// оркестрация) — тот же путь событий, что и у сервера.
func Reporter(reg *Registry) runevents.Reporter {
	return runevents.NewRouter(reg.Record)
}

// Project возвращает проект реестра.
func (r *Registry) Project() string { return r.project }

// Record подписан на поток событий агентского цикла (runevents.Sink). События,
// не влияющие на метрики (текст модели, дельты стриминга), игнорируются.
func (r *Registry) Record(ev runevents.Event) {
	switch ev.Type {
	case runevents.TypeToolStart:
		r.begin(ev)
	case runevents.TypeToolResult:
		r.end(ev)
	case runevents.TypeTokenCount:
		r.RecordTokens(ev.In, ev.Out, ev.TPS, ev.Scope, ev.Agent)
	case runevents.TypeAppLog:
		r.AddAppLogLines(1)
	}
}

// begin запоминает момент старта вызова инструмента.
//
// Повторный старт того же инструмента, пока предыдущий не завершён, НЕ затирает
// начало: иначе повторная доставка события обнулила бы длительность. Второй
// старт кладётся в очередь и будет забран первым же результатом.
func (r *Registry) begin(ev runevents.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t := r.toolLocked(ev.Tool); t != nil {
		t.calls++
	}
	if s := r.scopeLocked(ev.Scope); s != nil {
		s.calls++
	}
	if a := r.agentLocked(ev.Agent); a != nil {
		a.calls++
	}
	r.toolCalls++
	r.pending[ev.Tool] = append(r.pending[ev.Tool], pending{agent: ev.Agent, scope: ev.Scope, at: ev.Time})
}

// end закрывает вызов инструмента и записывает длительность в гистограммы
// инструмента и scope.
func (r *Registry) end(ev runevents.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, paired := r.takePendingLocked(ev.Tool)
	scope, agent := ev.Scope, ev.Agent
	if paired {
		if scope == "" {
			scope = p.scope
		}
		if agent == "" {
			agent = p.agent
		}
	}
	dur := 0.0
	if paired {
		dur = ev.Time.Sub(p.at).Seconds()
		if dur < 0 {
			dur = 0
		}
	} else {
		// Дедуп-корзина: результат без начала. Вызов не учитываем, длительность
		// нулевая — агрегаты остаются непротиворечивыми.
		r.unpaired++
		if t := r.toolLocked(ev.Tool); t != nil && t.calls > 0 {
			t.calls--
		}
		if s := r.scopeLocked(scope); s != nil && s.calls > 0 {
			s.calls--
		}
		if a := r.agentLocked(agent); a != nil && a.calls > 0 {
			a.calls--
		}
		if r.toolCalls > 0 {
			r.toolCalls--
		}
	}
	if !ev.OK {
		r.toolErrors++
	}
	if t := r.toolLocked(ev.Tool); t != nil {
		t.all.observe(dur)
		if !ev.OK {
			t.errors++
			t.failed.observe(dur)
		}
	}
	if s := r.scopeLocked(scope); s != nil {
		s.all.observe(dur)
		if !ev.OK {
			s.errors++
		}
	}
	if a := r.agentLocked(agent); a != nil && !ev.OK {
		a.errors++
	}
}

// takePendingLocked снимает самое раннее невыполненное начало вызова инструмента.
// Вызывается под mu.
func (r *Registry) takePendingLocked(tool string) (pending, bool) {
	q := r.pending[tool]
	if len(q) == 0 {
		return pending{}, false
	}
	if len(q) == 1 {
		delete(r.pending, tool)
	} else {
		r.pending[tool] = q[1:]
	}
	return q[0], true
}

// toolLocked возвращает агрегаты инструмента, создавая их при первом обращении.
func (r *Registry) toolLocked(name string) *toolStat {
	if name == "" {
		return nil
	}
	t, ok := r.tools[name]
	if !ok {
		t = &toolStat{all: newHistogram(), failed: newHistogram()}
		r.tools[name] = t
	}
	return t
}

// scopeLocked возвращает агрегаты единицы работы. Сверх maxSeries новые имена
// схлопываются в scopeOther, чтобы экспорт оставался конечным.
func (r *Registry) scopeLocked(scope string) *scopeStat {
	if scope == "" {
		return nil
	}
	if s, ok := r.scopes[scope]; ok {
		return s
	}
	// Место под scopeOther резервируется заранее, чтобы и после переполнения
	// число рядов не превышало maxSeries.
	limit := maxSeries
	if _, ok := r.scopes[scopeOther]; !ok {
		limit--
	}
	if len(r.scopes) >= limit {
		r.overflow++
		scope = scopeOther
		if s, ok := r.scopes[scope]; ok {
			return s
		}
	}
	s := &scopeStat{all: newHistogram()}
	r.scopes[scope] = s
	return s
}

// agentLocked возвращает агрегаты агента, создавая их при первом обращении.
func (r *Registry) agentLocked(name string) *agentStat {
	if name == "" {
		return nil
	}
	if a, ok := r.agents[name]; ok {
		return a
	}
	a := &agentStat{}
	r.agents[name] = a
	return a
}

// RecordTokens учитывает расход токенов раунда: глобальные итоги, итоги scope и
// агента, счётчик раундов и последнюю реальную скорость генерации.
// scope и agent необязательны (пустая строка — без атрибуции).
func (r *Registry) RecordTokens(in, out int64, tps float64, scope, agent string) {
	if in < 0 {
		in = 0
	}
	if out < 0 {
		out = 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokensIn += in
	r.tokensOut += out
	r.rounds++
	if tps > 0 {
		r.lastTPS = tps
	}
	if s := r.scopeLocked(scope); s != nil {
		s.tokensIn += in
		s.tokensOut += out
		s.rounds++
	}
	if a := r.agentLocked(agent); a != nil {
		a.tokensIn += in
		a.tokensOut += out
	}
}

// AddAppLogLines учитывает строки логов рантайма приложения (Ф-2), попавшие в
// поток событий агентского цикла.
func (r *Registry) AddAppLogLines(n int64) {
	if n <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.appLogLines += n
}

// SetStepMetric записывает именованную метрику шага (перенос
// checkpoint.TrackMetric в единый реестр). Ключ составной — scope/step/key — и
// перезаписывается целиком, повторная запись того же шага НЕ накапливается.
func (r *Registry) SetStepMetric(scope, step, key string, value float64) {
	if step == "" || key == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stepMetric[metricKey(scope, step, key)] = value
}

// StepMetric возвращает записанное значение метрики шага.
func (r *Registry) StepMetric(scope, step, key string) (float64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.stepMetric[metricKey(scope, step, key)]
	return v, ok
}

// Reset обнуляет накопленные значения (сброс метрик в Web UI).
func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startedAt = time.Now()
	r.runID = r.startedAt.UnixNano()
	r.rounds, r.tokensIn, r.tokensOut = 0, 0, 0
	r.toolCalls, r.toolErrors, r.appLogLines, r.unpaired = 0, 0, 0, 0
	r.overflow = 0
	r.tools = map[string]*toolStat{}
	r.scopes = map[string]*scopeStat{}
	r.agents = map[string]*agentStat{}
	r.stepMetric = map[string]float64{}
	r.pending = map[string][]pending{}
}

func metricKey(scope, step, key string) string {
	return scope + "\x00" + step + "\x00" + key
}

// --- снимок ---

// ToolMetrics — агрегаты по инструменту. Count/SumSec/Buckets описывают
// гистограмму длительности (Buckets[i] — число вызовов не длиннее
// StepBuckets()[i] секунд), Error* — то же по неуспешным вызовам.
type ToolMetrics struct {
	Tool         string  `json:"tool"`
	Calls        int64   `json:"calls"`
	Errors       int64   `json:"errors"`
	Count        int64   `json:"count"`
	SumSec       float64 `json:"sum_sec"`
	MeanSec      float64 `json:"mean_sec"`
	MaxSec       float64 `json:"max_sec"`
	ErrorCount   int64   `json:"error_count"`
	ErrorSumSec  float64 `json:"error_sum_sec"`
	Buckets      []int64 `json:"buckets"`
	ErrorBuckets []int64 `json:"error_buckets"`
}

// ScopeMetrics — агрегаты по единице работы.
type ScopeMetrics struct {
	Scope     string  `json:"scope"`
	Calls     int64   `json:"calls"`
	Errors    int64   `json:"errors"`
	Rounds    int64   `json:"rounds"`
	TokensIn  int64   `json:"tokens_in"`
	TokensOut int64   `json:"tokens_out"`
	TotalSec  float64 `json:"total_sec"`
	MeanSec   float64 `json:"mean_sec"`
}

// AgentMetrics — агрегаты по агенту.
type AgentMetrics struct {
	Agent     string `json:"agent"`
	Calls     int64  `json:"calls"`
	Errors    int64  `json:"errors"`
	TokensIn  int64  `json:"tokens_in"`
	TokensOut int64  `json:"tokens_out"`
}

// StepMetric — именованная метрика шага.
type StepMetric struct {
	Scope string  `json:"scope"`
	Step  string  `json:"step"`
	Key   string  `json:"key"`
	Value float64 `json:"value"`
}

// Snapshot — снимок реестра: всё, что нужно дашборду и /metrics.
type Snapshot struct {
	Project     string    `json:"project"`
	RunID       int64     `json:"run_id"`
	Since       time.Time `json:"since"`
	UptimeSec   float64   `json:"uptime_sec"`
	Rounds      int64     `json:"rounds"`
	TokensIn    int64     `json:"tokens_in"`
	TokensOut   int64     `json:"tokens_out"`
	TokensTotal int64     `json:"tokens_total"`
	// PriceIn/PriceOut — цены за 1M токенов, по которым посчитана Cost.
	PriceIn  float64 `json:"price_in"`
	PriceOut float64 `json:"price_out"`
	Cost     float64 `json:"cost"`
	// CostKnown — заданы ли цены. Без них Cost всегда 0, а интерфейс
	// показывает токены без денежной оценки, а не выдуманный ноль.
	CostKnown bool `json:"cost_known"`
	// CostIn/CostOut — составляющие стоимости по ценам реестра.
	CostIn  float64 `json:"cost_in"`
	CostOut float64 `json:"cost_out"`
	// StepCount/StepSumSec — общие наблюдения и сумма длительностей шагов
	// (вызовов инструментов) по всем единицам работы.
	StepCount   int64          `json:"step_count"`
	StepSumSec  float64        `json:"step_sum_sec"`
	LastTPS     float64        `json:"tps"`
	ToolCalls   int64          `json:"tool_calls"`
	ToolErrors  int64          `json:"tool_errors"`
	AppLogLines int64          `json:"app_log_lines"`
	Unpaired    int64          `json:"unpaired"`
	Overflow    int            `json:"overflow"`
	Tools       []ToolMetrics  `json:"tools"`
	Scopes      []ScopeMetrics `json:"scopes"`
	Agents      []AgentMetrics `json:"agents"`
	Steps       []StepMetric   `json:"steps"`
}

// Snapshot возвращает согласованный снимок реестра. Ряды отсортированы и
// уникальны по набору лейблов — требование формата Prometheus и одновременно
// дедуп: повторный экспорт одного и того же снимка даёт тот же текст.
func (r *Registry) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Snapshot{
		Project:     r.project,
		RunID:       r.runID,
		Since:       r.startedAt,
		UptimeSec:   time.Since(r.startedAt).Seconds(),
		Rounds:      r.rounds,
		TokensIn:    r.tokensIn,
		TokensOut:   r.tokensOut,
		TokensTotal: r.tokensIn + r.tokensOut,
		PriceIn:     r.pricing.InPerMillion,
		PriceOut:    r.pricing.OutPerMillion,
		CostKnown:   r.pricing.Known(),
		LastTPS:     r.lastTPS,
		ToolCalls:   r.toolCalls,
		ToolErrors:  r.toolErrors,
		AppLogLines: r.appLogLines,
		Unpaired:    r.unpaired,
		Overflow:    r.overflow,
	}
	if s.CostKnown {
		s.CostIn = round3(r.pricing.CostIn(s.TokensIn))
		s.CostOut = round3(r.pricing.CostOut(s.TokensOut))
		s.Cost = round3(s.CostIn + s.CostOut)
	}
	for name, t := range r.tools {
		mean := 0.0
		if t.all.count > 0 {
			mean = t.all.sum / float64(t.all.count)
		}
		s.StepCount += t.all.count
		s.StepSumSec += t.all.sum
		s.Tools = append(s.Tools, ToolMetrics{
			Tool: name, Calls: t.calls, Errors: t.errors,
			Count: t.all.count, SumSec: round3(t.all.sum),
			MeanSec: round3(mean), MaxSec: round3(t.approxMax()),
			ErrorCount: t.failed.count, ErrorSumSec: round3(t.failed.sum),
			Buckets: append([]int64(nil), t.all.counts...), ErrorBuckets: append([]int64(nil), t.failed.counts...),
		})
	}
	sort.Slice(s.Tools, func(i, j int) bool { return s.Tools[i].Tool < s.Tools[j].Tool })
	for name, sc := range r.scopes {
		mean := 0.0
		if sc.all.count > 0 {
			mean = sc.all.sum / float64(sc.all.count)
		}
		s.Scopes = append(s.Scopes, ScopeMetrics{
			Scope: name, Calls: sc.calls, Errors: sc.errors, Rounds: sc.rounds,
			TokensIn: sc.tokensIn, TokensOut: sc.tokensOut,
			TotalSec: round3(sc.all.sum), MeanSec: round3(mean),
		})
	}
	sort.Slice(s.Scopes, func(i, j int) bool { return s.Scopes[i].Scope < s.Scopes[j].Scope })
	for name, a := range r.agents {
		s.Agents = append(s.Agents, AgentMetrics{
			Agent: name, Calls: a.calls, Errors: a.errors,
			TokensIn: a.tokensIn, TokensOut: a.tokensOut,
		})
	}
	sort.Slice(s.Agents, func(i, j int) bool { return s.Agents[i].Agent < s.Agents[j].Agent })
	for k, v := range r.stepMetric {
		parts := strings.SplitN(k, "\x00", 3)
		if len(parts) != 3 {
			continue
		}
		s.Steps = append(s.Steps, StepMetric{Scope: parts[0], Step: parts[1], Key: parts[2], Value: v})
	}
	sort.Slice(s.Steps, func(i, j int) bool {
		a, b := s.Steps[i], s.Steps[j]
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		if a.Step != b.Step {
			return a.Step < b.Step
		}
		return a.Key < b.Key
	})
	return s
}

// approxMax — верхняя граница максимальной наблюдённой длительности.
// Гистограмма хранит только корзины, поэтому точное «максимум за шаг» не
// восстановить: отдаётся самая узкая граница, вмещающая ВСЕ наблюдения
// (первая корзина, чей счётчик равен общему). Для дашборда этого достаточно, а
// память остаётся константной независимо от числа вызовов.
func (t *toolStat) approxMax() float64 {
	if t.all.count == 0 {
		return 0
	}
	for i, c := range t.all.counts {
		if c == t.all.count {
			return stepBuckets[i]
		}
	}
	return stepBuckets[len(stepBuckets)-1]
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// StepBuckets — границы (в секундах) корзин гистограммы длительности шага.
// Отдаё наружу, чтобы дашборд и тесты рисовали ту же шкалу, что экспорт.
func StepBuckets() []float64 { return append([]float64(nil), stepBuckets...) }
