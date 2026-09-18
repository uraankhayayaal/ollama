package tools

// Инструмент LspCheck — точечная диагностика кода «глазами IDE».
//
// Ф-1: оборачивает однократные CLI-чекеры по стеку проекта (gopls / tsc /
// pyright / ruff) через общий runCommand и возвращает модели ТОЛЬКО строки
// ошибок (файл:строка:колонка:описание), а не сырые логи сборки (токено-
// эффективный вывод, см. PLAN-lsp.md). Если подходящий чекер не установлен —
// graceful degrade: статус skipped с подсказкой использовать Run, шаг не падает.
//
// Параметры: files (optional) — список относительных путей для точечной
// проверки; пусто — проверка проекта (подпроекта) целиком.

import (
	"ai/stackdetect"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// LspCheck — имя инструмента в реестре (см. registry.go newTool).
const LspCheck = "LspCheck"

// lspCheckTool — обёртка инструмента LspCheck в реестре.
type lspCheckTool struct{ ops *FileOps }

func (t *lspCheckTool) Name() string { return LspCheck }
func (t *lspCheckTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name: LspCheck,
		Description: "Используй этот инструмент, когда сборка/проверки упали, чтобы получить ТОЧНЫЕ строки ошибок компиляции или типов " +
			"(файл:строка:колонка:описание) по стеку проекта: gopls для Go, tsc для TypeScript/JavaScript, pyright/ruff для Python. " +
			"Это компактная диагностика без сырых логов — экономнее, чем парсить вывод Run. Вернёт массив diagnostics; если чекер " +
			"не установлен — status skipped и подсказка использовать Run.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":        "array",
					"description": "Опционально: список относительных путей для точечной проверки (например ['server/main.go']). Пусто/не задано — проверка проекта целиком.",
					"items":       map[string]any{"type": "string"},
				},
			},
			"required":             []string{},
			"additionalProperties": false,
		},
	}
}
func (t *lspCheckTool) Execute(args map[string]any) ([]byte, error) { return t.ops.LspCheck(args) }

// lspDiagnostic — одна строка диагностики, возвращаемая модели.
type lspDiagnostic struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Severity string `json:"severity"` // error | warning | info
	Message  string `json:"message"`
}

// LspCheckParams — JSON-параметры инструмента LspCheck.
type LspCheckParams struct {
	Files []string `json:"files"`
}

// lspLimit — лимиты вывода (LSP_MAX_DIAGS / LSP_MAX_OUTPUT). См. PLAN-lsp.md.
type lspLimit struct{ maxDiags, maxOutput int }

func lspLimits() lspLimit {
	l := lspLimit{maxDiags: 30, maxOutput: 4000}
	if v := strings.TrimSpace(os.Getenv("LSP_MAX_DIAGS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			l.maxDiags = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("LSP_MAX_OUTPUT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			l.maxOutput = n
		}
	}
	return l
}

