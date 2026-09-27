package tools

// sandbox.go — контейнерная песочница выполнения команд агента (Ф-4).
//
// Зачем она нужна именно здесь. Команды из Run приходят от модели, а модель
// пишет код по ТЗ и в том числе код из задания, которое никто не читал.
// Выполнение на хосте означает три вещи: произвольный код с правами
// пользователя, доступ ко всей файловой системе (в том числе к ~/.ssh,
// ~/.aws и каталогам других проектов) и сетевой доступ во внутреннюю сеть.
// При этом тулчейн проекта почти всегда живёт в контейнере, и «не найдено
// go/node/pytest» на хосте — обычное дело, а не поломка.
//
// Что делает песочница: монтирует рабочий каталог в контейнер и запускает
// команду там, от имени непривилегированного пользователя, без host-сети и с
// ограничением по ресурсам. Что НЕ делает: не меняет контракт инструмента Run —
// агент по-прежнему получает {command, stdout, stderr, status}.
//
// Чего песочница принципиально не даёт (и это важно не переоценивать):
//   - образы из реестра не изолированы по содержимому: доверять им нужно так
//     же, как к бинарям из apt. Для своего кода образ — доверенный;
//   - ПРОМЕНТЫ: изоляция файловой системы держится на --read-only, а
//     «-w /workspace» — это запись в рабочий каталог проекта, то есть в
//     файлы, которые агент и так правит по заданию. Защита здесь не в стене
//     песочницы, а в ревью диффа: DESTRUCTIVE-команды отбрасываются до
//     запуска, а результат показывается агенту;
//   - сеть: по умолчанию она ЕСТЬ (go mod download, npm ci без этого не
//     работают). CODEGEN_SANDBOX_NETWORK=none отключает её полностью; без сети
//     нужен локальный кэш модулей (см. sandboxNetworkHelp).
//
// Активация (явная): CODEGEN_SANDBOX=container — всегда контейнер (нет Docker
// → ошибка конфигурации, НЕ тихий хост), =auto — контейнер при доступном
// Docker, иначе хост с пометкой, =local/0 — хост (поведение по умолчанию).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// sandboxWorkspace — точка монтирования рабочего каталога внутри контейнера.
const sandboxWorkspace = "/workspace"

// defaultSandboxImage — dev-образ песочницы по умолчанию. Собирается из
// sandbox/Dockerfile (docker compose -f sandbox/compose.yaml build) и содержит
// тулчейн для монорепо. Для проекта одного стека дешевле стоковый образ по
// стеку — его выбирает sandboxImageFor.
const defaultSandboxImage = "ai-sandbox:latest"

// sandboxFallbackReason — результат без container/auto, когда Docker
// недоступен: агент должен знать, что команда шла на хосте, иначе «изоляция
// существует» останется в документации, а не в реальности.
const sandboxFallbackReason = "песочница недоступна: команда выполнена на хосте без изоляции (CODEGEN_SANDBOX=0/пропущен docker). " +
	"Уровень доверия к результату ниже, а команда имеет доступ к файловой системе хоста."

// sandboxSpec — параметры запуска команды в контейнере.
type sandboxSpec struct {
	Image    string
	Dir      string
	Workdir  string
	Network  string
	ReadOnly bool
	Memory   string
	CPUs     string
	Extra    []string
	User     string
	Env      map[string]string
}

// SandboxMode — выбранный исполнитель команд: хост или контейнер.
type SandboxMode string

const (
	SandboxModeLocal     SandboxMode = "local"
	SandboxModeContainer SandboxMode = "container"
)

// sandboxConfig — разобранная конфигурация песочницы (тесты подменяют поля
// напрямую, поэтому здесь всё в одном месте и без скрытых глобалок).
type sandboxConfig struct {
	Mode       SandboxMode
	Image      string
	Network    string
	Memory     string
	CPUs       string
	ReadOnly   bool
	AllowWrite bool
	// LocalCommand — исполнитель на хосте. Продакшн всегда nil (тогда
	// exec.Command), в тестах подменяется заглушкой: hermetic-тесты не должны
	// дёргать shell.
	LocalCommand func(command, workdir string) (*exec.Cmd, error)
}

