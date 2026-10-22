// Command sandbox-readiness — проверка acceptance criteria готовности
// песочницы авто-проверки (rollout за DevOps/CI).
//
// Основание: docs/plans/PLAN-2026-10-18-todo-devops-sandbox-provisioning.md
// (п. «Acceptance criteria (rollout — за DevOps/CI)») и
// docs/40-operations/sandbox-readiness.md.
//
// Подкоманды:
//
//	check — чеклист acceptance criteria (по умолчанию)
//	e2e   — самозавершающийся интеграционный прогон: up → migrate → e2e → down
//
// Коды возврата:
//
//	0 — READY: все пункты acceptance criteria выполнены
//	2 — INFRA_DEGRADED: инфраструктура недоступна (не дефект кода)
//	1 — FAIL: пункт acceptance criteria не выполнен
//
// Команды проверки — те же, что в корневом makefile и в CI
// (.github/workflows/sandbox-readiness.yml): make infra.up / make migrate /
// make e2e. Дублировать их руками нельзя — разъедутся с приёмкой.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"ai/infrastructure/sandbox"
)

const (
	exitOK    = 0
	exitFail  = 1
	exitInfra = 2
)

// verdict — оценка одного пункта acceptance criteria.
type verdict string

const (
	vPass  verdict = "PASS"
	vFail  verdict = "FAIL"
	vInfra verdict = "INFRA_DEGRADED"
	vLint  verdict = "LINT_DEGRADED"
)

// item — один пункт acceptance criteria с оценкой и пояснением.
type item struct {
	ID      string  `json:"id"`
	Title   string  `json:"title"`
	Verdict verdict `json:"verdict"`
	Detail  string  `json:"detail"`
}

// report — результат проверки готовности песочницы.
type report struct {
	Verdict string            `json:"verdict"`
	Exit    int               `json:"exit_code"`
	Items   []item            `json:"items"`
	Env     map[string]string `json:"env"`
}

// options — флаги командной строки.
type options struct {
	skipIntegration bool
	json            bool
	reportPath      string
}

func main() {
	cmd := "check"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}
	opts := parseOptions(args)
	switch cmd {
	case "check":
		os.Exit(runCheck(opts))
	case "e2e":
		os.Exit(runE2E(opts))
	default:
		fmt.Fprintln(os.Stderr, "usage: sandbox-readiness <check|e2e> [--skip-integration] [--json] [--report=FILE]")
		os.Exit(exitFail)
	}
}

// parseOptions разбирает флаги. Неизвестный флаг — ошибка: в тихо
// проигнорированном флаге легко потерять половину проверки.
func parseOptions(args []string) options {
	opts := options{}
	for _, arg := range args {
		switch {
		case arg == "--skip-integration":
			opts.skipIntegration = true
		case arg == "--json":
			opts.json = true
		case strings.HasPrefix(arg, "--report="):
			opts.reportPath = strings.TrimPrefix(arg, "--report=")
		default:
			fmt.Fprintf(os.Stderr, "sandbox-readiness: неизвестный флаг %q\n", arg)
			os.Exit(exitFail)
		}
	}
	return opts
}

// runCheck — чеклист готовности ОКРУЖЕНИЯ (AC1–AC4, AC6, AC7). Каждый пункт
// оценивается по факту; недоступная инфраструктура даёт INFRA_DEGRADED, а не
// молчаливый пропуск.
//
// AC5 (интеграционный прогон) в чеклисте НЕТ: прогон e2e — отдельный джоб
// .github/workflows/e2e.yml, чтобы «песочница не готова» и «упал e2e» были
// разными сигналами разных required checks.
func runCheck(opts options) int {
	dir, ok := makeDir(".")
	if !ok {
		fmt.Fprintln(os.Stderr, "sandbox-readiness: корневой makefile не найден")
		return exitFail
	}
	items := []item{
		checkDocker(),
		checkCompose(),
		checkStack(dir),
	}
	// Снимок состояния песочницы снимается, пока стек поднят: замер после
	// teardown противоречил бы объявленным SANDBOX_HAS_*=1 — ложный AC6/AC7.
	fact := detectFact()
	env, envErr := provisionEnv(dir)
	if !opts.skipIntegration {
		items = append(items,
			checkMake(dir, "migrate", "AC4", "Миграции проходят (make migrate — exit 0)"),
		)
	}
	declared := readDeclared()
	items = append(items,
		checkCapabilities(declared, fact, env, envErr),
		checkDegradeMarking(fact, env, envErr),
	)

	rep := report{Items: items, Env: env}
	rep.Verdict, rep.Exit = overall(items)
	printReport(rep, opts)
	writeReportFile(rep, opts.reportPath)
	writeGitHubOutputs(rep)
	return rep.Exit
}

