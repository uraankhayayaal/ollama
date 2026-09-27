package runmetrics

import (
	"strings"
	"testing"
	"time"

	"ai/runevents"
	"ai/tokens"
)

func start(tool, agent, scope string, at time.Time) runevents.Event {
	return runevents.Event{Type: runevents.TypeToolStart, Tool: tool, Agent: agent, Scope: scope, Time: at}
}

func result(tool, agent, scope string, at time.Time, ok bool) runevents.Event {
	return runevents.Event{Type: runevents.TypeToolResult, Tool: tool, Agent: agent, Scope: scope, Time: at, OK: ok}
}

var epoch = time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)

func TestRegistryAggregatesToolCalls(t *testing.T) {
	reg := New("proj", tokens.Pricing{})
	// Два успешных вызова Run по 0.3 с и один упавший по 7 с.
	reg.Record(start("Run", "developer", "task:t1", epoch))
	reg.Record(result("Run", "developer", "task:t1", epoch.Add(300*time.Millisecond), true))
	reg.Record(start("Run", "developer", "task:t1", epoch))
	reg.Record(result("Run", "developer", "task:t1", epoch.Add(300*time.Millisecond), true))
	reg.Record(start("Run", "developer", "task:t1", epoch))
	reg.Record(result("Run", "developer", "task:t1", epoch.Add(7*time.Second), false))

	s := reg.Snapshot()
	if s.ToolCalls != 3 || s.ToolErrors != 1 {
		t.Fatalf("вызовы/ошибки: %d/%d, ждём 3/1", s.ToolCalls, s.ToolErrors)
	}
	if len(s.Tools) != 1 {
		t.Fatalf("инструментов в снимке: %d, ждём 1", len(s.Tools))
	}
	run := s.Tools[0]
	if run.Tool != "Run" || run.Calls != 3 || run.Errors != 1 {
		t.Fatalf("агрегат Run: %+v", run)
	}
	if run.Count != 3 || run.ErrorCount != 1 {
		t.Fatalf("наблюдения/ошибки в гистограмме: %d/%d, ждём 3/1", run.Count, run.ErrorCount)
	}
	// 0.3+0.3+7 = 7.6 с суммарно, среднее ≈ 2.533.
	if got, want := run.SumSec, 7.6; got < want-0.001 || got > want+0.001 {
		t.Fatalf("сумма длительностей: %v, ждём ~%v", got, want)
	}
	if got, want := run.MeanSec, 2.533; got < want-0.001 || got > want+0.001 {
		t.Fatalf("среднее: %v, ждём ~%v", got, want)
	}
	// 7 с попадает в корзину le=10, а 0.3 с — в le=0.5; обе — в le=30.
	if len(run.Buckets) != len(StepBuckets()) {
		t.Fatalf("корзин: %d, ждём %d", len(run.Buckets), len(StepBuckets()))
	}
	if run.Buckets[0] != 0 || run.Buckets[len(StepBuckets())-1] != 3 {
		t.Fatalf("корзины le=0.05=%d, le=60=%d, ждём 0 и 3", run.Buckets[0], run.Buckets[len(StepBuckets())-1])
	}
	// Неуспешным был только вызов на 7 с: он попадает в le=10 и выше.
	if run.ErrorBuckets[0] != 0 || run.ErrorBuckets[len(StepBuckets())-1] != 1 {
		t.Fatalf("корзины неуспешных: %v", run.ErrorBuckets)
	}
	if s.Steps == nil && s.Scopes == nil {
		t.Fatal("снимок без единиц работы: поля серий должны быть пустыми срезами, а не nil-указателями на отсутствие")
	}
	// Агрегаты по scope и агенту.
	if len(s.Scopes) != 1 || s.Scopes[0].Scope != "task:t1" || s.Scopes[0].Calls != 3 || s.Scopes[0].Errors != 1 {
		t.Fatalf("агрегаты по scope: %+v", s.Scopes)
	}
	if len(s.Agents) != 1 || s.Agents[0].Agent != "developer" || s.Agents[0].Calls != 3 {
		t.Fatalf("агрегаты по агенту: %+v", s.Agents)
	}
	if s.StepCount != 3 {
		t.Fatalf("общее число наблюдений шага: %d, ждём 3", s.StepCount)
	}
}

