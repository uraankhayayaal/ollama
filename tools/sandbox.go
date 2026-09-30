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
// Что делает песочница: монтирует рабочий каталог в контейнер (--volume, то
// есть bind-mount) и запускает команду там, от имени непривилегированного
// пользователя, без host-сети и с ограничением по ресурсам. Что НЕ делает: не
// меняет контракт инструмента Run — агент по-прежнему получает {command, stdout,
// stderr, status}.
//
// Чего песочница принципиально не даёт (и это важно не переоценивать):
//   - образы из реестра не изолированы по содержимому: доверять им нужно так
//     же, как к бинарям из apt. Для своего кода образ — доверенный;
//   - ПРОМЕНТЫ: рабочий каталог монтируется на ЗАПИСЬ, потому что агент по
//     заданию правит файлы проекта (тесты, артефакты сборки, gofmt). «Стена»
//     песочницы не спасла бы от `rm -rf /workspace` — том это файлы проекта.
//     Защита здесь не в изоляции, а в отбраковке DESTRUCTIVE-команд до запуска
//     и в ревью диффа;
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
	"sort"
	"strconv"
	"strings"
)

// sandboxWorkspace — точка монтирования рабочего каталога внутри контейнера.
const sandboxWorkspace = "/workspace"

// sandboxTmp — каталог для HOME и всех кэшей (Go/npm/pip) внутри контейнера.
// Именно tmpfs: он существует в ЛЮБОМ образе (в том числе в стоковых
// golang:1.24/node:22/python:3.12), не переживает конец запуска и не оставляет
// в проекте мусор вида ~/.npmrc, который иначе попал бы в git diff.
const sandboxTmp = "/tmp"

// defaultSandboxImage — dev-образ песочницы по умолчанию. Собирается из
// sandbox/Dockerfile (docker compose -f sandbox/compose.yaml build) и содержит
// тулчейн для монорепо. Для проекта одного известного стека дешевле стоковый
// образ — его выбирает sandboxImageFor.
const defaultSandboxImage = "ai-sandbox:latest"

// sandboxFallbackReason — результат без container/auto, когда Docker
// недоступен: агент должен знать, что команда шла на хосте, иначе «изоляция
// существует» останется в документации, а не в реальности.
const sandboxFallbackReason = "песочница недоступна: команда выполнена на хосте без изоляции (CODEGEN_SANDBOX=0/пропущен docker). " +
	"Уровень доверия к результату ниже, а команда имеет доступ к файловой системе хоста."

