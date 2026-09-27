// REST и WebSocket-доставка метрик агентского цикла (Ф-1
// PLAN-2026-09-19-todo-owerview-for-prom.md).
//
// Метрики живут в реестре runmetrics, который подписан на поток событий сессии
// (Session.routeRunEvent → Registry.Record). Отсюда три представления:
//
//	GET  /api/projects/{id}/metrics       — снимок JSON для дашборда;
//	GET  /api/metrics                     — все проекты в формате Prometheus;
//	POST /api/projects/{id}/metrics/reset — обнулить телеметрию запуска.
//
// Почему /api/metrics, а не /metrics: корневой путь в этом сервере отдан статике
// SPA и не проходит через authHandler, то есть был бы доступен без сессии и
// раскрывал бы имена проектов. Префикс /api/ даёт аутентификацию и rate-limit
// без исключений.
package server

import (
	"math"
	"net/http"
	"sort"

	"ai/agents/architect"
	"ai/runmetrics"
	"ai/tokens"
)

// metricsView — ответ дашборда: снимок реестра плюс итоги за всё время жизни
// проекта. Разделение осмысленно: снимок относится к текущему запуску (живёт в
// памяти процесса), а Redis-счётчик tokens переживает перезапуски. Показывать
// только одно из двух — значит показывать половину картины.
type metricsView struct {
	runmetrics.Snapshot
	TotalTokensIn  int64   `json:"total_tokens_in"`
	TotalTokensOut int64   `json:"total_tokens_out"`
	TotalCost      float64 `json:"total_cost"`
	TotalCostKnown bool    `json:"total_cost_known"`
	Currency       string  `json:"currency"`
	// DurationBuckets — границы корзин гистограммы длительности, чтобы
	// дашборд рисовал ту же шкалу, что экспорт Prometheus.
	DurationBuckets []float64 `json:"duration_buckets"`
}

// metricsFor возвращает реестр метрик проекта, создавая его при первом
// обращении. Реестры переживают сессию: телеметрия запуска не должна обнуляться
// при переподключении WebSocket или пересоздании сессии проекта.
func (s *Server) metricsFor(project string) *runmetrics.Registry {
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	if s.metrics == nil {
		s.metrics = map[string]*runmetrics.Registry{}
	}
	reg, ok := s.metrics[project]
	if !ok {
		reg = runmetrics.New(project, tokens.PricingFromEnv())
		s.metrics[project] = reg
	}
	return reg
}

// metricsSnapshot возвращает снимки реестров всех проектов, у которых была хоть
// одна запись, в стабильном порядке (воспроизводимый экспорт). Проекты без
// телеметрии пропускаются: нулевые ряды только шумят.
func (s *Server) metricsSnapshot() []runmetrics.Snapshot {
	s.metricsMu.Lock()
	regs := make([]*runmetrics.Registry, 0, len(s.metrics))
	for _, reg := range s.metrics {
		regs = append(regs, reg)
	}
	s.metricsMu.Unlock()

	out := make([]runmetrics.Snapshot, 0, len(regs))
	for _, reg := range regs {
		snap := reg.Snapshot()
		if snap.ToolCalls == 0 && snap.Rounds == 0 && snap.AppLogLines == 0 {
			continue
		}
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out
}

// handleGetMetrics отдаёт снимок метрик одного проекта для дашборда.
func (s *Server) handleGetMetrics(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	reg := s.metricsFor(project)
	snap := reg.Snapshot()
	in, out := totalTokens(r, project)
	price := tokens.PricingFromEnv()
	view := metricsView{
		Snapshot:        snap,
		TotalTokensIn:   in,
		TotalTokensOut:  out,
		TotalCostKnown:  price.Known(),
		Currency:        price.Currency,
		DurationBuckets: runmetrics.StepBuckets(),
	}
	if price.Known() {
		view.TotalCost = round3(price.Cost(in, out))
	}
	writeJSON(w, http.StatusOK, view)
}

// totalTokens читает накопленный счётчик проекта. Сессия проекта намеренно не
// создаётся: метрики — наблюдательный интерфейс, и его запрос не должен
// поднимать доску/чат только ради чтения одного счётчика. Недоступный Redis —
// не ошибка: показаны метрики текущего запуска без накопленных итогов.
func totalTokens(r *http.Request, project string) (in, out int64) {
	cfg := architect.LoadConfig()
	store, err := tokens.NewStore(r.Context(), tokens.StoreConfig{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
		Project:  project,
	})
	if err != nil {
		return 0, 0
	}
	defer store.Close()
	if in, out, err = store.Get(r.Context()); err != nil {
		return 0, 0
	}
	return in, out
}

// handleGetPrometheus отдаёт метрики всех проектов в текстовом формате
// Prometheus (для скрайпера или внешнего Grafana).
func (s *Server) handleGetPrometheus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = runmetrics.WritePrometheus(w, s.metricsSnapshot()...)
}

// handleResetMetrics обнуляет телеметрию текущего запуска проекта. Накопленные
// в Redis токены не трогаются: сброс метрик — это про длительности и счётчики
// вызовов, а не про расход.
func (s *Server) handleResetMetrics(w http.ResponseWriter, r *http.Request) {
	s.metricsFor(r.PathValue("id")).Reset()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