// runE2E — самозавершающийся интеграционный прогон: стек поднимается,
// миграции применяются, e2e-тесты выполняются, стек снимается. Пропуск
// тестов (нет Chrome/Docker) — INFRA_DEGRADED, а не «прогон зелёный».
//
// Вердикт прогона пишется в GitHub-выводы: CI различает «упал e2e» (FAIL)
// и «песочница не готова» (INFRA_DEGRADED) без разбора текста лога.
func runE2E(opts options) int {
	dir, ok := makeDir(".")
	if !ok {
		fmt.Fprintln(os.Stderr, "sandbox-readiness: корневой makefile не найден")
		return finishE2E(exitFail)
	}
	if out, err := runMake(dir, "infra.up"); err != nil {
		fmt.Fprintln(os.Stderr, "INFRA_DEGRADED: стек песочницы не поднят:", tail(out))
		return finishE2E(exitInfra)
	}
	defer func() {
		if out, err := runMake(dir, "infra.down"); err != nil {
			fmt.Fprintln(os.Stderr, "предупреждение: стек песочницы не снят:", tail(out))
		}
	}()
	if out, err := runMake(dir, "migrate"); err != nil {
		if exitCode(err) == exitInfra {
			fmt.Fprintln(os.Stderr, "INFRA_DEGRADED: миграции не выполнены:", tail(out))
			return finishE2E(exitInfra)
		}
		fmt.Fprintln(os.Stderr, "ошибка миграций:", tail(out))
		return finishE2E(exitFail)
	}
	return finishE2E(runE2ETests(dir))
}

// finishE2E — код возврата прогона + GitHub-выводы (verdict/exit_code).
func finishE2E(code int) int {
	writeGitHubOutputs(report{Verdict: e2eVerdict(code), Exit: code})
	return code
}

// e2eVerdict — вердикт прогона по коду возврата: тесты прошли — READY,
// окружение недоступно — INFRA_DEGRADED, тесты упали — FAIL.
func e2eVerdict(code int) string {
	switch code {
	case exitOK:
		return "READY"
	case exitInfra:
		return "INFRA_DEGRADED"
	default:
		return "FAIL"
	}
}

// runE2ETests выполняет живые e2e-тесты репозитория (тег e2e, гейт
// E2E_FULL=1). Пропущенные тесты означают, что интеграционный прогон не
// состоялся: это деграйд окружения, а не успех.
func runE2ETests(dir string) int {
	cmd := exec.Command("go", "test", "-tags", "e2e", "-run", e2ePattern(),
		"-timeout", e2eTimeout(), "-v", "./server/")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "E2E_FULL=1")
	var buf bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
	cmd.Stderr = io.MultiWriter(os.Stderr, &buf)
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e-прогон упал:", err)
		return exitFail
	}
	if skipped := skippedTests(buf.String()); len(skipped) > 0 {
		fmt.Fprintf(os.Stderr,
			"INFRA_DEGRADED: интеграционный прогон пропущен (%s) — в песочнице нет Chrome/Docker\n",
			strings.Join(skipped, ", "))
		return exitInfra
	}
	fmt.Println("OK: интеграционный прогон выполнен")
	return exitOK
}

// checkDocker — AC1: демон Docker отвечает.
func checkDocker() item {
	title := "Docker daemon доступен (docker info — exit 0)"
	out, err := exec.Command("docker", "info").CombinedOutput()
	if err != nil {
		return item{"AC1", title, vInfra, dockerDetail(err, string(out))}
	}
	return item{"AC1", title, vPass, "docker info — exit 0"}
}

// dockerDetail — пояснение к провалу `docker info`: отдельно различается
// отсутствие CLI (демон по сокету может отвечать, но compose/migrate без
// CLI не работают) и прочие ошибки.
func dockerDetail(err error, out string) string {
	if errors.Is(err, exec.ErrNotFound) {
		return "docker: бинарь не найден в PATH (без CLI не работают compose/migrate)"
	}
	return "docker info: " + firstLine(out)
}

// checkCompose — AC2: плагин docker compose доступен.
func checkCompose() item {
	out, err := exec.Command("docker", "compose", "version").CombinedOutput()
	if err != nil {
		return item{"AC2", "docker compose доступен (docker compose version — exit 0)", vLint,
			"docker compose version: " + firstLine(string(out))}
	}
	return item{"AC2", "docker compose доступен (docker compose version — exit 0)", vPass,
		strings.TrimSpace(firstLine(string(out)))}
}

