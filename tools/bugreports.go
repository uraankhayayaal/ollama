package tools

import (
	"regexp"
	"strconv"
	"strings"
)

// BugReport — багрепорт, который QA-инженер публикует по результатам тестов
// в исполнителе плана (planner). Используется для обратной связи тестировщиков
// «в том же эпике»: после исправлений QA перепроверяет, и если дефект исчез —
// репорт считается закрытым.
type BugReport struct {
	// FilePath — путь к файлу, к которому относится дефект.
	FilePath string `json:"file_path"`
	// Line — номер строки (необязателен).
	Line int `json:"line"`
	// Severity — серьёзность: blocker/major/minor (необязательно).
	Severity string `json:"severity"`
	// Text — текст багрепорта: шаги воспроизведения, ожидаемый и фактический
	// результат по контракту.
	Text string `json:"text"`
}

// bugCallStartRe находит начало псевдо-вызова BugReport — формат, которым
// QA-инженер публикует дефекты в текстовом ответе, когда реальных
// инструментов доски у него нет (исполнитель плана работает без Kanban-доски).
var bugCallStartRe = regexp.MustCompile(`(?i)BugReport\s*\(`)

// bugArgsRe находит названные аргументы вызова: file/line/severity/text.
var (
	bugFilePathRe = regexp.MustCompile(`(?i)\bfile\s*=\s*([^,]+)`)
	bugLineRe     = regexp.MustCompile(`(?i)\bline\s*=\s*(\d+)`)
	bugSeverityRe = regexp.MustCompile(`(?i)\bseverity\s*=\s*([^,]+)`)
	bugTextRe     = regexp.MustCompile(`(?i)\btext\s*=\s*(.+)$`)
)

// parseBugArgs разбирает аргументы одного вызова BugReport в багрепорт.
// Поддерживаются названные аргументы (file=, line=, severity=, text=) и
// позиционные (file, line, text).
func parseBugArgs(args string) (BugReport, bool) {
	s := strings.TrimSpace(args)
	if s == "" {
		return BugReport{}, false
	}

	var bug BugReport

	if bugFilePathRe.MatchString(s) {
		// Названный формат: file=..., line=..., severity=..., text=...
		if m := bugFilePathRe.FindStringSubmatch(s); len(m) > 1 {
			if v, ok := stripOuterQuotes(m[1]); ok {
				bug.FilePath = v
			}
		}
		if m := bugLineRe.FindStringSubmatch(s); len(m) > 1 {
			bug.Line, _ = strconv.Atoi(m[1])
		}
		if m := bugSeverityRe.FindStringSubmatch(s); len(m) > 1 {
			bug.Severity, _ = stripOuterQuotes(m[1])
		}
		if m := bugTextRe.FindStringSubmatch(s); len(m) > 1 {
			bug.Text, _ = stripOuterQuotes(m[1])
		}
	} else {
		// Позиционный формат: "file", line, "text".
		rest := s
		path, after := readToken(rest)
		if path == "" || !findComma(after) {
			return BugReport{}, false
		}
		bug.FilePath = path
		rest = strings.TrimSpace(after[findCommaIdx(after)+1:])

		tok, tail := readToken(rest)
		line, err := strconv.Atoi(strings.TrimPrefix(tok, "~"))
		if err != nil || !findComma(tail) {
			return BugReport{}, false
		}
		bug.Line = line
		rest = strings.TrimSpace(tail[findCommaIdx(tail)+1:])

		bug.Text, _ = stripOuterQuotes(rest)
	}

	if strings.TrimSpace(bug.FilePath) == "" || strings.TrimSpace(bug.Text) == "" {
		return BugReport{}, false
	}
	bug.FilePath = strings.TrimSpace(strings.TrimPrefix(bug.FilePath, "./"))
	return bug, true
}

// ParseBugReports извлекает багрепорты из псевдо-вызовов BugReport(...) в
// текстовом ответе QA-инженера. Если вызовов не найдено — возвращает nil.
func ParseBugReports(content string) []BugReport {
	locs := bugCallStartRe.FindAllStringSubmatchIndex(content, -1)
	if len(locs) == 0 {
		return nil
	}

	var bugs []BugReport
	for _, loc := range locs {
		// loc[0] — начало "BugReport", loc[1] — сразу после "(".
		args, ok := scanParenArgs(content, loc[1]-1)
		if !ok {
			continue
		}
		if b, ok := parseBugArgs(args); ok {
			bugs = append(bugs, b)
		}
	}
	if len(bugs) == 0 {
		return nil
	}
	return bugs
}
