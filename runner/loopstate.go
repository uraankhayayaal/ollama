package runner

import (
	"ai/tools"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Семантический отпечаток состояния проекта по проверке.
//
// Проблема: детектор по сигнатурам вызовов пропускает петлю, в которой агент
// каждый раз делает формально новый вызов — сдвигает строки в ReadFiles, меняет
// ключи в grep-подобном поиске, добавляет пробел в команду. Сигнатуры разные, а
// состояние проекта и текст падения — те же.
//
// Отпечаток берётся по САМОЙ ПРОВЕРКЕ (нормализованная команда), а не по её
// выводу. Это принципиально: живая петля выглядела так — одна и та же падающая
// проверка запускалась раунд за раундом, но агент подкручивал только фильтр
// вывода (`| grep -A5 "stderr"`, `| grep -B3 -A20 "FAIL"`, `| grep -i "console"`).
// Команды разные, но и ВЫВОД каждый раз разный: grep показывает разные куски
// одного и того же падения. Отпечаток по выводу модель обходит за секунду.
// Нормализованная команда (`cd … && npm test src/pages/Login.test.tsx`) при этом
// не меняется раунд за раундом.
//
// Вывод проверки тоже нормализуется (нормализация ниже), но он идёт не в ключ
// состояния, а в ТЕКСТ ВМЕШАТЕЛЬСТВА: модели нужно показать суть ошибки без
// шума (цвета терминала, абсолютные пути, временные метки, тайминги, номера
// строк, счётчики).

// runResult — разбор результата инструмента Run (см. tools/runCommandSandbox).
type runResult struct {
	Status  string `json:"status"`
	ExitErr string `json:"exit_error"`
	Stdout  string `json:"stdout"`
	Stderr  string `json:"stderr"`
	Message string `json:"message"`
	Workdir string `json:"workdir"`
}

var (
	// Абсолютный путь (≥3 сегментов) → «/…/последний». Убирает префиксы вида
	// /home/user/temp/.wt-task-42/, которые меняются от запуска к запуску.
	reAbsPath = regexp.MustCompile(`(?:/[A-Za-z0-9._+\-@]+){2,}/`)
	// Метки времени: 12:04:31, 12:04:31.123.
	reClock = regexp.MustCompile(`\b\d{1,2}:\d{2}:\d{2}(?:[.,]\d+)?\b`)
	// Длительности: 1.5s, 250ms, «12 ms» (с пробелом), 3m.
	reDuration = regexp.MustCompile(`\b\d+(?:[.,]\d+)?\s?(?:ns|us|µs|ms|s|m)\b`)
	// Адреса и хеши: 0x7ffd12ab, 9f3c… и любой длинный hex.
	reHex = regexp.MustCompile(`\b0x[0-9a-fA-F]+\b|\b[0-9a-fA-F]{12,}\b`)
	// Ссылки вида file.tsx:12:5, file.go:7 и «at 12:5» → номера нормализуются:
	// сдвинутая строка не должна выглядеть новой ошибкой.
	reRef = regexp.MustCompile(`([A-Za-z0-9_./\\-]+\.[A-Za-z0-9]{1,6}):(\d+)(?::(\d+))?`)
	// Очереди/счётчики в квадратных скобках: [2 failed], [1/5].
	reCounter = regexp.MustCompile(`\[\s*\d+(?:\s*/\s*\d+)?\s*\]`)
	// Пустые строки схлопываются: форматирование вывода не должно влиять на
	// отпечаток состояния.
	reBlank = regexp.MustCompile(`\n{2,}`)
)

// loopStateFromCall превращает результат вызова инструмента в семантический
// отпечаток состояния. Непустой результат означает «проверка упала».
func loopStateFromCall(name string, args map[string]any, result []byte) (state, sample, label string) {
	cmd := verifyCommandOf(name, args)
	if cmd == "" || !loopIsVerificationCommand(cmd) {
		return "", "", ""
	}

	var run runResult
	if err := json.Unmarshal([]byte(tools.SanitizeToolOutput(string(result))), &run); err != nil {
		return "", "", ""
	}
	if run.Status != "error" {
		// Проверка прошла: состояние подтверждено, падения нет.
		return "", "", ""
	}

	text := strings.TrimSpace(strings.Join([]string{run.ExitErr, run.Stderr, run.Stdout, run.Message}, "\n"))
	if text == "" {
		text = "проверка завершилась с ошибкой без вывода"
	}
	return verifyIdentity(cmd), normalizeVerifyOutput(text), cmd
}

// verifyCommandOf достаёт команду проверки из аргументов вызова инструмента.
// Только Run: остальные инструменты проверками не являются.
func verifyCommandOf(name string, args map[string]any) string {
	if name != "Run" || args == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(args["command"]))
}

