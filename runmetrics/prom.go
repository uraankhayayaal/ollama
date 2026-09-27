// Экспорт реестра в текстовом формате Prometheus (Ф-1
// PLAN-2026-09-19-todo-owerview-for-prom.md).
//
// Формат — exposition text v0.0.4: HELP/TYPE-комментарии, затем ряды
// `имя{лейблы} значение`. Требования, которые снимок реестра выполняет по
// построению (см. Registry.Snapshot): у каждого набора лейблов ровно один ряд,
// порядок рядов стабильный, значения конечны.
package runmetrics

import (
	"bufio"
	"io"
	"math"
	"strconv"
	"strings"
)

// WritePrometheus пишет снимок в формате Prometheus.
func (r *Registry) WritePrometheus(w io.Writer) error {
	return WritePrometheus(w, r.Snapshot())
}

// WritePrometheus склеивает снимки нескольких проектов в один ответ /metrics.
// Заголовки HELP/TYPE печатаются один раз на весь ответ: имена метрик общие,
// а отличаются только лейблы project.
func WritePrometheus(w io.Writer, snaps ...Snapshot) error {
	bw := bufio.NewWriter(w)
	seen := map[string]bool{}
	for _, s := range snaps {
		writeSnapshot(bw, seen, s)
	}
	return bw.Flush()
}

func writeSnapshot(bw *bufio.Writer, seen map[string]bool, s Snapshot) {
	project := s.Project

	// --- счётчики токенов и раундов ---
	counter(seen, bw, "run_tokens_total", "Потреблённые токены агентского цикла (direction=input/output).")
	row(bw, "run_tokens_total", project, dir("input"), float64(s.TokensIn))
	row(bw, "run_tokens_total", project, dir("output"), float64(s.TokensOut))

	counter(seen, bw, "run_rounds_total", "Раунды модели (вызовы LLM) за время наблюдения.")
	row(bw, "run_rounds_total", project, nil, float64(s.Rounds))

	counter(seen, bw, "run_tool_calls_total", "Вызовы инструментов агентского цикла.")
	row(bw, "run_tool_calls_total", project, nil, float64(s.ToolCalls))
	counter(seen, bw, "run_tool_errors_total", "Неуспешные вызовы инструментов.")
	row(bw, "run_tool_errors_total", project, nil, float64(s.ToolErrors))
	counter(seen, bw, "run_tool_results_unpaired_total", "Результаты инструментов без парного начала (дедуп-корзина).")
	row(bw, "run_tool_results_unpaired_total", project, nil, float64(s.Unpaired))
	counter(seen, bw, "run_app_log_lines_total", "Строки логов рантайма, прочитанные инструментом ReadAppLogs.")
	row(bw, "run_app_log_lines_total", project, nil, float64(s.AppLogLines))

	// --- стоимость ---
	// Цена — гейдж (задаётся конфигурацией), накопленный расход — счётчик.
	// Без заданных цен ряды не выводятся вовсе: нулевая стоимость при
	// неизвестной цене была бы выдумкой.
	if s.CostKnown {
		gauge(seen, bw, "run_token_price_per_million", "Цена 1M токенов в валюте LLM_CURRENCY (direction=input/output).")
		row(bw, "run_token_price_per_million", project, dir("input"), s.PriceIn)
		row(bw, "run_token_price_per_million", project, dir("output"), s.PriceOut)
		counter(seen, bw, "run_cost_units_total", "Накопленная стоимость расхода токенов (kind=input/output/total).")
		row(bw, "run_cost_units_total", project, dir("input"), round3(s.CostIn))
		row(bw, "run_cost_units_total", project, dir("output"), round3(s.CostOut))
		row(bw, "run_cost_units_total", project, dir("total"), round3(s.Cost))
	}

	// --- гейджи времени ---
	gauge(seen, bw, "run_uptime_seconds", "Сколько наблюдается текущий запуск агентского цикла.")
	row(bw, "run_uptime_seconds", project, nil, round3(s.UptimeSec))
	gauge(seen, bw, "run_output_tps", "Последняя измеренная скорость генерации (вых. ток/с).")
	row(bw, "run_output_tps", project, nil, round3(s.LastTPS))
	gauge(seen, bw, "run_series_overflow", "Единицы работы, схлопнутые в scope=other из-за лимита кардинальности.")
	row(bw, "run_series_overflow", project, nil, float64(s.Overflow))

	// --- гистограммы длительности вызовов ---
	const toolDurHelp = "Длительность вызовов инструмента агентского цикла."
	for _, t := range s.Tools {
		ls := []label{{"tool", t.Tool}}
		okCalls := float64(t.Count - t.ErrorCount)
		okSum := t.SumSec - t.ErrorSumSec
		hist(seen, bw, "run_tool_duration_seconds", toolDurHelp, project,
			appendStatus(ls, "ok"), okCalls, okSum, t.Buckets)
		hist(seen, bw, "run_tool_duration_seconds", toolDurHelp, project,
			appendStatus(ls, "error"), float64(t.ErrorCount), t.ErrorSumSec, t.ErrorBuckets)
	}

	// Общая гистограмма шагов по всем единицам работы: счётчики и сумма берутся
	// из снимка (там, где серии нет, остаётся нулевой ряд — он тоже валиден).
	const stepDurHelp = "Длительность шагов (вызовов инструментов) агентского цикла."
	histogramHeader(seen, bw, "run_step_duration_seconds", stepDurHelp)
	row(bw, "run_step_duration_seconds_sum", project, nil, round3(s.StepSumSec))
	row(bw, "run_step_duration_seconds_count", project, nil, float64(s.StepCount))

	// --- именованные метрики шагов (единый реестр, бывший checkpoint) ---
	if len(s.Steps) > 0 {
		gauge(seen, bw, "run_step_metric", "Именованные метрики шага: contract_changes, files_added, rounds, verdict.")
		for _, st := range s.Steps {
			row(bw, "run_step_metric", project, []label{
				{"scope", st.Scope}, {"step", st.Step}, {"key", st.Key},
			}, st.Value)
		}
	}
}

