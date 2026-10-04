package server

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func tsLine(ts, level, msg string) string {
	if level == "" {
		return ts + " " + msg
	}
	return ts + " " + level + "   " + msg
}

// Уровень берётся из СОБСТВЕННОГО тега строки. Это не косметика: в логах
// агентов полно DETAIL-строк, где слово «ERROR» встречается внутри
// JSON-аргумента инструмента. Поиском подстроки такие строки попали бы в
// разбор (в logs/aistore.json их большинство среди «ошибок»).
func TestParseLogLineUsesOwnTagNotSubstring(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		ts    string
		level string
		msg   string
	}{
		{
			name:  "тег WARN",
			raw:   "2026-09-23 00:17:17.887 WARN   diff calc: патч файла не найден",
			ts:    "2026-09-23 00:17:17.887",
			level: "WARN",
			msg:   "diff calc: патч файла не найден",
		},
		{
			name:  "ERROR внутри DETAIL-аргумента — не ошибка",
			raw:   `2026-09-26 09:35:56.156 DETAIL [инструмент] модель вызывает X: {"level":"ERROR","msg":"boom"}`,
			ts:    "2026-09-26 09:35:56.156",
			level: "DETAIL",
			msg:   `[инструмент] модель вызывает X: {"level":"ERROR","msg":"boom"}`,
		},
		{
			name:  "INFO без тега",
			raw:   "2026-09-23 00:17:17.887 diff calc: baseline зафиксирован",
			ts:    "2026-09-23 00:17:17.887",
			level: "",
			msg:   "diff calc: baseline зафиксирован",
		},
		{
			name:  "строка без метки времени",
			raw:   "2026-10-04 09:45:11.573 FATAL  server: bind: address already in use",
			ts:    "2026-10-04 09:45:11.573",
			level: "FATAL",
			msg:   "server: bind: address already in use",
		},
		{
			name:  "строка без разметки вовсе",
			raw:   "=== Сессия 2026-09-23 19:34:03 (проект: calc) ===",
			ts:    "",
			level: "",
			msg:   "=== Сессия 2026-09-23 19:34:03 (проект: calc) ===",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseLogLine(c.raw)
			if got.TS != c.ts || got.Level != c.level || got.Msg != c.msg {
				t.Fatalf("разбор = %+v\nwant ts=%q level=%q msg=%q", got, c.ts, c.level, c.msg)
			}
		})
	}
}

// Логи приложений пишут уровень как угодно и часто не в начале строки.
func TestLogHeuristicLevelForUntaggedLines(t *testing.T) {
	cases := map[string]string{
		"panic: runtime error: index out of range":    "FATAL",
		"FATAL: не удалось подключиться":              "FATAL",
		"Traceback (most recent call last):":          "FATAL",
		"CRITICAL: очередь переполнена":               "FATAL",
		"2026-01-01 Exception in thread main":         "ERROR",
		"error: cannot read config":                   "ERROR",
		"Error: cannot read config":                   "ERROR",
		"2026-01-01 10:00:00 ERROR failed to connect": "ERROR",
		"warn: deprecated option":                     "WARN",
		"WARNING: deprecated option":                  "WARN",
		"2026-01-01 10:00:00 WARN slow query":         "WARN",
		"обычная строка про успех":                    "",
		"   ": "",
		`[инструмент] модель вызывает X: {"level":"ERROR", "msg":"boom"}`: "ERROR",
	}
	for msg, want := range cases {
		if got := logHeuristicLevel(msg); got != want {
			t.Errorf("logHeuristicLevel(%q) = %q, want %q", msg, got, want)
		}
	}
}