// verifyIdentity приводит команду проверки к каноническому виду: убирает
// префикс `cd <каталог> &&` (рабочие каталоги у разных запусков разные),
// конвейер после первого `|` (grep/tail/head подкручиваются каждым раундом —
// именно этим петля и маскировалась), перенаправления потоков и флаги;
// сохраняет программу и её аргументы (имя теста/файла различать обязательно).
//
//	cd frontend && npm test -- --run src/pages/Login.test.tsx 2>&1 | grep -B5 -A30 "FAIL"
//	  → cd … && npm test src/pages/Login.test.tsx
func verifyIdentity(cmd string) string {
	head, _, _ := strings.Cut(cmd, "|")
	head = strings.TrimSpace(head)

	fields := strings.Fields(head)
	out := make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if strings.HasPrefix(f, "-") || strings.Contains(f, ">&") || strings.Contains(f, "</dev/") || strings.Contains(f, ">") {
			continue
		}
		// Префикс `cd <каталог> &&`: сам каталог — шум (рабочая копия меняется),
		// но сам переход в подкаталог остаётся частью идентичности.
		if f == "cd" && i+1 < len(fields) {
			out = append(out, "cd", "…")
			i++ // каталог и следующий за ним разделитель пропускаем
			continue
		}
		out = append(out, f)
	}
	return strings.Join(out, " ")
}

// loopIsVerificationCommand отличает проверочную команду (тесты, сборка, линт,
// типы) от произвольной работы вроде git или ls. Именно по падению проверки
// судят, что проект не чинится.
func loopIsVerificationCommand(cmd string) bool {
	lower := strings.ToLower(cmd)
	for _, token := range []string{
		"test", "tests", "spec", "jest", "vitest", "playwright", "cypress",
		"mocha", "pytest", "rspec", "phpunit", "junit",
		"lint", "vet", "typecheck", "tsc", "check", "verify",
		"build", "make", "npm run", "pnpm", "yarn", "cargo", "go test",
	} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

// normalizeVerifyOutput приводит вывод проверки к каноническому виду: убирает
// ANSI, абсолютные пути, метки времени, тайминги, номера строк и счётчики,
// схлопывает пустые строки и обрезает до предела.
func normalizeVerifyOutput(text string) string {
	s := tools.SanitizeToolOutput(text)
	s = reAbsPath.ReplaceAllString(s, "/…/")
	s = reClock.ReplaceAllString(s, "<время>")
	s = reDuration.ReplaceAllString(s, "<длительность>")
	s = reHex.ReplaceAllString(s, "<хеш>")
	s = reRef.ReplaceAllString(s, "$1:<стр>")
	s = reCounter.ReplaceAllString(s, "[…]")
	s = reBlank.ReplaceAllString(s, "\n")

	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
		if len(out) >= loopVerifySampleLines {
			break
		}
	}
	return strings.Join(out, "\n")
}

// loopVerifySampleLines — сколько строк вывода проверки нормализуется в
// отпечаток и в текст вмешательства.
const loopVerifySampleLines = 12

// loopVerifyInjection — сообщение принудительного вмешательства: почему харнес
// считает, что проверка не двигается, что он заблокировал и что делать вместо
// повторного запуска. identity показывается отдельной строкой, чтобы модель
// видела: одинаковым был не текст вывода (grep меняет его каждый раунд), а сам
// запуск проверки.
func loopVerifyInjection(label, identity, sample string, runs int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "ВНИМАНИЕ ХАРНЕСА: ты уже %d раундов подряд запускаешь одну и ту же проверку, и она падает.\n", runs)
	fmt.Fprintf(&b, "Проверка: %s\n", label)
	fmt.Fprintf(&b, "Что именно повторяется: %s\n", identity)
	b.WriteString("Суть ошибки (нормализованная — пути и номера строк приведены к общему виду):\n")
	b.WriteString(Truncate(sample, 1200))
	b.WriteString("\n\n")
	b.WriteString("Это не «не повезло»: твои правки не меняют результат этой проверки, а менять фильтр вывода (grep, tail, -A/-B, номер строки) — не решение. Не запускай её снова: харнес заблокирует повторный запуск.\n")
	b.WriteString("Смени стратегию:\n")
	b.WriteString("1. Перечитай саму проверку и то, что она проверяет: возможно, падает не твой код, а сама проверка (ожидание, фикстура, мок, снапшот, окружение, порядок вызовов).\n")
	b.WriteString("2. Асинхронные ожидания: если проверка ждёт результат асинхронной операции, нужен явный waitFor/await findBy* и корректный таймаут, а не фиксированная пауза и не проверка «сразу после».\n")
	b.WriteString("3. Окружение тестов: в JSDOM нет настоящей навигации — прямой вызов window.location/location.assign/reload валит тест, используй useNavigate или мок роутера; также проверь, что jsdom-окружение вообще создано (vitest environment, setup-файл, polyfill matchMedia/IntersectionObserver/ResizeObserver).\n")
	b.WriteString("4. Проверь самое последнее изменение: какая строка падает и что в ней ожидается на самом деле.\n")
	b.WriteString("5. Раздели проверку на части (например, только один тест) и найди минимальный воспроизводящий случай.\n")
	b.WriteString("Внеси правку в причину, а не в симптом, и только затем запусти проверку снова.\n")
	return b.String()
}
