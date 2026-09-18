package tools

// Ф-4: нативные диагностики как источник для LspCheck.
//
// Если для стека проекта доступен языковой сервер (см. tools/lspclient),
// диагностики берутся из его стрима publishDiagnostics — без повторного запуска
// CLI-чекера. Если сервер не установлен, файлы не заданы или запрос не удался,
// LspCheck деградирует к прежнему CLI-режиму Ф-1. Управление: LSP_NATIVE
// (по умолчанию включено).

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