// LspCheck выполняет точечную диагностику проекта. Не модифицирует файлы
// (через общий runCommand; процессы гарантированно завершаются по таймауту).
func (ops *FileOps) LspCheck(args map[string]any) ([]byte, error) {
	var p LspCheckParams
	if raw, err := json.Marshal(args); err == nil {
		_ = json.Unmarshal(raw, &p)
	}

	dir := ops.OutputDir
	if dir == "" {
		return lspJSON(map[string]any{"status": "error", "message": "OutputDir не задан"}), nil
	}

	dir = filepath.ToSlash(filepath.Clean(dir))
	files := cleanLSPFiles(p.Files)

	proj, stack := lspProject(dir, files)
	if proj == "" {
		return lspJSON(map[string]any{
			"status":      "skipped",
			"checker":     "",
			"diagnostics": []lspDiagnostic{},
			"message":     "не удалось определить стек проекта (нет go.mod/package.json/requirements.txt в корне и подпроектах) — используй Run: go build/vet, npm run build или свой чекер по стеку",
		}), nil
	}

	cmd, checker, err := lspCheckerCommand(stack, proj, files)
	if err != nil {
		return lspJSON(map[string]any{
			"status":      "skipped",
			"checker":     checker,
			"diagnostics": []lspDiagnostic{},
			"message":     err.Error(),
		}), nil
	}

	rel := ""
	if proj != dir {
		if r, rerr := filepath.Rel(dir, proj); rerr == nil {
			rel = filepath.ToSlash(r)
		}
	}

	res, runErr := runCommand(cmd, proj)
	if runErr != nil {
		return lspJSON(map[string]any{
			"status":      "error",
			"checker":     checker,
			"diagnostics": []lspDiagnostic{},
			"message":     "команда не запустилась: " + runErr.Error(),
		}), nil
	}

	output := strings.TrimSpace(res["stdout"] + "\n" + res["stderr"])
	ds := parseLSPOutput(stack, output, dir)
	ds = sortDedupLSP(ds)

	limits := lspLimits()
	truncated := 0
	if len(ds) > limits.maxDiags {
		truncated = len(ds) - limits.maxDiags
		ds = ds[:limits.maxDiags]
	}

	result := map[string]any{
		"status":      "success",
		"checker":     checker,
		"diagnostics": ds,
	}
	if rel != "" {
		result["project"] = rel
	}
	if len(files) > 0 {
		result["files_checked"] = files
	}
	if truncated > 0 {
		result["truncated"] = truncated
	}

	// Символьный лимит суммарного ответа: при переполнении отсекаем хвост
	// диагностик до тех пор, пока JSON-вывод не влезет в лимит.
	if out, _ := json.Marshal(result); len(out) > limits.maxOutput {
		for len(ds) > 0 {
			ds = ds[:len(ds)-1]
			truncated++
			result["diagnostics"] = ds
			result["truncated"] = truncated
			out, _ = json.Marshal(result)
			if len(out) <= limits.maxOutput {
				break
			}
		}
	}
	if len(ds) == 0 && res["status"] == "error" {
		// Команда упала, но точечных строк выдать не удалось (шумы, сборка
		// модуля и т.п.) — честно отдаём шапку вывода с подсказкой.
		msg := strings.TrimSpace(output)
		if len(msg) > 400 {
			msg = msg[:400] + "…"
		}
		return lspJSON(map[string]any{
			"status":      "error",
			"checker":     checker,
			"diagnostics": []lspDiagnostic{},
			"message":     "чекер завершился с ошибкой, точечных строк не получено: «" + msg + "». Используй Run для полного вывода",
		}), nil
	}
	return json.Marshal(result)
}

// lspJSON сериализует результат инструмента (все ветки возвращают JSON, как
// принято в файловых инструментах).
func lspJSON(v map[string]any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// cleanLSPFiles нормализует относительные пути файлов: slash, без "./", без
// абсолютных/выходящих за OutputDir путей (они будут отфильтрованы).
func cleanLSPFiles(files []string) []string {
	var out []string
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		f = filepath.ToSlash(filepath.Clean(f))
		if strings.HasPrefix(f, "../") || strings.HasPrefix(f, "/") || strings.HasSuffix(f, "/.") {
			continue
		}
		// Пробелы внутри пути (результат «склейки» токенов моделью) — мусор.
		if strings.ContainsAny(f, " \t") {
			continue
		}
		out = append(out, f)
	}
	return out
}

// lspProject находит директорию (проект/подпроект), в которой запускать чекер:
//   - если корень — проект (есть маркер стека) — корень;
//   - иначе ищем прямые подкаталоги с маркерами (монорепозиторий): при заданных
//     files выбираем подкаталог-префикс первого файла, иначе если подпроект
//     ровно один — его; при неоднозначности возвращаем "" (degrade).
func lspProject(dir string, files []string) (proj string, stack stackdetect.Kind) {
	if stack := stackdetect.DetectKind(dir); stack != stackdetect.KindUnknown {
		return dir, stack
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", stackdetect.KindUnknown
	}
	var subs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || isLSPIgnoredDir(name) {
			continue
		}
		sub := filepath.Join(dir, name)
		if stackdetect.DetectKind(sub) != stackdetect.KindUnknown {
			subs = append(subs, filepath.ToSlash(filepath.Clean(sub)))
		}
	}
	if len(subs) == 0 {
		return "", stackdetect.KindUnknown
	}
	if len(subs) == 1 {
		return subs[0], stackdetect.DetectKind(subs[0])
	}
	// Несколько подпроектов: выбрать по префиксу первого запрошенного файла.
	for _, f := range files {
		for _, s := range subs {
			rel, rerr := filepath.Rel(dir, s)
			if rerr != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			if f == rel || strings.HasPrefix(f, rel+"/") {
				return s, stackdetect.DetectKind(s)
			}
		}
	}
	return "", stackdetect.KindUnknown
}

// isLSPIgnoredDir — служебные директории, не считающиеся подпроектами.
func isLSPIgnoredDir(name string) bool {
	switch name {
	case "vendor", "node_modules", "dist", "build", ".git", "target", "__pycache__", ".venv", "venv":
		return true
	}
	return false
}

