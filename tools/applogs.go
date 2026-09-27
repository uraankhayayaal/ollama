// Инструмент ReadAppLogs — чтение логов РАНТИЙМА приложения (Ф-2
// PLAN-2026-09-19-todo-owerview-for-prom.md).
//
// До этого шага панель «Логи» показывала только файлы из logs/ — то есть
// журнал работы самих агентов. Логов запущенного приложения там не было:
// агент видел «тесты прошли», но не видел «сервер упал с panic на строке 87».
// Инструмент закрывает этот пробел.
//
// Как устроено:
//
//  1. Определяем, чем запускается приложение: явная команда → ACCEPT_RUN_CMD →
//     цель Makefile (run/dev/start/serve) → npm-скрипт → типовой вариант по
//     стеку (go run ., php artisan serve, npm run dev, …).
//  2. Запускаем его как фоновый процесс с отдельной группой, построчно читаем
//     stdout+stderr и гасим по таймауту (как приёмка в agents/acceptor/run.go,
//     но построчно, а не «буфер целиком после выхода»).
//  3. Каждую строку отдаём в runevents (живая трансляция в Logboard) и
//     складываем в хвост, который runner'ер читает для самокоррекции.
//
// Degrade (правило репозитория: недоступная возможность — status:"skipped" с
// подсказкой, а не исключение):
//   - не нашли способ запуска              → skipped, «укажи command»;
//   - попросили docker, а docker/compose  → skipped, «используй local»;
//   - compose-сервис не поднят             → skipped, «docker compose up».
package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"ai/stackdetect"
)

// ReadAppLogs — имя инструмента чтения логов рантайма.
const ReadAppLogs = "ReadAppLogs"

// Границы параметров. Верхние пределы — не формальность: инструмент отдаёт
// строки обратно в контекст модели, поэтому и объём, и время ожидания должны
// быть предсказуемы.
const (
	appLogsDefaultLines = 200
	appLogsMaxLines     = 2000
	appLogsDefaultWait  = 3 * time.Second
	appLogsMaxWait      = 30 * time.Second
	// appLogsKillGrace — пауза между SIGTERM и SIGKILL: приложение успевает
	// дописать буферизованные строки и корректно закрыть слушатели.
	appLogsKillGrace = 500 * time.Millisecond
	// appLogsTailMax — сколько строк держим в хвосте для самокоррекции.
	appLogsTailMax = 200
)

// Источники логов.
const (
	appLogSourceAuto   = "auto"
	appLogSourceLocal  = "local"
	appLogSourceDocker = "docker"
)

// AppLogsArgs — аргументы инструмента (разбираются из карты вызова).
type AppLogsArgs struct {
	// Source — откуда читать: auto (по умолчанию), local, docker.
	Source string `json:"source,omitempty"`
	// Command — команда запуска приложения. Пусто — определить автоматически.
	Command string `json:"command,omitempty"`
	// Service — сервис docker compose (только для source=docker).
	Service string `json:"service,omitempty"`
	// Lines — сколько последних строк вернуть (по умолчанию 200, максимум 2000).
	Lines int `json:"lines,omitempty"`
	// WaitMS — сколько миллисекунд ждать вывода процесса, затем остановить его
	// (только для source=local; для docker читается хвост уже работающего сервиса).
	WaitMS int `json:"wait_ms,omitempty"`
}

// AppLogsResult — ответ инструмента.
type AppLogsResult struct {
	// Status — ok | skipped | error.
	Status string `json:"status"`
	// Source — фактически использованный источник (local/docker) или "" при skip.
	Source string `json:"source,omitempty"`
	// Command — команда запуска (local) или чтения (docker).
	Command string `json:"command,omitempty"`
	// Workdir — каталог, из которого запускалось приложение.
	Workdir string `json:"workdir,omitempty"`
	// Service — сервис compose (docker).
	Service string `json:"service,omitempty"`
	// Lines — последние строки логов (хвост).
	Lines []string `json:"lines,omitempty"`
	// LineCount — сколько строк прочитано всего (может быть больше len(Lines)).
	LineCount int `json:"line_count"`
	// Truncated — в ответ попал только хвост.
	Truncated bool `json:"truncated,omitempty"`
	// Stopped — процесс запуска был остановлен инструментом.
	Stopped bool `json:"stopped,omitempty"`
	// ExitCode — код завершения процесса, если он завершился сам.
	ExitCode int `json:"exit_code"`
	// Message — короткий вывод для агента.
	Message string `json:"message,omitempty"`
	// Hint — что делать дальше при skipped/error.
	Hint string `json:"hint,omitempty"`
}