func TestRegistryResultWithoutStartDoesNotInflateCounters(t *testing.T) {
	reg := New("proj", tokens.Pricing{})
	// Результат без парного начала: потерянный или повторно проигранный старт.
	reg.Record(result("ReadFiles", "", "", epoch, true))

	s := reg.Snapshot()
	if s.ToolCalls != 0 {
		t.Fatalf("вызовов без начала: %d, ждём 0", s.ToolCalls)
	}
	if s.Unpaired != 1 {
		t.Fatalf("счётчик дедуп-корзины: %d, ждём 1", s.Unpaired)
	}
	if len(s.Tools) != 1 || s.Tools[0].Calls != 0 {
		t.Fatalf("агрегат инструмента: %+v", s.Tools)
	}
	// Длительность нулевая, но наблюдение записано — гистограмма остаётся целой.
	if s.Tools[0].Count != 1 || s.Tools[0].SumSec != 0 {
		t.Fatalf("гистограмма: %+v", s.Tools[0])
	}
}

func TestRegistryRepeatedStartKeepsFirstTiming(t *testing.T) {
	reg := New("proj", tokens.Pricing{})
	// Повторная доставка старта не должна обнулять начало: задержка измеряется
	// от первого старта (0.4 с), а не от второго (0.1 с).
	reg.Record(start("LspCheck", "", "", epoch))
	reg.Record(start("LspCheck", "", "", epoch.Add(300*time.Millisecond)))
	reg.Record(result("LspCheck", "", "", epoch.Add(400*time.Millisecond), true))

	s := reg.Snapshot()
	if s.ToolCalls != 2 {
		t.Fatalf("вызовов: %d, ждём 2 (второй старт ждёт своего результата)", s.ToolCalls)
	}
	// Второй старт ещё ждёт своего результата — в дедуп-корзину он не попал.
	if s.Unpaired != 0 {
		t.Fatalf("незакрытый второй старт учтён как потерянный результат: %d", s.Unpaired)
	}
	if got, want := s.Tools[0].SumSec, 0.4; got < want-0.001 || got > want+0.001 {
		t.Fatalf("длительность от первого старта: %v, ждём ~%v", got, want)
	}
}

func TestRegistryScopeInheritedFromStartWhenResultHasNone(t *testing.T) {
	reg := New("proj", tokens.Pricing{})
	reg.Record(start("WriteFiles", "developer", "epic:e1", epoch))
	reg.Record(result("WriteFiles", "developer", "", epoch.Add(time.Second), true))

	s := reg.Snapshot()
	if len(s.Scopes) != 1 || s.Scopes[0].Scope != "epic:e1" || s.Scopes[0].Calls != 1 {
		t.Fatalf("scope не унаследован от старта: %+v", s.Scopes)
	}
	if len(s.Agents) != 1 || s.Agents[0].Agent != "developer" {
		t.Fatalf("агент не унаследован от старта: %+v", s.Agents)
	}
}

func TestRegistryStepMetricOverwritesInsteadOfAccumulating(t *testing.T) {
	reg := New("proj", tokens.Pricing{})
	// Перенос checkpoint.TrackMetric в единый реестр: повторная запись того же
	// шага перезаписывает значение, а не копит его.
	reg.SetStepMetric("task:t1", "step-1", "rounds", 2)
	reg.SetStepMetric("task:t1", "step-1", "rounds", 5)
	reg.SetStepMetric("task:t1", "step-1", "files_added", 3)
	reg.SetStepMetric("task:t2", "step-2", "rounds", 1)

	if v, ok := reg.StepMetric("task:t1", "step-1", "rounds"); !ok || v != 5 {
		t.Fatalf("значение метрики шага: %v/%v, ждём 5/true", v, ok)
	}
	s := reg.Snapshot()
	if len(s.Steps) != 3 {
		t.Fatalf("метрик шага в снимке: %d, ждём 3 (перезапись, не накопление)", len(s.Steps))
	}
	// Сортировка стабильная: scope → step → key.
	if s.Steps[0].Key != "files_added" || s.Steps[1].Key != "rounds" {
		t.Fatalf("порядок метрик шага: %+v", s.Steps)
	}
	// Пустые ключи игнорируются.
	reg.SetStepMetric("task:t1", "", "rounds", 9)
	reg.SetStepMetric("task:t1", "step-1", "", 9)
	if len(reg.Snapshot().Steps) != 3 {
		t.Fatal("метрики с пустым шагом или ключом не должны попадать в реестр")
	}
}

