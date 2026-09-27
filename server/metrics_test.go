package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai/runevents"
	"ai/runmetrics"
)

// toolRound прогоняет через роутер сессии полный цикл «вызов инструмента»
// (события старта и результата) и раунд с расходом токенов — так проверки
// видят ровно тот путь, по которому события идут в реестр в проде.
func toolRound(sess *Session, tool, scope string, dur time.Duration, ok bool, in, out int64) {
	now := time.Now()
	sess.routeRunEvent(runevents.Event{
		Type: runevents.TypeToolStart, Tool: tool, Agent: "developer", Scope: scope, Time: now,
	})
	sess.routeRunEvent(runevents.Event{
		Type: runevents.TypeToolResult, Tool: tool, Agent: "developer", Scope: scope, Time: now.Add(dur), OK: ok,
	})
	sess.routeRunEvent(runevents.Event{
		Type: runevents.TypeTokenCount, In: in, Out: out, TPS: 10, Scope: scope, Agent: "developer", Time: now.Add(dur),
	})
}

// TestRESTProjectMetrics проверяет снимок метрик проекта: агрегаты по
// инструментам и единицам работы, расход токенов и честный отказ считать
// стоимость, когда цены не заданы.
func TestRESTProjectMetrics(t *testing.T) {
	srv, handler, _ := newTestServer(t)
	t.Setenv("LLM_PRICE_IN", "")
	t.Setenv("LLM_PRICE_OUT", "")

	sess, _, err := srv.getOrCreate("m-proj")
	if err != nil {
		t.Fatal(err)
	}
	toolRound(sess, "Run", "task:t1", 400*time.Millisecond, true, 1000, 500)
	toolRound(sess, "Run", "task:t1", 2*time.Second, false, 2000, 1000)
	toolRound(sess, "ReadFiles", "task:t2", 100*time.Millisecond, true, 0, 0)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/projects/m-proj/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET metrics: %d, body: %s", rec.Code, rec.Body.String())
	}
	var view metricsView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ToolCalls != 3 || view.ToolErrors != 1 {
		t.Fatalf("вызовы/ошибки: %d/%d, ждём 3/1", view.ToolCalls, view.ToolErrors)
	}
	if view.TokensIn != 3000 || view.TokensOut != 1500 || view.Rounds != 3 {
		t.Fatalf("токены/раунды: %+v", view.Snapshot)
	}
	if len(view.Tools) != 2 {
		t.Fatalf("инструментов: %d, ждём 2", len(view.Tools))
	}
	if len(view.Scopes) != 2 {
		t.Fatalf("единиц работы: %d, ждём 2", len(view.Scopes))
	}
	if view.CostKnown {
		t.Fatal("цены не заданы — стоимость не должна считаться известной")
	}
	if view.TotalCost != 0 || view.Currency == "" {
		t.Fatalf("стоимость без цен: %+v", view)
	}
	// Событие TypeTokenCount проходит через routeRunEvent → chatEvent →
	// addTokens, поэтому Redis-счётчик проекта и снимок запуска совпадают.
	// Проверяем именно это: два представления расхода не расходятся.
	if view.TotalTokensIn != view.TokensIn || view.TotalTokensOut != view.TokensOut {
		t.Fatalf("итоги по проекту разошлись со снимком запуска: %+v", view)
	}
	if len(view.DurationBuckets) == 0 {
		t.Fatal("в ответе нет границ гистограммы — дашборду нечем рисовать шкалу")
	}
}