// Размеченная строка разбирается по СВОЕМУ тегу, и эвристика к ней не
// применяется: «ERROR» внутри JSON-аргумента не должен превращать
// DETAIL-строку в ошибку. Иначе в разбор попадала бы масса ложных находок.
func TestBuildLogDigestIgnoresLevelWordInsideTaggedPayload(t *testing.T) {
	content := strings.Join([]string{
		`2026-01-01 10:00:00.000 DETAIL [инструмент] модель вызывает X: {"level":"ERROR","msg":"boom"}`,
		`2026-01-01 10:00:00.001 DETAIL [инструмент] результат X: {"stderr":"ERROR: cannot connect"}`,
	}, "\n")
	d := buildLogDigest("x.log", content)
	if !d.empty() || d.ProblemLines != 0 {
		t.Fatalf("DETAIL-строки разобраны как ошибки: %+v", d)
	}
}

// Главное свойство дайджеста: сотни строк, различающихся только путём к файлу,
// схлопываются в одну находку с частотой и границами времени. Проверено на
// logs/calc.log: 4546 строк WARN → 2 находки.
func TestBuildLogDigestGroupsRepeats(t *testing.T) {
	var b strings.Builder
	for i, f := range []string{"a.js", "b.js", "c/d.js", "@scope/e.js", ".github/w.yml"} {
		ts := "2026-09-23 00:17:1" + string(rune('0'+i)) + ".000"
		b.WriteString(tsLine(ts, "WARN", "патч файла "+f+": файл не найден в снимке: "+f) + "\n")
	}
	d := buildLogDigest("calc.log", b.String())

	if d.ProblemLines != 5 || d.Lines != 5 {
		t.Fatalf("проблемных=%d строк=%d, want 5/5", d.ProblemLines, d.Lines)
	}
	// Две находки, а не пять: путь с каталогом шаблонизируется в <path>,
	// имя файла без каталога — в <file>. Склеить их в одну нельзя: заодно
	// склеились бы «docker-compose.yml» и «node_modules/@scope/pkg» в разных
	// сообщениях. Главное — частота и отсутствие конкретных имён.
	total := 0
	for _, f := range d.Findings {
		total += f.Count
		if strings.Contains(f.Template, "a.js") || strings.Contains(f.Template, "c/d.js") ||
			strings.Contains(f.Template, "@scope") || strings.Contains(f.Template, ".github") {
			t.Errorf("в шаблоне остался конкретный путь: %q", f.Template)
		}
	}
	if total != 5 {
		t.Fatalf("сумма частот = %d, want 5 (каждая строка учтена ровно раз)", total)
	}
	if len(d.Findings) != 2 {
		t.Fatalf("находок=%d, want 2 (<path> и <file>): %+v", len(d.Findings), d.Findings)
	}
	if d.Findings[0].Count != 3 {
		t.Errorf("count первой = %d, want 3", d.Findings[0].Count)
	}
	if d.Findings[0].FirstTS != "2026-09-23 00:17:12.000" || d.Findings[0].LastTS != "2026-09-23 00:17:14.000" {
		t.Errorf("границы времени = %q..%q", d.Findings[0].FirstTS, d.Findings[0].LastTS)
	}
	// Образец остаётся конкретным: по нему ассистент сможет опереться.
	if !strings.Contains(d.Findings[0].Sample, "/") {
		t.Errorf("образец должен остаться конкретным: %q", d.Findings[0].Sample)
	}
}

// Стек-трейс принадлежит находке, а не образует своих: иначе трейс на 40
// строк станет сорока находками, и ни одна не будет читаемой.
func TestBuildLogDigestAttachesStackFramesToFinding(t *testing.T) {
	content := strings.Join([]string{
		"2026-01-01 10:00:00.000 ERROR boom: cannot connect",
		"  at com.foo.Bar(Bar.java:42)",
		"  at com.foo.Baz(Baz.java:7)",
		"2026-01-01 10:00:01.000 INFO всё прошло",
	}, "\n")
	d := buildLogDigest("app.log", content)

	if len(d.Findings) != 1 {
		t.Fatalf("находок=%d, want 1: %+v", len(d.Findings), d.Findings)
	}
	f := d.Findings[0]
	if f.Count != 1 {
		t.Errorf("count = %d, want 1", f.Count)
	}
	if !strings.Contains(f.Frame, "com.foo.Bar") {
		t.Errorf("кадр стека не приклеен: %q", f.Frame)
	}
}