// ReadAppLogs запускает приложение, собирает строки его рантайма и
// транслирует их подписчикам проекта. Собственный исполнитель (не
// tools.runCommand): этот инструмент НАМЕРЕННО запускает долгоживущий процесс,
// который обычный Run блокирует (longRunningHint), и обязан уметь его
// остановить.
func (ops *FileOps) ReadAppLogs(args map[string]any) ([]byte, error) {
	res := ops.readAppLogs(args)
	out, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (ops *FileOps) readAppLogs(raw map[string]any) AppLogsResult {
	// Аргументы читаем с дефолтами: пустой вызов (частая ситуация — модель
	// просто зовёт инструмент, чтобы «посмотреть логи») не должен падать.
	var args AppLogsArgs
	if raw != nil {
		args = AppLogsArgs{
			Source:  strArg(raw, "source"),
			Command: strArg(raw, "command"),
			Service: strArg(raw, "service"),
			Lines:   intArg(raw, "lines"),
			WaitMS:  intArg(raw, "wait_ms"),
		}
	}
	want := args.Lines
	if want <= 0 {
		want = appLogsDefaultLines
	}
	if want > appLogsMaxLines {
		want = appLogsMaxLines
	}
	wait := appLogsDefaultWait
	if args.WaitMS > 0 {
		wait = time.Duration(args.WaitMS) * time.Millisecond
	}
	if wait > appLogsMaxWait {
		wait = appLogsMaxWait
	}

	source := strings.TrimSpace(args.Source)
	switch source {
	case "", appLogSourceAuto:
		source = appLogSourceAuto
	case appLogSourceLocal, appLogSourceDocker:
	default:
		return AppLogsResult{
			Status:  "error",
			Message: "source должен быть auto, local или docker",
			Hint:    "Передай source: \"local\" (запустить процесс) или \"docker\" (логи сервиса compose).",
		}
	}

	dir := ops.OutputDir
	cmd := strings.TrimSpace(args.Command)
	if cmd == "" {
		cmd = strings.TrimSpace(os.Getenv("ACCEPT_RUN_CMD"))
	}
	composeFile := findComposeFile(dir)

	// Выбор источника. auto: локальный процесс предпочтительнее — он работает
	// без внешнего демона; docker берётся, только если локально запускать нечем.
	if source == appLogSourceAuto {
		switch {
		case cmd != "" || resolveRunCommand(dir) != "":
			source = appLogSourceLocal
		case composeFile != "":
			source = appLogSourceDocker
		default:
			return AppLogsResult{
				Status:  "skipped",
				Message: "не удалось определить, чем запускается приложение",
				Hint: "Передай command (например \"npm run dev\") или добавь в Makefile цель run/dev/start/serve " +
					"либо docker-compose файл. Без способа запуска логи рантайма взять неоткуда.",
			}
		}
	}

	if source == appLogSourceDocker {
		return ops.readDockerLogs(composeFile, args.Service, want)
	}
	if cmd == "" {
		cmd = resolveRunCommand(dir)
	}
	if cmd == "" {
		return AppLogsResult{
			Status:  "skipped",
			Message: "не удалось определить команду запуска приложения",
			Hint: "Передай command явно (например \"npm run dev\", \"go run .\", \"php artisan serve\") " +
				"или заведи цель run в Makefile.",
		}
	}
	return ops.runAndTail(dir, cmd, want, wait)
}

// runAndTail запускает процесс, построчно читает вывод и останавливает его по
// истечении wait. Трансляция каждой строки идёт в шину (живой Logboard) и в
// хвост (самокоррекция), хвост же — единственное, что возвращается модели.
func (ops *FileOps) runAndTail(dir, cmd string, want int, wait time.Duration) AppLogsResult {
	// Контекст инструмента не должен отменять чтение логов: вызывающий (модель)
	// может закрыть соединение, и тогда мы потеряем уже собранные строки.
	runCtx, cancel := context.WithTimeout(context.Background(), wait+appLogsMaxWait)
	defer cancel()

	c := exec.CommandContext(runCtx, "sh", "-c", cmd)
	c.Dir = dir
	// Своя группа процессов: остановка «дерева» (npm → node → его дети) целиком,
	// иначе после выхода оболочки остались бы сиротские серверы на порту.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// os.Pipe, а не c.StdoutPipe(): нужен ОДИН канал на stdout и stderr сразу.
	// StdoutPipe даёт ReadCloser (только чтение), поэтому stderr пришлось бы
	// завести отдельным буфером — и лог распался бы на два непоследовательных
	// куска, а трейсер Go (пишет в stderr) с логгером stdlib перемешивались бы
	// в произвольном порядке.
	pr, pw, err := os.Pipe()
	if err != nil {
		return AppLogsResult{
			Status: "error", Source: appLogSourceLocal, Command: cmd, Workdir: dir,
			Message: "не удалось создать канал вывода процесса: " + err.Error(),
		}
	}
	c.Stdout = pw
	c.Stderr = pw
	if err := c.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return AppLogsResult{
			Status: "error", Source: appLogSourceLocal, Command: cmd, Workdir: dir,
			Message: "не удалось запустить приложение: " + err.Error(),
			Hint:    "Проверь команду и зависимости (часто нужны make install / go mod download / npm ci).",
		}
	}
	// Родитель закрывает свой конец записи: иначе чтение не увидит EOF, пока
	// дети процесса держат копию дескриптора (npm порождает их цепочкой).
	_ = pw.Close()
	defer pr.Close()

	sink := appLogSinkFor(dir)
	tail := &appLogTail{}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		// Строки логов бывают длиннее 64 КБ (дампы трейсеров, JSON-события):
		// без увеличенного буфера Scanner вернёт ErrTooLong и перестанет читать.
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			tail.push(line)
			if sink != nil {
				sink(appLogSourceLocal, line)
			}
		}
	}()

	// stderr уже направлен в тот же pipe, поэтому читает ровно один goroutine:
	// два читателя на одном *os.File разделили бы строки между собой поровну и
	// потеряли половину лога.
	// Канал-сигнал (закрытие), а не канал со значением: значение уходит в
	// переменную, потому что stopProcess ниже ТОЖЕ слушает этот канал, и
	// получение из общего chan error украло бы у вызывающего результат Wait —
	// тот завис бы навечно после успешного kill.
	var waitErr error
	exited := make(chan struct{})
	go func() {
		waitErr = c.Wait()
		close(exited)
	}()

	stopped := true
	// Таймер вместо runCtx: сам runCtx нужен как «потолок» для самого процесса
	// (если процесс пережил и сбор логов, и kill — его всё равно убьёт).
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	select {
	case <-exited:
		// Процесс завершился сам (упал или это разовая команда) — останавливать
		// нечего, и его код выхода важен модели.
		stopped = false
	case <-deadline.C:
		stopProcess(c, exited)
		stopped = true
	}
	// Читатель обязан дочитать до EOF: иначе теряем последние строки, а именно
	// они обычно и содержат причину падения.
	<-readDone

	lines := tail.last(want)
	// Хвост отдаём раннеру: без него следующий раунд агента не увидит, что
	// приложение упало, и начнёт «лечить» код по слепым догадкам.
	ops.setAppLogTail(appLogSourceLocal, lines)
	exit := 0
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			exit = ee.ExitCode()
		} else {
			exit = -1
		}
	}
	// Диагностика «процесс умер раньше, чем мы прочитали строки» — самая частая
	// ситуация, ради которой инструмент и нужен.
	finish := "приложение ещё работает (остановлено инструментом)"
	switch {
	case stopped && exit < 0 && len(lines) > 0:
		// -1 здесь — не «падение», а «убит нами сигналом»: сообщение обязано
		// это сказать, иначе модель начнёт искать несуществующий стектрейс.
		finish = "приложение отработало и было остановлено инструментом (код выхода неприменим)"
	case !stopped && exit == 0:
		finish = "приложение завершилось штатно"
		if len(lines) == 0 {
			finish += ", но ни одной строки лога не вывело"
		}
	case !stopped && exit < 0:
		finish = "процесс прерван сигналом"
	default:
		finish = fmt.Sprintf("приложение упало с кодом %d", exit)
	}
	return AppLogsResult{
		Status:    statusFor(exit, stopped, len(lines)),
		Source:    appLogSourceLocal,
		Command:   cmd,
		Workdir:   dir,
		Lines:     lines,
		LineCount: tail.total(),
		Truncated: tail.total() > len(lines),
		Stopped:   stopped,
		ExitCode:  exit,
		Message:   finish,
		Hint:      tailHint(len(lines), tail.total(), stopped),
	}
}

