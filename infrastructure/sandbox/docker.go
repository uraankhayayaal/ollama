package sandbox

// docker.go — контейнерная песочница на сессию оркестрации (п. 1.3, 1.5–1.7).
//
// Отличие от ephemeral-песочницы (tools, docker run на команду): контейнер
// ОДИН на сессию и живёт весь прогон runner'а. Команды выполняются через
// Engine API exec, каталоги монтируются bind-mount'ом по ИХ ЖЕ хостовым
// путям — поэтому Workdir внутри контейнера совпадает с хостовым, файлы
// видны обеим сторонам без подмены путей, а worktree задачи (сосед клона)
// попадает внутрь автоматически (в монтаж идёт родительский каталог).
//
// Что обеспечивает изоляцию (п. 1.4): non-root (uid хоста — файлы проекта
// остаются его), cap-drop ALL, no-new-privileges, лимиты CPU/RAM, read-only
// по флагу, /tmp — tmpfs, разрушительные команды отбраковываются до exec,
// сеть — изолирована по умолчанию (см. egress.go).
//
// Таймауты. У exec нет штатного таймаута, поэтому команда оборачивается в
// `timeout -s KILL <сек>` (бинарь есть в debian/busybox-образах; факт
// проверяется пробой при старте). Клиентский дедлай ctx — основной,
// контейнерный — страховка: если бинаря timeout нет, процесс может
// пережить команду до конца сессии (закрывается удалением контейнера,
// п. 1.7) — это честный фолбэк, а не молчаливая потеря изоляции.

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai/logging"
)

// SessionOptions — параметры старта контейнера сессии.
type SessionOptions struct {
	// Project — имя проекта (ключ реестра, имя контейнера, label).
	Project string
	// Dir — корень проекта: по нему выбирается образ и определяется
	// покрытие каталогов в Lookup.
	Dir string
	// Mounts — хостовые каталоги для bind-mount (по тем же путям внутри).
	Mounts []string
	Config Config
}

// DockerWorkspace — Workspace поверх долгоживущего контейнера.
type DockerWorkspace struct {
	engine        *Engine
	cfg           Config
	project       string
	dir           string
	mounts        []string
	env           []string // sorted K=V: базовое окружение + прокси/изоляция
	containerID   string
	containerName string
	networkMode   string
	proxy         *egressProxy // хостовый прокси на шлюзе (нативный Linux)
	// proxyContainer — имя gateway-контейнера прокси (Docker Desktop):
	// вместо хостового прокси на шлюзе VM, которого хост не может слушать.
	proxyContainer string
	// hasTimeout — в образе есть утилита timeout (проба при старте):
	// без неё таймаут обеспечива только клиентский дедлайн ctx.
	hasTimeout bool
}

