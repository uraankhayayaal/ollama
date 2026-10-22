package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ai/infrastructure/sandbox"
)

func TestOverallVerdict(t *testing.T) {
	cases := []struct {
		name    string
		items   []item
		verdict string
		exit    int
	}{
		{
			name:    "все пункты зелёные",
			items:   []item{{"AC1", "docker", vPass, ""}, {"AC2", "compose", vPass, ""}},
			verdict: "READY",
			exit:    exitOK,
		},
		{
			name:    "деграйд инфры",
			items:   []item{{"AC1", "docker", vPass, ""}, {"AC2", "compose", vInfra, ""}},
			verdict: "INFRA_DEGRADED",
			exit:    exitInfra,
		},
		{
			name:    "дефект важнее деграйда",
			items:   []item{{"AC1", "docker", vInfra, ""}, {"AC2", "compose", vFail, ""}},
			verdict: "FAIL",
			exit:    exitFail,
		},
		{
			name:    "отсутствие compose — деграйд линта",
			items:   []item{{"AC2", "compose", vLint, ""}},
			verdict: "INFRA_DEGRADED",
			exit:    exitInfra,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			verdict, exit := overall(c.items)
			if verdict != c.verdict || exit != c.exit {
				t.Fatalf("overall() = (%q, %d), ожидалось (%q, %d)", verdict, exit, c.verdict, c.exit)
			}
		})
	}
}

func TestCheckCapabilitiesContradiction(t *testing.T) {
	declared := sandbox.Capabilities{Docker: sandbox.StateAvailable}
	fact := sandbox.Capabilities{Docker: sandbox.StateUnknown}
	it := checkCapabilities(declared, fact, map[string]string{"SANDBOX_HAS_DOCKER": "1"}, nil)
	if it.Verdict != vFail {
		t.Fatalf("ожидался FAIL при противоречии объявления и факта, получено %s (%s)", it.Verdict, it.Detail)
	}
	if !strings.Contains(it.Detail, "SANDBOX_HAS_DOCKER=1") {
		t.Fatalf("в деталях нет указания на противоречие: %s", it.Detail)
	}
}

func TestCheckCapabilitiesMatchesFact(t *testing.T) {
	declared := sandbox.Capabilities{Docker: sandbox.StateAvailable, Postgres: sandbox.StateAvailable, MinIO: sandbox.StateAvailable}
	fact := sandbox.Capabilities{Docker: sandbox.StateAvailable, Postgres: sandbox.StateAvailable, MinIO: sandbox.StateAvailable}
	env := map[string]string{
		"SANDBOX_HAS_DOCKER":   "1",
		"SANDBOX_HAS_POSTGRES": "1",
		"SANDBOX_HAS_MINIO":    "1",
	}
	it := checkCapabilities(declared, fact, env, nil)
	if it.Verdict != vPass {
		t.Fatalf("ожидался PASS, получено %s (%s)", it.Verdict, it.Detail)
	}
}

// TestCheckCapabilitiesNotDeclared — переменные не объявлены в окружении:
// это не противоречие (факт неизвестен), но чеклист подсказывает, что их
// нужно экспортировать.
func TestCheckCapabilitiesNotDeclared(t *testing.T) {
	declared := sandbox.Capabilities{}
	fact := sandbox.Capabilities{Docker: sandbox.StateAvailable, Postgres: sandbox.StateAvailable, MinIO: sandbox.StateAvailable}
	it := checkCapabilities(declared, fact, map[string]string{
		"SANDBOX_HAS_DOCKER":   "1",
		"SANDBOX_HAS_POSTGRES": "1",
		"SANDBOX_HAS_MINIO":    "1",
	}, nil)
	if it.Verdict != vPass {
		t.Fatalf("ожидался PASS при необъявленных переменных, получено %s (%s)", it.Verdict, it.Detail)
	}
	if !strings.Contains(it.Detail, "не объявлены") {
		t.Fatalf("в деталях нет подсказки про экспорт: %s", it.Detail)
	}
}

// TestCheckCapabilitiesProvisionExportContradiction — экспорт провижининга
// («=1») не должен расходиться с фактом: иначе окружение помечено неверно.
func TestCheckCapabilitiesProvisionExportContradiction(t *testing.T) {
	declared := sandbox.Capabilities{}
	fact := sandbox.Capabilities{Docker: sandbox.StateAvailable, Postgres: sandbox.StateUnknown, MinIO: sandbox.StateAvailable}
	it := checkCapabilities(declared, fact, map[string]string{
		"SANDBOX_HAS_DOCKER":   "1",
		"SANDBOX_HAS_POSTGRES": "1",
		"SANDBOX_HAS_MINIO":    "1",
	}, nil)
	if it.Verdict != vFail {
		t.Fatalf("ожидался FAIL при расхождении экспорта и факта, получено %s (%s)", it.Verdict, it.Detail)
	}
	if !strings.Contains(it.Detail, "SANDBOX_HAS_POSTGRES") {
		t.Fatalf("в деталях нет имени переменной: %s", it.Detail)
	}
}

