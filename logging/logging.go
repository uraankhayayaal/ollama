// Package logging — двойное логирование: подробный лог пишется в файл
// logs/<проект>.log, а в консоль выводится человекочитаемая сводка.
//
// Основной поток running-агентов (generate/refactor/plan/accept) вызывает
// Setup с именем проекта, после чего все уровни логирования работают:
//   - Info*  — и консоль, и файл (ключевые события, прогресс, вердикты);
//   - Detail* — только файл (подробности, сырые ответы, служебные операции);
//   - Warn*  — консоль + файл (предупреждения);
//   - Fatal* — консоль + файл + os.Exit(1).
//
// Если Setup не вызывался (например, в тестах) — работаем в режиме «только
// консоль»: Detail-сообщения отбрасываются, чтобы не шуметь в тестах.
package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// logDir — каталог лог-файлов. Можно переопределить переменной LOG_DIR.
func logDir() string {
	if d := strings.TrimSpace(os.Getenv("LOG_DIR")); d != "" {
		return d
	}
	return "logs"
}

var (
	mu   sync.Mutex
	file *os.File
)

// Setup открывает (или создаёт) файл logs/<name>.log в режиме дозаписи и
// начинает вести в него подробный лог. Повторный вызов закрывает предыдущий
// файл. При ошибке открытия работаем с консольным логом (без падения).
func Setup(name string) {
	mu.Lock()
	defer mu.Unlock()

	if file != nil {
		_ = file.Close()
		file = nil
	}

	safe := SanitizeName(name)
	if safe == "" {
		safe = "unnamed"
	}

	dir := logDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "logging: не удалось создать %s: %v\n", dir, err)
		return
	}

	f, err := os.OpenFile(filepath.Join(dir, safe+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logging: не удалось открыть лог-файл logs/%s.log: %v\n", safe, err)
		return
	}

	file = f
	fmt.Fprintf(file, "\n=== Сессия %s (проект: %s) ===\n", time.Now().Format("2006-01-02 15:04:05"), safe)
	fmt.Fprintf(file, "Команда: %s\n", strings.Join(os.Args, " "))
}

// SanitizeName приводит имя проекта к безопасному имени файла.
func SanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_.")
}

// Infof выводит сообщение в консоль (человекочитаемо) и пишет в файл.
func Infof(format string, args ...any) { write(true, true, "INFO", format, args...) }

// Detailf пишет подробное сообщение только в файл лога.
func Detailf(format string, args ...any) { write(false, true, "DETAIL", format, args...) }

// Warnf выводит предупреждение в консоль и пишет в файл.
func Warnf(format string, args ...any) { write(true, true, "WARN", format, args...) }

// Fatalf выводит ошибку в консоль, пишет в файл и завершает процесс.
func Fatalf(format string, args ...any) {
	write(true, true, "FATAL", format, args...)
	os.Exit(1)
}

// write выполняет саму запись. console=true — сообщение попадает и в консоль;
// toFile=true — в файл (в подробном режиме с таймстампом).
func write(console, toFile bool, tag, format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()

	msg := fmt.Sprintf(format, args...)

	if console {
		if tag == "WARN" || tag == "FATAL" {
			fmt.Fprintf(os.Stderr, "%s: %s\n", tag, msg)
		} else {
			fmt.Fprintln(os.Stdout, msg)
		}
	}

	if toFile && file != nil {
		ts := time.Now().Format("2006-01-02 15:04:05.000")
		if tag != "" && tag != "INFO" {
			fmt.Fprintf(file, "%s %-6s %s\n", ts, tag, msg)
		} else {
			fmt.Fprintf(file, "%s %s\n", ts, msg)
		}
	}
}