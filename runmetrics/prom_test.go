package runmetrics

import (
	"strings"
	"testing"
	"time"

	"ai/runevents"
	"ai/tokens"
)

func write(t *testing.T, snaps ...Snapshot) string {
	t.Helper()
	var b strings.Builder
	if err := WritePrometheus(&b, snaps...); err != nil {
		t.Fatalf("WritePrometheus: %v", err)
	}
	return b.String()
}

// rows собирает все ряды (без комментариев) в виде «имя{лейблы} значение».
func rows(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

func metric(text, name string) []string {
	var out []string
	for _, line := range rows(text) {
		if strings.HasPrefix(line, name) {
			out = append(out, line)
		}
	}
	return out
}

func TestPrometheusExpositionFormat(t *testing.T) {
	reg := New("my-project", tokens.Pricing{InPerMillion: 3, OutPerMillion: 15})
	reg.Record(start("Run", "developer", "task:t1", epoch))
	reg.Record(result("Run", "developer", "task:t1", epoch.Add(300*time.Millisecond), true))
	reg.Record(start("LspCheck", "developer", "task:t1", epoch))
	reg.Record(result("LspCheck", "developer", "task:t1", epoch.Add(4*time.Second), false))
	reg.Record(runevents.Event{Type: runevents.TypeTokenCount, In: 1_000_000, Out: 500_000, TPS: 55.5})
	reg.SetStepMetric("task:t1", "step-1", "contract_changes", 4)

	text := write(t, reg.Snapshot())

	// HELP/TYPE для каждой метрики — ровно один раз.
	for _, name := range []string{
		"run_tokens_total", "run_rounds_total", "run_tool_calls_total", "run_tool_errors_total",
		"run_cost_units_total", "run_token_price_per_million", "run_uptime_seconds",
		"run_output_tps", "run_tool_duration_seconds", "run_step_duration_seconds", "run_step_metric",
	} {
		if n := strings.Count(text, "# TYPE "+name+" "); n != 1 {
			t.Fatalf("заголовков TYPE для %s: %d, ждём 1", name, n)
		}
		if !strings.Contains(text, "# HELP "+name+" ") {
			t.Fatalf("нет HELP для %s", name)
		}
	}

	// Значения и лейблы.
	want := []string{
		`run_tokens_total{project="my-project",direction="input"} 1000000`,
		`run_tokens_total{project="my-project",direction="output"} 500000`,
		`run_rounds_total{project="my-project"} 1`,
		`run_tool_calls_total{project="my-project"} 2`,
		`run_tool_errors_total{project="my-project"} 1`,
		`run_token_price_per_million{project="my-project",direction="input"} 3`,
		`run_token_price_per_million{project="my-project",direction="output"} 15`,
		`run_cost_units_total{project="my-project",direction="input"} 3`,
		`run_cost_units_total{project="my-project",direction="output"} 7.5`,
		`run_cost_units_total{project="my-project",direction="total"} 10.5`,
		`run_output_tps{project="my-project"} 55.5`,
		`run_step_metric{project="my-project",scope="task:t1",step="step-1",key="contract_changes"} 4`,
		`run_tool_duration_seconds{project="my-project",tool="Run",status="ok",le="+Inf"} 1`,
		`run_tool_duration_seconds{project="my-project",tool="LspCheck",status="error",le="+Inf"} 1`,
		`run_tool_duration_seconds_sum{project="my-project",tool="Run",status="ok"} 0.3`,
		`run_tool_duration_seconds_count{project="my-project",tool="Run",status="ok"} 1`,
		`run_tool_duration_seconds_sum{project="my-project",tool="LspCheck",status="error"} 4`,
		`run_step_duration_seconds_count{project="my-project"} 2`,
		`run_step_duration_seconds_sum{project="my-project"} 4.3`,
	}
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Fatalf("нет ряда %q в экспорте:\n%s", w, text)
		}
	}

	// Гистограмма: 0.3 с → le=0.5, 4 с → le=5; обе в le=60.
	if !strings.Contains(text, `run_tool_duration_seconds{project="my-project",tool="Run",status="ok",le="0.5"} 1`) {
		t.Fatalf("нет корзины le=0.5:\n%s", text)
	}
	if !strings.Contains(text, `run_tool_duration_seconds{project="my-project",tool="Run",status="ok",le="0.05"} 0`) {
		t.Fatalf("корзина le=0.05 должна быть нулевой:\n%s", text)
	}
}

