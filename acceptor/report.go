package acceptor

import (
	"ai/domain"
	"fmt"
	"regexp"
	"strings"
)

// Типы и константы отчёта приёмки канонически объявлены в domain
// (domain/acceptance.go); здесь — алиасы, чтобы инфраструктурные пакеты и
// существующие вызовы (acceptor.Report, acceptor.VerdictApprove) сохраняли
// прежнюю форму.
type (
	Verdict       = domain.Verdict
	Stage         = domain.Stage
	Report        = domain.Report
	BuildResult   = domain.BuildResult
	RunResult     = domain.RunResult
	InstallResult = domain.InstallResult
	CheckResult   = domain.CheckResult
	Issue         = domain.Issue
)

const (
	VerdictApprove = domain.VerdictApprove
	VerdictReject  = domain.VerdictReject

	StageBuild   = domain.StageBuild
	StageRun     = domain.StageRun
	StageFormat  = domain.StageFormat
	StageAnalyze = domain.StageAnalyze
	StageInstall = domain.StageInstall
	StageConfig  = domain.StageConfig
)

// критичные маркеры: присутствие любого из них в логе — признак падения
// приложения, даже если процесс завершился нулевым кодом.
var criticalMarkers = []string{
	"panic:",
	"fatal error:",
	"runtime error:",
	"traceback (most recent call last)",
	"segmentation fault",
	"out of memory",
	"cannot find module",
	"cannot find package",
	"module not found",
	"crashed",
	"unhandled promise rejection",
}

// errorMarkers — маркеры ошибок сборки/запуска для извлечения issues.
// Сопоставляются подстрокой (регистронезависимо), без учёта служебных
// «failed» в обычных логах (например, «0 failed»).
var errorMarkers = []string{
	"error",
	"Error:",
	"Exception",
	"undefined:",
	"cannot use",
	"cannot import",
	"declared but not used",
	"not enough arguments",
	"too many arguments",
	"syntax error",
	"syntaxerror",
	"typeerror",
	"referenceerror",
	"importerror",
	"modulenotfounderror",
	"nameerror",
	"attributeerror",
	"keyerror",
	"valueerror",
	"indentationerror",
	"file not found",
	"permission denied",
	"connection refused",
	"build failed",
}

// locateRe извлекает «файл:строка» из строки ошибки (Go, Node, Python и т.п.).
var locateRe = regexp.MustCompile(`([A-Za-z0-9_./\\ -]+\.(?:go|js|mjs|ts|py|rs|c|h|cpp|java|rb|php)):(\d+)(?::(\d+))?`)

// analyzeOutput возвращает замечания по выводу этапа (сборка/запуск) и
// признак наличия критичных маркеров в тексте.
func analyzeOutput(stage Stage, output string) (issues []Issue, critical bool) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)

		isCritical := containsAny(lower, criticalMarkers)
		isError := isCritical || containsAny(lower, errorMarkers)

		file, lineNo := locate(line)
		sev := "warning"
		if isCritical {
			sev = "error"
		} else if isError {
			// Ошибки сборки почти всегда фатальны; находки анализатора — это
			// реальные дефекты, которые должны вести к правке; ошибки в логе
			// запуска фиксируем, но вердикт решает код выхода/критичные
			// маркеры.
			if stage == StageBuild || stage == StageAnalyze {
				sev = "error"
			}
		}

		if isCritical {
			critical = true
		}

		if isError {
			issues = append(issues, Issue{
				Stage:    stage,
				Severity: sev,
				File:     file,
				Line:     lineNo,
				Text:     stripDiagnosticLoc(strings.TrimSpace(line), file, lineNo),
			})
		}
	}
	return issues, critical
}

// containsAny проверяет, содержит ли lowercase-строка хотя бы один маркер.
func containsAny(lower string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// stripDiagnosticLoc убирает из строки диагностики префикс «файл:строка[:колонка]: »,
// чтобы текст замечания не дублировал адресацию (go vet: «main.go:6:14: msg»).
func stripDiagnosticLoc(line, file string, lineNo int) string {
	if file == "" || lineNo == 0 {
		return line
	}
	if strings.HasPrefix(line, file+":") {
		if i := strings.Index(line, ": "); i >= 0 {
			return strings.TrimSpace(line[i+2:])
		}
	}
	return line
}

// locate вытаскивает файл и номер строки из строки ошибки.
func locate(line string) (string, int) {
	m := locateRe.FindStringSubmatch(line)
	if m == nil {
		return "", 0
	}
	var n int
	fmt.Sscanf(m[2], "%d", &n)
	return filepathToSlash(m[1]), n
}

func filepathToSlash(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}