// statusFor подбирает статус ответа. Пустой лог с упавшим процессом — это
// «skipped» с подсказкой (нет смысла показывать ошибку, надо её устранить), но
// строки при этом возвращаются: стек-трейс в stdout встречается.
func statusFor(exit int, stopped bool, lines int) string {
	switch {
	case lines > 0:
		return "ok"
	case stopped:
		// Ни одной строки за всё время ожидания: скорее всего процесс пишет
		// в файл, а не в консоль, либо он завис на старте.
		return "skipped"
	case exit == 0:
		return "ok"
	default:
		return "error"
	}
}

func tailHint(lines, total int, stopped bool) string {
	switch {
	case stopped && lines == 0:
		return "Процесс молчал: проверь, печатает ли приложение логи в stdout, и увеличь wait_ms."
	case stopped:
		return "Приложение было остановлено после сбора логов. Увеличь wait_ms, если старт дольше."
	case total > 0:
		return "Исправь причину падения по этим строкам; повторный вызов запустит приложение снова."
	}
	return ""
}

// readDockerLogs читает логи уже работающего сервиса compose (только чтение:
// поднимать и останавливать окружение инструмент не станет, это зона DevOps).
func (ops *FileOps) readDockerLogs(composeFile, service string, want int) AppLogsResult {
	res := AppLogsResult{Status: "ok", Source: appLogSourceDocker, Service: service, Workdir: ops.OutputDir}
	if composeFile == "" {
		return AppLogsResult{
			Status:  "skipped",
			Source:  appLogSourceDocker,
			Service: service,
			Message: "в проекте нет docker-compose файл",
			Hint:    "Добавь docker-compose.yml, либо читай логи локального процесса (source=local).",
		}
	}
	if !haveTool("docker") {
		return AppLogsResult{
			Status:  "skipped",
			Source:  appLogSourceDocker,
			Service: service,
			Message: "docker недоступен в этой среде",
			Hint:    "Запусти логи локального процесса (source=local, command=…) — docker compose здесь не работает.",
		}
	}
	args := []string{"compose", "-f", filepath.Base(composeFile), "logs", "--tail", fmt.Sprint(want)}
	if service != "" {
		args = append(args, service)
	}
	res.Command = "docker " + strings.Join(args, " ")
	out, code, timedOut, err := runCaptured(ops.OutputDir, res.Command, appLogsMaxWait)
	if err != nil {
		return AppLogsResult{
			Status: "error", Source: appLogSourceDocker, Service: service, Workdir: ops.OutputDir,
			Command: res.Command,
			Message: "не удалось прочитать логи compose: " + firstLine(out, err),
			Hint:    "Проверь, что окружение поднято (docker compose ps) и имя сервиса верно.",
		}
	}
	lines := splitLines(out)
	if code != 0 {
		return AppLogsResult{
			Status: "skipped", Source: appLogSourceDocker, Service: service, Workdir: ops.OutputDir,
			Command: res.Command, Lines: lines, LineCount: len(lines),
			Message: fmt.Sprintf("docker compose logs вернул код %d%s", code, timedOutSuffix(timedOut)),
			Hint: "Если окружение не поднято — подними его (docker compose up -d) и повтори; " +
				"либо читай локальный процесс (source=local).",
		}
	}
	if len(lines) == 0 {
		return AppLogsResult{
			Status: "skipped", Source: appLogSourceDocker, Service: service, Workdir: ops.OutputDir,
			Command: res.Command,
			Message: "сервис не выдал ни одной строки лога",
			Hint:    "Проверь docker compose ps: возможно, сервис ещё не поднят или пишет в файл.",
		}
	}
	res.Lines = trimLines(lines, want)
	ops.setAppLogTail(appLogSourceDocker, res.Lines)
	res.LineCount = len(lines)
	res.Truncated = len(lines) > len(res.Lines)
	res.ExitCode = 0
	res.Message = fmt.Sprintf("прочитано строк из docker compose logs: %d", len(lines))
	return res
}

