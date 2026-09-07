package acceptor

import (
	"ai/logging"
	"fmt"
	"strings"
)

// formatStatus возвращает короткую строку статуса проверки для логов.
func formatStatus(res *CheckResult) string {
	if res == nil {
		return "<нет>"
	}
	switch {
	case res.Skipped:
		return "пропущено"
	case res.OK:
		return "OK"
	default:
		return "нарушения"
	}
}

// runFormatCheck выполняет проверку стилизатора. Стилизаторы формата
// печатают список файлов и (как правило) выходят с 0 — признак нарушения это
// непустой вывод или ненулевой код. Нарушения — предупреждения: вердикт не
// меняют, но попадают в отчет и в план исправлений. Если инструмент не
// найден — проверка помечается как Skipped без ошибок.
func runFormatCheck(dir string, cfg Config, command, tool string) (*CheckResult, []Issue) {
	res := &CheckResult{Command: command, Tool: tool}
	if strings.TrimSpace(command) == "" {
		res.Skipped = true
		res.Output = fmt.Sprintf("стилизатор %s не найден — проверка пропущена", tool)
		return res, nil
	}

	out, code, timedOut, _ := runCommand(dir, command, cfg.BuildTimeout)
	res.Output = trimOutput(out, cfg.MaxLog)

	if isToolMissing(out, code) {
		res.Skipped = true
		res.Output = fmt.Sprintf("стилизатор %s недоступен в окружении — проверка пропущена", tool)
		return res, []Issue{
			{Stage: StageFormat, Severity: "warning", Text: fmt.Sprintf("стилизатор %s недоступен, проверка пропущена", tool)},
		}
	}
	if timedOut {
		res.Output += "\n[... превышен таймаут ...]"
		logging.Warnf("[Accept] стилизатор %q превысил таймаут", command)
		return res, []Issue{
			{Stage: StageFormat, Severity: "warning", Text: "проверка стилизатора превысила таймаут"},
		}
	}

	files := strings.TrimSpace(out)
	if code == 0 && files == "" {
		res.OK = true
		return res, nil
	}

	res.Output = trimOutput(out, cfg.MaxLog)
	var issues []Issue
	for _, line := range strings.Split(files, "\n") {
		f := strings.TrimSpace(line)
		if f == "" {
			continue
		}
		// Формат вывода инструментов разный: gofmt печатает только путь, black —
		// «would reformat file.py», prettier — путь, eslint в check-режиме иное.
		file, lineNo := locate(f)
		iss := Issue{Stage: StageFormat, Severity: "warning", Text: "не отформатировано (" + tool + ")"}
		if file == "" && lineNo == 0 {
			iss.Text = f
		} else {
			iss.File = file
			iss.Line = lineNo
		}
		issues = append(issues, iss)
	}
	if len(issues) == 0 {
		issues = append(issues, Issue{
			Stage:    StageFormat,
			Severity: "warning",
			Text:     fmt.Sprintf("есть нарушения форматирования (%s)", tool),
		})
	}
	return res, issues
}

// runAnalyzeCheck выполняет проверку анализатора. Находки — ошибки: при
// нарушении вердикт становится reject. Если инструмент не найден — проверка
// помечается как Skipped без ошибок.
func runAnalyzeCheck(dir string, cfg Config, command, tool string) (*CheckResult, []Issue) {
	res := &CheckResult{Command: command, Tool: tool}
	if strings.TrimSpace(command) == "" {
		res.Skipped = true
		res.Output = fmt.Sprintf("анализатор %s не найден — проверка пропущена", tool)
		return res, nil
	}

	out, code, timedOut, _ := runCommand(dir, command, cfg.BuildTimeout)
	res.Output = trimOutput(out, cfg.MaxLog)

	if isToolMissing(out, code) {
		res.Skipped = true
		res.Output = fmt.Sprintf("анализатор %s недоступен в окружении — проверка пропущена", tool)
		return res, []Issue{
			{Stage: StageAnalyze, Severity: "warning", Text: fmt.Sprintf("анализатор %s недоступен, проверка пропущена", tool)},
		}
	}

	issues, _ := analyzeOutput(StageAnalyze, out)
	if !timedOut && code == 0 && len(issues) == 0 {
		res.OK = true
		return res, nil
	}
	// Ненулевой код или нераспознанные маркерами строки: анализатор печатает
	// находки как «файл:строка:сообщение» — вытаскиваем их как ошибки.
	if len(issues) == 0 {
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(strings.TrimRight(line, "\r"))
			if line == "" {
				continue
			}
			file, lineNo := locate(line)
			issues = append(issues, Issue{
				Stage:    StageAnalyze,
				Severity: "error",
				File:     file,
				Line:     lineNo,
				Text:     stripDiagnosticLoc(line, file, lineNo),
			})
		}
	}
	if len(issues) == 0 {
		msg := "анализатор сообщил об ошибке"
		if timedOut {
			msg = "анализатор превысил таймаут"
		}
		issues = append(issues, Issue{Stage: StageAnalyze, Severity: "error", Text: msg})
	}
	return res, issues
}

// isToolMissing определяет, что команда была не найдена/недоступна в
// окружении, а не упала по делу. Такое мы не считаем ошибкой приёмки.
func isToolMissing(out string, code int) bool {
	if code == 127 {
		return true
	}
	low := strings.ToLower(out)
	return strings.Contains(low, "command not found") ||
		strings.Contains(low, "no module named") ||
		strings.Contains(low, "not recognized as an internal or external command")
}