// StartSession поднимает контейнер сессии и регистрирует его (п. 1.5).
// При любой ошибке частичные ресурсы (прокси, контейнер) освобождаются.
func StartSession(ctx context.Context, opts SessionOptions) (*DockerWorkspace, error) {
	if strings.TrimSpace(opts.Project) == "" {
		return nil, fmt.Errorf("sandbox: не задано имя проекта сессии")
	}
	if strings.TrimSpace(opts.Dir) == "" {
		return nil, fmt.Errorf("sandbox: не задан корень проекта")
	}
	if len(opts.Mounts) == 0 {
		return nil, fmt.Errorf("sandbox: не заданы каталоги для монтирования")
	}
	eng, err := NewEngineFromEnv()
	if err != nil {
		return nil, err
	}
	if err := eng.Ping(ctx); err != nil {
		return nil, fmt.Errorf("CODEGEN_SANDBOX=session требует Docker: %w", err)
	}

	w := &DockerWorkspace{
		engine:  eng,
		cfg:     opts.Config,
		project: opts.Project,
		dir:     filepath.Clean(opts.Dir),
	}
	// Образ: явный → по стеку проекта → dev-образ.
	image := w.cfg.Image
	if image == "" {
		image = ImageFor(opts.Dir)
	}
	if image == "" {
		image = DefaultImage
	}

	// Локальный dev-образ (DefaultImage) в реестре нет: если он ещё не собран,
	// неявный pull при create упадёт с «pull access denied». Собираем ДО сети
	// и прокси — ошибка автосборки должна вернуться до создания ресурсов.
	if err := ensureDevImage(ctx, eng, image); err != nil {
		return nil, err
	}

	// Монтирования: те же хостовые пути внутри контейнера. Колонка/запятая
	// ломают синтаксис binds — ошибка конфигурации до запуска.
	for _, m := range opts.Mounts {
		abs, err := filepath.Abs(m)
		if err != nil {
			return nil, fmt.Errorf("sandbox: каталог %q недоступен: %w", m, err)
		}
		abs = filepath.Clean(abs)
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("sandbox: каталог для монтирования %q не найден", m)
		}
		if strings.ContainsAny(abs, ":,") {
			return nil, fmt.Errorf("sandbox: путь %q содержит «:» или «,» и не переносится в bind-mount", abs)
		}
		w.mounts = appendUniqueDir(w.mounts, abs)
	}

	w.containerName = containerName(opts.Project)

	// П. 1.7: остатки прошлой сессии (падение процесса) убираются силой —
	// иначе повторный старт упирается в конфликт имени.
	if err := eng.RemoveContainer(ctx, w.containerName, true); err != nil {
		return nil, err
	}
	// Новая сессия проекта заменяет прежнюю запись реестра, если вдруг осталась.
	if old := Deactivate(w.project); old != nil {
		oldCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		_ = old.Close(oldCtx)
		cancel()
	}

	// Сеть (1.10/1.11): по умолчанию для сессии — изоляция.
	var proxyVars map[string]string
	switch w.cfg.Network {
	case "none":
		w.networkMode = "none"
	case "whitelist":
		gateway, err := eng.EnsureInternalNetwork(ctx, egressNetworkName)
		if err != nil {
			return nil, err
		}
		if eng.IsDesktop(ctx) {
			// Docker Desktop: шлюз принадлежит гостевой VM — хост не может
			// на нём слушать, а маршрута из internal-сети до хоста не
			// существует. Прокси поднимается gateway-контейнером с двумя
			// сетями (proxycontainer.go), адрес для env — его IP во
			// внутренней сети (on-link, достижим без default route).
			ip, err := startProxyContainer(ctx, eng, w.project, w.cfg.AllowDomains)
			if err != nil {
				return nil, err
			}
			w.proxyContainer = proxyContainerName(w.project)
			proxyVars = proxyEnv(proxyAddr(ip))
		} else {
			// Нативный Linux: шлюз и есть хост — прокси слушает на его IP,
			// LAN до него не достучится.
			p, err := startEgressProxy(gateway, gateway, w.cfg.AllowDomains)
			if err != nil {
				return nil, err
			}
			w.proxy = p
			proxyVars = p.env()
		}
		w.networkMode = egressNetworkName
	default: // default / bridge — открытая сеть (явный opt-out из изоляции)
		w.networkMode = "bridge"
	}

	// Окружение exec/контейнера: базовое (HOME=/tmp, git-идентичность) плюс
	// сетевые переменные. Сортировка — стабильные аргументы и логи.
	env := BaseEnv()
	if w.networkMode == "none" {
		// Без сети go mod download/npm ci не пройдут — это ожидаемо, но
		// модель должна знать про кэш, иначе будет перебирать команды.
		env["GOFLAGS"] = "-mod=mod"
		env["GOPROXY"] = "off"
	}
	for k, v := range proxyVars {
		env[k] = v
	}
	w.env = sortedEnv(env)

	// Контейнер: спящий цикл (sh обязателен — он же интерпретатор команд),
	// exec подключается к нему на время каждой команды.
	id, err := eng.CreateContainer(ctx, w.containerName, containerReq{
		Image:  image,
		Cmd:    []string{"sh", "-c", "while true; do sleep 3600; done"},
		Env:    w.env,
		User:   HostUser(),
		Labels: map[string]string{"ai.sandbox": "session", "ai.project": opts.Project},
		HostConfig: containerHost{
			Binds:       w.binds(),
			NetworkMode: w.networkMode,
			Tmpfs:       map[string]string{"/tmp": "rw,exec,mode=1777"},
			ReadOnly:    w.cfg.ReadOnly,
			Memory:      parseBytesLimit(w.cfg.Memory),
			NanoCPUs:    parseCPULimit(w.cfg.CPUs),
			CapDrop:     []string{"ALL"},
			SecurityOpt: []string{"no-new-privileges"},
		},
	})
	if err != nil {
		w.cleanupProxy(ctx)
		return nil, err
	}
	w.containerID = id

	// Любая ошибка после создания контейнера обязана его убрать (п. 1.7):
	// иначе после неудачного старта в демоне остаётся брошенный контейнер.
	fail := func(cause error) (*DockerWorkspace, error) {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		_ = eng.StopContainer(cctx, w.containerName, 5)
		_ = eng.RemoveContainer(cctx, w.containerName, true)
		w.cleanupProxy(ctx)
		return nil, cause
	}
	if err := eng.StartContainer(ctx, w.containerID); err != nil {
		return fail(fmt.Errorf("sandbox: запуск контейнера сессии: %w", err))
	}

	// Проба timeout: однократный exec при старте, дальше — cached факт.
	w.hasTimeout = w.probeTimeout()

	Activate(w.project, w, w.dir, w.mounts)
	logging.For(w.project).Infof("[sandbox] контейнер сессии %s запущен (образ=%s, сеть=%s, монтирования=%s, timeout=%v)",
		w.containerName, image, w.networkMode, strings.Join(w.mounts, ", "), w.hasTimeout)
	return w, nil
}