// --- мелкие помощники формата ---

type label struct{ name, value string }

func header(seen map[string]bool, bw *bufio.Writer, name, help, typ string) {
	if seen[name] {
		return
	}
	seen[name] = true
	_, _ = io.WriteString(bw, "# HELP "+name+" "+escapeHelp(help)+"\n")
	_, _ = io.WriteString(bw, "# TYPE "+name+" "+typ+"\n")
}

func gauge(seen map[string]bool, bw *bufio.Writer, name, help string) {
	header(seen, bw, name, help, "gauge")
}

func counter(seen map[string]bool, bw *bufio.Writer, name, help string) {
	header(seen, bw, name, help, "counter")
}

func histogramHeader(seen map[string]bool, bw *bufio.Writer, name, help string) {
	header(seen, bw, name, help, "histogram")
}

// hist печатает одну серию гистограммы с набором лейблов ls: le-корзины
// (по selBuckets), затем +Inf, _sum и _count. Так гистограмма одного
// инструмента публикуется дважды — по status=ok и status=error — из одного
// снимка реестра.
func hist(seen map[string]bool, bw *bufio.Writer, name, help, project string, ls []label,
	count, sum float64, selBuckets []int64) {
	histogramHeader(seen, bw, name, help)
	for i, b := range stepBuckets {
		if i >= len(selBuckets) {
			break
		}
		row(bw, name, project, appendLe(ls, formatFloat(b)), float64(selBuckets[i]))
	}
	row(bw, name, project, appendLe(ls, "+Inf"), count)
	row(bw, name+"_sum", project, ls, round3(sum))
	row(bw, name+"_count", project, ls, count)
}

func appendStatus(ls []label, status string) []label {
	out := make([]label, 0, len(ls)+1)
	out = append(out, ls...)
	return append(out, label{"status", status})
}

func appendLe(ls []label, le string) []label {
	out := make([]label, 0, len(ls)+1)
	out = append(out, ls...)
	return append(out, label{"le", le})
}

func dir(d string) []label { return []label{{"direction", d}} }

func row(bw *bufio.Writer, name, project string, ls []label, v float64) {
	var b strings.Builder
	b.WriteString(name)
	b.WriteString(labels(ls, project))
	b.WriteByte(' ')
	b.WriteString(formatFloat(v))
	b.WriteByte('\n')
	_, _ = io.WriteString(bw, b.String())
}

func labels(ls []label, project string) string {
	all := make([]label, 0, len(ls)+1)
	all = append(all, label{"project", project})
	all = append(all, ls...)
	var b strings.Builder
	b.WriteByte('{')
	for i, l := range all {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(l.name)
		b.WriteString(`="`)
		b.WriteString(escapeLabel(l.value))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

func formatFloat(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "0"
	}
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func escapeLabel(v string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v)
}

func escapeHelp(v string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(v)
}