func TestBuildLogDigestGroupsPanicTraceback(t *testing.T) {
	content := "panic: index out of range [3]\n\ngoroutine 1 [running]:\nmain.serve(0x1)\n\t/tmp/x.go:12\n"
	d := buildLogDigest("app.log", content)
	if len(d.Findings) != 1 || d.Findings[0].Level != "FATAL" {
		t.Fatalf("ожидалась одна находка FATAL: %+v", d.Findings)
	}
	if !strings.Contains(d.Findings[0].Frame, "goroutine 1") {
		t.Errorf("начало трейса должно стать кадром: %q", d.Findings[0].Frame)
	}
}

// Тяжесть важнее частоты: один FATAL должен подняться над тысячей WARN, иначе
// падение сервера утонет в шуме предупреждений. Внутри уровня — по частоте.
func TestBuildLogDigestSortsBySeverityThenCount(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 30; i++ {
		b.WriteString("2026-01-01 10:00:00.000 WARN   шум кеша\n")
	}
	for i := 0; i < 5; i++ {
		b.WriteString("2026-01-01 10:00:00.000 WARN   таймаут апстрима\n")
	}
	b.WriteString("2026-01-01 10:00:00.000 WARN   редкий вывод\n")
	b.WriteString("2026-01-01 10:00:00.000 ERROR разовое падение\n")
	b.WriteString("2026-01-01 10:00:00.000 FATAL  сервер упал\n")
	d := buildLogDigest("x.log", b.String())

	want := []struct {
		level string
		count int
	}{
		{"FATAL", 1}, {"ERROR", 1}, {"WARN", 30}, {"WARN", 5}, {"WARN", 1},
	}
	if len(d.Findings) != len(want) {
		t.Fatalf("находок=%d, want %d: %+v", len(d.Findings), len(want), d.Findings)
	}
	for i, w := range want {
		if d.Findings[i].Level != w.level || d.Findings[i].Count != w.count {
			t.Errorf("находка %d = %q x%d, want %q x%d", i,
				d.Findings[i].Level, d.Findings[i].Count, w.level, w.count)
		}
	}
}

// Дайджест — это вход в контекст модели, поэтому его размер обязан быть
// предсказуемым: лишние находки отбрасываются, но их количество видно.
func TestBuildLogDigestCapsFindingsAndCountsDropped(t *testing.T) {
	var b strings.Builder
	for i := 0; i < logDigestMax+8; i++ {
		b.WriteString("2026-01-01 10:00:00.000 WARN   уникальная проблема " + string(rune('A'+i)) + "\n")
	}
	d := buildLogDigest("x.log", b.String())

	if len(d.Findings) != logDigestMax {
		t.Fatalf("находок=%d, want %d", len(d.Findings), logDigestMax)
	}
	if d.Dropped != 8 {
		t.Fatalf("dropped = %d, want 8", d.Dropped)
	}
	if !strings.Contains(d.Text(), "опущено") {
		t.Errorf("Text() должен сообщать об отброшенных находках: %q", d.Text())
	}
}

// Разбор ограничен последними logDigestWindow строками: старые записи в
// разбор не идут, зато стоимость не растёт с размером файла.
func TestBuildLogDigestLooksAtLastWindowOnly(t *testing.T) {
	var b strings.Builder
	for i := 0; i < logDigestWindow+100; i++ {
		b.WriteString("2026-01-01 10:00:00.000 WARN   повтор\n")
	}
	b.WriteString("2026-01-01 10:00:00.000 ERROR   уникальное падение\n")
	d := buildLogDigest("x.log", b.String())

	if d.Lines != logDigestWindow {
		t.Fatalf("Lines = %d, want %d", d.Lines, logDigestWindow)
	}
	if d.ProblemLines != logDigestWindow {
		t.Fatalf("ProblemLines = %d, want %d", d.ProblemLines, logDigestWindow)
	}
	// Ровно одна строка «повтор» осталась за окном разбора.
	if len(d.Findings) != 2 {
		t.Fatalf("находок=%d, want 2 (повтор + уникальное падение): %+v", len(d.Findings), d.Findings)
	}
	if d.Findings[0].Level != "ERROR" || d.Findings[0].Count != 1 {
		t.Fatalf("первой должна быть уникальная ошибка: %+v", d.Findings[0])
	}
	if d.Findings[1].Level != "WARN" || d.Findings[1].Count != logDigestWindow-1 {
		t.Fatalf("повторов в окне = %d, want %d", d.Findings[1].Count, logDigestWindow-1)
	}
}