func TestCheckDegradeMarkingExplicit(t *testing.T) {
	fact := sandbox.Capabilities{Docker: sandbox.StateUnknown, Postgres: sandbox.StateUnknown, MinIO: sandbox.StateAvailable}
	env := map[string]string{
		"SANDBOX_HAS_DOCKER":   "0",
		"SANDBOX_HAS_POSTGRES": "0",
		"SANDBOX_HAS_MINIO":    "1",
	}
	it := checkDegradeMarking(fact, env, nil)
	if it.Verdict != vPass {
		t.Fatalf("ожидался PASS при явном SANDBOX_HAS_*=0, получено %s (%s)", it.Verdict, it.Detail)
	}
	if !strings.Contains(it.Detail, "INFRA_DEGRADED") {
		t.Fatalf("в деталях нет маркера INFRA_DEGRADED: %s", it.Detail)
	}
}

func TestCheckDegradeMarkingUnmarked(t *testing.T) {
	fact := sandbox.Capabilities{Docker: sandbox.StateUnknown, Postgres: sandbox.StateAvailable, MinIO: sandbox.StateAvailable}
	env := map[string]string{"SANDBOX_HAS_POSTGRES": "1", "SANDBOX_HAS_MINIO": "1"}
	it := checkDegradeMarking(fact, env, nil)
	if it.Verdict != vFail {
		t.Fatalf("ожидался FAIL при немаркированном деграде, получено %s (%s)", it.Verdict, it.Detail)
	}
	if !strings.Contains(it.Detail, "SANDBOX_HAS_DOCKER") {
		t.Fatalf("в деталях нет имени переменной: %s", it.Detail)
	}
}

func TestCheckDegradeMarkingEnvUnavailable(t *testing.T) {
	fact := sandbox.Capabilities{Docker: sandbox.StateUnknown}
	it := checkDegradeMarking(fact, nil, os.ErrNotExist)
	if it.Verdict != vInfra {
		t.Fatalf("ожидался INFRA_DEGRADED при недоступных переменных, получено %s", it.Verdict)
	}
}

// TestDockerDetail — отсутствие CLI и прочие ошибки docker info различаются:
// демон по сокету может отвечать, но без CLI не работают compose/migrate.
func TestDockerDetail(t *testing.T) {
	missing := dockerDetail(exec.ErrNotFound, "")
	if !strings.Contains(missing, "не найден в PATH") {
		t.Fatalf("ожидалась пометка об отсутствии CLI, получено %q", missing)
	}
	other := dockerDetail(errors.New("exit status 1"), "Cannot connect to the Docker daemon")
	if !strings.Contains(other, "Cannot connect") {
		t.Fatalf("ожидался текст вывода docker info, получено %q", other)
	}
}

func TestSkippedTests(t *testing.T) {
	out := strings.Join([]string{
		"=== RUN   TestE2ETodoAppFullFlow",
		"    --- SKIP: sub-тест (0.00s)",
		"--- SKIP: TestE2ETodoAppFullFlow (0.00s)",
		"--- PASS: TestE2EOrchestrationGuards (12.3s)",
	}, "\n")
	got := skippedTests(out)
	if len(got) != 1 || got[0] != "TestE2ETodoAppFullFlow" {
		t.Fatalf("skippedTests = %v, ожидался только верхнеуровневый пропуск", got)
	}
}

func TestParseEnv(t *testing.T) {
	out := strings.Join([]string{
		"export SANDBOX_HAS_DOCKER=1",
		"export SANDBOX_HAS_POSTGRES=0",
		"export SANDBOX_HAS_MINIO=1",
		"export SANDBOX_COMPOSE_FILE=/abs/path/compose.yaml",
	}, "\n")
	env := parseEnv(out)
	if env["SANDBOX_HAS_DOCKER"] != "1" || env["SANDBOX_HAS_POSTGRES"] != "0" {
		t.Fatalf("parseEnv вернул %v", env)
	}
	if env["SANDBOX_COMPOSE_FILE"] != "/abs/path/compose.yaml" {
		t.Fatalf("SANDBOX_COMPOSE_FILE = %q", env["SANDBOX_COMPOSE_FILE"])
	}
}

func TestRenderMarkdownContainsVerdictAndItems(t *testing.T) {
	rep := report{
		Verdict: "INFRA_DEGRADED",
		Exit:    exitInfra,
		Items: []item{
			{"AC1", "Docker daemon доступен", vPass, "docker info — exit 0"},
			{"AC3", "Стек поднимается", vInfra, "postgres: контейнер не создан"},
		},
		Env: map[string]string{"SANDBOX_HAS_DOCKER": "0"},
	}
	md := renderMarkdown(rep)
	for _, want := range []string{
		"**INFRA_DEGRADED**", "AC1", "docker info — exit 0",
		"AC3", "postgres: контейнер не создан", "export SANDBOX_HAS_DOCKER=0",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("отчёт не содержит %q:\n%s", want, md)
		}
	}
}

