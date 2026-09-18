package acceptor

// Точечная ЛСП-диагностика приёмки (Ф-5): результаты «огранки» по стеку через
// языковой сервер (native publishDiagnostics). В отличие от вывода сборки,
// ЛСП даёт точные файл:строка:колонка — планировщик исправлений получает
// точечный scope для фикс-шагов (см. Report.IssueFiles/FixPrompt).

import (
	"ai/tools"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// lspIgnoredDir — служебные директории, не участвующие в ЛСП-сканировании
// проекта (дублирует политику tools, не импортируемую сюда во избежание цикла).
var lspIgnoredDir = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, "target": true, "__pycache__": true, ".venv": true, "venv": true,
}

// lspSourceExt — расширения исходников, попадающие в ЛСП-сканирование.
var lspSourceExt = map[string]bool{
	".go": true, ".js": true, ".jsx": true, ".ts": true, ".tsx": true,
	".py": true, ".pyi": true,
}

// maxLSPFiles — максимум исходников, которые открываются в языковом сервере
// за одну ЛСП-диагностику (защита от тысяч файлов в монорепозитории).
const maxLSPFiles = 100

// projectSourceFiles собирает исходники проекта (относительные пути), кроме
// служебных директорий. Ограничено maxLSPFiles.
func projectSourceFiles(dir string, limit int) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != dir && lspIgnoredDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !lspSourceExt[filepath.Ext(d.Name())] {
			return nil
		}
		if len(out) >= limit {
			return filepath.SkipAll
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr == nil {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

// acceptorLSPDiags — точка входа к нативным диагностикам (подменяется в
// тестах). handled=false — сервера нет, проверка пропускается.
var acceptorLSPDiags = func(dir string, files []string) ([]tools.LSPDiag, string, bool) {
	return (&tools.FileOps{OutputDir: dir}).LSPDiagnostics(files)
}

// runLSPCheck выполняет точечную ЛСП-диагностику проекта: нативные
// publishDiagnostics языкового сервера по исходникам проекта. Замечания — как
// у анализатора: error ведёт к reject (обрабатывает acceptOne). Сервер не
// установлен для стека — проверка пропускается без ошибок (деградация).
func runLSPCheck(dir string) (*CheckResult, []Issue) {
	res := &CheckResult{Command: "LspCheck", Tool: "lsp"}
	files := projectSourceFiles(dir, maxLSPFiles)
	if len(files) == 0 {
		res.Skipped = true
		res.Output = "исходников для ЛСП-диагностики не найдено — проверка пропущена"
		return res, nil
	}

	diags, _, handled := acceptorLSPDiags(dir, files)
	if !handled {
		res.Skipped = true
		res.Output = "языковой сервер для стека проекта не найден — проверка пропущена (LSP_NATIVE=1, установка серверов — readme)"
		return res, []Issue{
			{Stage: StageAnalyze, Severity: "warning", Text: "ЛСП-диагностика не выполнена: языковой сервер не найден, проверка пропущена"},
		}
	}

	return lspDiagsToIssues(diags)
}

// lspDiagsToIssues превращает ЛСП-диагностики в analyze-замечания приёмки.
// res.OK = false только при error-замечаниях; предупреждения в отчёт попадают,
// но вердикт не меняют (как у анализатора).
func lspDiagsToIssues(diags []tools.LSPDiag) (*CheckResult, []Issue) {
	res := &CheckResult{Command: "LspCheck", Tool: "lsp"}
	if len(diags) == 0 {
		res.OK = true
		res.Output = "замечаний не обнаружено"
		return res, nil
	}
	issues := make([]Issue, 0, len(diags))
	hasErr := false
	for _, d := range diags {
		if d.Severity == "error" {
			hasErr = true
		}
		issues = append(issues, Issue{
			Stage:    StageAnalyze,
			Severity: d.Severity,
			File:     d.File,
			Line:     d.Line,
			Text:     strings.TrimSpace(d.Message),
		})
	}
	res.OK = !hasErr
	res.Output = fmt.Sprintf("ЛСП-замечаний: %d", len(diags))
	return res, issues
}
