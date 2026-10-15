package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ai/domain"
	"ai/infrastructure/sandbox"
)

func runOutputLimit() int {
	n := runMaxOutputDefault
	if v := os.Getenv("CODEGEN_RUN_MAX_OUTPUT"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 {
			n = x
		}
	}
	return n
}

// truncateRunOutput обрезает поток вывода до лимита, добавляя маркер обрезки
// (как в ReadFiles). Возвращает итог и признак обрезки: вызывающий ставит
// флаг truncated в результат инструмента — модель видит, что хвост вывода
// отброшен, и может перезапустить команду с фильтром (grep/tail), а не
// искать ошибку в обрывке.
func truncateRunOutput(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	return s[:limit] + fmt.Sprintf("\n\n[... вывод обрезан, показаны первые %d символов из %d; повтори команду с фильтром (grep/tail), если нужен хвост ...]",
		limit, len(s)), true
}

// RunParams соответствует JSON-параметрам инструмента Run
type RunParams struct {
	Command string `json:"command"`
}

// Run запускает команду в OutputDir (например, go build) и возвращает вывод ИИ-агенту.
// runTimeout возвращает таймаут исполнения команды инструмента Run.
// Значение берётся из CODEGEN_RUN_TIMEOUT (например "90s"), по умолчанию 60s.
// Таймаут важен: агент может запустить долгоживущую команду (npm run dev,
// сервер, интерактивная утилита) — без ограничения шаг завис бы навсегда,
// а у модели не было бы информации, что команда не завершается.
func runTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("CODEGEN_RUN_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 60 * time.Second
}

// runCommand запускает команду и возвращает её результат выполнения.
// Команда ограничена по времени runTimeout: если она не завершается (например,
// агент запустил дев-сервер), процесс и его группа убиваются, а в результате
// появляется понятное сообщение о таймауте — шаг продолжается, а не виснет.
//
// Исполнитель выбирается в три шага (Ф-4 + Этап 1):
//  1. активный сессионный контейнер (sandbox.Lookup): проектная сессия
//     поднимается сервером до старта runner'а, и тогда команда идёт в него;
//  2. при CODEGEN_SANDBOX=session без активной сессии — строгая ошибка, НЕ
//     тихий запуск на хосте: изоляция либо есть, либо об этом сказано прямо;
//  3. иначе прежние режимы: ephemeral docker run (container) или хост.
//
// project — имя проекта (ops.ProjectName), когда оно известно: точное имя
// важнее совпадения по пути. Пустое имя (ЛСП-чекер) работает как раньше.
func runCommand(command, workdir, project string) (map[string]string, error) {
	if ws := sandbox.Lookup(project, workdir); ws != nil {
		return runCommandWorkspace(command, workdir, ws)
	}
	cfg := loadSandboxConfig()
	if cfg.Mode == SandboxModeSession {
		// Сессионный режим объявлен, но контейнера нет: сервер не поднял
		// сессию (старый процесс, ошибка старта). Молчаливый хост здесь
		// означал бы «песочница есть» там, где её нет.
		return map[string]string{
			"command":    command,
			"workdir":    workdir,
			"exit_error": "песочница не активна",
			"status":     "error",
			"sandbox":    string(SandboxModeSession),
			"message": "CODEGEN_SANDBOX=session задан, но сессионный контейнер проекта не активен: " +
				"команда НЕ выполнена (без тихого запуска на хосте). " +
				"Перезапусти сессию проекта или выполни задачу в новой сессии сервера.",
		}, nil
	}
	return runCommandSandbox(command, workdir, cfg)
}

// runCommandWorkspace — выполнение команды через активный сессионный
// контейнер (sandbox.Workspace). Контракт результата неотличим от
// runCommandSandbox: те же поля, санитайзинг, подсказки и обрезка вывода.
func runCommandWorkspace(command, workdir string, ws sandbox.Workspace) (map[string]string, error) {
	// Отбраковка разрушительных команд ДО запуска — общий гейт со всеми
	// путями (в контейнере rm -rf уничтожит файлы проекта на хосте: том
	// смонтирован без копии).
	if reason, unsafe := destructiveCommandReason(command); unsafe {
		return map[string]string{
			"command":    command,
			"workdir":    workdir,
			"exit_error": "заблокировано политикой безопасности",
			"status":     "error",
			"message":    reason,
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), runTimeout())
	defer cancel()

	res, err := ws.Run(ctx, workdir, command)
	if err != nil {
		// Инфраструктурный сбой (нет демона, каталог вне монтирования) —
		// как и падение cmd.Start в host-пути: ошибка вызывающего, а не
		// результат команды.
		return nil, err
	}

	result := map[string]string{
		"command":    command,
		"workdir":    workdir,
		"exit_error": "",
		"stdout":     domain.SanitizeToolOutput(res.Stdout),
		"stderr":     domain.SanitizeToolOutput(res.Stderr),
		// Явно сообщаем модели, где выполнялась команда.
		"sandbox": string(SandboxModeSession),
	}
	if nws, ok := ws.(interface{ SandboxNetwork() string }); ok && nws.SandboxNetwork() == "none" {
		result["hint"] = sandboxNetworkHelp()
	}
	finishRunResult(result, command, res.TimedOut, res.ExitErr)
	return result, nil
}