// runCaptured — разовый запуск команды с буферизацией вывода (для docker).
func runCaptured(dir, command string, timeout time.Duration) (string, int, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, "sh", "-c", command)
	c.Dir = dir
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := c.CombinedOutput()
	// Сверяемся с контекстом ДО разбора err: после отмены контекста CommandContext
	// убивает процесс, и err больше не различает «команда упала» и «истёкло
	// время» — для модели это разные ситуации с разными подсказками.
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	if err == nil {
		return string(out), 0, timedOut, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), ee.ExitCode(), timedOut, nil
	}
	return string(out), -1, timedOut, err
}

// stopProcess гасит группу процессов: сначала SIGTERM (дать дописать логи),
// затем SIGKILL. Ровно тот же приём, что в agents/acceptor/run.go.
//
// exited — канал, который ЗАКРЫВАЕТСЯ по завершении c.Wait(). Именно сигнал, а не
// канал со значением: значение из общего chan error забрал бы себе stopProcess,
// и вызывающий остался бы ждать результат, которого уже нет. Свой
// Process.Wait() здесь тоже не запускается: два Wait на одном процессе
// конкурируют за статус, и второй получил бы «процесс уже освобождён» —
// из-за чего ExitCode уехал бы в -1.
func stopProcess(c *exec.Cmd, exited <-chan struct{}) {
	if c.Process == nil {
		return
	}
	pid := c.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-exited:
		return
	case <-time.After(appLogsKillGrace):
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	select {
	case <-exited:
	case <-time.After(appLogsKillGrace):
		// Процесс пережил SIGKILL (uninterruptible sleep в io): не блокируем
		// инструмент вечно — контекст процесса всё равно его добьёт.
	}
}