// loadSandboxConfig читает CODEGEN_SANDBOX* и определяет исполнитель.
//
// По умолчанию — ХОСТ, и это осознанно. Пробный запуск с режимом auto показал,
// почему: «docker доступен» не значит «в контейнере всё работает». В песочницу
// уезжал ЛСП-чекер (он вызывает тот же runCommand) и падал на проектах без
// распознанного стека, потому что образ выбирался по манифесту, которого у
// чекера нет. Молчаливый перенос ВСЕГО исполнения (сборка, тесты, LSP) в
// контейнер — поведенческое изменение, ломающее проекты по неочевидной причине
// и бьющее по тем, кто Docker не просил. Поэтому песочница включается явно
// (CODEGEN_SANDBOX=container/auto), а пустое значение означает прежнее поведение.
//
// auto отличается от container тем, что при недоступном Docker не падает, а
// возвращается на хост с явной пометкой sandbox-полями в результате.
func loadSandboxConfig() sandboxConfig {
	cfg := sandboxConfig{Mode: SandboxModeLocal}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODEGEN_SANDBOX"))) {
	case "1", "on", "true", "yes", "container", "docker":
		cfg.Mode = SandboxModeContainer
	case "auto":
		if dockerAvailable() {
			cfg.Mode = SandboxModeContainer
		} else {
			cfg.Mode = SandboxModeLocal
		}
	case "", "0", "off", "false", "no", "local", "host":
		cfg.Mode = SandboxModeLocal
	default:
		// Неизвестное значение — не угадываем: хост с честной пометкой.
		cfg.Mode = SandboxModeLocal
	}
	cfg.Image = firstEnv("CODEGEN_SANDBOX_IMAGE", "CODEGEN_IMAGE")
	if cfg.Image == "" {
		cfg.Image = defaultSandboxImage
	}
	cfg.Network = firstEnv("CODEGEN_SANDBOX_NETWORK", "CODEGEN_SANDBOX_NET")
	switch cfg.Network {
	case "", "default", "host":
		cfg.Network = "default"
	case "none":
		cfg.Network = "none"
	case "bridge":
		cfg.Network = "bridge"
	}
	cfg.Memory = firstEnv("CODEGEN_SANDBOX_MEMORY", "CODEGEN_MEMORY")
	if cfg.Memory == "" {
		cfg.Memory = "2g"
	}
	cfg.CPUs = firstEnv("CODEGEN_SANDBOX_CPUS", "CODEGEN_CPUS")
	if cfg.CPUs == "" {
		cfg.CPUs = "2"
	}
	// Запись в рабочий каталог — норма для агента (тесты, артефакты сборки),
	// поэтому по умолчанию она разрешена, но выключается флагом: режимы
	// «только прочитать» нужны для проверок и приёмки.
	if v := strings.TrimSpace(os.Getenv("CODEGEN_SANDBOX_RO")); v != "" {
		if ro, err := strconv.ParseBool(v); err == nil {
			cfg.ReadOnly = ro
		}
	}
	if v := strings.TrimSpace(os.Getenv("CODEGEN_SANDBOX_ALLOW_WRITE")); v != "" {
		if w, err := strconv.ParseBool(v); err == nil {
			cfg.AllowWrite = !w
		}
	}
	return cfg
}

// sandboxModeUnset — «режим не определён»: значит, определение само упало и
// звать исполнитель рано.
const SandboxModeUnset SandboxMode = ""

// dockerAvailable — доступен ли Docker для запуска контейнера.
func dockerAvailable() bool {
	if runtime.GOOS == "windows" {
		// На Windows имя бинаря и поведение песочницы другие; хостовый фолбэк
		// честнее, чем неверно собранный docker run.
		return false
	}
	path, err := exec.LookPath("docker")
	if err != nil {
		return false
	}
	if v := strings.TrimSpace(os.Getenv("CODEGEN_SANDBOX_DOCKER")); v != "" {
		path = v
	}
	// Наличие бинаря недостаточно: демон может быть не запущен. Короткая
	// проверка версии — дешёвая и не тянет образы.
	cmd := exec.Command(path, "version", "--format", "{{.Server.Version}}")
	cmd.Env = dockerEnv()
	if out, err := cmd.Output(); err != nil || strings.TrimSpace(string(out)) == "" {
		return false
	}
	return true
}