// checkStack — AC3: стек Postgres+MinIO поднят, Postgres healthy, порт 9000
// отвечает.
func checkStack(dir string) item {
	title := "Стек поднимается (Postgres healthy, порт 9000 отвечает)"
	if out, err := runMake(dir, "infra.up"); err != nil {
		return item{"AC3", title, vInfra, "make infra.up: " + tail(out)}
	}
	health := serviceHealth(dir, "postgres")
	if health != "healthy" {
		return item{"AC3", title, vInfra, "postgres: " + health}
	}
	if fact := detectFact(); fact.MinIO != sandbox.StateAvailable {
		return item{"AC3", title, vInfra, "minio: порт 9000 не отвечает"}
	}
	return item{"AC3", title, vPass, "postgres: healthy, minio: порт 9000 отвечает"}
}

// checkMake — AC4/AC5: make-цель завершилась успешно (0), деграйдом инфры
// (2) или ошибкой (иное).
func checkMake(dir, target, id, title string) item {
	out, err := runMake(dir, target)
	if err == nil {
		return item{id, title, vPass, "make " + target + " — exit 0"}
	}
	if exitCode(err) == exitInfra {
		return item{id, title, vInfra, "make " + target + " — INFRA_DEGRADED: " + tail(out)}
	}
	return item{id, title, vFail, fmt.Sprintf("make %s — exit %d: %s", target, exitCode(err), tail(out))}
}

// checkCapabilities — AC6: объявленные SANDBOX_HAS_* не противоречат факту.
func checkCapabilities(declared, fact sandbox.Capabilities, env map[string]string, envErr error) item {
	title := "SANDBOX_HAS_* отражают фактическое состояние окружения"
	var problems []string
	for _, c := range []struct {
		name     string
		declared sandbox.State
		fact     sandbox.State
	}{
		{"SANDBOX_HAS_DOCKER", declared.Docker, fact.Docker},
		{"SANDBOX_HAS_POSTGRES", declared.Postgres, fact.Postgres},
		{"SANDBOX_HAS_MINIO", declared.MinIO, fact.MinIO},
	} {
		if c.declared == sandbox.StateAvailable && c.fact != sandbox.StateAvailable {
			problems = append(problems, c.name+"=1, по факту недоступно")
		}
		// Экспорт провижининга (`cmd/sandbox-provision env`) обязан совпадать
		// с фактом: расхождение означает, что окружение помечено неверно.
		if v, ok := env[c.name]; ok && v == "1" && c.fact != sandbox.StateAvailable {
			problems = append(problems, c.name+"=1 (экспорт провижининга), по факту недоступно")
		}
	}
	if len(problems) > 0 {
		return item{"AC6", title, vFail, strings.Join(problems, "; ")}
	}
	if envErr != nil {
		return item{"AC6", title, vInfra, "SANDBOX_HAS_* получить не удалось: " + envErr.Error()}
	}
	if declared.Docker == sandbox.StateUnknown && declared.Postgres == sandbox.StateUnknown &&
		declared.MinIO == sandbox.StateUnknown {
		return item{"AC6", title, vPass, "SANDBOX_HAS_* в окружении не объявлены — " +
			"экспортируйте их (см. блок «Переменные возможностей») в CI/оркестратор"}
	}
	return item{"AC6", title, vPass, fmt.Sprintf("по факту: docker=%s postgres=%s minio=%s",
		fact.Docker, fact.Postgres, fact.MinIO)}
}

