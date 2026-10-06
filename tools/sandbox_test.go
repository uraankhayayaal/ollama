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
	if !dockerAvailable() {
		if cfg.Mode != SandboxModeLocal {
			t.Errorf("auto без Docker: ожидался хост, получен %q", cfg.Mode)
		}
	}
}

// Пустой CODEGEN_SANDBOX_ALLOW_WRITE обязан означать «запись РАЗРЕШЕНА».
// Инвертированная семантика давала контейнер с read-only /workspace, в
// котором агент не видел ни одного файла проекта: выглядит как «песочница
// сломалась», а на деле это был запрет записи по умолчанию.
func TestSandboxWriteAllowedByDefault(t *testing.T) {
	t.Setenv("CODEGEN_SANDBOX_ALLOW_WRITE", "")
	if cfg := loadSandboxConfig(); cfg.WorkdirReadOnly {
		t.Error("без CODEGEN_SANDBOX_ALLOW_WRITE рабочий каталог должен быть доступен на запись")
	}
	t.Setenv("CODEGEN_SANDBOX_ALLOW_WRITE", "true")
	if cfg := loadSandboxConfig(); cfg.WorkdirReadOnly {
		t.Error("CODEGEN_SANDBOX_ALLOW_WRITE=true обязан разрешать запись")
	}
	t.Setenv("CODEGEN_SANDBOX_ALLOW_WRITE", "false")
	if cfg := loadSandboxConfig(); !cfg.WorkdirReadOnly {
		t.Error("CODEGEN_SANDBOX_ALLOW_WRITE=false обязан запрещать запись в рабочий каталог")
	}
}