// TestRESTProjectMetricsCostWithPricing: с заданными ценами снимок отдаёт и
// стоимость запуска, и стоимость по проекту.
func TestRESTProjectMetricsCostWithPricing(t *testing.T) {
	srv, handler, _ := newTestServer(t)
	t.Setenv("LLM_PRICE_IN", "3")
	t.Setenv("LLM_PRICE_OUT", "15")
	t.Setenv("LLM_CURRENCY", "₽")

	sess, _, err := srv.getOrCreate("m-cost")
	if err != nil {
		t.Fatal(err)
	}
	// Реальный путь расхода: событие цикла TypeTokenCount обрабатывается
	// дважды — реестром (длительности/снимок запуска) и addTokens (Redis-счётчик
	// проекта). Именно этот путь и должен давать согласованные числа.
	sess.routeRunEvent(runevents.Event{
		Type: runevents.TypeTokenCount, In: 1_000_000, Out: 200_000, TPS: 12, Scope: "task:t1", Agent: "developer",
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/projects/m-cost/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET metrics: %d, body: %s", rec.Code, rec.Body.String())
	}
	var view metricsView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.CostKnown || view.Currency != "₽" {
		t.Fatalf("цены не подхватились: %+v", view)
	}
	// Стоимость по проекту: 1M×3 + 0.2M×15 = 6.
	if view.TotalTokensIn != 1_000_000 || view.TotalCost != 6 {
		t.Fatalf("итоги/стоимость по проекту: %d/%v, ждём 1000000/6", view.TotalTokensIn, view.TotalCost)
	}
	// Реестр подписан на routeRunEvent, а addTokens — это обработчик того же
	// события TypeTokenCount: расход, прошедший через addTokens, виден и в
	// снимке запуска. Иначе «стоимость запуска» и «стоимость по проекту»
	// показывали бы разные числа для одного и того же расхода.
	if view.TokensIn != 1_000_000 || view.TokensOut != 200_000 {
		t.Fatalf("токены запуска: %d/%d, ждём 1000000/200000", view.TokensIn, view.TokensOut)
	}
	if view.Cost != 6 {
		t.Fatalf("стоимость запуска: %v, ждём 6", view.Cost)
	}
}

// TestRESTPrometheusMetrics: экспорт в формате Prometheus по всем проектам.
func TestRESTPrometheusMetrics(t *testing.T) {
	srv, handler, _ := newTestServer(t)

	a, _, err := srv.getOrCreate("m-alpha")
	if err != nil {
		t.Fatal(err)
	}
	toolRound(a, "Run", "task:t1", 500*time.Millisecond, true, 100, 50)
	b, _, err := srv.getOrCreate("m-beta")
	if err != nil {
		t.Fatal(err)
	}
	toolRound(b, "ReadFiles", "", 200*time.Millisecond, true, 10, 5)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/metrics: %d, body: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type: %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"# TYPE run_tool_calls_total counter",
		`run_tool_calls_total{project="m-alpha"} 1`,
		`run_tool_calls_total{project="m-beta"} 1`,
		`run_tokens_total{project="m-alpha",direction="input"} 100`,
		`run_tool_duration_seconds_count{project="m-alpha",tool="Run",status="ok"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("нет %q в экспорте:\n%s", want, body)
		}
	}
	// Заголовок на оба проекта — один: имена метрик общие.
	if n := strings.Count(body, "# TYPE run_tool_calls_total "); n != 1 {
		t.Fatalf("заголовков TYPE: %d, ждём 1", n)
	}
}

// TestRESTPrometheusSkipsIdleProjects: проект без единого вызова инструмента
// не засоряет экспорт нулевыми рядами.
func TestRESTPrometheusSkipsIdleProjects(t *testing.T) {
	srv, handler, _ := newTestServer(t)
	if _, _, err := srv.getOrCreate("m-idle"); err != nil {
		t.Fatal(err)
	}
	active, _, err := srv.getOrCreate("m-active")
	if err != nil {
		t.Fatal(err)
	}
	toolRound(active, "Run", "", time.Second, true, 1, 1)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/metrics", nil))
	if body := rec.Body.String(); strings.Contains(body, `project="m-idle"`) {
		t.Fatalf("проект без телеметрии попал в экспорт:\n%s", body)
	}
}

// TestRESTResetProjectMetrics: сброс обнуляет телеметрию запуска.
func TestRESTResetProjectMetrics(t *testing.T) {
	srv, handler, _ := newTestServer(t)

	sess, _, err := srv.getOrCreate("m-reset")
	if err != nil {
		t.Fatal(err)
	}
	toolRound(sess, "Run", "task:t1", time.Second, true, 700, 300)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/projects/m-reset/metrics/reset", strings.NewReader("{}")))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST reset: %d, body: %s", rec.Code, rec.Body.String())
	}

	snap := srv.metricsFor("m-reset").Snapshot()
	if snap.ToolCalls != 0 || snap.TokensIn != 0 || snap.TokensOut != 0 {
		t.Fatalf("после сброса реестр не пуст: %+v", snap)
	}
}

// TestMetricsRegistrySurvivesSessionRecreate: реестр принадлежит проекту, а не
// сессии — пересоздание сессии не обнуляет телеметрию запуска.
func TestMetricsRegistrySurvivesSessionRecreate(t *testing.T) {
	srv, _, _ := newTestServer(t)

	sess, created, err := srv.getOrCreate("m-persist")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("первая сессия должна создаваться")
	}
	toolRound(sess, "Run", "task:t1", time.Second, true, 10, 5)

	again, created2, err := srv.getOrCreate("m-persist")
	if err != nil {
		t.Fatal(err)
	}
	if created2 || again != sess {
		t.Fatal("сессия должна переиспользоваться, а не пересоздаваться")
	}
	if again.metrics != sess.metrics {
		t.Fatal("реестр метрик должен быть общим на проект")
	}
	if got := sess.metrics.Snapshot().ToolCalls; got != 1 {
		t.Fatalf("вызовов в реестре: %d, ждём 1", got)
	}
}

// TestMetricsSnapshotSortedByProject: порядок проектов в экспорте стабилен.
func TestMetricsSnapshotSortedByProject(t *testing.T) {
	srv, _, _ := newTestServer(t)
	for _, name := range []string{"m-z", "m-a", "m-m"} {
		sess, _, err := srv.getOrCreate(name)
		if err != nil {
			t.Fatal(err)
		}
		toolRound(sess, "Run", "", time.Second, true, 1, 1)
	}
	snaps := srv.metricsSnapshot()
	if len(snaps) != 3 {
		t.Fatalf("снимков: %d, ждём 3", len(snaps))
	}
	if snaps[0].Project != "m-a" || snaps[1].Project != "m-m" || snaps[2].Project != "m-z" {
		t.Fatalf("порядок проектов: %s, %s, %s", snaps[0].Project, snaps[1].Project, snaps[2].Project)
	}
	// Снимок реестра напрямую — тоже в реестре проектов.
	if got := srv.metricsFor("m-a").Snapshot().Project; got != "m-a" {
		t.Fatalf("проект реестра: %q", got)
	}
	if runmetrics.StepBuckets()[0] != 0.05 {
		t.Fatal("границы гистограммы изменились — снимок и экспорт разойдутся")
	}
}