// Run выполняет command в workdir внутри контейнера сессии.
func (w *DockerWorkspace) Run(ctx context.Context, workdir, command string) (Result, error) {
	// Отбраковка здесь же (п. 1.4): сюда приходят и прямые вызовы вроде
	// acceptor, мимо общего гейта FileOps.
	if reason, unsafe := DestructiveReason(command); unsafe {
		return Result{}, fmt.Errorf("заблокировано политикой безопасности: %s", reason)
	}
	dir := filepath.Clean(workdir)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return Result{}, fmt.Errorf("рабочий каталог %q не найден", workdir)
	}
	if !(entry{Dir: w.dir, Proj: w.project}).covers(dir) {
		return Result{}, fmt.Errorf("каталог %q вне смонтированных областей сессии проекта %s", dir, w.project)
	}

	// Таймаут: секунды до дедлайна ctx (ceil — команда не должна пережить
	// клиентский дедлайн из-за округления вниз).
	seconds := 0
	if dl, ok := ctx.Deadline(); ok {
		seconds = int(math.Ceil(time.Until(dl).Seconds()))
		if seconds < 1 {
			seconds = 1
		}
	}
	argv := timeoutArgv(w.hasTimeout, seconds, command)

	stdout, stderr, code, err := w.engine.Exec(ctx, w.containerID, ExecOpts{
		Workdir: dir,
		Env:     w.env,
		User:    HostUser(),
		Cmd:     argv,
	})
	res := Result{Stdout: stdout, Stderr: stderr, ExitCode: code}
	if err != nil {
		if ctx.Err() != nil {
			// Дедлайн истёк: команда остановлена (клиентски и/или контейнерным
			// timeout) — это таймаут, а не инфраструктурный сбой.
			res.TimedOut = true
			res.ExitCode = -1
			return res, nil
		}
		return res, err
	}
	if w.hasTimeout && code == 124 {
		// Код 124 — контейнерный timeout считает команду просроченной.
		res.TimedOut = true
		res.ExitCode = -1
		return res, nil
	}
	if code != 0 {
		res.ExitErr = exitStatusError(code)
	}
	return res, nil
}

// Close снимает контейнер и прокси (п. 1.7). Реестр чистит StopSession до
// вызова — новые Lookup не получают умирающий Workspace.
func (w *DockerWorkspace) Close(ctx context.Context) error {
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if w.containerName != "" {
		note(w.engine.StopContainer(ctx, w.containerName, 7))
		note(w.engine.RemoveContainer(ctx, w.containerName, true))
	}
	w.cleanupProxy(ctx)
	logging.For(w.project).Infof("[sandbox] контейнер сессии %s удалён", w.containerName)
	return firstErr
}

// SandboxNetwork — режим сети сессии (none / whitelist / bridge). Нужен
// вызывающему для подсказки модели о работе без интернета: Workspace —
// узкий интерфейс, поэтому сеть сообщается через узкий же type-assert.
func (w *DockerWorkspace) SandboxNetwork() string { return w.cfg.Network }