// TestCIWorkflowRunsSameTargets — CI обязан звать те же make-цели, что и
// локальная проверка: разъезд команд означает, что acceptance criteria
// проверяется не тем, чем заявлено.
func TestCIWorkflowRunsSameTargets(t *testing.T) {
	dir, ok := makeDir(".")
	if !ok {
		t.Fatal("корневой makefile не найден")
	}
	path := filepath.Join(dir, ".github", "workflows", "sandbox-readiness.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("CI-джоб готовности песочницы не найден (%s): %v", path, err)
	}
	workflow := string(data)
	for _, want := range []string{
		"make infra.up",
		"make sandbox-readiness",
		"SANDBOX_READINESS_ARGS",
		"bin/sandbox-provision env",
		"INFRA_DEGRADED",
		"upload-artifact",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("CI-джоб не содержит %q", want)
		}
	}
}

// TestE2EWorkflowRunsSameTargets — интеграционный прогон в CI обязан звать
// ту же make-цель, что и локальная проверка, и различать вердикты:
// «упал e2e» (FAIL) и «песочница не готова» (INFRA_DEGRADED).
func TestE2EWorkflowRunsSameTargets(t *testing.T) {
	dir, ok := makeDir(".")
	if !ok {
		t.Fatal("корневой makefile не найден")
	}
	path := filepath.Join(dir, ".github", "workflows", "e2e.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("CI-джоб интеграционного прогона не найден (%s): %v", path, err)
	}
	workflow := string(data)
	for _, want := range []string{
		"make e2e",
		"INFRA_DEGRADED",
		"::error::",
		"upload-artifact",
		"shell: bash",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("CI-джоб интеграционного прогона не содержит %q", want)
		}
	}
}

// TestReadinessChecklistDoesNotRunE2E — AC5 (интеграционный прогон) живёт в
// отдельном джобе e2e.yml. Если он вернётся в чеклист готовности, снова
// смешаются два разных сигнала: «песочница не готова» и «упал e2e».
func TestReadinessChecklistDoesNotRunE2E(t *testing.T) {
	dir, ok := makeDir(".")
	if !ok {
		t.Fatal("корневой makefile не найден")
	}
	data, err := os.ReadFile(filepath.Join(dir, "cmd", "sandbox-readiness", "main.go"))
	if err != nil {
		t.Fatalf("чтение cmd/sandbox-readiness/main.go: %v", err)
	}
	if strings.Contains(string(data), `checkMake(dir, "e2e"`) {
		t.Fatal("чеклист готовности снова запускает e2e — AC5 должен проверяться джобом e2e.yml")
	}
}

// TestE2EVerdictByExitCode — вердикт интеграционного прогона выводится из
// кода возврата: тесты прошли — READY, окружение недоступно —
// INFRA_DEGRADED, тесты упали — FAIL.
func TestE2EVerdictByExitCode(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{exitOK, "READY"},
		{exitInfra, "INFRA_DEGRADED"},
		{exitFail, "FAIL"},
	}
	for _, c := range cases {
		if got := e2eVerdict(c.code); got != c.want {
			t.Fatalf("e2eVerdict(%d) = %q, ожидалось %q", c.code, got, c.want)
		}
	}
}

// TestMakefileHasRolloutTargets — acceptance criteria ссылаются на команды
// корневого makefile: если цели исчезнут, критерий станет непроверяемым.
func TestMakefileHasRolloutTargets(t *testing.T) {
	dir, ok := makeDir(".")
	if !ok {
		t.Fatal("корневой makefile не найден")
	}
	data, err := os.ReadFile(filepath.Join(dir, "makefile"))
	if err != nil {
		t.Fatalf("чтение makefile: %v", err)
	}
	targets := makefileTargets(string(data))
	for _, want := range []string{
		"migrate", "e2e", "sandbox-readiness",
		"infra.up", "infra.down", "infra.ps", "backend-test",
	} {
		if !targets[want] {
			t.Fatalf("в корневом makefile нет цели %q (найдены: %v)", want, targets)
		}
	}
}

// makefileTargets — цели makefile (имя до двоеточия, служебные имена с
// ведущей точкой пропускаются).
func makefileTargets(data string) map[string]bool {
	targets := map[string]bool{}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, ":") {
			continue
		}
		name := strings.TrimSpace(strings.SplitN(line, ":", 2)[0])
		if name == "" || strings.ContainsAny(name, "=%$") {
			continue
		}
		for _, n := range strings.Fields(name) {
			if strings.HasPrefix(n, ".") {
				continue
			}
			targets[n] = true
		}
	}
	return targets
}