// INFO/DETAIL/DEBUG в разбор не идут: в логах агентов их подавляющее
// большинство, и включив их, мы бы вытеснили настоящие ошибки.
func TestBuildLogDigestIgnoresVerboseLevels(t *testing.T) {
	content := strings.Join([]string{
		"2026-01-01 10:00:00.000 INFO   агент начал задачу",
		"2026-01-01 10:00:00.001 DETAIL [инструмент] модель вызывает Read",
		"2026-01-01 10:00:00.002 DEBUG  внутренний кеш",
		"2026-01-01 10:00:00.003 TRACE  трейс вызова",
	}, "\n")
	d := buildLogDigest("x.log", content)
	if !d.empty() || d.ProblemLines != 0 {
		t.Fatalf("пустой лог без проблем разобран как проблемный: %+v", d)
	}
	if !strings.Contains(d.Text(), "не найдено") {
		t.Errorf("Text() для пустого дайджеста: %q", d.Text())
	}
}

// Худший уровень внутри группы побеждает: то же сообщение может иногда
// логироваться как WARN, иногда как ERROR.
func TestBuildLogDigestKeepsWorstLevelInGroup(t *testing.T) {
	content := strings.Join([]string{
		"2026-01-01 10:00:00.000 WARN   диск переполнен на узле 1",
		"2026-01-01 10:00:01.000 ERROR диск переполнен на узле 2",
		"2026-01-01 10:00:02.000 WARN   диск переполнен на узле 3",
	}, "\n")
	d := buildLogDigest("x.log", content)

	if len(d.Findings) != 1 {
		t.Fatalf("находок=%d, want 1: %+v", len(d.Findings), d.Findings)
	}
	f := d.Findings[0]
	if f.Level != "ERROR" || f.Count != 3 {
		t.Fatalf("level=%q count=%d, want ERROR/3", f.Level, f.Count)
	}
	if f.FirstTS != "2026-01-01 10:00:00.000" || f.LastTS != "2026-01-01 10:00:02.000" {
		t.Errorf("границы = %q..%q", f.FirstTS, f.LastTS)
	}
}

// Обрезка не должна порвать многобайтовый символ: текст уходит в промпт.
func TestTruncateLogTextKeepsValidUTF8(t *testing.T) {
	long := strings.Repeat("я", 500)
	got := truncateLogText(long, 100)
	if !utf8.ValidString(got) {
		t.Fatalf("невалидный UTF-8: %q", got[:20])
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("обрезка не помечена: %q", got[len(got)-10:])
	}
	// Обрезка ровно по границе не должна ничего портить.
	if got := truncateLogText("коротко", 100); got != "коротко" {
		t.Errorf("короткая строка изменена: %q", got)
	}
}

// Находка не должна раздувать промпт: пример и кадр стека ограничены.
func TestBuildLogDigestBoundsSampleLength(t *testing.T) {
	long := strings.Repeat("A", 5000)
	d := buildLogDigest("x.log", "2026-01-01 10:00:00.000 ERROR "+long+"\n  at foo"+long)
	f := d.Findings[0]
	if len(f.Sample) > logDigestSample+4 {
		t.Errorf("образец длиной %d байт, предел %d", len(f.Sample), logDigestSample)
	}
	if len(f.Frame) > logDigestSample+4 {
		t.Errorf("кадр длиной %d байт, предел %d", len(f.Frame), logDigestSample)
	}
}
