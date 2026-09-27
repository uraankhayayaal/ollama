package tools

// sandbox_test.go — hermetic-тесты песочницы (Ф-4).
//
// Требование из плана: тесты обязаны быть быстрее, чем печать в контекст, и не
// зависеть от Docker. Поэтому проверяются ЧИСТЫЕ функции (dockerArgs,
// sandboxSpecFor, destructiveCommandReason) и подменённый исполнитель
// LocalCommand: настоящий docker, shell и проекты агентов не запускаются
// вообще. Реальный запуск песочницы проверяется отдельно, условно
// (TestSandboxRealContainer), и пропускается, если Docker недоступен.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// goProject — временный каталог с go.mod, чтобы sandboxImageFor определил стек.
func goProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module app\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Песочница по умолчанию выключена: пустое CODEGEN_SANDBOX = хост. Регрессия
// здесь означает, что чужой Docker на машине разработчика молча переносит всё
// исполнение агентов в контейнеры.
func TestSandboxDisabledByDefault(t *testing.T) {
	t.Setenv("CODEGEN_SANDBOX", "")
	if got := loadSandboxConfig().Mode; got != SandboxModeLocal {
		t.Errorf("CODEGEN_SANDBOX пуст: ожидался хост, получен %q", got)
	}
	for _, v := range []string{"0", "off", "false", "local", "host", "localhost"} {
		t.Setenv("CODEGEN_SANDBOX", v)
		if got := loadSandboxConfig().Mode; got != SandboxModeLocal {
			t.Errorf("CODEGEN_SANDBOX=%q: ожидался хост, получен %q", v, got)
		}
	}
	for _, v := range []string{"1", "on", "true", "container", "docker"} {
		t.Setenv("CODEGEN_SANDBOX", v)
		if got := loadSandboxConfig().Mode; got != SandboxModeContainer {
			t.Errorf("CODEGEN_SANDBOX=%q: ожидалась песочница, получен %q", v, got)
		}
	}
	// auto при недоступном Docker обязан тихо вернуть хост, а не упасть.
	t.Setenv("CODEGEN_SANDBOX", "auto")
	t.Setenv("CODEGEN_SANDBOX_DOCKER", filepath.Join(t.TempDir(), "no-such-docker"))
	cfg := loadSandboxConfig()
	if cfg.Mode != SandboxModeLocal && cfg.Mode != SandboxModeContainer {
		t.Errorf("auto: неожиданный режим %q", cfg.Mode)
	}
	if !dockerAvailable() {
		if cfg.Mode != SandboxModeLocal {
			t.Errorf("auto без Docker: ожидался хост, получен %q", cfg.Mode)
		}
	}
}

