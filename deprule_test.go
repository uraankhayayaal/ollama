package main

// Импортный гейт Dependency Rule (Этап 8): application-слой не должен зависеть
// от внешних/инфраструктурных пакетов напрямую — только от домена (ai/domain,
// ai/domain/services), портов и допустимых shared-утилит. Проверка гоняет
// `go list` по ./application/... и для каждого НЕ-тестового импорта под `ai/`
// сверяет его с allowlist'ом префиксов. Запускается в каноническом наборе
// (`go test .`), поэтому регрессия направления зависимости падает сразу.

import (
	"os/exec"
	"strings"
	"testing"
)

// allowedPrefixes — префиксы импортов, разрешённые для application-слоя.
// Всё остальное под ai/ (agents, tools, runner, server, chat, checkpoint,
// runevents, workspace, infrastructure/*, gitops, runner, repositories, …)
// нарушает Dependency Rule.
var allowedPrefixes = []string{
	"ai/domain",      // домен и его под-пакеты (domain/services, domain/log, …)
	"ai/application", // соседние под-пакеты application
	"ai/shared",      // shared-утилиты (logging, projects, tokens, langdetect)
}

func allowedImport(imp string) bool {
	if !strings.HasPrefix(imp, "ai/") {
		return true // внешние (stdlib/сторонние) импорты не регулируются гейтом
	}
	for _, p := range allowedPrefixes {
		if imp == p || strings.HasPrefix(imp, p+"/") {
			return true
		}
	}
	return false
}

func TestApplicationDependencyRule(t *testing.T) {
	// go list по application/...: .Imports — только НЕ-тестовые импорты.
	out, err := exec.Command("go", "list", "-f",
		"{{.ImportPath}} {{join .Imports \" \"}}", "./application/...").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list ./application/... failed: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("go list ./application/... failed: %v", err)
	}

	violations := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pkg := fields[0]
		for _, imp := range fields[1:] {
			if !allowedImport(imp) {
				t.Errorf("%s imports %s (нарушение Dependency Rule)", pkg, imp)
				violations++
			}
		}
	}
	if violations > 0 {
		t.Errorf("найдено нарушений: %d — application должен зависеть только от домена/портов/shared-утилит", violations)
	}
}
