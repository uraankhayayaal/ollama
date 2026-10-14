// Дайджест логов для разбора ассистентом: сырой лог в контекст модели не
// годится (хвост файла — до 1 МБ), а повторяющиеся строки съедают окно.
//
// Проверено на реальных логах: 2670 строк WARN в logs/calc.log после
// абстрагирования переменных частей схлопываются в 2 находки. Без этого
// модель получила бы простыню однотипных предупреждений и ответила бы
// «да, есть предупреждения».
//
// Что делает дайджест:
//   - уровень берётся из СОБСТВЕННОГО тега строки, а не из поиска подстроки:
//     слово «ERROR» внутри JSON-аргумента инструмента не должно считаться
//     ошибкой (в logs/aistore.log таких ложных срабатываний большинство);
//   - нетегированные строки (логи самого приложения) классифицируются по
//     эвристикам: panic/Traceback/Exception/error;
//   - строки стека приклеиваются к предыдущей находке, а не живут своей —
//     иначе трейс на 40 строк превратится в 40 находок;
//   - переменные части (пути, числа, UUID, кавычки) заменяются
//     плейсхолдерами: получается шаблон, по которому строки группируются;
//   - находки сортируются по тяжести и частоте и обрезаются до LogDigestMax.

package log

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Границы дайджеста. Заданы сверху: дайджест — это вход в контекст модели,
// то есть его размер должен быть предсказуемым и небольшим.
const (
	// LogDigestWindow — сколько последних строк лога разбираем.
	LogDigestWindow = 5000
	// LogDigestMax — сколько находок уходит в ответ (остальные отбрасываются,
	// их число видно в поле Dropped).
	LogDigestMax = 12
	// LogDigestSample — предел длины строки-примера и строки стека.
	LogDigestSample = 300
)

// LogLine — разобранная строка лога.
type LogLine struct {
	Raw   string
	TS    string // метка времени, если строка размечена
	Level string // FATAL/ERROR/WARN/..., если строка размечена
	Msg   string // текст без метки времени и тега уровня
}

// LogFinding — группа повторяющихся проблем: шаблон + частота + границы времени.
type LogFinding struct {
	Level    string // худший уровень в группе
	Template string // сообщение с абстрагированными переменными
	Sample   string // реальная строка-пример
	Count    int
	FirstTS  string
	LastTS   string
	Frame    string // первая строка стека, если была
}

// LogDigest — сводка лога для ассистента.
type LogDigest struct {
	File         string       `json:"file"`
	Lines        int          `json:"lines"`         // строк в окне разбора
	ProblemLines int          `json:"problem_lines"` // из них помеченных проблемными
	Findings     []LogFinding `json:"findings"`
	Dropped      int          `json:"dropped"` // находок сверх LogDigestMax
}

// empty — дайджест без находок: UI показывает «проблем не найдено».
func (d LogDigest) Empty() bool { return len(d.Findings) == 0 }

// Text — дайджест строкой для промпта ассистента.
func (d LogDigest) Text() string {
	if d.Empty() {
		return "Дайджест логов: проблемных строк не найдено."
	}
	var b strings.Builder
	for i, f := range d.Findings {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(string(f.Level))
		b.WriteString(" x")
		b.WriteString(strconv.Itoa(f.Count))
		if f.Level != "" {
			b.WriteString(" | ")
		}
		b.WriteString(f.Template)
		if f.FirstTS != "" {
			b.WriteString("\n  первое: ")
			b.WriteString(f.FirstTS)
			b.WriteString(", последнее: ")
			b.WriteString(f.LastTS)
		}
		if f.Sample != "" {
			b.WriteString("\n  пример: ")
			b.WriteString(f.Sample)
		}
		if f.Frame != "" {
			b.WriteString("\n  стек: ")
			b.WriteString(f.Frame)
		}
	}
	if d.Dropped > 0 {
		b.WriteString("\n")
		b.WriteString("(ещё ")
		b.WriteString(strconv.Itoa(d.Dropped))
		b.WriteString(" находок сверх лимита опущено)")
	}
	return b.String()
}