// lspCheckerCommand выбирает команду и имя чекера по стеку проекта. Каскад
// фолбэков идёт по убыванию точности; если ни одного бинаря нет — ошибка со
// статусом skipped (degrade: подсказка использовать Run).
func lspCheckerCommand(stack stackdetect.Kind, dir string, files []string) (cmd, checker string, err error) {
	switch stack {
	case stackdetect.KindGo:
		if commandAvailable("gopls") {
			args := append([]string{"gopls", "check"}, relArgs(dir, files)...)
			return strings.Join(args, " "), "gopls", nil
		}
		// go vet — фолбэк: диагностики в том же формате file:line:col: message.
		return "go vet ./...", "go vet", nil
	case stackdetect.KindNode:
		// Локальный tsc (node_modules/.bin) или tsc в PATH. npx не используем:
		// он качает пакет из сети, что недопустимо для рабочего прогона.
		// Точечный tsc осмыслен при заданных files или наличии tsconfig.json.
		if hasTSInput(dir, files) {
			if stackdetect.HasFile(dir, "node_modules/.bin/tsc") {
				return lspTscCommand("./node_modules/.bin/tsc", relArgs(dir, files)), "tsc", nil
			}
			if commandAvailable("tsc") {
				args := append([]string{"tsc", "--noEmit", "--pretty", "false"}, relArgs(dir, files)...)
				return strings.Join(args, " "), "tsc", nil
			}
		}
		if nodeHasBuildScript(dir) {
			return "npm run build", "npm run build", nil
		}
		return "", "", fmt.Errorf("не найден tsc (ни в node_modules/.bin, ни в PATH) и нет build-скрипта — используй Run: npm run build")
	case stackdetect.KindPython:
		if commandAvailable("pyright") {
			args := append([]string{"pyright", "--outputjson"}, relArgs(dir, files)...)
			return strings.Join(args, " "), "pyright", nil
		}
		if commandAvailable("ruff") {
			return "ruff check .", "ruff", nil
		}
		return "", "", fmt.Errorf("не найден ни pyright, ни ruff — используй Run: python3 -m compileall или свой чекер")
	default:
		return "", "", fmt.Errorf("стек не определён")
	}
}

// hasTSInput решает, осмысленно ли запускать tsc: точечные файлы заданы или
// есть tsconfig.json для проектной проверки.
func hasTSInput(dir string, files []string) bool {
	if len(files) > 0 {
		return true
	}
	return stackdetect.HasFile(dir, "tsconfig.json")
}

func nodeHasBuildScript(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pj struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &pj) != nil {
		return false
	}
	return pj.Scripts["build"] != ""
}

// lspTscCommand собирает команду локального tsc (без relative-подстановки —
// процесс уже работает в подпроекте).
func lspTscCommand(bin string, files []string) string {
	args := []string{bin, "--noEmit", "--pretty", "false"}
	args = append(args, files...)
	return strings.Join(args, " ")
}

// relArgs приводит относительные пути файлов к виду для запуска из dir
// (если dir глубже корня OutputDir — у файлов убираем префикс подпроекта).
func relArgs(dir string, files []string) []string {
	dirSl := filepath.ToSlash(dir)
	var out []string
	for _, f := range files {
		if strings.HasPrefix(f, dirSl+"/") {
			f = strings.TrimPrefix(f, dirSl+"/")
		}
		out = append(out, f)
	}
	return out
}

// commandAvailable проверяет наличие команды в PATH.
func commandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// parseLSPOutput разбирает вывод чекера в зависимости от стека.
func parseLSPOutput(stack stackdetect.Kind, output, outputDir string) []lspDiagnostic {
	switch stack {
	case stackdetect.KindPython:
		if strings.Contains(output, "\"generalDiagnostics\"") {
			return parsePyrightJSON(output, outputDir)
		}
		return parseFileColonLine(output, outputDir)
	default:
		return parseFileColonLine(output, outputDir)
	}
}

var (
	reFileColon = regexp.MustCompile(`^(.+?):(\d+):(\d+):\s*(.*)$`)
	reTscFile   = regexp.MustCompile(`^(.+?)\((\d+)(?:,(\d+))?\):\s*(?:(error|warning)\s+)?(.*)$`)
)