func TestRegistryScopeCardinalityLimit(t *testing.T) {
	reg := New("proj", tokens.Pricing{})
	for i := 0; i < maxSeries+10; i++ {
		reg.Record(start("Run", "", scopeName(i), epoch))
		reg.Record(result("Run", "", scopeName(i), epoch.Add(time.Second), true))
	}
	s := reg.Snapshot()
	if len(s.Scopes) > maxSeries {
		t.Fatalf("единиц работы в снимке: %d, лимит %d", len(s.Scopes), maxSeries)
	}
	if s.Overflow == 0 {
		t.Fatal("переполнение кардинальности не учтено")
	}
	var collapsed bool
	for _, sc := range s.Scopes {
		if sc.Scope == scopeOther && sc.Calls > 0 {
			collapsed = true
		}
	}
	if !collapsed {
		t.Fatal("вытесненные единицы работы не схлопнуты в scope=other")
	}
}

func scopeName(i int) string { return "task:t" + itoa(i) }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestRegistryTokensAndCost(t *testing.T) {
	// Без цен стоимость неизвестна, а не нулевая.
	reg := New("proj", tokens.PricingFromEnv())
	reg.Record(runevents.Event{Type: runevents.TypeTokenCount, In: 1000, Out: 500, TPS: 42.5, Scope: "task:t1", Agent: "developer"})
	reg.Record(runevents.Event{Type: runevents.TypeTokenCount, In: 2000, Out: 1500, TPS: 30, Scope: "epic:e1", Agent: "developer"})

	s := reg.Snapshot()
	if s.Rounds != 2 || s.TokensIn != 3000 || s.TokensOut != 2000 || s.TokensTotal != 5000 {
		t.Fatalf("токены/раунды: %+v", s)
	}
	if s.LastTPS != 30 {
		t.Fatalf("tps: %v, ждём последнюю измеренную 30", s.LastTPS)
	}
	if s.CostKnown {
		t.Fatal("цены не заданы — стоимость не должна считаться известной")
	}
	if s.Cost != 0 {
		t.Fatalf("стоимость без цен: %v, ждём 0", s.Cost)
	}
	// Атрибуция по scope и агенту.
	if len(s.Scopes) != 2 || s.Scopes[0].Scope != "epic:e1" || s.Scopes[0].TokensIn != 2000 {
		t.Fatalf("токены по scope: %+v", s.Scopes)
	}
	if len(s.Agents) != 1 || s.Agents[0].TokensIn != 3000 || s.Agents[0].TokensOut != 2000 {
		t.Fatalf("токены по агенту: %+v", s.Agents)
	}
}

func TestRegistryCostWithPricing(t *testing.T) {
	reg := New("proj", tokens.Pricing{InPerMillion: 3, OutPerMillion: 15, Currency: "$"})
	reg.Record(runevents.Event{Type: runevents.TypeTokenCount, In: 1_000_000, Out: 500_000})

	s := reg.Snapshot()
	if !s.CostKnown {
		t.Fatal("цены заданы — стоимость должна быть известна")
	}
	if s.CostIn != 3 || s.CostOut != 7.5 || s.Cost != 10.5 {
		t.Fatalf("стоимость: in=%v out=%v total=%v, ждём 3/7.5/10.5", s.CostIn, s.CostOut, s.Cost)
	}
}

func TestRegistryReset(t *testing.T) {
	reg := New("proj", tokens.Pricing{})
	reg.Record(start("Run", "developer", "task:t1", epoch))
	reg.Record(result("Run", "developer", "task:t1", epoch.Add(time.Second), true))
	reg.Record(runevents.Event{Type: runevents.TypeTokenCount, In: 10, Out: 5})
	reg.SetStepMetric("task:t1", "s", "rounds", 1)
	before := reg.Snapshot().RunID

	reg.Reset()
	s := reg.Snapshot()
	if s.RunID == before {
		t.Fatal("сброс должен открывать новый запуск (новый run_id)")
	}
	if s.ToolCalls != 0 || s.TokensIn != 0 || s.TokensOut != 0 || len(s.Tools) != 0 || len(s.Steps) != 0 {
		t.Fatalf("после сброса реестр не пуст: %+v", s)
	}
	// Незакрытый вызов не переживает сброс: его результат приходит уже без
	// парного начала и попадает в дедуп-корзину, а не «закрывает» старт,
	// начавшийся в прошлом запуске.
	reg.Record(result("Run", "developer", "task:t1", epoch, true))
	after := reg.Snapshot()
	if after.Unpaired != 1 || after.ToolCalls != 0 {
		t.Fatalf("очередь незакрытых вызовов не очищена сбросом: unpaired=%d calls=%d", after.Unpaired, after.ToolCalls)
	}
}