// cleanupProxy — освобождение ресурсов прокси: хостового (Linux) и/или
// gateway-контейнера (Docker Desktop). Безопасно к отменённому ctx:
// очистка идёт по отвязанному контексту (иначе падение оркестрации оставит
// брошенный прокси-контейнер в демоне).
func (w *DockerWorkspace) cleanupProxy(ctx context.Context) {
	if w.proxy != nil {
		_ = w.proxy.Close()
		w.proxy = nil
	}
	if w.proxyContainer != "" {
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if err := stopProxyContainer(pctx, w.engine, w.project); err != nil {
			logging.For(w.project).Warnf("[sandbox] остановка egress-прокси: %v", err)
		} else {
			logging.For(w.project).Infof("[sandbox] egress-прокси-контейнер %s удалён", w.proxyContainer)
		}
		w.proxyContainer = ""
	}
}

// probeTimeout — есть ли в образе timeout (см. шапку файла). Проба best-effort:
// нет шелла/ exec — считаем, что нет, и полагаемся на клиентский дедлайн.
func (w *DockerWorkspace) probeTimeout() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	stdout, _, _, err := w.engine.Exec(ctx, w.containerID, ExecOpts{
		Workdir: "/",
		Env:     w.env,
		User:    HostUser(),
		Cmd:     []string{"sh", "-c", "command -v timeout >/dev/null 2>&1 && printf y"},
	})
	return err == nil && strings.Contains(stdout, "y")
}

// binds — монтирования в формате host:container[:ro], пути совпадают.
func (w *DockerWorkspace) binds() []string {
	mode := ""
	if w.cfg.WorkdirReadOnly {
		mode = ":ro"
	}
	out := make([]string, 0, len(w.mounts))
	for _, m := range w.mounts {
		out = append(out, m+":"+m+mode)
	}
	return out
}

// timeoutArgv — argv команды внутри контейнера: с контейнерным таймером
// (hasTimeout и известный дедлайн) либо без него — тогда таймаут обеспечивает
// только клиентский ctx.
func timeoutArgv(hasTimeout bool, seconds int, command string) []string {
	if hasTimeout && seconds > 0 {
		return []string{"timeout", "-s", "KILL", strconv.Itoa(seconds), "sh", "-c", command}
	}
	return []string{"sh", "-c", command}
}

// namePart — допустимая часть имени контейнера: [a-z0-9_.-], прочие символы
// (включая не-латиницу) вымарываются в дефис, краевые дефисы снимаются.
func namePart(project string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(project) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// containerName — стабильное имя контейнера проекта (остатки прошлых
// сессий находятся по нему). Допустимые символы docker: [a-zA-Z0-9_.-].
func containerName(project string) string {
	if part := namePart(project); part != "" {
		return "ai-sandbox-" + part
	}
	// Весь вклад проекта вымаран (не-латинское имя): имя обязано оставаться
	// с префиксом, иначе коллизии.
	return "ai-sandbox-project"
}

// proxyContainerName — имя gateway-контейнера egress-прокси проекта
// (Docker Desktop; однократная замена хостовому прокси на шлюзе).
func proxyContainerName(project string) string {
	if part := namePart(project); part != "" {
		return "ai-sandbox-proxy-" + part
	}
	return "ai-sandbox-proxy-project"
}

// sortedEnv — map → отсортированный "K=V" (обход map неупорядочен).
func sortedEnv(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// parseBytesLimit — "2g"/"512m"/"1048576" → байты для HostConfig.Memory.
// Некорректное значение → 0 (без лимита): хуже отказать в старте сессии из-за
// опечатки в CODEGEN_SANDBOX_MEMORY, чем не поставить лимит.
func parseBytesLimit(v string) int64 {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return 0
	}
	mult := int64(1)
	switch v[len(v)-1] {
	case 'k':
		mult, v = 1<<10, v[:len(v)-1]
	case 'm':
		mult, v = 1<<20, v[:len(v)-1]
	case 'g':
		mult, v = 1<<30, v[:len(v)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n * mult
}

// parseCPULimit — "2"/"1.5" → наноядра для HostConfig.NanoCPUs.
func parseCPULimit(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 0
	}
	return int64(f * 1e9)
}

// appendUniqueDir — добавление каталога без дублей (и без вложения: корень
// покрывает подкаталог, второй бинд ему не нужен).
func appendUniqueDir(list []string, dir string) []string {
	for _, e := range list {
		if e == dir {
			return list
		}
		if strings.HasPrefix(dir, e+string(filepath.Separator)) {
			return list
		}
	}
	// Новый короче существующего, покрывающего его — заменяем.
	for i, e := range list {
		if strings.HasPrefix(e, dir+string(filepath.Separator)) {
			list[i] = dir
			return list
		}
	}
	return append(list, dir)
}
