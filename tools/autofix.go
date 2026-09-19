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

// TakeTouched атомарно забирает и очищает очередь файлов, затронутых мутацией
// с прошлого вызова. Единый источник затронутых файлов для хуков раннера
// (авто-лечение LSP и переиндексация RAG): дренится ОДИН раз за раунд,
// а полученный список раздаётся включённым хукам.
func (ops *FileOps) TakeTouched() []string {
	return ops.takeTouched()
}

// LspCheckFiles выполняет LspCheck по явному списку относительных файлов
// (очередь НЕ дренит — список передаёт раннер). hadMutation=true, когда
// список непуст (файлы менялись), даже если чекер недоступен и диагностик нет.
func (ops *FileOps) LspCheckFiles(files []string) ([]string, bool) {
	if ops == nil || len(files) == 0 {
		return nil, false
	}
	args := make([]any, len(files))
	for i, f := range files {
		args[i] = f
	}
	res, err := ops.LspCheck(map[string]any{"files": args})
	if err != nil {
		return nil, true
	}
	return lspDiagnosticLines(res), true
}

// LspAutoFix выполняет LspCheck по файлам, затронутым с предыдущего вызова,
// сбрасывает очередь и возвращает строки диагностик. hadMutation=true, если
// файлы вообще менялись (даже когда чекер недоступен и диагностик нет).
func (ops *FileOps) LspAutoFix() ([]string, bool) {
	if ops == nil {
		return nil, false
	}
	return ops.LspCheckFiles(ops.takeTouched())
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
