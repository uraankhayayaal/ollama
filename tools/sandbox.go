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
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"ai/sandbox"
)

// sandboxWorkspace — точка монтирования рабочего каталога внутри контейнера.
const sandboxWorkspace = "/workspace"

// sandboxTmp — каталог для HOME и всех кэшей (Go/npm/pip) внутри контейнера.
// Именно tmpfs: он существует в ЛЮБОМ образе (в том числе в стоковых
// golang:1.24/node:22/python:3.12), не переживает конец запуска и не оставляет
// в проекте мусор вида ~/.npmrc, который иначе попал бы в git diff.
// Источник истины — sandbox.Tmp (общий с сессионным контейнером).
const sandboxTmp = sandbox.Tmp

// defaultSandboxImage — dev-образ песочницы по умолчанию. Собирается из
// sandbox/Dockerfile (docker compose -f sandbox/compose.yaml build) и содержит
// тулчейн для монорепо. Для проекта одного известного стека дешевле стоковый
// образ — его выбирает sandboxImageFor.
const defaultSandboxImage = sandbox.DefaultImage

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

// SandboxMode — выбранный исполнитель команд: хост, эфемерный контейнер или
// сессионный контейнер (алиасы sandbox.Mode — один тип на весь пакет).
type SandboxMode = sandbox.Mode

const (
	SandboxModeLocal     = sandbox.ModeLocal
	SandboxModeContainer = sandbox.ModeContainer
	// SandboxModeSession — контейнер на сессию оркестрации (Этап 1): команды
	// идут через активный sandbox.Workspace, а не через docker run на команду.
	SandboxModeSession = sandbox.ModeSession
	// SandboxModeUnset — режим не определён: значит, определение само упало
	// и звать исполнитель рано.
	SandboxModeUnset = sandbox.ModeUnset
)

// sandboxConfig — разобранная конфигурация песочницы (тесты подменяют поля
// напрямую, поэтому здесь всё в одном месте и без скрытых глобалок).
// Алиас sandbox.Config: парсер один (sandbox.LoadConfig), эфемерный путь и
// сервер читают одни и те же поля.
//
// Ноль-значения безопасны: конфигурация без флагов (тест, ручной вызов) даёт
// работающую песочницу — проект смонтирован на запись, корень контейнера
// доступен на запись, кэши в tmpfs. Именно так, а не наоборот: обратная
// конфигурация даёт контейнер, в котором агент не видит ни одного файла
// проекта, и это выглядит как «песочница сломалась», а не как «нечего было
// запускать».
type sandboxConfig = sandbox.Config

// loadSandboxConfig читает CODEGEN_SANDBOX* и определяет исполнитель.
//
// По умолчанию — ХОСТ, и это осознанно. Пробный запуск с режимом auto показал,
// почему: «docker доступен» не значит «в контейнере всё работает». В песочницу
// уезжал ЛСП-чекер (он вызывает тот же runCommand) и падал на проектах без
// распознанного стека, потому что образ выбирался по манифесту, которого у
// чекера нет. Молчаливый перенос ВСЕГО исполнения (сборка, тесты, LSP) в
// контейнер — поведенческое изменение, ломающее проекты по неочевидной причине
// и бьющее по тем, кто Docker не просил. Поэтому песочница включается явно
// (CODEGEN_SANDBOX=container/session/auto), а пустое значение означает прежнее
// поведение.
//
// Разбор общий (sandbox.LoadConfig). Отличие этого гейта — ветка auto:
// здесь она определяется по CLI-бинарю (dockerAvailable, читает
// CODEGEN_SANDBOX_DOCKER и PATH), а не по Engine API: прежнее поведение и
// TestSandboxDisabledByDefault завязаны на «бинаря docker нет → хост».
func loadSandboxConfig() sandboxConfig {
	cfg := sandbox.LoadConfig()
	if strings.EqualFold(strings.TrimSpace(os.Getenv("CODEGEN_SANDBOX")), "auto") {
		if dockerAvailable() {
			cfg.Mode = SandboxModeContainer
		} else {
			cfg.Mode = SandboxModeLocal
		}
	}
	return cfg
}

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

