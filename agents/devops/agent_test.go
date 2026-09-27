package devops

import (
	"ai/agents/promptcheck"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestDevops создаёт DevOps-агента во временной директории.
func newTestDevops(t *testing.T) *Devops {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	return newDevops(dir, "", Config{})
}

func TestResolvePathRejectsTraversal(t *testing.T) {
	d := newTestDevops(t)
	for _, bad := range []string{"../evil.yaml", "a/../../etc/passwd", "/etc/passwd"} {
		if _, err := d.ResolvePath(bad); err == nil {
			t.Errorf("ResolvePath(%q) должен вернуть ошибку выхода за пределы OutputDir", bad)
		}
	}
}

func TestWriteWritesIntoOutputDir(t *testing.T) {
	d := newTestDevops(t)
	if err := d.Write("compose.yaml", "services: {}\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := filepath.Join(d.OutputDir, "compose.yaml")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("файл %q не создан: %v", want, err)
	}
}

func TestDevopsToolsAreSelected(t *testing.T) {
	d := newTestDevops(t)
	got := d.GetTools()
	names := map[string]bool{}
	for _, td := range got {
		names[td.Name] = true
	}
	for _, want := range []string{"WriteFiles", "ReadFiles", "DeleteFiles", "AppendFile", "List", "Run"} {
		if !names[want] {
			t.Errorf("агент не включает инструмент %q", want)
		}
	}
}

func TestConstrainScopePreventsWriteOutsideScope(t *testing.T) {
	d := newTestDevops(t)
	d.SetScope([]string{"k8s/"})
	if err := d.Write("k8s/deploy.yaml", "apiVersion: v1\n"); err != nil {
		t.Fatalf("запись внутри области работы должна быть разрешена: %v", err)
	}
	if err := d.Write("compose.yaml", "services: {}\n"); err == nil {
		t.Fatal("запись вне области работы должна быть отклонена")
	}
}

// TestDevopsPromptMentionsMakefile — Ф-4 PLAN-2026-09-24-todo-makefile.md:
// инфра-блок DevOps живёт в корневом Makefile: цели up/down/logs/ps,
// зеркальные infra.<цель> (docker compose run) и самозавершающийся e2e.
func TestDevopsPromptMentionsMakefile(t *testing.T) {
	d := newTestDevops(t)
	p := d.GetSystemMessages(nil)[0].Message
	for _, want := range []string{
		"Makefile", "infra.<цель>", "docker compose run -it --rm",
		"up", "down", "logs", "ps", "e2e",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт DevOps-инженера не содержит %q:\n%s", want, p)
		}
	}
}

// TestPromptMentionsOnlyAvailableTools — согласованность промпта DevOps и его
// набора инструментов (Ф-2).
//
// Отдельно проверяем ReadAppLogs: валидный compose-файл ещё не значит, что
// сервисы поднимаются, и единственный способ это увидеть — прочитать логи
// сервиса.
func TestPromptMentionsOnlyAvailableTools(t *testing.T) {
	d, err := NewDevopsInDir("задание", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prompt := d.GetSystemMessages(nil)[0].Message
	for _, p := range promptcheck.CheckToolSet("devops", prompt, devopsToolNames) {
		t.Error(p)
	}
	for _, p := range promptcheck.CheckTypos("devops", prompt) {
		t.Error(p)
	}
	if !promptcheck.Contains(devopsToolNames, "ReadAppLogs") {
		t.Error("devops: в наборе нет ReadAppLogs — состояние сервисов не проверить")
	}
	if !promptcheck.MentionsWord(prompt, "ReadAppLogs") {
		t.Error("devops: промпт не упоминает ReadAppLogs")
	}
}

// TestDevopsPromptCICDAndContainers — Ф-3: обязательные артефакты CI/CD,
// многостадийный Dockerfile и K8s-манифесты. Без этого теста промпт можно
// сократить до «пиши инфраструктуру» и потерять всё сразу, не упав ни в одном
// тесте.
func TestDevopsPromptCICDAndContainers(t *testing.T) {
	d := newTestDevops(t)
	p := d.GetSystemMessages(nil)[0].Message
	for _, want := range []string{
		".github/workflows/ci.yml", ".gitlab-ci.yml",
		"многостадийный", "non-root", ".dockerignore",
		"readinessProbe", "livenessProbe", "resource requests/limits",
		"DetectStack",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт DevOps не содержит %q", want)
		}
	}
}

// TestDevopsPromptDegradeRules — Ф-3: проверка должна отличаться от
// предположения. Агент писал «манифест валиден», не имея docker/kubectl в
// окружении, и это доходило до приёмки как подтверждённый факт.
func TestDevopsPromptDegradeRules(t *testing.T) {
	d := newTestDevops(t)
	p := d.GetSystemMessages(nil)[0].Message
	// Регистр не важен: требование смысловое, а не строчное совпадение.
	low := strings.ToLower(p)
	for _, want := range []string{
		"degrade", "command not found", "error 127",
		"не проверено", "допустима только после успешного",
	} {
		if !strings.Contains(low, want) {
			t.Errorf("промпт DevOps не содержит degrade-требование %q", want)
		}
	}
	if !strings.Contains(p, "не успех") {
		t.Error("промпт DevOps должен называть отсутствие проверки провалом, а не успехом")
	}
}
