package tools

// Тесты ReadAppLogs (Ф-2 PLAN-2026-09-19-todo-owerview-for-prom.md).
//
// Hermetic: сетевых вызовов нет, «приложение» — настоящий дочерний процесс,
// который печатает заданные строки и ведёт себя как надо (падает / молчит /
// работает вечно). Так проверяется ровно то, ради чего инструмент написан:
// построчный захват stdout+stderr, остановка группы процессов, код выхода и
// подсказки при degrade.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func applogsResult(t *testing.T, raw string) AppLogsResult {
	t.Helper()
	ops := &FileOps{OutputDir: t.TempDir()}
	var args map[string]any
	if raw != "" {
		if err := jsonUnmarshalStrict(raw, &args); err != nil {
			t.Fatalf("разбор аргументов %s: %v", raw, err)
		}
	}
	return ops.readAppLogs(args)
}

func jsonUnmarshalStrict(raw string, out *map[string]any) error {
	return json.Unmarshal([]byte(raw), out)
}

func TestReadAppLogsCapturesStdoutAndStderr(t *testing.T) {
	// Пишем в оба потока: если инструмент возьмёт только stdout, тест упадёт
	// на отсутствии строки из stderr.
	res := applogsResult(t, `{"command":"echo out-line; echo err-line 1>&2","wait_ms":400}`)
	if res.Status != "ok" {
		t.Fatalf("status: %q (%s)", res.Status, res.Message)
	}
	if res.Source != appLogSourceLocal || res.Command == "" {
		t.Fatalf("источник/команда: %+v", res)
	}
	joined := strings.Join(res.Lines, "\n")
	for _, want := range []string{"out-line", "err-line"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("нет строки %q в логе:\n%s", want, joined)
		}
	}
	if res.LineCount != len(res.Lines) {
		t.Fatalf("line_count=%d, строк=%d — хвост не обрезан, но счётчик разошёлся", res.LineCount, len(res.Lines))
	}
}

func TestReadAppLogsReportsExitCodeOnCrash(t *testing.T) {
	// Приложение падает само: код выхода — главный сигнал для модели.
	res := applogsResult(t, `{"command":"echo 'panic: runtime error' 1>&2; exit 3","wait_ms":4000}`)
	if res.ExitCode != 3 {
		t.Fatalf("exit_code=%d, ждём 3 (%s)", res.ExitCode, res.Message)
	}
	if res.Stopped {
		t.Fatalf("процесс завершился сам, stopped должен быть false: %+v", res)
	}
	if len(res.Lines) != 1 || !strings.Contains(res.Lines[0], "panic") {
		t.Fatalf("не потеряли стек-трейс: %+v", res.Lines)
	}
	if !strings.Contains(res.Message, "3") {
		t.Fatalf("в сообщении нет кода выхода: %q", res.Message)
	}
}

func TestReadAppLogsStopsLongRunningProcess(t *testing.T) {
	start := time.Now()
	// «Вечный» сервер: без остановки инструмент вис бы вечно — именно ради
	// этого теста и введено приложение kill-а группы процессов.
	res := applogsResult(t, `{"command":"echo listening on :8080; sleep 30","wait_ms":300}`)
	if !res.Stopped {
		t.Fatalf("процесс должен быть остановлен инструментом: %+v", res)
	}
	// Код выхода после SIGTERM равен -1 («убит сигналом») — это НЕ падение
	// приложения, и сообщение обязано это различать, иначе модель начнёт
	// искать причину в несуществующем стектрейсе.
	if res.ExitCode != -1 {
		t.Fatalf("exit_code=%d, ждём -1 (убит сигналом)", res.ExitCode)
	}
	if res.Status != "ok" {
		t.Fatalf("status: %q, ждём ok — остановка инструментом не ошибка", res.Status)
	}
	if !strings.Contains(res.Message, "остановлено инструментом") {
		t.Fatalf("сообщение должно отличать остановку инструментом от падения: %q", res.Message)
	}
	if !strings.Contains(strings.Join(res.Lines, "\n"), "listening") {
		t.Fatalf("потеряли строку запуска: %+v", res.Lines)
	}
	// + допуск на медленный CI: важно, что ждали примерно wait, а не 30 секунд.
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("инструмент ждал %v — процесс не остановлен", elapsed)
	}
}

