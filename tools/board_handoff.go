package tools

import (
	"fmt"
	"os"
	"strings"
)

// handoffGate — Handoff Gate «Ready for QA» (п.3.3 PLAN-2026-10-18-A7-04):
// перед передачей задачи из работы в приёмку (in_progress → testing) рабочая
// директория задачи должна быть «чистой» (правки закоммичены) и проект —
// собираться. Это ловит класс A7-04: в приёмку уходил противоречивый WIP со
// сломанной сборкой.
//
// Возвращает nil, если передавать можно; иначе — конкретную причину для отказа
// (её видит разработчик в ответе инструмента).
//
// Инфраструктурная невозможность проверки (песочница не настроена, нет
// тулчейна, недоступен git) — degrade: гейт НЕ блокирует, чтобы инфра не
// превращалась в дефект кода (см. AGENTS.md, «Классификация ошибок»).
func handoffGate(ops *FileOps) error {
	if ops == nil || handoffDisabled() {
		return nil
	}
	dir := strings.TrimSpace(ops.WorkingDir())
	if dir == "" {
		return nil
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil
	}
	project := ops.ProjectName()

	// 1. Незакоммиченные изменения ОТСЛЕЖИВАЕМЫХ файлов — противоречивый WIP,
	// который при повторном входе порождает конфликтный ребилд. Untracked
	// артефакты (например тестовый отчёт) не блокируют.
	if r := handoffExec("git status --short --untracked-files=no", dir, project); !r.infra && r.failure == "" {
		if dirty := strings.TrimSpace(r.stdout); dirty != "" {
			return fmt.Errorf("в рабочей директории задачи есть незакоммиченные изменения — закоммить их перед передачей в приёмку (Handoff Gate):\n%s", dirty)
		}
	}

	// 2. Сборка проекта: не отдавать в QA заведомо сломанную сборку.
	cmd := handoffBuildCommand(dir)
	if cmd == "" {
		return nil
	}
	r := handoffExec(cmd, dir, project)
	if r.infra {
		return nil
	}
	if r.failure != "" {
		return fmt.Errorf("сборка не проходит (%s) — почини и закоммить перед передачей в приёмку (Handoff Gate):\n%s", cmd, r.failure)
	}
	return nil
}

// handoffDisabled — гейт выключается CODEGEN_HANDOFF_GATE=off|0|false|no
// (аварийный обход, когда проект не использует гейт).
func handoffDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODEGEN_HANDOFF_GATE"))) {
	case "off", "0", "false", "no":
		return true
	}
	return false
}

// handoffBuildCommand — команда сборки по маркерам проекта. Пусто — стек не
// распознан, проверка сборки не выполняется (degrade, не блокер).
// CODEGEN_HANDOFF_CMD переопределяет автоопределение.
func handoffBuildCommand(dir string) string {
	if v := strings.TrimSpace(os.Getenv("CODEGEN_HANDOFF_CMD")); v != "" {
		return v
	}
	switch {
	case hasFile(dir, "go.mod"):
		return "go build ./..."
	case hasFile(dir, "package.json"):
		return "npm run build --if-present"
	case hasFile(dir, "pyproject.toml") || hasFile(dir, "requirements.txt"):
		return "python -m compileall -q ."
	default:
		return ""
	}
}

// handoffRunCommand — точка запуска проверочных команд Handoff Gate. Вынесена
// в переменную ради герметичных тестов (подменяется стабом без песочницы).
var handoffRunCommand = runCommand

// handoffExec — результат проверочной команды с классификацией сбоя:
// failure — сбой выполнения (дефект/несоответствие условию); infra — сбой
// окружения/политики, который НЕ считается дефектом кода.
type handoffExecResult struct {
	stdout  string
	failure string
	infra   bool
}

func handoffExec(cmd, dir, project string) handoffExecResult {
	res, err := handoffRunCommand(cmd, dir, project)
	if err != nil || res == nil {
		return handoffExecResult{infra: true}
	}
	if res["status"] == "success" {
		return handoffExecResult{stdout: res["stdout"]}
	}
	// Инфраструктурные и политические сбои несут message/hint (песочница не
	// настроена, команда не найдена, заблокировано политикой) — это degrade.
	if strings.TrimSpace(res["message"]) != "" || strings.TrimSpace(res["hint"]) != "" {
		return handoffExecResult{infra: true}
	}
	out := strings.TrimSpace(res["exit_error"] + "\n" + res["stdout"] + "\n" + res["stderr"])
	return handoffExecResult{failure: truncateHandoff(out, 800)}
}

func truncateHandoff(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}
