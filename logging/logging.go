// Package logging — двойное логирование: подробный лог пишется в файл
// logs/<проект>.log, а в консоль выводится человекочитаемая сводка.
//
// Основной поток running-агентов (backend/frontend/plan/accept) вызывает
// Setup с именем проекта, после чего все уровни логирования работают:
//   - Info*  — и консоль, и файл (ключевые события, прогресс, вердикты);
//   - Detail* — только файл (подробности, сырые ответы, служебные операции);
//   - Warn*  — консоль + файл (предупреждения);
//   - Fatal* — консоль + файл + os.Exit(1).
//
// Если Setup не вызывался (например, в тестах) — работаем в режиме «только
// консоль»: Detail-сообщения отбрасываются, файлы не создаются, чтобы не
// шуметь и не засорять каталог logs/.
//
// Несколько проектов одновременно (режим serve): один процесс ведёт много
// проектов, поэтому общего файла недостаточно. Setup задаёт файл ПО УМОЛЧАНИЮ
// (logs/server.log — служебные сообщения процесса), а For(project) возвращает
// именованный логгер, пишущий в logs/<проект>.log. Файлы проектов открываются
// лениво и переиспользуются, поэтому параллельные сессии не мешают друг другу:
// каждая строка попадает в файл своего проекта.
package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// unnamed — имя файла, когда имя проекта не удалось вывести.
const unnamed = "unnamed"

// logDir — каталог лог-файлов. Можно переопределить переменной LOG_DIR.
func logDir() string {
	if d := strings.TrimSpace(os.Getenv("LOG_DIR")); d != "" {
		return d
	}
	return "logs"
}

var (
	mu sync.Mutex
	// configured — вызывался ли Setup. До него файлы не создаются: тесты и
	// утилиты работают в консольный режим.
	configured bool
	// defName — приведённое имя файла по умолчанию (без ".log").
	defName string
	// file — поток файла по умолчанию.
	file *os.File
	// sinks — открытые файлы проектов: приведённое имя → поток.
	sinks = map[string]*os.File{}
)

// Setup открывает (или создаёт) файл logs/<name>.log в режиме дозаписи и
// начинает вести в него подробный лог по умолчанию. Повторный вызов закрывает
// предыдущий файл. При ошибке открытия работаем с консольным логом (без
// падения).
//
// В режиме serve сюда передают имя процесса ("server"), а логи отдельных
// проектов идут через For(project).
func Setup(name string) {
	mu.Lock()
	defer mu.Unlock()

	configured = true

	if file != nil {
		_ = file.Close()
		file = nil
	}

	safe := SanitizeName(name)
	if safe == "" {
		safe = unnamed
	}
	defName = safe

	// Файл с таким именем мог быть открыт как проектный — переиспользуем его,
	// иначе два потока писали бы в один файл независимо друг от друга.
	if f := sinks[safe]; f != nil {
		delete(sinks, safe)
		file = f
	} else {
		dir := logDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "logging: не удалось создать %s: %v\n", dir, err)
			return
		}
		f, err := os.OpenFile(filepath.Join(dir, safe+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "logging: не удалось открыть лог-файл %s/%s.log: %v\n", dir, safe, err)
			return
		}
		file = f
	}

	fmt.Fprintf(file, "\n=== Сессия %s (проект: %s) ===\n", time.Now().Format("2006-01-02 15:04:05"), safe)
	fmt.Fprintf(file, "Команда: %s\n", strings.Join(os.Args, " "))
}

// Attach заранее открывает файл проекта (например, при старте сессии), чтобы
// первая же запись не платила за открытие каталога и файла.
func Attach(project string) {
	mu.Lock()
	defer mu.Unlock()
	if !configured {
		return
	}
	sinkFor(safeName(project))
}

// Detach закрывает файл проекта. Повторное обращение откроет его заново.
func Detach(project string) {
	mu.Lock()
	defer mu.Unlock()
	safe := safeName(project)
	if safe == defName {
		return // файл по умолчанию закрывает только Setup/Close
	}
	if f := sinks[safe]; f != nil {
		_ = f.Close()
		delete(sinks, safe)
	}
}

// Close закрывает все открытые лог-файлы (завершение работы процесса).
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		_ = file.Close()
		file = nil
	}
	for name, f := range sinks {
		_ = f.Close()
		delete(sinks, name)
	}
	configured = false
}

// ProjectFile возвращает путь к лог-файлу проекта (logs/<проект>.log).
// Полезно, когда путь нужен наружу (например, панели логов в Web UI).
func ProjectFile(project string) string {
	return filepath.Join(logDir(), ProjectLogName(project)+".log")
}