// parseFileColonLine разбирает формат gopls/ruff/go vet:
// "path:line:col: message" (и "…:line: message" — без колонки).
func parseFileColonLine(output, outputDir string) []lspDiagnostic {
	var ds []lspDiagnostic
	for _, ln := range strings.Split(output, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if m := reFileColon.FindStringSubmatch(ln); m != nil {
			line, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			file := normLSPFile(m[1], outputDir)
			if file == "" {
				continue
			}
			msg := strings.TrimSpace(m[4])
			if msg == "" {
				continue
			}
			sev := "error"
			if strings.HasPrefix(msg, "warning") || strings.HasPrefix(msg, "WARN") {
				sev = "warning"
			}
			ds = append(ds, lspDiagnostic{File: file, Line: line, Col: col, Severity: sev, Message: msg})
			continue
		}
		if m := reTscFile.FindStringSubmatch(ln); m != nil {
			line, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			file := normLSPFile(m[1], outputDir)
			if file == "" {
				continue
			}
			msg := strings.TrimSpace(m[5])
			if msg == "" {
				continue
			}
			// tsc печатает "error TS2322: …" — убираем префикс-код для компактности.
			if i := strings.Index(msg, ": "); i > 0 && len(msg) > i+2 &&
				(strings.HasPrefix(msg, "TS") || strings.HasPrefix(msg, "TS")) {
				msg = msg[i+2:]
			}
			sev := "error"
			if m[4] == "warning" {
				sev = "warning"
			}
			ds = append(ds, lspDiagnostic{File: file, Line: line, Col: col, Severity: sev, Message: msg})
			continue
		}
	}
	return ds
}

// parsePyrightJSON разбирает вывод "pyright --outputjson".
func parsePyrightJSON(output, outputDir string) []lspDiagnostic {
	var p struct {
		GeneralDiagnostics []struct {
			File     string `json:"file"`
			Severity string `json:"severity"`
			Message  string `json:"message"`
			Range    struct {
				Start struct{ Line, Character int } `json:"start"`
			} `json:"range"`
		} `json:"generalDiagnostics"`
	}
	if json.Unmarshal([]byte(output), &p) != nil {
		return parseFileColonLine(output, outputDir)
	}
	var ds []lspDiagnostic
	for _, d := range p.GeneralDiagnostics {
		sev := "info"
		switch d.Severity {
		case "error":
			sev = "error"
		case "warning":
			sev = "warning"
		default:
			sev = "info" // information/hint — отбрасываем как малозначимые
		}
		if sev == "info" {
			continue
		}
		file := normLSPFile(d.File, outputDir)
		if file == "" {
			continue
		}
		ds = append(ds, lspDiagnostic{
			File:     file,
			Line:     d.Range.Start.Line + 1,
			Col:      d.Range.Start.Character + 1,
			Severity: sev,
			Message:  strings.TrimSpace(d.Message),
		})
	}
	return ds
}

// normLSPFile приводит путь из вывода чекера к относительному (slash) от
// outputDir: pyright печатает абсолютные пути, gopls/tsc/ruff — как правило
// относительные в рамках CWD. Пути вне outputDir игнорируются.
func normLSPFile(f, outputDir string) string {
	f = filepath.ToSlash(filepath.Clean(strings.TrimSpace(f)))
	// go vet печатает префикс "vet: <file>:<line>:<col>: msg" — убираем его.
	f = strings.TrimPrefix(f, "vet:")
	f = strings.TrimSpace(f)
	f = filepath.ToSlash(filepath.Clean(f))
	if f == "" {
		return ""
	}
	if filepath.IsAbs(f) {
		if outputDir == "" {
			return ""
		}
		rel, err := filepath.Rel(outputDir, f)
		if err != nil || strings.HasPrefix(rel, "..") {
			return ""
		}
		f = filepath.ToSlash(rel)
	}
	if strings.HasPrefix(f, "../") || strings.HasPrefix(f, "/") {
		return ""
	}
	return f
}

// sortDedupLSP сортирует (error → warning → info, затем файл/строка/колонка)
// и убирает дубликаты.
func sortDedupLSP(ds []lspDiagnostic) []lspDiagnostic {
	if len(ds) < 2 {
		return ds
	}
	sort.SliceStable(ds, func(i, j int) bool {
		if ds[i].Severity != ds[j].Severity {
			return sevRank(ds[i].Severity) < sevRank(ds[j].Severity)
		}
		if ds[i].File != ds[j].File {
			return ds[i].File < ds[j].File
		}
		if ds[i].Line != ds[j].Line {
			return ds[i].Line < ds[j].Line
		}
		if ds[i].Col != ds[j].Col {
			return ds[i].Col < ds[j].Col
		}
		return ds[i].Message < ds[j].Message
	})
	out := ds[:0]
	for i, d := range ds {
		if i > 0 && ds[i-1] == d {
			continue
		}
		out = append(out, d)
	}
	return out
}

func sevRank(s string) int {
	switch s {
	case "error":
		return 0
	case "warning":
		return 1
	default:
		return 2
	}
}