func TestPrometheusOneRowPerLabelSet(t *testing.T) {
	reg := New("proj", tokens.Pricing{})
	for i := 0; i < 3; i++ {
		reg.Record(start("Run", "developer", "task:t1", epoch))
		reg.Record(result("Run", "developer", "task:t1", epoch.Add(time.Second), true))
	}
	text := write(t, reg.Snapshot())

	seen := map[string]bool{}
	for _, line := range rows(text) {
		open := strings.Index(line, "{")
		if open < 0 {
			continue
		}
		series := line[:strings.LastIndex(line, " ")]
		if seen[series] {
			t.Fatalf("повтор ряда с тем же набором лейблов: %q\n%s", series, text)
		}
		seen[series] = true
	}
}

func TestPrometheusMultipleProjectsShareHeaders(t *testing.T) {
	a, b := New("alpha", tokens.Pricing{}), New("beta", tokens.Pricing{})
	for _, reg := range []*Registry{a, b} {
		reg.Record(start("Run", "developer", "task:t1", epoch))
		reg.Record(result("Run", "developer", "task:t1", epoch.Add(time.Second), true))
	}
	text := write(t, a.Snapshot(), b.Snapshot())

	if n := strings.Count(text, "# TYPE run_tool_calls_total "); n != 1 {
		t.Fatalf("заголовков TYPE на два проекта: %d, ждём 1", n)
	}
	for _, want := range []string{
		`run_tool_calls_total{project="alpha"} 1`,
		`run_tool_calls_total{project="beta"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("нет ряда %q:\n%s", want, text)
		}
	}
	// Одинаковое имя метрики с разными лейблами project — это разные ряды,
	// повтора по набору лейблов нет.
	seen := map[string]bool{}
	for _, line := range rows(text) {
		if strings.HasPrefix(line, "run_tool_calls_total{") {
			if seen[line] {
				t.Fatalf("повтор ряда: %q", line)
			}
			seen[line] = true
		}
	}
}

func TestPrometheusSkipsCostWithoutPricing(t *testing.T) {
	reg := New("proj", tokens.Pricing{})
	reg.Record(runevents.Event{Type: runevents.TypeTokenCount, In: 1000, Out: 500})
	text := write(t, reg.Snapshot())
	if strings.Contains(text, "run_cost_units_total") || strings.Contains(text, "run_token_price_per_million") {
		t.Fatalf("без цен метрики стоимости выводиться не должны:\n%s", text)
	}
	if !strings.Contains(text, `run_tokens_total{project="proj",direction="input"} 1000`) {
		t.Fatalf("токены должны выводиться всегда:\n%s", text)
	}
}

func TestPrometheusEscapesLabelValues(t *testing.T) {
	reg := New(`quo"te\pro`, tokens.Pricing{})
	reg.Record(runevents.Event{Type: runevents.TypeTokenCount, In: 1})
	text := write(t, reg.Snapshot())
	if !strings.Contains(text, `project="quo\"te\\pro"`) {
		t.Fatalf("лейбл не экранирован:\n%s", text)
	}
	// Одна строка на ряд: необработанный перевод строки разорвал бы формат.
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "run_") && strings.Count(line, "{") != 1 {
			t.Fatalf("ряд разорван переводом строки: %q", line)
		}
	}
}

func TestPrometheusExpositionIsStable(t *testing.T) {
	reg := New("proj", tokens.Pricing{InPerMillion: 1})
	for _, tool := range []string{"Run", "ReadFiles", "WriteFiles"} {
		reg.Record(start(tool, "developer", "task:t1", epoch))
		reg.Record(result(tool, "developer", "task:t1", epoch.Add(time.Second), tool == "Run"))
	}
	s := reg.Snapshot()
	first := write(t, s)
	// Uptime — единственное «плавающее» поле; вырезаем его и сравниваем
	// остальное, которое обязано быть побайтово тем же.
	strip := func(in string) string {
		var keep []string
		for _, line := range strings.Split(in, "\n") {
			if strings.HasPrefix(line, "run_uptime_seconds{") {
				continue
			}
			keep = append(keep, line)
		}
		return strings.Join(keep, "\n")
	}
	if a, b := strip(first), strip(write(t, s)); a != b {
		t.Fatalf("экспорт нестабилен между вызовами")
	}
	if got := metric(first, "run_tool_duration_seconds_count"); len(got) != 6 {
		t.Fatalf("ряд _count по двум статусам на инструмент ждём 6, получено %d: %v", len(got), got)
	}
}
