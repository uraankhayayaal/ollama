package tools

// Ф-4/Ф-5: нативные диагностики (publishDiagnostics) — общий код:
//   - tools/lspnative.go: LSPDiag, LSPDiagnostics (экспорт, для acceptor/executor)
//   - tools/lspcheck.go:  LspCheck (инструмент модели) — через private lspNativeDiagnostics

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"ai/stackdetect"
	"ai/tools/lspclient"
)

// lspDiagProvider — точка входа к нативным диагностикам; в тестах подменяется.
var lspDiagProvider = func(ctx context.Context, dir string, kind stackdetect.Kind) (lspclient.DiagnosticsProvider, error) {
	return lspclient.Shared().DiagnosticsProvider(ctx, dir, kind)
}

// nativeEnabled сообщает, разрешён ли нативный режим (LSP_NATIVE).
func nativeEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LSP_NATIVE"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// LSPDiag — одна строка нативной (publishDiagnostics) диагностики для внешних
// потребителей (приёмка, scope-гейт шагов плана). Пути — относительно OutputDir.
type LSPDiag struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// LSPDiagnostics выдаёт нативные диагностики по файлам (пути относительно
// OutputDir), используя publishDiagnostics языкового сервера. CLI-чекер не
// используется: при недоступном сервере handled=false — потребитель деградирует
// (acceptor = пропуск, executor-гейт = без ворнингов).
func (ops *FileOps) LSPDiagnostics(files []string) (diags []LSPDiag, checker string, handled bool) {
	if ops == nil || !nativeEnabled() || len(files) == 0 {
		return nil, "", false
	}
	dir := filepath.ToSlash(filepath.Clean(ops.OutputDir))
	proj, kind := lspProject(dir, files)
	if proj == "" {
		return nil, "", false
	}
	projFiles := lspProjectFiles(dir, proj, files)
	if len(projFiles) == 0 {
		return nil, "", false
	}
	ds, ok := ops.lspNativeDiagnostics(proj, kind, projFiles)
	if !ok {
		return nil, "", false
	}
	out := make([]LSPDiag, 0, len(ds))
	for _, d := range ds {
		out = append(out, LSPDiag{File: d.File, Line: d.Line, Col: d.Col, Severity: d.Severity, Message: d.Message})
	}
	return out, "lsp", true
}

// lspNativeDiagnostics пытается получить диагностики нативно по заданным файлам
// (пути уже относительно proj). handled=false, если нативный режим выключен,
// файлы не заданы или сервер недоступен/упал — тогда LspCheck использует
// CLI-чекер. Пути в результате приводятся к OutputDir-относительным.
func (ops *FileOps) lspNativeDiagnostics(proj string, kind stackdetect.Kind, files []string) ([]lspDiagnostic, bool) {
	if ops == nil || !nativeEnabled() || len(files) == 0 {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*lspclient.RequestTimeout())
	defer cancel()

	prov, err := lspDiagProvider(ctx, proj, kind)
	if err != nil {
		return nil, false
	}
	ds, err := prov.Diagnostics(ctx, files)
	if err != nil {
		return nil, false
	}

	prefix := ""
	dir := filepath.ToSlash(filepath.Clean(ops.OutputDir))
	if proj != dir {
		if r, rerr := filepath.Rel(dir, proj); rerr == nil {
			prefix = filepath.ToSlash(r)
		}
	}

	out := make([]lspDiagnostic, 0, len(ds))
	for _, d := range ds {
		f := d.File
		if prefix != "" {
			f = prefix + "/" + f
		}
		out = append(out, lspDiagnostic{File: f, Line: d.Line, Col: d.Col, Severity: d.Severity, Message: d.Message})
	}
	return out, true
}
