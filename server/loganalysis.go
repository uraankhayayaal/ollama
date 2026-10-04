package server

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"ai/logging"
)

// Разбор логов по запросу панели логов (кнопка «Разобрать»).
//
// Поток целиком: панель отдаёт файл/поиск/уровень → сервер строит ДЕТЕРМИНИРОВАННЫЙ
// дайджест (buildLogDigest) → дайджест уходит в промпт ассистента → модель
// отвечает в чате и при необходимости докапывает находку инструментом
// ReadProjectLogs. Сырой лог целиком в промпт не идёт никогда.

// logAnalysisReq — запрос разбора из POST /api/projects/{id}/chat.
// Все поля опциональны: пустой запрос разбирает самые свежие логи проекта.
type logAnalysisReq struct {
	// File — имя файла лога (например calc.log). Пусто — самые свежие.
	File string `json:"file"`
	// Level — минимальная тяжесть: WARN или ERROR. Пусто — любые находки.
	Level string `json:"level"`
	// Query — подстрока-фильтр строк лога (из поиска панели).
	Query string `json:"query"`
}

// logAnalysis — подготовленный разбор для промпта.
type logAnalysis struct {
	Files  []string // какие файлы разобраны (для заголовка)
	Digest logDigest
	// Note — человекочитаемые условия выборки (поиск/уровень) для заголовка в
	// промпте. Модель должна знать, что она видит подмножество, а не весь лог.
	Note string
}

// FilesLabel — заголовок разбора: один файл или перечень.
func (a logAnalysis) FilesLabel() string { return filesLabel(a.Files) }

// buildLogAnalysis готовит разбор: собирает логи проекта ровно теми же
// правилами, что и Logboard (collectProjectLogs), фильтрует строки запросом
// и уровнем, после чего строит дайджест.
//
// Фильтр уровня — «не легче, чем запрошено»: при level=ERROR в разбор не
// попадают WARN, иначе кнопка «покажи ошибки» приносила бы тонну
// предупреждений. Фильтр применяется к сырым строкам ДО группировки, чтобы
// счётчики в находках отражали именно то, что запросили.
func (sess *Session) buildLogAnalysis(req logAnalysisReq) (logAnalysis, error) {
	inf, err := sess.srv.reg.Get(sess.project)
	if err != nil {
		return logAnalysis{}, fmt.Errorf("проект %s не найден", sess.project)
	}
	globalDir := sess.srv.logsDir()
	own := logging.ProjectLogName(sess.project) + ".log"
	files, _ := collectProjectLogs(sess.srv.logDirs(inf.Root), globalDir, own)

	sel := pickLogFiles(files, strings.TrimSpace(req.File), own)
	if len(sel) == 0 {
		return logAnalysis{}, fmt.Errorf("лог %q не найден среди файлов проекта", req.File)
	}

	minLevel := ""
	switch strings.ToUpper(strings.TrimSpace(req.Level)) {
	case "ERROR", "ERR":
		minLevel = "ERROR"
	case "WARN", "WARNING":
		minLevel = "WARN"
	case "FATAL", "CRITICAL":
		minLevel = "FATAL"
	}
	query := strings.ToLower(strings.TrimSpace(req.Query))

	merged := logDigest{Findings: []logFinding{}}
	for _, f := range sel {
		body, err := os.ReadFile(f.Path)
		if err != nil {
			return logAnalysis{}, fmt.Errorf("чтение лога %s: %w", f.Name, err)
		}
		content := string(body)
		if query != "" {
			content = filterLogLines(content, query)
		}
		if minLevel != "" {
			content = filterLogByLevel(content, minLevel)
		}
		d := buildLogDigest(f.Name, content)
		merged.Lines += d.Lines
		merged.ProblemLines += d.ProblemLines
		merged.Dropped += d.Dropped
		// Шаблоны склеиваем по файлу: одинаковая ошибка в двух логах — одна
		// находка с суммой частот, а не две.
		merged.Findings = append(merged.Findings, d.Findings...)
	}
	merged.Findings = mergeLogFindings(merged.Findings, logDigestMax)

	names := make([]string, 0, len(sel))
	for _, f := range sel {
		names = append(names, f.Name)
	}
	merged.File = filesLabel(names)

	note := ""
	if query != "" {
		note = "только строки, содержащие «" + strings.TrimSpace(req.Query) + "»"
	}
	if minLevel != "" {
		if note != "" {
			note += "; "
		}
		note += "только уровни не легче " + minLevel
	}

	return logAnalysis{Files: names, Digest: merged, Note: note}, nil
}

// key — ключ разбора для cooldown: файл, его размер/время и фильтры. Меняется
// при любом изменении лога, поэтому «тот же лог» действительно тот же.
func (a logAnalysis) key() string {
	fs := ""
	if len(a.Digest.Findings) > 0 {
		fs = fmt.Sprintf("%s|%d|%s|%s", a.Digest.File, a.Digest.Lines, a.Digest.Findings[0].FirstTS, a.Digest.Findings[0].LastTS)
	}
	return strings.Join(a.Files, ",") + "|" + a.Note + "|" + fs
}