// Фолбэк должен быть ВИДЕН в результате: иначе «песочница» останется в
// документации, а выполнение пойдёт на хост без следа.
func TestRunCommandLocalReportsNoSandbox(t *testing.T) {
	t.Setenv("CODEGEN_SANDBOX", "local")
	t.Setenv("CODEGEN_RUN_TIMEOUT", "10s")
	res, err := runCommand("echo sandbox-check", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if res["sandbox"] != string(SandboxModeLocal) {
		t.Errorf("sandbox=%q, ожидалось %q", res["sandbox"], SandboxModeLocal)
	}
	if res["sandbox_note"] == "" {
		t.Error("хостовый запуск должен сопровождаться sandbox_note — иначе изоляция неотличима от «есть песочница»")
	}
	if !strings.Contains(res["stdout"], "sandbox-check") {
		t.Errorf("команда не выполнилась на хосте: %q", res["stdout"])
	}
}

// Контейнерный режим включается только явно. Конфигурацию задаём напрямую:
// loadSandboxConfig всегда подставляет образ по умолчанию (dev-образ песочницы
// из compose.yaml), поэтому «образа нет» проверяется на уровне spec.
func TestRunCommandSandboxContainerRefusesWithoutImage(t *testing.T) {
	dir := t.TempDir() // без манифестов — стек не определить
	res, err := runCommandSandbox("echo hi", dir, sandboxConfig{
		Mode: SandboxModeContainer, Image: "", Network: "default", Memory: "1g", CPUs: "1", AllowWrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res["status"] != "error" {
		t.Errorf("ожидалась ошибка конфигурации песочницы, получено %q", res["status"])
	}
	if res["sandbox"] != string(SandboxModeContainer) {
		t.Errorf("sandbox=%q, ожидалось %q", res["sandbox"], SandboxModeContainer)
	}
	if !strings.Contains(res["message"], "CODEGEN_SANDBOX_IMAGE") {
		t.Errorf("сообщение должно подсказывать, что задать образ: %q", res["message"])
	}
}

// То же самое для несуществующего рабочего каталога: docker смонтировал бы
// мусор, команда упала бы с невнятной ошибкой — конфигурацию надо ловить заранее.
func TestRunCommandContainerRefusesBadWorkdir(t *testing.T) {
	t.Setenv("CODEGEN_SANDBOX", "container")
	t.Setenv("CODEGEN_SANDBOX_IMAGE", "ai-sandbox:test")
	res, err := runCommand("echo hi", filepath.Join(t.TempDir(), "нет-такого-каталога"))
	if err != nil {
		t.Fatal(err)
	}
	if res["status"] != "error" || !strings.Contains(res["message"], "не найден") {
		t.Errorf("ожидалась ошибка «каталог не найден», получено %+v", res)
	}
}

// Аргументы docker run — то, что делает «песочницу» песочницей. Каждая важная
// гарантия проверяется списком: забытый --network или --read-only = тихая
// регрессия безопасности.
func TestDockerArgsContainIsolationFlags(t *testing.T) {
	dir := goProject(t)
	t.Setenv("CODEGEN_SANDBOX_IMAGE", "")
	spec, err := sandboxSpecFor("go test ./...", dir, sandboxConfig{Mode: SandboxModeContainer, Image: "", Network: "none", Memory: "2g", CPUs: "2", AllowWrite: true})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Image != "golang:1.24" {
		t.Errorf("образ по стеку Go: ожидался %q, получен %q", "golang:1.24", spec.Image)
	}
	args := strings.Join(dockerArgs(spec, "go test ./..."), " ")

	for _, want := range []string{
		"--rm",           // не копить мусор
		"--network none", // не host-сеть
		"--memory 2g",    // лимит памяти
		"--cpus 2",       // лимит CPU
		"--cap-drop ALL", // без привилегированных возможностей
		"--security-opt no-new-privileges",
		"-w " + spec.Dir + ":/workspace:rw",
		"--workdir /workspace",
		"golang:1.24",
		"sh -c go test ./...",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("docker run без %q: %s", want, args)
		}
	}
	if strings.Contains(args, "--privileged") || strings.Contains(args, "--network host") {
		t.Errorf("в песочнице недопустимы --privileged/--network host: %s", args)
	}
	if !strings.Contains(args, "--user "+sandboxHostUser()) && sandboxHostUser() != "" {
		t.Errorf("контейнер должен работать как non-root с UID/GID хоста: %s", args)
	}
}

// Без сети зависимости не скачать — команда получает подсказку, а модель не
// начинает перебирать варианты установки.
func TestDockerArgsNetworkNoneDisablesModuleFetch(t *testing.T) {
	dir := goProject(t)
	spec, err := sandboxSpecFor("go build ./...", dir, sandboxConfig{Mode: SandboxModeContainer, Image: "golang:1.24", Network: "none", Memory: "1g", CPUs: "1", AllowWrite: true})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Env["GOPROXY"] != "off" {
		t.Errorf("при --network none GOPROXY должен быть off, получено %q", spec.Env["GOPROXY"])
	}
	h := sandboxNetworkHelp()
	if !strings.Contains(h, "GOMODCACHE") || !strings.Contains(h, "офлайн") {
		t.Errorf("подсказка для режима без сети неполна: %q", h)
	}
}

// Режим «только чтение»: корень read-only, рабочий каталог НЕ монтируется
// (иначе запись всё равно проходит), временные каталоги — tmpfs, иначе
// go/node/py не запустятся.
func TestDockerArgsReadOnlyMode(t *testing.T) {
	dir := goProject(t)
	spec, err := sandboxSpecFor("go vet ./...", dir, sandboxConfig{Mode: SandboxModeContainer, Image: "golang:1.24", Network: "default", Memory: "1g", CPUs: "1", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(dockerArgs(spec, "go vet ./..."), " ")
	if !strings.Contains(args, "--read-only") {
		t.Errorf("ожидался --read-only: %s", args)
	}
	if strings.Contains(args, "/workspace:rw") {
		t.Errorf("в read-only режиме рабочий каталог не должен монтироваться на запись: %s", args)
	}
	if !strings.Contains(args, "--tmpfs /tmp:exec") {
		t.Errorf("нужен tmpfs /tmp, иначе тулчейн не запустится: %s", args)
	}
}

// Локальный исполнитель подменяется: тест проверяет маршрутизацию и
// обработку ошибки, не запуская ни shell, ни проект.
func TestRunCommandUsesInjectedLocalExecutor(t *testing.T) {
	t.Setenv("CODEGEN_RUN_TIMEOUT", "10s")
	// Каталог обязан существовать: exec с несуществующим Dir падает с
	// «fork/exec ...: no such file or directory», и тест врал бы про песочницу.
	workdir := t.TempDir()
	var gotCmd, gotDir string
	calls := 0
	res, err := runCommandSandbox("make test", workdir, sandboxConfig{
		Mode: SandboxModeLocal,
		LocalCommand: func(command, workdir string) (*exec.Cmd, error) {
			calls++
			gotCmd, gotDir = command, workdir
			// sh, а не echo: в части окружений /usr/bin/echo — битая ссылка
			// на coreutils из cargo, и exec на ней падает независимо от песочницы.
			return exec.CommandContext(context.Background(), "sh", "-c", "echo injected"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("подменённый исполнитель вызван %d раз, ожидался 1", calls)
	}
	if gotCmd != "make test" || gotDir != workdir {
		t.Errorf("исполнитель получил (%q, %q)", gotCmd, gotDir)
	}
	if !strings.Contains(res["stdout"], "injected") || res["status"] != "success" {
		t.Errorf("результат подменённого исполнителя не обработан: %+v", res)
	}
}

// Реальный запуск песочницы — условный тест: без Docker он молча пропускается
// (и это нормально: hermetic-проверки выше не зависят от демона).
func TestSandboxRealContainer(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("Docker недоступен — hermetic-проверки покрывают песочницу")
	}
	image := firstEnv("CODEGEN_SANDBOX_IMAGE", "CODEGEN_SANDBOX_TEST_IMAGE")
	if image == "" {
		image = "ai-sandbox:latest"
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Skipf("образ %s не собран локально (соберите его через compose.yaml песочницы)", image)
	}
	t.Setenv("CODEGEN_SANDBOX", "container")
	t.Setenv("CODEGEN_SANDBOX_IMAGE", image)
	t.Setenv("CODEGEN_RUN_TIMEOUT", "60s")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("sandbox-ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Изоляция рабочего каталога: файл из /workspace должен быть виден.
	res, err := runCommand("cat marker.txt", dir)
	if err != nil {
		t.Fatal(err)
	}
	if res["status"] != "success" || !strings.Contains(res["stdout"], "sandbox-ok") {
		t.Errorf("песочница не видит рабочий каталог: %+v", res)
	}
	// Изоляция сети и прав: пользователь контейнера — не root.
	res, err = runCommand("id -u", dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(res["stdout"]) == "0" {
		t.Errorf("команда выполнена от root: %+v", res)
	}
}

// Образ песочницы объявлен в трёх местах: дефолт в коде, hermetic-тест и
// sandbox/compose.yaml. Расхождение означает «тесты зелёные, а песочница
// работает на другом образе» — самый неприятный вид расхождения, потому что
// он невидим до первого реального запуска.
func TestSandboxComposeMatchesCodeDefaults(t *testing.T) {
	t.Setenv("CODEGEN_SANDBOX_IMAGE", "")
	if got := loadSandboxConfig().Image; got != defaultSandboxImage {
		t.Errorf("образ по умолчанию в коде = %q, тесты ждут %q", got, defaultSandboxImage)
	}

	composePath := filepath.Join("..", "sandbox", "compose.yaml")
	raw, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("нет compose песочницы (%s): %v", composePath, err)
	}
	compose := string(raw)
	for _, want := range []string{"image: " + defaultSandboxImage, "/workspace:rw", "cap_drop", "no-new-privileges"} {
		if !strings.Contains(compose, want) {
			t.Errorf("sandbox/compose.yaml не содержит %q", want)
		}
	}

	// Точка монтирования обязана совпадать с константой песочницы, иначе проект
	// в контейнере окажется не там.
	if !strings.Contains(compose, sandboxWorkspace+":rw") {
		t.Errorf("в compose ожидался монтаж %q, а он не согласован с константой sandboxWorkspace", sandboxWorkspace+":rw")
	}
	if _, err := os.Stat(filepath.Join("..", "sandbox", "Dockerfile")); err != nil {
		t.Errorf("нет Dockerfile песочницы: %v", err)
	}
}
