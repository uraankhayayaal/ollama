package tools

// Ф-2: авто-самоисправление. Инструменты-мутаторы (WriteFiles, AppendFile,
// DeleteFiles, PatchGoFunction, SearchReplace) после успешной записи помечают
// файл в очереди FileOps.touched. Раннер после раунда с мутациями вызывает
// LspAutoFix: тот прогоняет LspCheck по затронутым файлам, сбрасывает очередь
// и возвращает готовые строки диагностик (file:line:col: message). Если чекер
// недоступен (degrade), диагностик нет — фича тихо выключается.

import (
	"encoding/json"
	"fmt"
)

// recordTouched помечает файл как затронутый мутацией для авто-проверки LSP.
// Вызывается пишущими инструментами после успешной записи/удаления (внутри
// пер-проектной блокировки). Путь приводится к относительному от OutputDir.
func (ops *FileOps) recordTouched(full string) {
	if ops == nil || full == "" {
		return
	}
	rel := ops.relPath(full)
	if rel == "" || rel == "." {
		return
	}
	ops.touchedMu.Lock()
	defer ops.touchedMu.Unlock()
	for _, t := range ops.touched {
		if t == rel {
			return
		}
	}
	ops.touched = append(ops.touched, rel)
}

// takeTouched атомарно забирает и очищает очередь затронутых файлов.
func (ops *FileOps) takeTouched() []string {
	ops.touchedMu.Lock()
	defer ops.touchedMu.Unlock()
	out := ops.touched
	ops.touched = nil
	return out
}

// LspAutoFix выполняет LspCheck по файлам, затронутым с предыдущего вызова,
// сбрасывает очередь и возвращает строки диагностик. hadMutation=true, если
// файлы вообще менялись (даже когда чекер недоступен и диагностик нет).
func (ops *FileOps) LspAutoFix() ([]string, bool) {
	if ops == nil {
		return nil, false
	}
	touched := ops.takeTouched()
	if len(touched) == 0 {
		return nil, false
	}
	files := make([]any, len(touched))
	for i, f := range touched {
		files[i] = f
	}
	res, err := ops.LspCheck(map[string]any{"files": files})
	if err != nil {
		return nil, true
	}
	return lspDiagnosticLines(res), true
}

// lspDiagnosticLines разбирает JSON-ответ LspCheck и форматирует диагностики
// в компактные строки для скрытого промпта модели.
func lspDiagnosticLines(res []byte) []string {
	var out struct {
		Diagnostics []lspDiagnostic `json:"diagnostics"`
	}
	if json.Unmarshal(res, &out) != nil {
		return nil
	}
	lines := make([]string, 0, len(out.Diagnostics))
	for _, d := range out.Diagnostics {
		switch {
		case d.Line > 0 && d.Col > 0:
			lines = append(lines, fmt.Sprintf("%s:%d:%d: %s", d.File, d.Line, d.Col, d.Message))
		case d.Line > 0:
			lines = append(lines, fmt.Sprintf("%s:%d: %s", d.File, d.Line, d.Message))
		default:
			lines = append(lines, fmt.Sprintf("%s: %s", d.File, d.Message))
		}
	}
	return lines
}