// checkDegradeMarking — AC7: недоступная инфра помечается явно (SANDBOX_HAS_*=0
// и маркер INFRA_DEGRADED), а не молча пропускается.
func checkDegradeMarking(fact sandbox.Capabilities, env map[string]string, envErr error) item {
	title := "Недоступность инфры помечена явно (INFRA_DEGRADED, SANDBOX_HAS_*=0)"
	if envErr != nil {
		return item{"AC7", title, vInfra, "SANDBOX_HAS_* получить не удалось: " + envErr.Error()}
	}

	var unmarked []string
	for _, c := range []struct {
		name  string
		state sandbox.State
	}{
		{"SANDBOX_HAS_DOCKER", fact.Docker},
		{"SANDBOX_HAS_POSTGRES", fact.Postgres},
		{"SANDBOX_HAS_MINIO", fact.MinIO},
	} {
		if c.state == sandbox.StateAvailable {
			continue
		}
		if v, ok := env[c.name]; !ok || v != "0" {
			unmarked = append(unmarked, fmt.Sprintf("%s=%q (ожидается 0)", c.name, v))
		}
	}
	if len(unmarked) > 0 {
		return item{"AC7", title, vFail, "деград не помечен: " + strings.Join(unmarked, ", ")}
	}
	var unavailable []string
	for _, c := range []struct {
		name  string
		state sandbox.State
	}{
		{"Docker", fact.Docker},
		{"Postgres", fact.Postgres},
		{"MinIO", fact.MinIO},
	} {
		if c.state != sandbox.StateAvailable {
			unavailable = append(unavailable, c.name)
		}
	}
	if len(unavailable) > 0 {
		return item{"AC7", title, vPass, "недоступно: " + strings.Join(unavailable, ", ") +
			" — помечено INFRA_DEGRADED, SANDBOX_HAS_*=0"}
	}
	return item{"AC7", title, vPass, "инфра доступна — деград не требуется (SANDBOX_HAS_*=1)"}
}

// overall — итоговый вердикт и код возврата: любой FAIL важнее деграйда,
// деграйд важнее успеха.
func overall(items []item) (string, int) {
	hasFail, hasDegrade := false, false
	for _, it := range items {
		switch it.Verdict {
		case vFail:
			hasFail = true
		case vInfra, vLint:
			hasDegrade = true
		}
	}
	switch {
	case hasFail:
		return "FAIL", exitFail
	case hasDegrade:
		return "INFRA_DEGRADED", exitInfra
	default:
		return "READY", exitOK
	}
}

// verdictComment — пояснение итогового вердикта.
func verdictComment(verdict string) string {
	switch verdict {
	case "READY":
		return "все пункты acceptance criteria выполнены"
	case "INFRA_DEGRADED":
		return "инфраструктура недоступна: интеграционные проверки невозможны — это деграйд окружения, а не дефект кода"
	default:
		return "пункт acceptance criteria не выполнен — требуется исправление"
	}
}

// printReport печатает отчёт в stdout (и JSON при --json).
func printReport(rep report, opts options) {
	if opts.json {
		data, err := json.MarshalIndent(rep, "", "  ")
		if err == nil {
			fmt.Println(string(data))
			return
		}
	}
	fmt.Println("Готовность песочницы авто-проверки (acceptance criteria)")
	for _, it := range rep.Items {
		fmt.Printf("  %-4s %-16s %s\n", it.ID, it.Verdict, it.Title)
		fmt.Printf("       %s\n", it.Detail)
	}
	if len(rep.Env) > 0 {
		fmt.Println("Переменные возможностей (применить в оркестраторе):")
		for _, name := range []string{"SANDBOX_HAS_DOCKER", "SANDBOX_HAS_POSTGRES", "SANDBOX_HAS_MINIO"} {
			if v, ok := rep.Env[name]; ok {
				fmt.Printf("  export %s=%s\n", name, v)
			}
		}
	}
	fmt.Printf("Итог: %s — %s\n", rep.Verdict, verdictComment(rep.Verdict))
}

// writeReportFile сохраняет отчёт в markdown (артефакт CI).
func writeReportFile(rep report, path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	if err := os.WriteFile(path, []byte(renderMarkdown(rep)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "sandbox-readiness: отчёт не записан:", err)
	}
}

// renderMarkdown — отчёт в markdown (артефакт CI).
func renderMarkdown(rep report) string {
	var b strings.Builder
	b.WriteString("# Готовность песочницы авто-проверки\n\n")
	fmt.Fprintf(&b, "Итог: **%s** (код возврата %d) — %s\n\n", rep.Verdict, rep.Exit, verdictComment(rep.Verdict))
	b.WriteString("| Пункт | Acceptance criterion | Вердикт | Детали |\n|---|---|---|---|\n")
	for _, it := range rep.Items {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", it.ID, it.Title, it.Verdict, it.Detail)
	}
	if len(rep.Env) > 0 {
		b.WriteString("\n## Переменные возможностей\n\n```\n")
		for _, name := range []string{"SANDBOX_HAS_DOCKER", "SANDBOX_HAS_POSTGRES", "SANDBOX_HAS_MINIO"} {
			if v, ok := rep.Env[name]; ok {
				fmt.Fprintf(&b, "export %s=%s\n", name, v)
			}
		}
		b.WriteString("```\n")
	}
	return b.String()
}