func TestReadAppLogsSilentLongRunningIsSkipped(t *testing.T) {
	// Молчащий долгоживущий процесс: инструмент отработал, но логов нет. Это
	// НЕ ошибка, а degraded-результат с подсказкой — иначе модель будет искать
	// стектрейс, которого нет.
	res := applogsResult(t, `{"command":"sleep 30","wait_ms":300}`)
	if res.Status != "skipped" {
		t.Fatalf("status: %q, ждём skipped", res.Status)
	}
	if res.Hint == "" {
		t.Fatalf("нет подсказки при skipped: %+v", res)
	}
}

func TestReadAppLogsSilentShortRunIsOk(t *testing.T) {
	// Разовая команда без вывода отработала штатно: логи пусты, но инструмент
	// сработал — degraded-статус здесь был бы враньём.
	res := applogsResult(t, `{"command":"true","wait_ms":300}`)
	if res.Status != "ok" {
		t.Fatalf("status: %q, ждём ok", res.Status)
	}
	if !strings.Contains(res.Message, "ни одной строки") {
		t.Fatalf("сообщение должно сообщать об отсутствии строк: %q", res.Message)
	}
}

func TestReadAppLogsTailLimit(t *testing.T) {
	// Больше строк, чем просят: в ответ должен попасть только хвост, и хвост
	// должен быть ПОСЛЕДНИМ (там причина падения), а не первым.
	res := applogsResult(t, `{"command":"i=1; while [ $i -le 50 ]; do echo line-$i; i=$((i+1)); done","lines":5,"wait_ms":4000}`)
	if len(res.Lines) != 5 {
		t.Fatalf("строк: %d, ждём 5: %+v", len(res.Lines), res.Lines)
	}
	if res.LineCount != 50 {
		t.Fatalf("line_count=%d, ждём 50", res.LineCount)
	}
	if !res.Truncated {
		t.Fatalf("truncated должен быть true при 50 строках и лимите 5")
	}
	if res.Lines[4] != "line-50" {
		t.Fatalf("хвост должен оканчиваться последней строкой, получено %q", res.Lines[4])
	}
}

func TestReadAppLogsUndetectableRunIsSkipped(t *testing.T) {
	// Пустой каталог: запускать нечем. Инструмент обязан деградировать с
	// подсказкой, а не возвращать error или выдумывать команду.
	res := applogsResult(t, "")
	if res.Status != "skipped" {
		t.Fatalf("status: %q, ждём skipped", res.Status)
	}
	if res.Hint == "" || !strings.Contains(res.Hint, "command") {
		t.Fatalf("подсказка должна предлагать задать command: %q", res.Hint)
	}
}

func TestReadAppLogsBadSourceIsError(t *testing.T) {
	res := applogsResult(t, `{"source":"kubernetes","command":"true"}`)
	if res.Status != "error" {
		t.Fatalf("status: %q, ждём error", res.Status)
	}
	if !strings.Contains(res.Message, "source") {
		t.Fatalf("сообщение должно называть проблемный параметр: %q", res.Message)
	}
}

func TestReadAppLogsSendsLiveLinesToSink(t *testing.T) {
	// Живая трансляция — то, ради чего «рантайм»-вкладка вообще появляется:
	// строки должны уйти подписчику проекта ДО возврата ответа.
	dir := t.TempDir()
	var got []string
	SetAppLogSink(dir, func(source, line string) {
		got = append(got, source+"|"+line)
	})
	t.Cleanup(func() { SetAppLogSink(dir, nil) })

	ops := &FileOps{OutputDir: dir}
	res := ops.readAppLogs(map[string]any{
		"command": "echo alpha; echo beta 1>&2",
		"wait_ms": 400,
	})
	if res.Status != "ok" {
		t.Fatalf("status: %q", res.Status)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"local|alpha", "local|beta"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("подписчик не получил %q, получено:\n%s", want, joined)
		}
	}
}

func TestReadAppLogsTailAvailableForRunner(t *testing.T) {
	// Хвост должен быть забираемым РОВНО ОДИН раз: иначе один и тот же лог
	// подмешивается модели в двух раундах подряд и «самокоррекция» зациклится.
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}
	res := ops.readAppLogs(map[string]any{"command": "echo crash-reason", "wait_ms": 400})
	if len(res.Lines) == 0 {
		t.Fatalf("нет строк: %+v", res)
	}
	src, lines := ops.TakeAppLogTail()
	if src != appLogSourceLocal || len(lines) == 0 {
		t.Fatalf("хвост не отдан раннеру: %q %+v", src, lines)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "crash-reason") {
		t.Fatalf("в хвосте нет строки приложения: %+v", lines)
	}
	if _, again := ops.TakeAppLogTail(); len(again) != 0 {
		t.Fatalf("хвост должен забираться один раз, второй вернул %+v", again)
	}
}