// --- разбор строки ---

// Тег уровня в начале остатка строки: сначала длинные названия, чтобы FATAL
// не съелось префиксом ERROR. DETAIL обязателен: без него строка вида
// «DETAIL [инструмент] … {"level":"ERROR"}» не распозналась бы как
// размеченная и ушла в разбор по эвристике.
var logLevelTag = regexp.MustCompile(`^(FATAL|ERROR|WARN|INFO|DEBUG|TRACE|CRITICAL|PANIC|DETAIL)\s+`)

// Метка времени, которой пишет пакет logging: «2006-01-02 15:04:05.000 ».
// Уровень для INFO не пишется вовсе (emit: тег пустой), поэтому он и не
// разбирается — такие строки проблемными не считаются.
var logTimeMark = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}) `)

// ParseLogLine разбирает строку на метку времени, тег уровня и текст.
func ParseLogLine(raw string) LogLine {
	l := LogLine{Raw: raw}
	rest := raw
	if m := logTimeMark.FindStringSubmatch(rest); m != nil {
		l.TS = m[1]
		rest = rest[len(m[0]):]
	}
	if m := logLevelTag.FindStringSubmatch(rest); m != nil {
		l.Level = m[1]
		rest = rest[len(m[0]):]
	}
	l.Msg = strings.TrimRight(rest, "\r")
	return l
}

// logProblemLevels — уровни, которые разбираем как проблему. DEBUG/TRACE/INFO
// в разбор не идут: в логах агентов их подавляющее большинство, и включив их
// в дайджест, мы бы вытеснили настоящие ошибки.
var logProblemLevels = map[string]bool{
	"FATAL": true, "ERROR": true, "WARN": true, "CRITICAL": true, "PANIC": true,
}

// logLevelWord — уровень отдельным словом в строке. Логи приложений пишут
// уровень как угодно («2026-01-01 Exception in thread main», «10:00:00 WARN
// slow query»), поэтому ищем слово в любом месте. На размеченных строках
// (тег распознан) эта эвристика НЕ применяется — иначе «ERROR» внутри
// JSON-аргумента DETAIL-строки стал бы ошибкой.
var logLevelWord = regexp.MustCompile(`(?i)\b(FATAL|CRITICAL|ERROR|EXCEPTION|WARNING|WARN)\b`)

// LogHeuristicLevel — уровень нетегированной строки. Нужен для логов САМОГО
// приложения (пишутся его собственным логгером, наш тег туда не попадает).
func LogHeuristicLevel(msg string) string {
	low := strings.ToLower(strings.TrimSpace(msg))
	if low == "" {
		return ""
	}
	// Паника и трейс — до поиска слова уровня: в них нет ни ERROR, ни FATAL.
	if strings.HasPrefix(low, "panic:") || strings.HasPrefix(low, "fatal:") ||
		strings.Contains(low, "traceback (most recent call last)") {
		return "FATAL"
	}
	m := logLevelWord.FindStringSubmatch(low)
	if m == nil {
		return ""
	}
	switch strings.ToUpper(m[1]) {
	case "FATAL", "CRITICAL":
		return "FATAL"
	case "ERROR", "EXCEPTION":
		return "ERROR"
	default: // WARN, WARNING
		return "WARN"
	}
}

// logStackFrame — продолжение стека. Такие строки принадлежат предыдущей
// находке, а не образуют новую.
var logStackFrame = regexp.MustCompile(
	`^\s+at\s+\S+|` + // JS/Java: "  at com.foo.Bar(Bar.java:42)"
		`^\s+(?:[a-zA-Z_][\w.]*\.)?[A-Za-z_]\w*\(.*\d+\)|` + // Go: "  main.serve(0x..):"
		`^\s*at [\w./-]+:\d+|` + // Python/Ruby: "  File \"x.py\", line 3, in f"
		`^goroutine \d+ \[`, // Go: начало трейса
)

// IsLogStackFrame — строка является кадром стека (продолжением находки).
func IsLogStackFrame(msg string) bool {
	return logStackFrame.MatchString(msg)
}

// --- шаблонизация ---

// Порядок важен: более специфичные шаблоны применяются первыми, иначе путь
// съел бы UUID внутри него, а кавычка — содержимое сообщения об ошибке.
var logTemplateRules = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`), "<uuid>"},
	{regexp.MustCompile(`\b0x[0-9a-fA-F]+\b`), "<hex>"},
	{regexp.MustCompile(`\bhttps?://\S+`), "<url>"},
	{regexp.MustCompile(`"(?:[^"\\]|\\.)*"`), `"<str>"`},
	{regexp.MustCompile(`'(?:[^'\\]|\\.)*'`), `'<str>'`},
	// Путь — любая непрерывная последовательность НЕПРОБЕЛЬНЫХ символов, в
	// которой есть «/». Такой шаблон покрывает и .github/workflows/ci.yml, и
	// node_modules/@scope/pkg, и /api/embeddings: обычное «\b\w+/\S+» рвало
	// их на части из-за ведущей точки и @, и одна проблема распадалась на
	// четыре находки вместо одной.
	{regexp.MustCompile(`\S*/\S*`), "<path>"},
	// Имя файла без каталога. Без этого правила пять РАЗНЫХ файлов (go.sum,
	// AGENTS.md, main.go, readme.md, …) с одной и той же ошибкой эмбеддинга
	// дают пять находок вместо одной — а это ровно тот случай, ради которого
	// дайджест и нужен. Расширение требует букв, поэтому версия 1.2.3 не
	// попадёт под шаблон (её разбирает правило чисел ниже).
	{regexp.MustCompile(`\b[\w\-]+\.[a-zA-Z]{1,5}\b`), "<file>"},
	{regexp.MustCompile(`\b\d+\b`), "<n>"},
}