// writeGitHubOutputs публикует вердикт в $GITHUB_OUTPUT: CI различает
// READY / INFRA_DEGRADED / FAIL по значению, а не по «шаг упал».
func writeGitHubOutputs(rep report) {
	path := strings.TrimSpace(os.Getenv("GITHUB_OUTPUT"))
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sandbox-readiness: GITHUB_OUTPUT недоступен:", err)
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "verdict=%s\n", rep.Verdict)
	fmt.Fprintf(f, "exit_code=%d\n", rep.Exit)
}

// runMake выполняет make-цель в корне репозитория и возвращает вывод.
func runMake(dir, target string) (string, error) {
	cmd := exec.Command("make", target)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// exitCode — код возврата завершившейся команды (-1 — не запустилась).
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// makeDir — каталог с корневым makefile (вверх по дереву от start).
func makeDir(start string) (string, bool) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	for {
		for _, name := range []string{"makefile", "Makefile"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				return dir, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// composePath — compose-файл стека песочницы.
func composePath(dir string) string {
	if p := strings.TrimSpace(os.Getenv("SANDBOX_PROVISION_COMPOSE")); p != "" {
		return p
	}
	return filepath.Join(dir, "infrastructure", "sandbox", "provision", "compose.yaml")
}

// serviceHealth — состояние healthcheck контейнера сервиса compose.
func serviceHealth(dir, service string) string {
	id := serviceID(composePath(dir), service)
	if id == "" {
		return "контейнер не создан"
	}
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Health.Status}}", id).Output()
	if err != nil {
		return "нет healthcheck"
	}
	if s := strings.TrimSpace(string(out)); s != "" {
		return s
	}
	return "нет healthcheck"
}

// serviceID — id контейнера сервиса compose ("" — сервис не создан).
func serviceID(compose, service string) string {
	out, err := exec.Command("docker", "compose", "-f", compose, "ps", "-q", service).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// readDeclared — объявленные DevOps переменные SANDBOX_HAS_*.
func readDeclared() sandbox.Capabilities { return sandbox.ReadCapabilities() }

// detectFact — фактическое состояние окружения (авто-детект).
func detectFact() sandbox.Capabilities { return sandbox.DetectCapabilities() }

// provisionEnv — экспортные строки SANDBOX_HAS_* из реального CLI
// провижининга (тот же путь, что использует оркестратор).
func provisionEnv(dir string) (map[string]string, error) {
	bin := filepath.Join(dir, "bin", "sandbox-provision")
	var cmd *exec.Cmd
	if _, err := os.Stat(bin); err == nil {
		cmd = exec.Command(bin, "env")
	} else {
		cmd = exec.Command("go", "run", "./cmd/sandbox-provision", "env")
	}
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s: %s", err, firstLine(string(out)))
	}
	return parseEnv(string(out)), nil
}

// parseEnv — разбор строк «export NAME=VALUE».
func parseEnv(out string) map[string]string {
	env := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "export ") {
			continue
		}
		name, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			continue
		}
		env[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}
	return env
}

// skippedTests — имена верхнеуровневых e2e-тестов, которые были пропущены.
func skippedTests(out string) []string {
	var skipped []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "--- SKIP: ") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(line, "--- SKIP: "))
		if i := strings.IndexAny(name, " \t("); i > 0 {
			name = name[:i]
		}
		if name != "" {
			skipped = append(skipped, name)
		}
	}
	return skipped
}

// e2ePattern — набор живых e2e-тестов (SANDBOX_E2E_PATTERN переопределяет).
func e2ePattern() string {
	if v := strings.TrimSpace(os.Getenv("SANDBOX_E2E_PATTERN")); v != "" {
		return v
	}
	return "TestE2ETodoAppFullFlow|TestE2EOrchestrationGuards"
}

// e2eTimeout — таймаут e2e-прогона (SANDBOX_E2E_TIMEOUT, по умолчанию 30m).
func e2eTimeout() string {
	if v := strings.TrimSpace(os.Getenv("SANDBOX_E2E_TIMEOUT")); v != "" {
		return v
	}
	return "30m"
}

// firstLine — первая непустая строка вывода.
func firstLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return strings.TrimSpace(out)
}

// tail — последние строки вывода (для коротких сообщений об ошибке).
func tail(out string) string {
	var kept []string
	for _, l := range strings.Split(out, "\n") {
		if s := strings.TrimSpace(l); s != "" {
			kept = append(kept, s)
		}
	}
	if len(kept) > 6 {
		kept = kept[len(kept)-6:]
	}
	if len(kept) == 0 {
		return "нет вывода"
	}
	return strings.Join(kept, " | ")
}