// sandboxImageFor — образ по стеку проекта. Обёртка над sandbox.ImageFor:
// источник один для эфемерного пути и сессионного контейнера. Здесь
// учитываются и CODEGEN_SANDBOX_IMAGE, и его алиас CODEGEN_IMAGE.
func sandboxImageFor(dir string) string { return sandbox.ImageFor(dir) }

// ensureSandboxImageTimeout — лимит автосборки dev-образа. Отдельная граница,
// а не CODEGEN_RUN_TIMEOUT: сборка на холодном кэше (базовый образ + apt)
// длится дольше минутного командного шага, и таймаут сборки не должен
// зависеть от конфигурации шага.
const ensureSandboxImageTimeout = 10 * time.Minute

// ensureSandboxImage — гарантия наличия выбранного образа в кэше демона до
// docker run. Образ выбирается теми же правилами, что в sandboxSpecFor
// (явный → по стеку → dev-образ); автосборку делает sandbox.EnsureImage и
// только для локального dev-образа — реестровые образы docker тянет сам.
func ensureSandboxImage(workdir string, sb sandboxConfig) error {
	image := sb.Image
	if image == "" {
		image = sandboxImageFor(workdir)
	}
	if image == "" {
		image = defaultSandboxImage
	}
	ctx, cancel := context.WithTimeout(context.Background(), ensureSandboxImageTimeout)
	defer cancel()
	return sandbox.EnsureImage(ctx, image)
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

// sandboxEnv — окружение внутри контейнера. Обёртка над sandbox.BaseEnv:
// оно совпадает с sandbox/Dockerfile и sandbox/compose.yaml (HOME и все кэши
// в /tmp, чтобы рабочий каталог был единственным местом записи, а
// read-only-режим работал на любом образе).
func sandboxEnv() map[string]string { return sandbox.BaseEnv() }

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
// uid:gid для --user. Обёртка над sandbox.HostUser: os.Getuid/Getgid, а НЕ
// `id -u` (тот же идентификатор, что и у процесса Go, без четырёх лишних
// подпроцессов на каждый вызов sandboxSpecFor — а он зовётся дважды на
// команду: пре-флайт и сам запуск).
func sandboxHostUser() string { return sandbox.HostUser() }

// sandboxNetworkHelp — подсказка модели для режима без сети: что делать с
// зависимостями, чтобы не перебирать команды.
func sandboxNetworkHelp() string {
	return "Песочница запущена БЕЗ СЕТИ (CODEGEN_SANDBOX_NETWORK=none): загрузка зависимостей из интернета невозможна. " +
		"Работай с тем, что уже есть в проекте: vendor/, локальные кэши (GOMODCACHE/GOPATH/npm_config_cache см. текст ошибки), node_modules, .venv. " +
		"Если нужен модуль из сети — не повторяй команду, а укажи в отчёте, какой именно пакет недоступен офлайн."
}

// firstEnv — первый непустой env из списка имён (обёртка sandbox.FirstEnv).
func firstEnv(names ...string) string { return sandbox.FirstEnv(names...) }

// detectSandboxStack — стек проекта по манифестам (обёртка sandbox.DetectStack):
// тот же список манифестов, что у acceptor.detectKind и ReadAppLogs, иначе
// песочница и остальной конвейер считали бы один проект разными.
func detectSandboxStack(dir string) string { return sandbox.DetectStack(dir) }

// detectSandboxStacks — множество стеков на глубине не глубже depth
// (0 = только корень; обёртка sandbox.DetectStacks).
func detectSandboxStacks(dir string, depth int) map[string]bool {
	return sandbox.DetectStacks(dir, depth)
}