// ProjectLogName возвращает имя файла лога проекта (без расширения и каталога).
// Единая точка приведения имени: по ней файл создают Attach/For и по ней же
// его ищет панель логов, поэтому расхождений быть не может.
func ProjectLogName(project string) string {
	if s := SanitizeName(project); s != "" {
		return s
	}
	return unnamed
}

// safeName — алиас ProjectLogName для внутреннего использования.
func safeName(project string) string { return ProjectLogName(project) }

// sinkFor возвращает поток файла проекта, открывая его при первом обращении.
// Вызывать под mu. Если Setup ещё не вызывался — возвращает nil (консольный
// режим), поэтому тесты не создают файлов.
func sinkFor(safe string) *os.File {
	if !configured {
		return nil
	}
	if safe == defName && file != nil {
		return file
	}
	if f := sinks[safe]; f != nil {
		return f
	}
	dir := logDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "logging: не удалось создать %s: %v\n", dir, err)
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, safe+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logging: не удалось открыть лог-файл %s/%s.log: %v\n", dir, safe, err)
		return nil
	}
	fmt.Fprintf(f, "\n=== Сессия %s (проект: %s) ===\n", time.Now().Format("2006-01-02 15:04:05"), safe)
	sinks[safe] = f
	return f
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

// Logger — именованный лог проекта: пишет в logs/<проект>.log и в консоль.
//
// Нулевой получатель (nil) допустим и означает «файл по умолчанию», поэтому
// владельцу не нужно проверять инициализацию перед каждым вызовом.
type Logger struct {
	project string
}

// For возвращает логгер проекта. Открытие файла откладывается до первой
// записи, поэтому вызов дёшев и безопасен в конструкторах.
func For(project string) *Logger { return &Logger{project: project} }

// name возвращает имя проекта или "" для записи в файл по умолчанию.
func (l *Logger) name() string {
	if l == nil {
		return ""
	}
	return l.project
}

// Infof пишет ключевое событие в лог проекта и в консоль.
func (l *Logger) Infof(format string, args ...any) {
	emit(l.name(), true, true, "INFO", format, args...)
}

// Detailf пишет подробность только в лог проекта.
func (l *Logger) Detailf(format string, args ...any) {
	emit(l.name(), false, true, "DETAIL", format, args...)
}

// Warnf пишет предупреждение в лог проекта и в консоль.
func (l *Logger) Warnf(format string, args ...any) {
	emit(l.name(), true, true, "WARN", format, args...)
}

// Fatalf пишет ошибку в лог проекта и в консоль, затем завершает процесс.
func (l *Logger) Fatalf(format string, args ...any) {
	emit(l.name(), true, true, "FATAL", format, args...)
	os.Exit(1)
}

// Infof выводит сообщение в консоль (человекочитаемо) и пишет в файл по
// умолчанию.
func Infof(format string, args ...any) { emit("", true, true, "INFO", format, args...) }

// Detailf пишет подробное сообщение только в файл по умолчанию.
func Detailf(format string, args ...any) { emit("", false, true, "DETAIL", format, args...) }

// Warnf выводит предупреждение в консоль и пишет в файл по умолчанию.
func Warnf(format string, args ...any) { emit("", true, true, "WARN", format, args...) }

// Fatalf выводит ошибку в консоль, пишет в файл по умолчанию и завершает
// процесс.
func Fatalf(format string, args ...any) {
	emit("", true, true, "FATAL", format, args...)
	os.Exit(1)
}

// emit выполняет саму запись. console=true — сообщение попадает и в консоль;
// toFile=true — в файл проекта (project="") либо в файл по умолчанию.
//
// Форматирование и вывод в консоль идут вне мьютекса: блокировка нужна только
// на выбор потока и запись в файл.
func emit(project string, console, toFile bool, tag, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)

	if console {
		if tag == "WARN" || tag == "FATAL" {
			fmt.Fprintf(os.Stderr, "%s: %s\n", tag, msg)
		} else {
			fmt.Fprintln(os.Stdout, msg)
		}
	}
	if !toFile {
		return
	}

	mu.Lock()
	defer mu.Unlock()

	var f *os.File
	if project == "" {
		f = file
	} else {
		f = sinkFor(safeName(project))
	}
	if f == nil {
		return
	}

	ts := time.Now().Format("2006-01-02 15:04:05.000")
	if tag != "" && tag != "INFO" {
		fmt.Fprintf(f, "%s %-6s %s\n", ts, tag, msg)
	} else {
		fmt.Fprintf(f, "%s %s\n", ts, msg)
	}
}