// dockerEnv — окружение для docker: путь к сокету и настройки из
// CODEGEN_DOCKER_HOST/CODEGEN_SANDBOX_DOCKER_* (в тестах и в CI демон часто
// не в /var/run/docker.sock).
func dockerEnv() []string {
	env := os.Environ()
	for k, v := range map[string]string{
		"CODEGEN_DOCKER_HOST":  "DOCKER_HOST",
		"CODEGEN_DOCKER_TLS":   "DOCKER_TLS_VERIFY",
		"CODEGEN_DOCKER_CERT":  "DOCKER_CERT_PATH",
		"CODEGEN_SANDBOX_BIN":  "PATH_SANDBOX",
		"CODEGEN_SANDBOX_USER": "SANDBOX_UID",
	} {
		if v2 := strings.TrimSpace(os.Getenv(k)); v2 != "" {
			env = append(env, v+"="+v2)
		}
	}
	return env
}

// sandboxImageFor — образ по стеку проекта: тот же список, что у
// acceptor.detectKind и ReadAppLogs, иначе песочница и остальной конвейер
// будут считать один и тот же проект разными.
func sandboxImageFor(dir string) string {
	if v := strings.TrimSpace(os.Getenv("CODEGEN_SANDBOX_IMAGE")); v != "" {
		return v
	}
	switch detectSandboxStack(dir) {
	case "go":
		return "golang:1.24"
	case "node":
		return "node:22"
	case "python":
		return "python:3.12"
	case "php":
		return "php:8.3-cli"
	}
	return ""
}

// sandboxSpecFor собирает параметры docker run для команды агента. Чистая
// функция (без запуска): тесты проверяют именно её — что не утекает
// произвольный путь в аргументы, что кавычки экранированы, что опасные
// команды отбрасываются до старта контейнера.
func sandboxSpecFor(command, workdir string, cfg sandboxConfig) (sandboxSpec, error) {
	if reason, unsafe := destructiveCommandReason(command); unsafe {
		return sandboxSpec{}, fmt.Errorf("%s", reason)
	}
	dir, err := filepath.Abs(workdir)
	if err != nil {
		return sandboxSpec{}, fmt.Errorf("рабочий каталог %q недоступен: %w", workdir, err)
	}
	// Каталог должен существовать: docker смонтирует и путь, но команда
	// упадёт с невнятным «not a directory».
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return sandboxSpec{}, fmt.Errorf("рабочий каталог %q не найден", workdir)
	}
	image := cfg.Image
	if image == "" {
		image = sandboxImageFor(dir)
	}
	spec := sandboxSpec{
		Image:   image,
		Dir:     dir,
		Workdir: sandboxWorkspace,
		Network: cfg.Network,
		Memory:  cfg.Memory,
		CPUs:    cfg.CPUs,
		// Пользователь контейнера по умолчанию (node/ubuntu) не совпадает с
		// UID хоста: файлы, созданные в /workspace, получают чужой владелец и
		// следующий агентский запуск (или удаление) упирается в права. Поэтому
		// USER подставляется всегда, синхронизируя UID/GID с хостом.
		User:     sandboxHostUser(),
		Env:      map[string]string{"HOME": sandboxWorkspace, "GOCACHE": "/tmp/gocache", "GOMODCACHE": "/tmp/gomodcache", "GOPATH": "/tmp/gopath", "XDG_CACHE_HOME": "/tmp/.cache", "npm_config_cache": "/tmp/.npm", "PIP_CACHE_DIR": "/tmp/.cache/pip"},
		ReadOnly: !cfg.AllowWrite || cfg.ReadOnly,
	}
	if spec.Image == "" {
		return sandboxSpec{}, fmt.Errorf("не удалось определить образ песочницы для проекта: добавь CODEGEN_SANDBOX_IMAGE или манифест (go.mod/package.json/requirements.txt/composer.json)")
	}
	if spec.Network == "none" {
		// Без сети go mod download/npm ci не пройдут — это ожидаемо, но
		// модель должна знать про кэш, иначе она будет перебирать команды.
		spec.Env["GOFLAGS"] = "-mod=mod"
		spec.Env["GOPROXY"] = "off"
	}
	if cfg.ReadOnly || !cfg.AllowWrite {
		// Read-only корневая ФС: то, что нужно для работы, живёт во
		// временных каталогах (--tmpfs), иначе go/node/py не запустятся вовсе.
		spec.Extra = append(spec.Extra,
			"--tmpfs", "/tmp:exec,mode=1777",
			"--tmpfs", "/run:exec,mode=755",
		)
	}
	if uid := sandboxHostUID(); uid != "" {
		spec.Extra = append(spec.Extra, "--user", uid+":"+sandboxHostGID())
	}
	return spec, nil
}