// logTemplate абстрагирует переменные части сообщения: без этого пять
// десяти тысяч строк WARN, различающихся только путём к файлу, остаются
// пятьюдесятью тысячами находками вместо одной.
func logTemplate(msg string) string {
	out := msg
	for _, r := range logTemplateRules {
		out = r.re.ReplaceAllString(out, r.with)
	}
	return strings.TrimSpace(out)
}

// --- сборка дайджеста ---

// BuildLogDigest разбирает содержимое лог-файла и собирает сводку проблем.
// Берутся последние LogDigestWindow строк: старые записи не участвуют в
// разборе, зато стоимость не растёт с размером файла.
func BuildLogDigest(file, content string) LogDigest {
	// Хвостовой перевод строки дал бы в разбор пустой элемент, который
	// засчитывался бы в Lines/ProblemLines.
	content = strings.TrimRight(content, "\n")
	d := LogDigest{File: file, Findings: []LogFinding{}}
	if content == "" {
		return d
	}
	lines := strings.Split(content, "\n")
	if n := len(lines); n > LogDigestWindow {
		lines = lines[n-LogDigestWindow:]
	}
	d.Lines = len(lines)

	type group struct {
		f      LogFinding
		lastTS string
	}
	groups := map[string]*group{}
	// Порядок появления: при равной тяжести и частоте первое вхождение
	// важнее — оно обычно ближе к причине, а не к её последствиям.
	var order []string

	for _, raw := range lines {
		l := ParseLogLine(raw)
		// Кадр стека приклеиваем к последней находке: иначе трейс на 40
		// строк станет сорока находками, и ни одна не будет читаемой.
		// Проверяем ДО TrimSpace: кадр опознаётся по ведущему отступу.
		if IsLogStackFrame(l.Msg) {
			if len(order) > 0 {
				g := groups[order[len(order)-1]]
				if g != nil && g.f.Frame == "" {
					g.f.Frame = TruncateLogText(strings.TrimSpace(l.Msg), LogDigestSample)
				}
			}
			continue
		}
		level := l.Level
		if level == "" {
			level = LogHeuristicLevel(l.Msg)
		}
		msg := strings.TrimSpace(l.Msg)
		if msg == "" {
			continue
		}
		if !logProblemLevels[level] {
			continue
		}
		d.ProblemLines++
		tpl := logTemplate(msg)
		g := groups[tpl]
		if g == nil {
			g = &group{f: LogFinding{
				Level:    level,
				Template: tpl,
				Sample:   TruncateLogText(l.Raw, LogDigestSample),
				Count:    0,
				FirstTS:  l.TS,
			}}
			groups[tpl] = g
			order = append(order, tpl)
		}
		g.f.Count++
		if LevelRank(level) > LevelRank(g.f.Level) {
			g.f.Level = level
		}
		if l.TS != "" {
			g.f.LastTS = l.TS
			if g.f.FirstTS == "" {
				g.f.FirstTS = l.TS
			}
		}
	}

	for _, tpl := range order {
		d.Findings = append(d.Findings, groups[tpl].f)
	}
	sort.SliceStable(d.Findings, func(i, j int) bool {
		if ri, rj := LevelRank(d.Findings[i].Level), LevelRank(d.Findings[j].Level); ri != rj {
			return ri > rj
		}
		return d.Findings[i].Count > d.Findings[j].Count
	})
	if len(d.Findings) > LogDigestMax {
		d.Dropped = len(d.Findings) - LogDigestMax
		d.Findings = d.Findings[:LogDigestMax]
	}
	return d
}