// --- определение способа запуска ---

// resolveRunCommand определяет команду запуска приложения для каталога dir.
// Порядок повторяет логику приёмки (agents/acceptor/detect.go), но живёт в
// tools: пакет acceptor уже импортирует tools, обратная зависимость была бы
// циклом. Порядок: Makefile (единая точка входа по Ф-1..Ф-5 плана makefile) →
// npm-скрипт → типовой вариант по стеку.
func resolveRunCommand(dir string) string {
	if cmd := makefileRunCommand(dir); cmd != "" {
		return cmd
	}
	if cmd := nodeRunCommand(dir); cmd != "" {
		return cmd
	}
	return defaultRunCommand(dir)
}

// makefileRunTargetNames — цели запуска, которые ищем по порядку.
var makefileRunTargetNames = []string{"run", "dev", "start", "serve", "up"}

func makefileRunCommand(dir string) string {
	targets := makefileTargets(readFileIfExists(filepath.Join(dir, "Makefile")))
	if targets == nil {
		targets = makefileTargets(readFileIfExists(filepath.Join(dir, "makefile")))
	}
	for _, name := range makefileRunTargetNames {
		if targets[name] {
			return "make " + name
		}
	}
	return ""
}

// makefileTargets разбирает объявленные цели Makefile (имя → объявлена ли).
// Разбор намеренно поверхностный: достаточно знать, что цель ЕСТЬ. Аргументы
// (с переменными, префиксами) намеренно не мешают — команда «make run» сама
// подставит значения из Makefile.
func makefileTargets(text string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, " ") {
			continue // строка рецепта, не объявление
		}
		i := strings.Index(line, ":")
		if i <= 0 {
			continue
		}
		// Присваивание переменной: двоеточие в нём — часть «:=», знак «=»
		// стоит ПОСЛЕ него, поэтому ищем «=» в обеих частях строки.
		head, rest := line[:i], line[i+1:]
		if strings.Contains(head, "=") || strings.HasPrefix(strings.TrimSpace(rest), "=") {
			continue
		}
		// Спец-цели (.PHONY, .DEFAULT, .gitignore) объявляют зависимости, а не
		// цели: «build» в «.PHONY: build» — это список .PHONY, и принимать его
		// за цель означало бы предложить «make build» там, где цели нет.
		if strings.HasPrefix(strings.TrimSpace(head), ".") {
			continue
		}
		for _, name := range strings.Fields(head) {
			if name == "" || strings.HasPrefix(name, "%") {
				continue
			}
			out[name] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// nodeRunCommand ищет стартовый скрипт в package.json.
func nodeRunCommand(dir string) string {
	raw := readFileIfExists(filepath.Join(dir, "package.json"))
	if raw == "" {
		return ""
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal([]byte(raw), &pkg); err != nil {
		return ""
	}
	for _, name := range []string{"dev", "start", "serve"} {
		if pkg.Scripts[name] != "" {
			runner := npmRunner(dir)
			return runner + " run " + name
		}
	}
	return ""
}

// npmRunner выбирает пакетный менеджер по lock-файлу: yarn/pnpm без npm
// install не запустят проект.
func npmRunner(dir string) string {
	switch {
	case fileExists(filepath.Join(dir, "pnpm-lock.yaml")):
		return "pnpm"
	case fileExists(filepath.Join(dir, "yarn.lock")):
		return "yarn"
	}
	return "npm"
}

// defaultRunCommand — типовой запуск по определённому стеку проекта.
func defaultRunCommand(dir string) string {
	switch stackdetect.DetectKind(dir) {
	case stackdetect.KindGo:
		if fileExists(filepath.Join(dir, "main.go")) {
			return "go run ."
		}
		if pkg := findGoMainPackage(dir); pkg != "" {
			return "go run " + pkg
		}
	case stackdetect.KindPhp:
		if fileExists(filepath.Join(dir, "artisan")) {
			return "php artisan serve --host=127.0.0.1 --port=8080"
		}
		if fileExists(filepath.Join(dir, "public/index.php")) {
			return "php -S 127.0.0.1:8080 -t public"
		}
	case stackdetect.KindNode:
		// Статика без сервера запускается встроенным сервером node: этого
		// достаточно, чтобы прочитать рантайм-лог и код ответа.
		if fileExists(filepath.Join(dir, "index.html")) {
			return "node -e \"const h=require('http'),f=require('fs');h.createServer((q,s)=>{s.setHeader('content-type','text/html');s.end(f.readFileSync('index.html'))}).listen(8080,()=>console.log('serving on :8080'))\""
		}
	case stackdetect.KindPython:
		if fileExists(filepath.Join(dir, "app.py")) {
			return "python3 app.py"
		}
		if fileExists(filepath.Join(dir, "main.py")) {
			return "python3 main.py"
		}
	}
	return ""
}

// findComposeFile ищет файл compose в корне (docker-compose.yml — самый
// распространённый, compose.yaml — принятый в этом репозитории).
func findComposeFile(dir string) string {
	for _, name := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
		if fileExists(filepath.Join(dir, name)) {
			return filepath.Join(dir, name)
		}
	}
	return ""
}

// --- хвост логов и обмен с раннером ---

// appLogTail — накопитель последних строк рантайма. Кольцевой по размеру, а не
// по времени: приложение может писать тысячи строк в секунду, и в буфер надо
// попадать раньше модели.
type appLogTail struct {
	mu    sync.Mutex
	lines []string
	count int
}

func (t *appLogTail) push(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.count++
	t.lines = append(t.lines, line)
	if len(t.lines) > appLogsTailMax {
		// Сдвигаем начало, а не выделяем новый слайс: удерживаем одну
		// ёмкость и не плодим мусор на каждой строке.
		n := copy(t.lines, t.lines[len(t.lines)-appLogsTailMax:])
		t.lines = t.lines[:n]
	}
}

func (t *appLogTail) total() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count
}