func TestResolveRunCommandPrefersMakefile(t *testing.T) {
	// Порядок детекта: Makefile — единая точка входа по Ф-1..Ф-5 плана
	// makefile, npm-скрипт и типовой запуск по стеку идут следом.
	dir := t.TempDir()
	writeFile(t, dir, "Makefile", "build:\n\tgo build ./...\n\nrun:\n\tgo run .\n")
	writeFile(t, dir, "package.json", `{"scripts":{"dev":"vite"}}`)
	if got := resolveRunCommand(dir); got != "make run" {
		t.Fatalf("команда: %q, ждём \"make run\"", got)
	}
}

func TestResolveRunCommandFromPackageJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"scripts":{"build":"tsc","start":"node dist/main.js"}}`)
	if got := resolveRunCommand(dir); got != "npm run start" {
		t.Fatalf("команда: %q, ждём \"npm run start\"", got)
	}
}

func TestResolveRunCommandPicksPackageManagerByLock(t *testing.T) {
	// pnpm/yarn-проекты на чистой машине без соответствующего пакетного
	// менеджера не запустятся: выбор по lock-файлу, а не всегда npm.
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"scripts":{"dev":"next dev"}}`)
	writeFile(t, dir, "pnpm-lock.yaml", "lockfileVersion: '9'\n")
	if got := resolveRunCommand(dir); got != "pnpm run dev" {
		t.Fatalf("команда: %q, ждём \"pnpm run dev\"", got)
	}
}

func TestResolveRunCommandFromStack(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module demo\n\ngo 1.22\n")
	writeFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	if got := resolveRunCommand(dir); got != "go run ." {
		t.Fatalf("команда: %q, ждём \"go run .\"", got)
	}
}

func TestReadAppLogsAutoUsesComposeWhenNoRunCommand(t *testing.T) {
	// Только compose-файл: локально запускать нечем, поэтому auto переходит на
	// docker-источник. Само чтение в тесте не выполняется — проверяем выбор и
	// корректный degrade, когда docker недоступен.
	dir := t.TempDir()
	writeFile(t, dir, "docker-compose.yml", "services:\n  api:\n    build: .\n")
	ops := &FileOps{OutputDir: dir}
	res := ops.readAppLogs(nil)
	if res.Source != appLogSourceDocker {
		t.Fatalf("источник: %q, ждём docker", res.Source)
	}
	if res.Status == "ok" {
		t.Skipf("docker доступен в этой среде — чтение compose логов пропускаем: %+v", res)
	}
	if res.Status != "skipped" || res.Hint == "" {
		t.Fatalf("ожидали skipped с подсказкой: %+v", res)
	}
}

func TestMakefileTargetsIgnoresVariablesAndSpecials(t *testing.T) {
	// Разбор целей поверхностный, но не должен принимать за цель присваивание
	// переменной («NAME := app») — иначе инструмент предложил бы «make NAME».
	// «build» после «.PHONY:» — зависимость спец-цели, а не цель.
	targets := makefileTargets("NAME := app\n\nbuild test:\n\tgo build ./...\n\n.PHONY: build\n")
	if targets["NAME"] {
		t.Fatalf("присваивание переменной принято за цель: %+v", targets)
	}
	for _, want := range []string{"build", "test"} {
		if !targets[want] {
			t.Fatalf("цель %q не разобрана: %+v", want, targets)
		}
	}
	if len(makefileTargets("NAME := app\n")) != 0 {
		t.Fatalf("разобраны лишние цели: %+v", makefileTargets("NAME := app\n"))
	}
}

func TestMakefileTargetsTreatsPrerequisitesAsNotTargets(t *testing.T) {
	// Семантика make: цель — только то, что ДО двоеточия. «build» в
	// «multi: build test» — зависимость, и предлагать «make build» там, где
	// такой цели нет, значило бы подсунуть модели несуществующую команду.
	targets := makefileTargets("multi: build test\n\tgo build\n")
	if !targets["multi"] {
		t.Fatalf("цель multi не разобрана: %+v", targets)
	}
	for _, dep := range []string{"build", "test"} {
		if targets[dep] {
			t.Fatalf("зависимость %q принята за цель: %+v", dep, targets)
		}
	}
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("запись %s: %v", name, err)
	}
}