// Ровно так же, но на уровне аргументов docker: проект смонтирован всегда,
// по ХОСТОВОМУ пути (иначе cd <путь из workdir> и git-worktree в контейнере не
// работают), а режим монтирования переключается между :rw и :ro.
func TestSandboxMountsWorkdirReadWriteByDefault(t *testing.T) {
	dir := goProject(t)
	spec, err := sandboxSpecFor("go test ./...", dir, sandboxConfig{Mode: SandboxModeContainer, Image: "golang:1.24", Network: "default", Memory: "1g", CPUs: "1"})
	if err != nil {
		t.Fatal(err)
	}
	want := dir + ":" + dir + ":rw"
	if len(spec.Mounts) != 1 || spec.Mounts[0] != want {
		t.Errorf("монтирование рабочего каталога: ожидался ровно [%q], получено %q", want, spec.Mounts)
	}
	// workdir контейнера обязан совпадать с путём, который модели сообщает
	// result["workdir"]: иначе cd из полученного пути отвечает «can't cd».
	if spec.Workdir != dir {
		t.Errorf("workdir контейнера: ожидался %q, получен %q", dir, spec.Workdir)
	}
	joined := strings.Join(dockerArgs(spec, "go test ./..."), " ")
	if strings.Contains(joined, "-w "+dir+":"+dir) {
		t.Error("docker run не должен получать -w с аргументом монтажа: -w задаёт workdir, а не том")
	}
	if !strings.Contains(joined, "--volume "+want) {
		t.Errorf("ожидался --volume %q", want)
	}
	if !strings.Contains(joined, "--workdir "+dir) {
		t.Errorf("ожидался --workdir %q", dir)
	}

	ro, err := sandboxSpecFor("go test ./...", dir, sandboxConfig{Mode: SandboxModeContainer, Image: "golang:1.24", Network: "default", Memory: "1g", CPUs: "1", WorkdirReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if wantRO := dir + ":" + dir + ":ro"; len(ro.Mounts) != 1 || ro.Mounts[0] != wantRO {
		t.Errorf("при запрете записи ожидался [%q], получено %q", wantRO, ro.Mounts)
	}
}

// worktreeLayout раскладывает «главный клон + worktree задачи» без git:
// sandboxGitMounts читает только файлы .git и commondir, поэтому настоящий
// репозиторий не нужен — тест остаётся hermetic (без git, без Docker).
//
// Возвращает каталог worktree (рабочий каталог задачи) и общий каталог git'а
// главного клона.
func worktreeLayout(t *testing.T, mode string) (string, string) {
	t.Helper()
	root := t.TempDir()
	mainDir := filepath.Join(root, "proj")
	common := filepath.Join(mainDir, ".git")
	gitdir := filepath.Join(common, "worktrees", "wt1")
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, ".wt-proj-QAL-01")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	// gitdir в файле .git — как пишет git worktree add: абсолютный путь.
	target := gitdir
	if mode == "relative" {
		rel, err := filepath.Rel(wt, gitdir)
		if err != nil {
			t.Fatal(err)
		}
		target = rel
	}
	gitfile := "gitdir: " + target + "\n"
	if mode == "sep-inside" {
		// git init --separate-git-dir, указывающий внутрь самого каталога:
		// монтировать отдельно нечего.
		target = filepath.Join(wt, "mygit")
		gitfile = "gitdir: " + target + "\n"
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "missing" {
		gitfile = "gitdir: " + filepath.Join(root, "gone", ".git") + "\n"
	}
	if mode == "colon" {
		// Путь с двоеточием не переносится в --volume: каталог существует
		// (иначе сработала бы более ранняя проверка на существование), но том
		// не добавляется.
		target = filepath.Join(root, "main:clone", ".git")
		gitfile = "gitdir: " + target + "\n"
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte(gitfile), 0o644); err != nil {
		t.Fatal(err)
	}
	// commondir — только у worktree; у отдельного gitdir его нет, и тогда
	// gitdir сам является репозиторием.
	if mode != "sep-inside" && mode != "missing" && mode != "colon" {
		if err := os.WriteFile(filepath.Join(gitdir, "commondir"), []byte("../..\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return wt, common
}

// Worktree задачи — это чужой каталог git'а: .git внутри него файл со ссылкой,
// а объекты и refs живут в главном клоне. Песочница монтирует и то, и другое,
// иначе git внутри контейнера отвечает «not a git repository» (живой случай:
// QAL-01). Обычный клон и битая ссылка лишних томов не получают: docker
// создал бы по пути пустой каталог.
func TestSandboxWorktreeMountsGitCommonDir(t *testing.T) {
	specFor := func(t *testing.T, dir string, readOnly bool) sandboxSpec {
		t.Helper()
		spec, err := sandboxSpecFor("git status", dir, sandboxConfig{
			Mode: SandboxModeContainer, Image: "golang:1.24", Network: "default",
			Memory: "1g", CPUs: "1", WorkdirReadOnly: readOnly,
		})
		if err != nil {
			t.Fatal(err)
		}
		return spec
	}

	t.Run("worktree", func(t *testing.T) {
		wt, common := worktreeLayout(t, "abs")
		spec := specFor(t, wt, false)
		want := []string{wt + ":" + wt + ":rw", common + ":" + common + ":rw"}
		if strings.Join(spec.Mounts, "\n") != strings.Join(want, "\n") {
			t.Errorf("ожидались тома %q, получено %q", want, spec.Mounts)
		}
		joined := strings.Join(dockerArgs(spec, "git status"), " ")
		for _, m := range want {
			if !strings.Contains(joined, "--volume "+m) {
				t.Errorf("docker run без --volume %q: %s", m, joined)
			}
		}
	})

	t.Run("worktree relative gitdir", func(t *testing.T) {
		wt, common := worktreeLayout(t, "relative")
		spec := specFor(t, wt, false)
		if len(spec.Mounts) != 2 || spec.Mounts[1] != common+":"+common+":rw" {
			t.Errorf("относительный gitdir должен разрешаться к %q, получено %q", common, spec.Mounts)
		}
	})

	t.Run("read-only", func(t *testing.T) {
		wt, common := worktreeLayout(t, "abs")
		spec := specFor(t, wt, true)
		want := wt + ":" + wt + ":ro"
		if len(spec.Mounts) != 2 || spec.Mounts[0] != want || spec.Mounts[1] != common+":"+common+":ro" {
			t.Errorf("запрет записи должен покрыть оба тома, получено %q", spec.Mounts)
		}
	})

	t.Run("обычный клон", func(t *testing.T) {
		dir := goProject(t)
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		spec := specFor(t, dir, false)
		want := dir + ":" + dir + ":rw"
		if len(spec.Mounts) != 1 || spec.Mounts[0] != want {
			t.Errorf("у обычного клона .git — каталог, лишний том не нужен: %q", spec.Mounts)
		}
	})

	for _, mode := range []string{"sep-inside", "missing", "colon"} {
		t.Run("без лишнего тома: "+mode, func(t *testing.T) {
			wt, _ := worktreeLayout(t, mode)
			spec := specFor(t, wt, false)
			if len(spec.Mounts) != 1 {
				t.Errorf("ожидался только монтаж рабочего каталога, получено %q", spec.Mounts)
			}
		})
	}
}

// Образ выбирается по цепочке «явно заданный → стек проекта → dev-образ».
// Последний шаг обязателен: у ЛСП-чекера манифеста нет, и без него
// контейнерный режим был бы недостижим (именно из-за этого раньше дефолтный
// образ ставился в loadSandboxConfig, и ветка выбора по стеку была мёртвой).
func TestSandboxImageFallbackChain(t *testing.T) {
	dir := goProject(t)
	t.Setenv("CODEGEN_SANDBOX_IMAGE", "")
	t.Setenv("CODEGEN_IMAGE", "")

	spec, err := sandboxSpecFor("go test ./...", dir, sandboxConfig{Mode: SandboxModeContainer, Network: "default", Memory: "1g", CPUs: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Image != "golang:1.24" {
		t.Errorf("проект на Go без заданного образа: ожидался %q, получен %q", "golang:1.24", spec.Image)
	}

	// Алиас CODEGEN_IMAGE работает наравне с CODEGEN_SANDBOX_IMAGE.
	t.Setenv("CODEGEN_IMAGE", "alpine:3.20")
	if got := sandboxImageFor(dir); got != "alpine:3.20" {
		t.Errorf("CODEGEN_IMAGE должен учитываться при выборе образа, получено %q", got)
	}
	t.Setenv("CODEGEN_SANDBOX_IMAGE", "explicit:1")
	if got := sandboxImageFor(dir); got != "explicit:1" {
		t.Errorf("CODEGEN_SANDBOX_IMAGE должен перекрывать стек, получено %q", got)
	}

	// Каталог без манифестов (как у ЛСП-чекера) → dev-образ, а не ошибка.
	t.Setenv("CODEGEN_IMAGE", "")
	t.Setenv("CODEGEN_SANDBOX_IMAGE", "")
	bare := t.TempDir()
	spec, err = sandboxSpecFor("cat marker.txt", bare, sandboxConfig{Mode: SandboxModeContainer, Network: "default", Memory: "1g", CPUs: "1"})
	if err != nil {
		t.Fatalf("проект без манифестов обязан запускаться на dev-образе: %v", err)
	}
	if spec.Image != defaultSandboxImage {
		t.Errorf("ожидался dev-образ %q, получен %q", defaultSandboxImage, spec.Image)
	}
}

// Путь с двоеточием или запятой ломает разбор --volume «src:dst:opts» молча:
// контейнер стартует и не видит проекта. Лучше отказ до запуска.
func TestSandboxRejectsUnmountablePath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "проект:с-двоеточием")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := sandboxSpecFor("ls", dir, sandboxConfig{Mode: SandboxModeContainer, Image: "golang:1.24", Network: "default", Memory: "1g", CPUs: "1"})
	if err == nil || !strings.Contains(err.Error(), "--volume") {
		t.Errorf("ожидался отказ по пути каталога с «:», получено %v", err)
	}
}

// Кэши и HOME в tmpfs, а не в рабочем каталоге: иначе git/npm оставляют
// ~/.gitconfig и ~/.npmrc прямо в проекте, и они попадают в ревьюируемый diff.
func TestSandboxEnvKeepsHomeOutOfWorkdir(t *testing.T) {
	env := sandboxEnv()
	if env["HOME"] != sandboxTmp {
		t.Errorf("HOME=%q, ожидался %q: иначе конфиги тулчейна попадут в проект", env["HOME"], sandboxTmp)
	}
	for _, k := range []string{"GOCACHE", "GOMODCACHE", "GOPATH", "npm_config_cache", "PIP_CACHE_DIR", "XDG_CACHE_HOME"} {
		if !strings.HasPrefix(env[k], sandboxTmp) {
			t.Errorf("%s=%q должен быть внутри %s, иначе кэш осядет в проекте", k, env[k], sandboxTmp)
		}
	}
	// Коммит из песочницы не должен падать из-за отсутствия ~/.gitconfig.
	if env["GIT_CONFIG_GLOBAL"] == "" || env["GIT_AUTHOR_EMAIL"] == "" || env["GIT_COMMITTER_EMAIL"] == "" {
		t.Errorf("нужна git-идентификация для make-целей приёмки: %+v", env)
	}
}

// Фолбэк должен быть ВИДЕН в результате: иначе «песочница» останется в
// документации, а выполнение пойдёт на хост без следа.
func TestRunCommandLocalReportsNoSandbox(t *testing.T) {
	t.Setenv("CODEGEN_SANDBOX", "local")
	t.Setenv("CODEGEN_RUN_TIMEOUT", "10s")
	res, err := runCommand("echo sandbox-check", t.TempDir(), "")
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

// Контейнерный режим включается только явно, и конфигурацию ловит пре-флайт
// ДО запуска: несуществующий рабочий каталог — это ошибка конфигурации, а не
// падение команды с невнятным «not a directory».
func TestRunCommandContainerRefusesBadWorkdir(t *testing.T) {
	t.Setenv("CODEGEN_SANDBOX", "container")
	t.Setenv("CODEGEN_SANDBOX_IMAGE", "ai-sandbox:test")
	res, err := runCommand("echo hi", filepath.Join(t.TempDir(), "нет-такого-каталога"), "")
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
	spec, err := sandboxSpecFor("go test ./...", dir, sandboxConfig{Mode: SandboxModeContainer, Image: "", Network: "none", Memory: "2g", CPUs: "2"})
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
		"--volume " + spec.Dir + ":" + spec.Dir + ":rw", // проект ВИДЕН контейнеру
		"--workdir " + spec.Dir,                         // и по тому же пути
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
	// --user ровно один: раньше он попадал и в spec.Extra, и отдельно.
	if n := strings.Count(args, "--user "); n > 1 {
		t.Errorf("--user продублирован %d раз: %s", n, args)
	}
	if !strings.Contains(args, "--user "+sandboxHostUser()) && sandboxHostUser() != "" {
		t.Errorf("контейнер должен работать как non-root с UID/GID хоста: %s", args)
	}
}

// Порядок аргументов -e детерминирован: обход map неупорядочен, а из-за этого
// скакали логи, сообщения об ошибках и строгие тесты.
func TestDockerArgsEnvOrderIsStable(t *testing.T) {
	dir := goProject(t)
	spec, err := sandboxSpecFor("go test ./...", dir, sandboxConfig{Mode: SandboxModeContainer, Image: "golang:1.24", Network: "none", Memory: "1g", CPUs: "1"})
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Join(dockerArgs(spec, "go test ./..."), " ")
	for i := 0; i < 8; i++ {
		if got := strings.Join(dockerArgs(spec, "go test ./..."), " "); got != first {
			t.Fatalf("порядок аргументов плавает между вызовами:\n%s\n%s", first, got)
		}
	}
}

// Без сети зависимости не скачать — команда получает подсказку, а модель не
// начинает перебирать варианты установки.
func TestDockerArgsNetworkNoneDisablesModuleFetch(t *testing.T) {
	dir := goProject(t)
	spec, err := sandboxSpecFor("go build ./...", dir, sandboxConfig{Mode: SandboxModeContainer, Image: "golang:1.24", Network: "none", Memory: "1g", CPUs: "1"})
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

// Два независимых уровня записи, и их важно не путать:
//
//	CODEGEN_SANDBOX_RO=true        → read-only КОРЕНЬ контейнера, проект :rw
//	CODEGEN_SANDBOX_ALLOW_WRITE=0  → проект :ro
//
// Раньше read-only убирал монтирование вовсе, и агент в «безопасном» режиме
// не видел ни одного файла проекта.
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
	// Проект остаётся доступен НА ЗАПИСЬ: это файлы, которые агент правит.
	if !strings.Contains(args, "--volume "+dir+":"+dir+":rw") {
		t.Errorf("read-only корня не должен отнимать запись в рабочий каталог: %s", args)
	}
	if !strings.Contains(args, "--tmpfs "+sandboxTmp+":exec") {
		t.Errorf("нужен tmpfs %s, иначе тулчейн не запустится: %s", sandboxTmp, args)
	}
	if !strings.Contains(args, "--tmpfs /run:exec") {
		t.Errorf("при read-only корневой ФС нужен tmpfs /run: %s", args)
	}

	// А вот запрет записи в проект — это отдельный флаг и отдельный режим :ro.
	ro, err := sandboxSpecFor("go vet ./...", dir, sandboxConfig{Mode: SandboxModeContainer, Image: "golang:1.24", Network: "default", Memory: "1g", CPUs: "1", WorkdirReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	roArgs := strings.Join(dockerArgs(ro, "go vet ./..."), " ")
	if strings.Contains(roArgs, ":rw") {
		t.Errorf("CODEGEN_SANDBOX_ALLOW_WRITE=false обязан дать монтаж :ro: %s", roArgs)
	}
	if !strings.Contains(roArgs, "--volume "+dir+":"+dir+":ro") {
		t.Errorf("проект должен остаться видимым и на чтение: %s", roArgs)
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
	// Изоляция рабочего каталога: файл из рабочего каталога должен быть виден
	// (и по хостовому пути — модель делает cd именно по нему).
	res, err := runCommand("cat marker.txt && pwd", dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if res["status"] != "success" || !strings.Contains(res["stdout"], "sandbox-ok") {
		t.Errorf("песочница не видит рабочий каталог: %+v", res)
	}
	if !strings.Contains(res["stdout"], dir) {
		t.Errorf("pwd контейнера должен совпадать с хостовым путём %q: %+v", dir, res)
	}
	// Изоляция сети и прав: пользователь контейнера — не root.
	res, err = runCommand("id -u", dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(res["stdout"]) == "0" {
		t.Errorf("команда выполнена от root: %+v", res)
	}
	// Кэши и HOME не должны оседать в проекте: иначе ~/.gitconfig и ~/.npmrc
	// попадают в diff, который потом ревьюит человек.
	res, err = runCommand("git init -q . && echo ok", dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if res["status"] != "success" {
		t.Fatalf("git в песочнице должен работать без ~/.gitconfig: %+v", res)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("служебный файл %q появился в рабочем каталоге: HOME должен быть в tmpfs", e.Name())
		}
	}
	// Явно: HOME внутри контейнера указывает на tmpfs, а не на проект.
	res, err = runCommand("echo $HOME", dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(res["stdout"]); got != sandboxTmp {
		t.Errorf("HOME внутри песочницы = %q, ожидался %q (tmpfs), иначе конфиги тулчейна осядут в проекте", got, sandboxTmp)
	}
}

// Режимы записи проверяются на ЖИВОМ контейнере: именно тут отличается
// «флаг назван --read-only» от «проект действительно монтируется».
func TestSandboxRealContainerWriteModes(t *testing.T) {
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
	t.Setenv("CODEGEN_SANDBOX_ALLOW_WRITE", "")
	t.Setenv("CODEGEN_SANDBOX_RO", "")

	dir := t.TempDir()

	// Дефолт: проект виден И доступен на запись, иначе агент ничего не может
	// сделать — это был исходный дефект (нет монтирования вовсе).
	res, err := runCommand("touch written.txt && echo wrote", dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if res["status"] != "success" {
		t.Fatalf("в режиме по умолчанию запись в проект обязана работать: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "written.txt")); err != nil {
		t.Errorf("файл, созданный в песочнице, не появился на хосте: %v", err)
	}

	// CODEGEN_SANDBOX_ALLOW_WRITE=false: проект виден, но запись запрещена.
	t.Setenv("CODEGEN_SANDBOX_ALLOW_WRITE", "false")
	res, err = runCommand("cat go.mod >/dev/null 2>&1; ls >/dev/null && touch denied.txt; echo exit=$?", dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res["stdout"], "exit=0") {
		t.Errorf("при ALLOW_WRITE=false запись обязана падать: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "denied.txt")); err == nil {
		t.Error("при ALLOW_WRITE=false файл не должен появляться в проекте")
	}
	t.Setenv("CODEGEN_SANDBOX_ALLOW_WRITE", "")

	// CODEGEN_SANDBOX_RO=true: корень контейнера read-only, но тулчейн обязан
	// работать (кэши в tmpfs), а проект — остаться на запись.
	t.Setenv("CODEGEN_SANDBOX_RO", "true")
	res, err = runCommand("touch root-ok.txt && echo root-fs-writable-enough", dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if res["status"] != "success" || !strings.Contains(res["stdout"], "root-fs-writable-enough") {
		t.Errorf("read-only корень не должен ломать запись в рабочий каталог: %+v", res)
	}
}

// Живая проверка worktree: git в контейнере обязан видеть репозиторий и
// коммитить в него. Регрессия выглядит как «not a git repository» на каждой
// команде агента (живой случай: QAL-01, mytrip) — hermetic-тесты выше
// проверяют только форму томов, а тут важна реальная связка.
func TestSandboxRealContainerWorktree(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("Docker недоступен — hermetic-проверки покрывают песочницу")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен — hermetic-проверки покрывают песочницу")
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

	gitArgs := func(dir string, args ...string) string {
		t.Helper()
		full := append([]string{"-C", dir, "-c", "user.email=sandbox@test", "-c", "user.name=sandbox"}, args...)
		out, err := exec.Command("git", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v на хосте: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	root := t.TempDir()
	mainDir := filepath.Join(root, "proj")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitArgs(mainDir, "init", "-q", "-b", "main")
	gitArgs(mainDir, "commit", "-q", "--allow-empty", "-m", "base")
	wt := filepath.Join(root, ".wt-proj-QAL-01")
	gitArgs(mainDir, "worktree", "add", "-q", wt, "-b", "feature-x")

	// git видит worktree и работает по хостовому пути (путь берётся из .git).
	res, err := runCommand("git status --short --branch && git rev-parse --show-toplevel", wt, "")
	if err != nil {
		t.Fatal(err)
	}
	if res["status"] != "success" || !strings.Contains(res["stdout"], "feature-x") {
		t.Fatalf("git внутри песочницы не видит worktree: %+v", res)
	}
	if !strings.Contains(res["stdout"], wt) {
		t.Errorf("git должен работать по хостовому пути %q: %+v", wt, res)
	}

	// Коммит из контейнера — настоящий: объекты лежат в главном клоне и видны
	// хосту (иначе приёмка не увидит артефактов работы агента).
	res, err = runCommand("echo in-container > c.txt && git add c.txt && git commit -q -m from-container", wt, "")
	if err != nil {
		t.Fatal(err)
	}
	if res["status"] != "success" {
		t.Fatalf("коммит из песочницы должен работать: %+v", res)
	}
	if got := gitArgs(wt, "log", "-1", "--pretty=%s"); got != "from-container" {
		t.Errorf("коммит из контейнера не виден на хосте: %q", got)
	}
	if _, err := os.Stat(filepath.Join(wt, "c.txt")); err != nil {
		t.Errorf("файл из коммита не появился в worktree на хосте: %v", err)
	}
}

// Образ песочницы объявлен в трёх местах: дефолт в коде, hermetic-тест и
// sandbox/compose.yaml. Расхождение означает «тесты зелёные, а песочница
// работает на другом образе» — самый неприятный вид расхождения, потому что
// он невидим до первого реального запуска.
func TestSandboxComposeMatchesCodeDefaults(t *testing.T) {
	// loadSandboxConfig образ по умолчанию НЕ подставляет: fallback живёт в
	// sandboxSpecFor, иначе ветка выбора образа по стеку недостижима.
	t.Setenv("CODEGEN_SANDBOX_IMAGE", "")
	t.Setenv("CODEGEN_IMAGE", "")
	if got := loadSandboxConfig().Image; got != "" {
		t.Errorf("loadSandboxConfig не должен подставлять образ сам (иначе sandboxImageFor мёртв): %q", got)
	}
	if got := sandboxImageFor(t.TempDir()); got != "" {
		t.Errorf("sandboxImageFor не должен угадывать образ для каталога без манифеста: %q", got)
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
	// HOME обязан совпадать с sandboxEnv: /tmp, а не /tmp/home (под tmpfs на
	// /tmp каталога /tmp/home просто не существует) и тем более не /workspace.
	if !strings.Contains(compose, "HOME: "+sandboxTmp) {
		t.Errorf("в compose ожидался HOME: %s, как в sandboxEnv", sandboxTmp)
	}
	if strings.Contains(compose, "HOME: "+sandboxWorkspace) {
		t.Error("HOME в /workspace означает, что ~/.gitconfig и ~/.npmrc осядут в проекте и попадут в ревьюимый diff")
	}
	if _, err := os.Stat(filepath.Join("..", "sandbox", "Dockerfile")); err != nil {
		t.Errorf("нет Dockerfile песочницы: %v", err)
	}

	// Тот же env в Dockerfile — иначе образ и docker run ведут себя по-разному.
	dockerfile, err := os.ReadFile(filepath.Join("..", "sandbox", "Dockerfile"))
	if err != nil {
		t.Fatalf("нет Dockerfile песочницы: %v", err)
	}
	if !strings.Contains(string(dockerfile), "HOME="+sandboxTmp) {
		t.Errorf("в Dockerfile ожидался HOME=%s, как в sandboxEnv", sandboxTmp)
	}
	for k, v := range sandboxEnv() {
		if !strings.Contains(string(dockerfile), k) {
			t.Errorf("переменная %s=%s есть в sandboxEnv, но не объявлена в Dockerfile образа", k, v)
		}
	}
}