func (t *appLogTail) last(n int) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n <= 0 || n > len(t.lines) {
		n = len(t.lines)
	}
	out := make([]string, n)
	copy(out, t.lines[len(t.lines)-n:])
	return out
}

// --- живая трансляция строк подписчикам проекта ---

// appLogSinks — подписчики на строки логов рантайма по каталогу проекта.
// Реестр по каталогу, а не одна глобальная переменная: циклы разных проектов
// идут параллельно, и логи одного проекта не должны попадать в панель другого.
var appLogSinks struct {
	mu    sync.RWMutex
	byDir map[string]appLogSub
	// seq — счётчик регистраций. По нему снятие подписки понимает, что
	// текущая подписка всё ещё ЕГО, а не подменённая более поздней.
	seq uint64
}

// appLogSub — подписка проекта на строки логов.
type appLogSub struct {
	id uint64
	fn func(source, line string)
}

// SetAppLogSink подписывает fn на строки логов рантайма проекта в каталоге dir
// и возвращает снятие подписки. Зовёт сессия (server) при создании: без
// подписки «рантайм»-вкладка и REST-хвост остались бы пустыми — инструмент
// живёт в пакете tools и не имеет доступа к сессии (у Tool нет контекста).
//
// Повторный вызов для того же dir заменяет подписку (у цикла она одна).
// Снятие безопасно: если подписку уже заменили, cancel ничего не делает —
// иначе вытесненная сессия погасила бы «рантайм» новой.
func SetAppLogSink(dir string, fn func(source, line string)) (cancel func()) {
	if dir == "" {
		return func() {}
	}
	appLogSinks.mu.Lock()
	appLogSinks.seq++
	sub := appLogSub{id: appLogSinks.seq, fn: fn}
	if appLogSinks.byDir == nil {
		appLogSinks.byDir = map[string]appLogSub{}
	}
	if fn == nil {
		delete(appLogSinks.byDir, dir)
	} else {
		appLogSinks.byDir[dir] = sub
	}
	appLogSinks.mu.Unlock()
	return func() {
		appLogSinks.mu.Lock()
		defer appLogSinks.mu.Unlock()
		if cur, ok := appLogSinks.byDir[dir]; ok && cur.id == sub.id {
			delete(appLogSinks.byDir, dir)
		}
	}
}

