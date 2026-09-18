package planner

// Scope-гейт ЛСП по шагу (Ф-5): после успешного выполнения кодирующего шага
// исполнитель проверяет нативные диагностики (publishDiagnostics) строго по
// области работы шага. Найденные error-замечания означают, что субагент оставил
// код сломанным в своей области: шаг помечается упавшим, его scope откатывается,
// план останавливается — проблемы видны в момент появления, а не на приёмке.
//
// Сервер не установлен для стека или оригинальных файлов в scope нет — гейт не
// срабатывает (деградация, как у LspCheck). Управление: LSP_STEP_GATE.

import (
	"ai/logging"
	"ai/tools"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// stepLSPGateEnabled — активен ли scope-гейт ЛСП по шагу (LSP_STEP_GATE).
// По умолчанию включён; 0/false/off/no — выключить.
func stepLSPGateEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LSP_STEP_GATE"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// stepLSPFind — точка входа гейта к нативным диагностикам (подменяется в
// тестах). files — относительные пути от проекта. handled=false — сервера нет,
// гейт неактивен.
var stepLSPFind = func(projectDir string, files []string) ([]tools.LSPDiag, bool) {
	diags, _, handled := (&tools.FileOps{OutputDir: projectDir}).LSPDiagnostics(files)
	return diags, handled
}

// scopeSourceDirs — служебные директории, не участвующие в расширении scope
// до файлов (не дублируют политику tools, см. lspIgnoredDir в acceptor).
var scopeSourceDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, "target": true, "__pycache__": true, ".venv": true, "venv": true,
}

// scopeSourceExt — расширения исходников, участвующие в гейте.
var scopeSourceExt = map[string]bool{
	".go": true, ".js": true, ".jsx": true, ".ts": true, ".tsx": true,
	".py": true, ".pyi": true,
}

// maxScopeFiles — максимум файлов области шага для ЛСП-проверки (защита от
// тысячи файлов в широком scope).
const maxScopeFiles = 100

// scopeSourceFiles разворачивает scope шага (файл или директория, пути от
// проекта) в список существующих исходников. Директории сканируются без
// служебных подкаталогов; новые файлы (создаваемые шагом) в scope остаются —
// их существование проверит сам сервер. Ограничение — maxScopeFiles.
func scopeSourceFiles(projectDir string, scope []string, limit int) []string {
	seen := map[string]bool{}
	var out []string
	add := func(rel string) {
		if rel == "" || seen[rel] || len(out) >= limit {
			return
		}
		seen[rel] = true
		out = append(out, rel)
	}
	var walkDir func(abs, rel string)
	walkDir = func(abs, rel string) {
		_ = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if p != abs && scopeSourceDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if !scopeSourceExt[filepath.Ext(d.Name())] {
				return nil
			}
			r, rerr := filepath.Rel(projectDir, p)
			if rerr == nil {
				add(filepath.ToSlash(r))
			}
			if len(out) >= limit {
				return filepath.SkipAll
			}
			return nil
		})
	}
	for _, s := range scope {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		abs := filepath.Join(projectDir, filepath.FromSlash(s))
		info, err := os.Stat(abs)
		if err != nil {
			// Новый файл из plan ещё не создан — что ж, сервер/проверки не
			// найдут по нему данных, гейт просто не сработает.
			continue
		}
		if info.IsDir() {
			walkDir(abs, s)
		} else {
			if scopeSourceExt[filepath.Ext(s)] && filepath.ToSlash(s) != "" {
				add(filepath.ToSlash(s))
			}
		}
	}
	return out
}

// checkStepLSP выполняет scope-гейт шага: нативные ЛСП-диагностики строго по
// области работы. Ошибок нет (или нет данных) — nil; иначе возвращает ошибку,
// которую runOneStep трактует как падение шага (scope откатывается).
func (e *Executor) checkStepLSP(ctx context.Context, step *Step, projectDir string, scope []string) error {
	if !stepLSPGateEnabled() {
		return nil
	}
	files := scopeSourceFiles(projectDir, scope, maxScopeFiles)
	if len(files) == 0 {
		return nil
	}
	diags, handled := stepLSPFind(projectDir, files)
	if !handled {
		logging.Detailf("[%s] шаг %s: ЛСП-гейт неактивен (нет языкового сервера для стека)", agentLabel(step.Agent, step.Role), step.ID)
		return nil
	}

	var examples []string
	errs := 0
	warns := 0
	for _, d := range diags {
		if d.Severity != "error" {
			if d.Severity == "warning" {
				warns++
			}
			continue
		}
		errs++
		if len(examples) < 6 {
			examples = append(examples, fmt.Sprintf("%s:%d:%d: %s", d.File, d.Line, d.Col, d.Message))
		}
	}
	e.trackStepMetrics(context.TODO(), step.ID, map[string]any{"lsp_errors": errs, "lsp_warnings": warns})

	if errs == 0 {
		logging.Detailf("[%s] шаг %s: ЛСП по scope: замечаний-ошибок нет (предупреждений: %d)", agentLabel(step.Agent, step.Role), step.ID, warns)
		return nil
	}
	msg := fmt.Sprintf("шаг %q: ЛСП-замечания по scope шага: %d ошибок — субагент оставил код сломанным в своей области", step.ID, errs)
	if len(examples) > 0 {
		msg += ": " + strings.Join(examples, "; ")
	}
	logging.Warnf("[%s] %s", agentLabel(step.Agent, step.Role), msg)
	return fmt.Errorf("%s", msg)
}