// finishRunResult — общий хвост упаковки результата: статус, текст ошибки,
// подсказки (недостающий тулчейн) и обрезка вывода до runOutputLimit.
// timedOut=true приоритетнее runErr: таймаут — это своя история для модели
// («команда виснет», а не «упала»), и ExitErr там — ctx.Err().
func finishRunResult(result map[string]string, command string, timedOut bool, runErr error) {
	if timedOut {
		result["exit_error"] = "signal: killed (timeout)"
		result["status"] = "error"
		result["message"] = fmt.Sprintf("команда не завершилась за %s и была остановлена (timeout)", runTimeout())
	} else if runErr != nil {
		result["exit_error"] = runErr.Error()
		result["status"] = "error"
		if h := missingToolHint(command, result["stdout"]+"\n"+result["stderr"], runErr); h != "" {
			result["hint"] = h
		}
	} else {
		result["status"] = "success"
	}
	// Обрезка потоков вывода до лимита (см. runOutputLimit): маркер в тексте
	// и флаг truncated — модель понимает, что хвост отброшен. Статус и код
	// выхода обрезка не трогает: гейты остаются честными.
	limit := runOutputLimit()
	if out, trunc := truncateRunOutput(result["stdout"], limit); trunc {
		result["stdout"] = out
		result["stdout_truncated"] = "true"
	}
	if out, trunc := truncateRunOutput(result["stderr"], limit); trunc {
		result["stderr"] = out
		result["stderr_truncated"] = "true"
	}
}