// sandboxSpec — параметры запуска команды в контейнере.
type sandboxSpec struct {
	Image   string
	Dir     string
	Mount   string // аргумент --volume: <dir>:/workspace:rw либо :ro
	Workdir string
	Network string
	// ReadOnly — read-only корневая ФС контейнера. Рабочий каталог при этом
	// монтируется отдельно (по Mount), поэтому агент по-прежнему видит проект.
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
//
// Ноль-значения безопасны: конфигурация без флагов (тест, ручной вызов) даёт
// работающую песочницу — проект смонтирован на запись, корень контейнера
// доступен на запись, кэши в tmpfs. Именно так, а не наоборот: обратная
// конфигурация даёт контейнер, в котором агент не видит ни одного файла
// проекта, и это выглядит как «песочница сломалась», а не как «нечего было
// запускать».
type sandboxConfig struct {
	Mode     SandboxMode
	Image    string
	Network  string
	Memory   string
	CPUs     string
	ReadOnly bool // read-only корневая ФС контейнера (CODEGEN_SANDBOX_RO)
	// WorkdirReadOnly — рабочий каталог монтируется только на чтение
	// (CODEGEN_SANDBOX_ALLOW_WRITE=false).
	WorkdirReadOnly bool
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
	// Образ по умолчанию НЕ подставляется здесь: sandboxSpecFor сначала
	// спрашивает стек проекта, и лишь для нераспознанного (или вовсе
	// безманифестного — как у ЛСП-чекера) берёт dev-образ. Раньше дефолт
	// ставился здесь, и из-за этого ветка выбора образа по стеку была
	// недостижимой: sandboxImageFor не вызывался никогда.
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
	// Два независимых уровня записи, по умолчанию оба разрешены:
	//   - CODEGEN_SANDBOX_RO=true — read-only корень контейнера. Рабочий
	//     каталог при этом всё равно доступен на запись (это файлы проекта,
	//     которые агент правит по заданию), зато тулчейн не может писать в слои
	//     образа. Режим для приёмки и «только посмотреть»;
	//   - CODEGEN_SANDBOX_ALLOW_WRITE=false — рабочий каталог монтируется
	//     :ro. Команда, которая пишет в проект, упадёт с error, а не сделает
	//     вид, что отработала.
	// Значение по умолчанию — «можно писать»: неинвертированное, иначе
	// пустая переменная выдавала запрет записи и песочница запускалась с
	// неработающим /workspace.
	if v := strings.TrimSpace(os.Getenv("CODEGEN_SANDBOX_RO")); v != "" {
		if ro, err := strconv.ParseBool(v); err == nil {
			cfg.ReadOnly = ro
		}
	}
	if v := strings.TrimSpace(os.Getenv("CODEGEN_SANDBOX_ALLOW_WRITE")); v != "" {
		if w, err := strconv.ParseBool(v); err == nil {
			cfg.WorkdirReadOnly = !w
		}
	}
	return cfg
}

// sandboxModeUnset — «режим не определён»: значит, определение само упало и
// звать исполнитель рано.
const SandboxModeUnset SandboxMode = ""

// sandboxDockerBin — путь к клиенту docker. CODEGEN_SANDBOX_DOCKER читается
// ДО LookPath: в CI и в тестах бинаря нет в PATH, а путь задан явно, и старая
// проверка всё равно отвечала «Docker недоступен», хотя dockerAvailable и
// sandboxCommand разрешали бинарь по-разному.
func sandboxDockerBin() (string, bool) {
	if v := strings.TrimSpace(os.Getenv("CODEGEN_SANDBOX_DOCKER")); v != "" {
		return v, true
	}
	path, err := exec.LookPath("docker")
	if err != nil {
		return "", false
	}
	return path, true
}

// dockerAvailable — доступен ли Docker для запуска контейнера.
func dockerAvailable() bool {
	if runtime.GOOS == "windows" {
		// На Windows имя бинаря и поведение песочницы другие; хостовый фолбэк
		// честнее, чем неверно собранный docker run.
		return false
	}
	path, ok := sandboxDockerBin()
	if !ok {
		return false
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
//
// Здесь учитываются и CODEGEN_SANDBOX_IMAGE, и его алиас CODEGEN_IMAGE:
// loadSandboxConfig читает оба, и расхождение означало бы, что образ из
// CODEGEN_IMAGE работает, а из sandboxImageFor — нет.
func sandboxImageFor(dir string) string {
	if v := firstEnv("CODEGEN_SANDBOX_IMAGE", "CODEGEN_IMAGE"); v != "" {
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
	// Путь рабочего каталога попадает в аргумент --volume «src:dst:opts»,
	// поэтому двоеточие или запятая в пути тихо ломают монтаж: контейнер
	// стартует и не видит проекта — ровно тот дефект, который здесь чинится.
	// Лучше явная ошибка конфигурации до запуска.
	if strings.ContainsAny(dir, ":,") {
		return sandboxSpec{}, fmt.Errorf("путь рабочего каталога %q содержит «:» или «,» и не переносится в --volume: перенесите проект в каталог без этих символов", dir)
	}
	// Порядок выбора образа: явно заданный → по стеку проекта → dev-образ
	// песочницы. Последний шаг обязателен: у ЛСП-чекера манифеста проекта нет,
	// и без него контейнерный режим был бы недостижим вовсе.
	image := cfg.Image
	if image == "" {
		image = sandboxImageFor(dir)
	}
	if image == "" {
		image = defaultSandboxImage
	}
	spec := sandboxSpec{
		Image: image,
		Dir:   dir,
		// Рабочий каталог монтируется ВСЕГДА. Раньше здесь стоял -w
		// (working directory) вместо -v (bind mount): контейнер стартовал с
		// несуществующим workdir и не видел ни одного файла проекта.
		Mount:    dir + ":" + sandboxWorkspace + sandboxMountMode(cfg.WorkdirReadOnly),
		Workdir:  sandboxWorkspace,
		Network:  cfg.Network,
		Memory:   cfg.Memory,
		CPUs:     cfg.CPUs,
		ReadOnly: cfg.ReadOnly,
		// Файлы в /workspace создаёт процесс с UID/GID хоста, иначе артефакты
		// сборки получат чужого владельца и следующий запуск агента (или
		// обычный rm) упрётся в права. Пользователь по умолчанию образа
		// (node/ubuntu) хосту не равен, поэтому --user подставляется всегда.
		User: sandboxHostUser(),
		Env:  sandboxEnv(),
	}
	if spec.Network == "none" {
		// Без сети go mod download/npm ci не пройдут — это ожидаемо, но
		// модель должна знать про кэш, иначе она будет перебирать команды.
		spec.Env["GOFLAGS"] = "-mod=mod"
		spec.Env["GOPROXY"] = "off"
	}
	// Кэши и HOME — в tmpfs: их не должно быть ни в слое образа, ни в
	// проекте. Именно поэтому HOME=/tmp, а не /workspace: иначе git и npm
	// раскладывают ~/.gitconfig и ~/.npmrc прямо в рабочий каталог, и они
	// попадают в diff, который потом ревьюит человек.
	spec.Extra = append(spec.Extra, "--tmpfs", sandboxTmp+":exec,mode=1777")
	if spec.ReadOnly {
		// При read-only корневой ФС /run тоже нужен на запись, иначе часть
		// инструментов падает на отсутствующем сокете.
		spec.Extra = append(spec.Extra, "--tmpfs", "/run:exec,mode=755")
	}
	return spec, nil
}

// sandboxMountMode — суффикс режима монтирования рабочего каталога.
func sandboxMountMode(readOnly bool) string {
	if readOnly {
		return ":ro"
	}
	return ":rw"
}

// sandboxEnv — окружение внутри контейнера. Совпадает с sandbox/Dockerfile и
// sandbox/compose.yaml: HOME и все кэши в /tmp, чтобы рабочий каталог был
// единственным местом записи, а read-only-режим работал на любом образе.
func sandboxEnv() map[string]string {
	return map[string]string{
		"HOME":                     sandboxTmp,
		"XDG_CACHE_HOME":           sandboxTmp + "/.cache",
		"GOCACHE":                  sandboxTmp + "/.cache/go-build",
		"GOMODCACHE":               sandboxTmp + "/go/pkg/mod",
		"GOPATH":                   sandboxTmp + "/go",
		"npm_config_cache":         sandboxTmp + "/.npm",
		"PIP_CACHE_DIR":            sandboxTmp + "/.cache/pip",
		"PLAYWRIGHT_BROWSERS_PATH": sandboxTmp + "/ms-playwright",
		"CI":                       "true",
		// Коммит из песочницы (через make-цели приёмки) не должен падать из-за
		// отсутствия ~/.gitconfig: HOME — tmpfs, глобального конфига в ней нет,
		// а без него git не может определить автора коммита.
		"GIT_CONFIG_GLOBAL":   sandboxTmp + "/.gitconfig",
		"GIT_AUTHOR_NAME":     "AI Sandbox",
		"GIT_AUTHOR_EMAIL":    "sandbox@localhost",
		"GIT_COMMITTER_NAME":  "AI Sandbox",
		"GIT_COMMITTER_EMAIL": "sandbox@localhost",
	}
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
	// --volume, а НЕ -w: -w задаёт рабочий каталог ВНУТРИ контейнера, монтаж
	// хоста делает только --volume. С -w проект не попадал в контейнер вовсе.
	args = append(args, "--volume", spec.Mount)
	if spec.ReadOnly {
		args = append(args, "--read-only")
	}
	if spec.User != "" {
		args = append(args, "--user", spec.User)
	}
	if len(spec.Env) > 0 {
		// Ключи сортируются: обход map в Go неупорядочен, а из-за этого
		// скакали аргументы в логах, ошибках и строгих тестах.
		keys := make([]string, 0, len(spec.Env))
		for k := range spec.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			args = append(args, "-e", k+"="+spec.Env[k])
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
	path, ok := sandboxDockerBin()
	if !ok {
		// Недостижимо: сюда попадают только из режима container, который
		// dockerAvailable уже проверил. Но «docker» вместо пути лучше паника.
		path = "docker"
	}
	return path, dockerArgs(spec, command), nil
}

// sandboxHostUser — идентификаторы текущего пользователя хоста в формате
// uid:gid для --user. Берутся из os.Getuid/Getgid, а НЕ из `id -u`: это тот же
// идентификатор, что и у процесса Go, без четырёх лишних подпроцессов на
// каждый вызов sandboxSpecFor (а он зовётся дважды на команду — пре-флайт и
// сам запуск). На Windows идентификаторов нет — возвращаем пустую строку, и
// dockerAvailable там всё равно всегда false.
func sandboxHostUser() string {
	uid, gid := os.Getuid(), os.Getgid()
	if uid < 0 || gid < 0 {
		return ""
	}
	return strconv.Itoa(uid) + ":" + strconv.Itoa(gid)
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