func TestPricingFromEnvParsesAndRejectsGarbage(t *testing.T) {
	t.Setenv("LLM_PRICE_IN", "2,5")
	t.Setenv("LLM_PRICE_OUT", "-1")
	t.Setenv("LLM_CURRENCY", "₽")
	p := tokens.PricingFromEnv()
	if p.InPerMillion != 2.5 {
		t.Fatalf("цена входа: %v, ждём 2.5 (запятая как разделитель)", p.InPerMillion)
	}
	if p.OutPerMillion != 0 {
		t.Fatalf("отрицательная цена должна игнорироваться, получено %v", p.OutPerMillion)
	}
	if p.Currency != "₽" {
		t.Fatalf("валюта: %q", p.Currency)
	}

	t.Setenv("LLM_PRICE_IN", "")
	t.Setenv("LLM_PRICE_OUT", "")
	t.Setenv("LLM_CURRENCY", "")
	if tokens.PricingFromEnv().Known() {
		t.Fatal("без цен Known() должен быть false")
	}
	if tokens.PricingFromEnv().Currency != tokens.DefaultCurrency {
		t.Fatalf("валюта по умолчанию: %q", tokens.PricingFromEnv().Currency)
	}
}

func TestPricingCostZeroWhenUnknown(t *testing.T) {
	var p tokens.Pricing
	if p.Cost(1_000_000, 1_000_000) != 0 {
		t.Fatal("без цен стоимость обязана быть нулевой")
	}
	p = tokens.Pricing{InPerMillion: 1, OutPerMillion: 2}
	if got := p.Cost(1_000_000, 1_000_000); got != 3 {
		t.Fatalf("стоимость: %v, ждём 3", got)
	}
	if got := p.Cost(-5, -5); got != 0 {
		t.Fatalf("отрицательные токены: %v, ждём 0", got)
	}
}

func TestSnapshotIsDeterministic(t *testing.T) {
	reg := New("proj", tokens.Pricing{})
	for _, tool := range []string{"Run", "ReadFiles", "WriteFiles", "LspCheck"} {
		reg.Record(start(tool, "developer", "task:t1", epoch))
		reg.Record(result(tool, "developer", "task:t1", epoch.Add(time.Second), tool != "LspCheck"))
	}
	reg.Record(start("Run", "qa", "task:t2", epoch))
	reg.Record(result("Run", "qa", "task:t2", epoch.Add(2*time.Second), true))
	reg.SetStepMetric("task:t1", "s1", "b", 2)
	reg.SetStepMetric("task:t2", "s2", "a", 1)

	// Порядок обхода map'ов случайный — снимок обязан быть отсортирован.
	for i := 0; i < 20; i++ {
		s := reg.Snapshot()
		var tools []string
		for _, tm := range s.Tools {
			tools = append(tools, tm.Tool)
		}
		want := "LspCheck,ReadFiles,Run,WriteFiles"
		if strings.Join(tools, ",") != want {
			t.Fatalf("инструменты: %v, ждём %s", tools, want)
		}
		if s.Scopes[0].Scope != "task:t1" || s.Scopes[1].Scope != "task:t2" {
			t.Fatalf("порядок scope нарушен: %+v", s.Scopes)
		}
		if s.Agents[0].Agent != "developer" || s.Agents[1].Agent != "qa" {
			t.Fatalf("порядок агентов нарушен: %+v", s.Agents)
		}
		if s.Steps[0].Scope != "task:t1" || s.Steps[1].Scope != "task:t2" {
			t.Fatalf("порядок метрик шага нарушен: %+v", s.Steps)
		}
	}
}