// runCommandSandbox — реализация запуска с явной конфигурацией песочницы.
// LocalCommand подменяется в hermetic-тестах, поэтому тесты не запускают
// shell, docker и тем более проекты агентов.
func runCommandSandbox(command, workdir string, sb sandboxConfig) (map[string]string, error) {
	// Отбраковка разрушительных команд ДО любого запуска: в контейнере
	// `rm -rf` рабочего каталога уничтожит файлы проекта на хосте (том
	// смонтирован без копии), а на хосте тем более.
	if reason, unsafe := destructiveCommandReason(command); unsafe {
		return map[string]string{
			"command":    command,
			"workdir":    workdir,
			"exit_error": "заблокировано политикой безопасности",
			"status":     "error",
			"message":    reason,
		}, nil
	}

	mode := sb.Mode
	if mode == SandboxModeUnset {
		// Конфигурация не определилась (сбой env-разбора) — сознательно
		// возвращаемся к хосту, но помечаем это в результате.
		mode = SandboxModeLocal
	}
	sandboxed := mode == SandboxModeContainer
	if sandboxed {
		// Образ/каталог проверяем ДО запуска: нечитаемый путь или неизвестный
		// стек — это ошибка конфигурации, а не падение команды.
		if _, err := sandboxSpecFor(command, workdir, sb); err != nil {
			return map[string]string{
				"command":    command,
				"workdir":    workdir,
				"exit_error": "песочница не настроена",
				"status":     "error",
				"sandbox":    "container",
				"message":    err.Error(),
			}, nil
		}
		// Образ до docker run: локальный dev-образ (ai-sandbox:latest) в
		// реестре отсутствует — если он не собран, первая же команда упадёт
		// с «pull access denied». Автосборка — только для него (EnsureImage —
		// no-op для реестровых образов, их docker тянет неявно при run).
		if err := ensureSandboxImage(workdir, sb); err != nil {
			return map[string]string{
				"command":    command,
				"workdir":    workdir,
				"exit_error": "песочница не настроена",
				"status":     "error",
				"sandbox":    "container",
				"message":    err.Error(),
			}, nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), runTimeout())
	defer cancel()

	var cmd *exec.Cmd
	if sandboxed {
		path, args, err := sandboxCommand(command, workdir, sb)
		if err != nil {
			return nil, err
		}
		cmd = exec.CommandContext(ctx, path, args...)
		// Docker-демон живёт по сокету, а переменные вроде DOCKER_HOST нужно
		// передать дочернему процессу иначе, чем основному окружению агента.
		cmd.Env = dockerEnv()
	} else {
		var err error
		if sb.LocalCommand != nil {
			cmd, err = sb.LocalCommand(command, workdir)
		} else {
			cmd = exec.CommandContext(ctx, "sh", "-c", command)
		}
		if err != nil {
			return nil, err
		}
		cmd.Env = os.Environ()
	}
	cmd.Dir = workdir
	// День: команда может порождать детей (серверы, npm). Отдельная группа
	// процессов позволяет при таймауте убить их всех, а не только шелл.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Ждём завершения в отдельной горутине: при таймауте необходимо убить
	// ВСЮ группу процессов — выживший потомок (go run → бинарь, npm → сервер,
	// sleep) держит каналы stdout/stderr открытыми, и прямой cmd.Wait()
	// блокируется навсегда, игнорируя таймаут.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var runErr error
	timedOut := false
	select {
	case <-ctx.Done():
		// Превышен таймаут: процесс (и его потомки) ещё живы — принудительно
		// завершаем всю группу, чтобы закрылись унаследованные каналы вывода
		// и cmd.Wait() (в done) разблокировался.
		if cmd.Process != nil && cmd.Process.Pid > 0 {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		<-done
		timedOut = true
		runErr = ctx.Err()
	case werr := <-done:
		runErr = werr
	}

	// Вывод команды нормализуется ДО упаковки в JSON: цвета терминала (ANSI),
	// управляющие и NUL-байты в строке ломают JSON-пакет и разбор аргументов
	// следующих вызовов модели («invalid character ']' looking for beginning of
	// value»), из-за чего падал сам харнес.
	result := map[string]string{
		"command":    command,
		"workdir":    workdir,
		"exit_error": "",
		"stdout":     domain.SanitizeToolOutput(stdout.String()),
		"stderr":     domain.SanitizeToolOutput(stderr.String()),
		// Явно сообщаем модели, где выполнялась команда. Без этого «песочница»
		// существует только в коде, а агент (и человек в логе) считает все
		// запуски изолированными — включая те, что ушли на хост по фолбэку.
		"sandbox": string(mode),
	}
	if !sandboxed {
		result["sandbox_note"] = sandboxFallbackReason
	} else if sb.Network == "none" {
		result["hint"] = sandboxNetworkHelp()
	}
	finishRunResult(result, command, timedOut, runErr)
	return result, nil
}

// missingToolHint распознаёт «инструмент/модуль недоступен в окружении хоста»
// (sh: cargo: command not found, exit 127, ModuleNotFoundError) и возвращает
// подсказку модели. Без неё агент начинает перебирать окружение — which/find по
// всей файловой системе (минуты до таймаута), pip install, повторные запуски
// той же команды — и сжигает раунды, хотя тулчейн проекта живёт в контейнере.
func missingToolHint(command, output string, runErr error) string {
	lower := strings.ToLower(output)
	missing := strings.Contains(lower, "command not found") ||
		strings.Contains(lower, "modulenotfounderror") ||
		strings.Contains(lower, "no module named") ||
		strings.Contains(lower, "not recognized as an internal or external command")
	if !missing {
		// Любой носитель кода выхода: *exec.ExitError (хост/эфемерный
		// контейнер) и sandbox.exitStatusError (сессионный контейнер) —
		// 127 значит «команда не найдена» в обоих.
		if ec, ok := runErr.(interface{ ExitCode() int }); ok && ec.ExitCode() == 127 {
			missing = true
		}
	}
	if !missing {
		return ""
	}
	tool := strings.TrimSpace(command)
	if f := strings.Fields(command); len(f) > 0 {
		tool = f[0]
	}
	return fmt.Sprintf("Инструмент/модуль для %q отсутствует в окружении хоста — НЕ ищи его (which/find), НЕ устанавливай (pip/brew/apt) и НЕ повторяй команду. "+
		"Сборку и тесты проекта выполняй в его собственном окружении: docker compose run --rm <сервис> <команда> (сервис и команды см. в docker-compose.yml и README проекта). "+
		"Если у проекта есть корневой Makefile — используй его инфра-цели: 'make infra.<цель>' = 'docker compose run -it --rm <сервис> <исходная команда>' (см. Makefile, цель help). "+
		"Если контейнерного стека нет — проверь результат статически (чтение файлов, LSP-диагностика) и честно укажи в отчёте, что проверка на хосте недоступна.", tool)
}

func (ops *FileOps) Run(args map[string]any) ([]byte, error) {
	var params RunParams

	raw, err := json.Marshal(args)
	if err == nil {
		_ = json.Unmarshal(raw, &params)
	}

	if params.Command == "" {
		resultJSON, _ := json.Marshal(map[string]string{
			"status":  "error",
			"message": "команда не указана",
		})
		return resultJSON, nil
	}

	// Защита от зависания: долгоживущие серверные команды (go run server/main.go,
	// npm run dev и т.п.) не завершаются сами — вместо ожидания таймаута сразу
	// возвращаем ошибку с инструкцией верифицировать через сборку и автотесты.
	if hint, ok := longRunningHint(params.Command); ok {
		resultJSON, _ := json.Marshal(map[string]string{
			"status":  "error",
			"message": hint,
		})
		return resultJSON, nil
	}

	result, err := runCommand(params.Command, ops.OutputDir, ops.ProjectName())
	if err != nil {
		return nil, err
	}

	resultJSON, _ := json.Marshal(result)
	return resultJSON, nil
}

// longRunningHint определяет, похожа ли команда на запуск долгоживущего
// процесса (сервер/дев-режим), и возвращает пояснение для модели. Длинные
// команды «запуска приложения» не завершаются до таймаута, и агент виснет на
// 60+ секунд в ожидании вывода; для проверки правильности кода достаточно
// сборки и автотестов.
func longRunningHint(command string) (string, bool) {
	cmd := strings.TrimSpace(command)
	lower := strings.ToLower(cmd)
	// Команды для фонового запуска/быстрой проверки (с sleep-овым прогревом и
	// последующим curl) не блокируют шаг — пропускаем хинт.
	if strings.Contains(lower, "&") || strings.Contains(lower, "sleep") && strings.Contains(lower, "curl") {
		return "", false
	}
	// Запуск приложения/сервера через go run (server/main.go, ./, cmd/server
	// и т.п.) не завершается — блокируем, оставляя go run только для быстрых
	// утилит/скриптов, которые не ведут себя как серверы.
	if idx := strings.Index(lower, "go run"); idx >= 0 {
		rest := strings.TrimSpace(lower[idx+len("go run"):])
		target := rest
		if i := strings.IndexAny(rest, " \t\n"); i >= 0 {
			target = rest[:i]
		}
		blocked := target == "." || target == "./" || target == "./main" || target == "./app"
		for _, marker := range []string{"server", "cmd/", "main.go", "/app", "app/"} {
			if strings.Contains(target, marker) {
				blocked = true
				break
			}
		}
		if blocked {
			return "Команда похожа на запуск приложения/сервера через go run (например, go run server/main.go) — процесс не завершается сам, и шаг завис бы на таймауте. Не запускай приложение: вместо этого проверь код через сборку и автотесты (go build ./... и go test ./...), а поднятие сервисов опиши в README.", true
		}
		return "", false
	}
	fragments := []string{
		"npm run dev",
		"npm start",
		"npm run start",
		"npm run serve",
		"yarn start",
		"yarn run start",
		"yarn dev",
		"yarn run dev",
		"pnpm dev",
		"pnpm start",
		"bun dev",
		"bun run dev",
		"bun start",
		"npx dev",
		"npx run dev",
		"vite dev",
		"next dev",
		"nuxt dev",
		"ng serve",
		"quasar dev",
		"uvicorn",
		"gunicorn",
		"flask run",
		"fastapi dev",
		"python app",
		"python3 app",
		"python main",
		"node server",
		"node index",
		"deno run",
		"docker compose up",
		"docker-compose up",
		"docker-compose -f up",
		"kubectl port-forward",
		"helm install",
		"dotnet run",
		"java -jar",
		"rails server",
		"python manage.py runserver",
		"python manage.py migrate",
		"celery worker",
	}
	for _, f := range fragments {
		if strings.Contains(lower, f) {
			return "Команда похожа на запуск долгоживущего фронтенд-процесса/сервера в дев-режиме (npm/pnpm/yarn run dev|start и аналоги), который не завершается сам — шаг завис бы на таймауте. Не запускай приложение: для фронтенда проверь через сборку и тесты (npm run build / pnpm build / yarn build и npm test / pnpm test / yarn test по аналогии), для бэкенда — go build ./... и go test ./....", true
		}
	}
	return "", false
}