// dockerArgs — аргументы docker run для spec и команды агента. Порядок и
// значения зафиксированы тестом: регрессия здесь (забытый --network, лишний
// --privileged) означает «песочница» в документации и полный доступ в
// реальности.
func dockerArgs(spec sandboxSpec, command string) []string {
	args := []string{"run", "--rm", "-i", "--init",
		"--network", spec.Network,
		"--memory", spec.Memory,
		"--cpus", spec.CPUs,
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
	}
	args = append(args, spec.Extra...)
	if spec.ReadOnly {
		args = append(args, "--read-only")
	} else {
		args = append(args, "-w", spec.Dir+":/workspace"+":rw")
	}
	if spec.User != "" {
		args = append(args, "--user", spec.User)
	}
	if len(spec.Env) > 0 {
		for k, v := range spec.Env {
			args = append(args, "-e", k+"="+v)
		}
	}
	args = append(args, "--workdir", spec.Workdir, spec.Image)
	args = append(args, "sh", "-c", command)
	return args
}

// sandboxCommand — команда контейнерного запуска (без исполнения): удобно для
// тестов и для диагностических подсказок модели.
func sandboxCommand(command, workdir string, cfg sandboxConfig) (string, []string, error) {
	spec, err := sandboxSpecFor(command, workdir, cfg)
	if err != nil {
		return "", nil, err
	}
	path := strings.TrimSpace(os.Getenv("CODEGEN_SANDBOX_DOCKER"))
	if path == "" {
		path = "docker"
	}
	return path, dockerArgs(spec, command), nil
}

// sandboxHostUID/GID/USER — идентификаторы текущего пользователя хоста.
func sandboxHostUID() string { return hostID("-u") }
func sandboxHostGID() string { return hostID("-g") }

func sandboxHostUser() string {
	uid, gid := sandboxHostUID(), sandboxHostGID()
	if uid == "" || gid == "" {
		return ""
	}
	return uid + ":" + gid
}

func hostID(flag string) string {
	out, err := exec.Command("id", flag).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// sandboxNetworkHelp — подсказка модели для режима без сети: что делать с
// зависимостями, чтобы не перебирать команды.
func sandboxNetworkHelp() string {
	return "Песочница запущена БЕЗ СЕТИ (CODEGEN_SANDBOX_NETWORK=none): загрузка зависимостей из интернета невозможна. " +
		"Работай с тем, что уже есть в проекте: vendor/, локальные кэши (GOMODCACHE/GOPATH/npm_config_cache см. текст ошибки), node_modules, .venv. " +
		"Если нужен модуль из сети — не повторяй команду, а укажи в отчёте, какой именно пакет недоступен офлайн."
}

// firstEnv — первый непустой env из списка имён.
func firstEnv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// detectSandboxStack — минимальное определение стека по манифестам в корне
// проекта. Отдельная копия acceptor.detectKind, а не импорт: tools не должен
// зависеть от агентов (acceptor сам импортирует tools), иначе получается цикл.
func detectSandboxStack(dir string) string {
	for name, stack := range map[string]string{
		"go.mod":           "go",
		"package.json":     "node",
		"requirements.txt": "python",
		"pyproject.toml":   "python",
		"composer.json":    "php",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return stack
		}
	}
	return ""
}