// markAnalyzed запоминает разбор. recent возвращает true, если такой же ключ
// разбирали недавно (в пределах cooldown).
func (sess *Session) markAnalyzed(k string) (recent bool) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	now := time.Now()
	if sess.logLastKey == k && now.Sub(sess.logLastAt) < logAnalysisCooldown {
		return true
	}
	sess.logLastKey, sess.logLastAt = k, now
	return false
}

// logAnalysisCooldown — окно, в котором повторный разбор того же лога не
// запускает модель заново. Кнопку легко кликнуть дважды, а каждый разбор —
// полный ход ассистента с промптом и инструментами.
const logAnalysisCooldown = 90 * time.Second

// filesLabel — заголовок перечня файлов для промпта и логов.
func filesLabel(files []string) string {
	switch len(files) {
	case 0:
		return "(файлы не выбраны)"
	case 1:
		return files[0]
	default:
		return strings.Join(files, ", ")
	}
}

// filterLogLines оставляет строки, содержащие подстроку (регистронезависимо).
func filterLogLines(content, needleLower string) string {
	var b strings.Builder
	for _, l := range strings.Split(content, "\n") {
		if strings.Contains(strings.ToLower(l), needleLower) {
			b.WriteString(l)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// filterLogByLevel оставляет строки не легче уровня min. Разбор строки идёт
// тем же parseLogLine/logHeuristicLevel, что и при построении дайджеста, иначе
// фильтр и разбор смотрели бы на строку по-разному.
func filterLogByLevel(content, min string) string {
	want := levelRank(min)
	var b strings.Builder
	for _, raw := range strings.Split(content, "\n") {
		l := parseLogLine(raw)
		if isLogStackFrame(l.Msg) {
			// Кадры стека не имеют уровня, но нужны для чтения ошибки:
			// оставляем их, иначе вырезается контекст находки.
			b.WriteString(raw)
			b.WriteString("\n")
			continue
		}
		level := l.Level
		if level == "" {
			level = logHeuristicLevel(l.Msg)
		}
		if levelRank(level) >= want {
			b.WriteString(raw)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// mergeLogFindings склеивает находки с одинаковым шаблоном (сумма частот,
// худший уровень, самое раннее/позднее время), сортирует и обрезает до limit.
func mergeLogFindings(in []logFinding, limit int) []logFinding {
	idx := map[string]int{}
	out := []logFinding{}
	for _, f := range in {
		key := f.Level + "|" + f.Template
		if i, ok := idx[key]; ok {
			cur := &out[i]
			cur.Count += f.Count
			if levelRank(f.Level) > levelRank(cur.Level) {
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
		if ri, rj := levelRank(out[i].Level), levelRank(out[j].Level); ri != rj {
			return ri > rj
		}
		return out[i].Count > out[j].Count
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// logDigestPrompt — блок промпта с дайджестом и контрактом ответа.
//
// Контракт задан явно, потому что без него модель по сводке находок пишет
// пересказ лога вместо разбора: без «цитаты-доказательства» и «гипотезы
// причины» она ограничивается «в логах есть ошибки».
func logDigestPrompt(a logAnalysis) string {
	var b strings.Builder
	b.WriteString("\n\n=== РАЗБОР ЛОГА ПРОЕКТА ===\n")
	fmt.Fprintf(&b, "Пользователь нажал «Разобрать» в панели логов. Файл(ы): %s.\n", a.FilesLabel())
	fmt.Fprintf(&b, "Сводка подготовлена сервером детерминированно: разобрано строк %d, из них с признаком проблемы %d, сгруппировано в находки %d (частота — сколько раз встречался один и тот же шаблон; <path>/<file>/<n>/<uuid>/<str> — абстракции, конкретные значения есть в примере).\n",
		a.Digest.Lines, a.Digest.ProblemLines, len(a.Digest.Findings))
	if a.Note != "" {
		fmt.Fprintf(&b, "Условия выборки: %s.\n", a.Note)
	}
	if len(a.Digest.Findings) == 0 {
		b.WriteString("Находок нет: в выборке нет ни одной строки уровня WARN/ERROR/FATAL.\n")
		b.WriteString("Ответь коротко: критичных проблем в этом логе не найдено, объясни, что смотрел, и предложи, что проверить дальше.\n")
		return b.String()
	}
	b.WriteString("\nНаходки (по убыванию тяжести и частоты):\n")
	b.WriteString(a.Digest.Text())
	b.WriteString(`
Твой ответ (в чат, по-русски):
1. Возьми 5–7 самых значимых находок. Для каждой: что случилось, частота и тяжесть, ЦИТАТА-доказательство из примера, гипотеза причины (явно назови её гипотезой, если не уверен), является ли это шумом.
2. Отдельно скажи, что выглядит как первопричина остального.
3. Дай конкретные следующие шаги (что проверить, поправить, где смотреть).
4. Если по находкам ничего критичного нет — прямо скажи это, не выдумывай проблему.
Полный лог не читай целиком: он большой. Точечно докапывай находки инструментом ReadProjectLogs (grep по подстроке) — и только то, что реально нужно для вывода.
Отвечай сразу по этому разбору, не переспрашивай, что именно разобрать.
`)
	return b.String()
}