// LevelRank — тяжесть уровня для сортировки (больше — хуже).
func LevelRank(level string) int {
	switch level {
	case "FATAL", "PANIC":
		return 4
	case "CRITICAL":
		return 3
	case "ERROR":
		return 2
	case "WARN":
		return 1
	}
	return 0
}

// TruncateLogText обрезает строку по границе символа и помечает многоточием.
func TruncateLogText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[:limit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimSpace(cut) + " …"
}

// --- фильтры выборки ---

// FilterLogLines оставляет строки, содержащие подстроку (регистронезависимо).
func FilterLogLines(content, needleLower string) string {
	var b strings.Builder
	for _, l := range strings.Split(content, "\n") {
		if strings.Contains(strings.ToLower(l), needleLower) {
			b.WriteString(l)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// FilterLogByLevel оставляет строки не легче уровня min. Разбор строки идёт
// тем же ParseLogLine/LogHeuristicLevel, что и при построении дайджеста, иначе
// фильтр и разбор смотрели бы на строку по-разному.
func FilterLogByLevel(content, min string) string {
	want := LevelRank(min)
	var b strings.Builder
	for _, raw := range strings.Split(content, "\n") {
		l := ParseLogLine(raw)
		if IsLogStackFrame(l.Msg) {
			// Кадры стека не имеют уровня, но нужны для чтения ошибки:
			// оставляем их, иначе вырезается контекст находки.
			b.WriteString(raw)
			b.WriteString("\n")
			continue
		}
		level := l.Level
		if level == "" {
			level = LogHeuristicLevel(l.Msg)
		}
		if LevelRank(level) >= want {
			b.WriteString(raw)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// MergeLogFindings склеивает находки с одинаковым шаблоном (сумма частот,
// худший уровень, самое раннее/позднее время), сортирует и обрезает до limit.
func MergeLogFindings(in []LogFinding, limit int) []LogFinding {
	idx := map[string]int{}
	out := []LogFinding{}
	for _, f := range in {
		key := f.Level + "|" + f.Template
		if i, ok := idx[key]; ok {
			cur := &out[i]
			cur.Count += f.Count
			if LevelRank(f.Level) > LevelRank(cur.Level) {
				cur.Level = f.Level
			}
			if f.FirstTS != "" && (cur.FirstTS == "" || f.FirstTS < cur.FirstTS) {
				cur.FirstTS = f.FirstTS
			}
			if f.LastTS != "" && f.LastTS > cur.LastTS {
				cur.LastTS = f.LastTS
			}
			if cur.Frame == "" {
				cur.Frame = f.Frame
			}
			continue
		}
		idx[key] = len(out)
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := LevelRank(out[i].Level), LevelRank(out[j].Level); ri != rj {
			return ri > rj
		}
		return out[i].Count > out[j].Count
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