func appLogSinkFor(dir string) func(source, line string) {
	appLogSinks.mu.RLock()
	defer appLogSinks.mu.RUnlock()
	if sub, ok := appLogSinks.byDir[dir]; ok {
		return sub.fn
	}
	return nil
}

// appLogs — последние строки рантайма, прочитанные инструментом, за последний
// вызов. Держится на FileOps, чтобы раннер мог подмешивать их модели в цикл
// самокоррекции (см. runner.RuntimeLogger): инструмент и цикл живут в разных
// пакетах, а общего контекста у них нет.
var appLogs struct {
	mu    sync.Mutex
	lines []string
	src   string
}

func (ops *FileOps) setAppLogTail(source string, lines []string) {
	appLogs.mu.Lock()
	defer appLogs.mu.Unlock()
	appLogs.src = source
	appLogs.lines = append([]string(nil), lines...)
}

// TakeAppLogTail возвращает и забирает строки логов рантайма, накопленные
// последним вызовом ReadAppLogs. «Забирает» — чтобы один и тот же хвост не
// подмешивался модели дважды.
func (ops *FileOps) TakeAppLogTail() (source string, lines []string) {
	appLogs.mu.Lock()
	defer appLogs.mu.Unlock()
	out := append([]string(nil), appLogs.lines...)
	appLogs.lines = nil
	return appLogs.src, out
}

// --- мелкие помощники ---

// intArg — целочисленный аргумент из map (JSON-числа приходят float64).
func intArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

// findGoMainPackage ищет каталог с пакетом main и func main: точку входа,
// если main.go лежит не в корне (в отличие от agents/acceptor/detect.go, здесь
// обход ограничен по глубине — этого хватает для типового сервиса).
func findGoMainPackage(dir string) string {
	best := ""
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || best != "" {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		// vendor и сгенерированные деревья пропускаем: там main-пакетов нет,
		// а обход по ним дорог.
		name := d.Name()
		if name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".") {
			return filepath.SkipDir
		}
		if depth := len(strings.Split(filepath.ToSlash(rel), "/")); depth > 3 {
			return filepath.SkipDir
		}
		if goMainPackageAt(path) {
			best = "./" + filepath.ToSlash(rel)
		}
		return nil
	})
	return best
}

// goMainPackageAt проверяет наличие func main() в .go-файлах каталога.
func goMainPackageAt(dir string) bool {
	found := false
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if found || e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			continue
		}
		text := string(b)
		if strings.Contains(text, "package main") && regexp.MustCompile(`(?m)^func main\(\)`).MatchString(text) {
			found = true
		}
	}
	return found
}

func haveTool(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func readFileIfExists(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func splitLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimRight(l, "\r"); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func trimLines(lines []string, n int) []string {
	if n <= 0 || len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}

func firstLine(out string, err error) string {
	for _, l := range splitLines(out) {
		return l
	}
	if err != nil {
		return err.Error()
	}
	return "нет вывода"
}

func timedOutSuffix(timedOut bool) string {
	if timedOut {
		return " (по таймауту)"
	}
	return ""
}
