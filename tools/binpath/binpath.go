// Package binpath — поиск исполняемых файлов (языковые серверы, чекеры)
// за пределами PATH процесса агента.
//
// Проблема: агент запускается как сервер/демон, и его PATH часто урезан —
// например, gopls стоит в ~/go/bin (go install), typescript-language-server —
// в префиксе npm, а в PATH процесса этих каталогов нет. Тогда exec.LookPath
// не находит бинарник, LSP-инструменты возвращают status skipped, и модель
// уходит обратно на дорогое чтение файлов (ReadFiles/ReadMap).
//
// Пакет нейтральный (не импортирует tools/lspclient), поэтому его используют
// и tools/lspclient (запуск серверов), и tools (CLI-чекеры, runCommand).
package binpath

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// ExtraPathEnv — переменная окружения со списком дополнительных каталогов
// поиска бинарников (разделитель — как у PATH). Имеет наивысший приоритет.
const ExtraPathEnv = "LSP_BIN_PATH"

var (
	dirsOnce sync.Once
	dirsList []string
)

// Dirs возвращает дополнительные каталоги поиска бинарников, которых может
// не быть в PATH процесса: каталоги из LSP_BIN_PATH, GOBIN/GOPATH/bin,
// ~/go/bin, ~/.local/bin, префиксы npm (включая nvm), Homebrew и /usr/local.
// Несуществующие каталоги отбрасываются; результат кэшируется на процесс.
func Dirs() []string {
	dirsOnce.Do(func() { dirsList = existingDirs(extraDirs()) })
	return dirsList
}

// Reset сбрасывает кэш каталогов (используется тестами после изменения
// переменных окружения).
func Reset() {
	dirsOnce = sync.Once{}
	dirsList = nil
}

// extraDirs собирает кандидатные каталоги в порядке приоритета.
func extraDirs() []string {
	var out []string

	if v := strings.TrimSpace(os.Getenv(ExtraPathEnv)); v != "" {
		out = append(out, filepath.SplitList(v)...)
	}

	// Go-бинарники (gopls, staticcheck и т.п.): GOBIN, затем GOPATH/bin,
	// затем каталог по умолчанию ~/go/bin.
	if v := strings.TrimSpace(os.Getenv("GOBIN")); v != "" {
		out = append(out, filepath.SplitList(v)...)
	}
	if v := strings.TrimSpace(os.Getenv("GOPATH")); v != "" {
		for _, p := range filepath.SplitList(v) {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, filepath.Join(p, "bin"))
			}
		}
	}

	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		out = append(out,
			filepath.Join(home, "go", "bin"),
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, ".npm-global", "bin"),
			filepath.Join(home, ".cargo", "bin"),
		)
		// nvm держит каждый Node отдельно: глобальные npm-пакеты
		// (typescript-language-server, pyright) лежат в bin конкретной версии.
		if m, gerr := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin")); gerr == nil {
			out = append(out, m...)
		}
	}

	out = append(out,
		"/opt/homebrew/bin",
		"/opt/homebrew/sbin",
		"/usr/local/bin",
		"/usr/local/go/bin",
		"/opt/local/bin",
	)
	return out
}

// existingDirs оставляет только существующие каталоги, убирая дубликаты.
func existingDirs(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, d := range in {
		d = strings.TrimSpace(d)
		if d == "" || seen[d] {
			continue
		}
		st, err := os.Stat(d)
		if err != nil || !st.IsDir() {
			continue
		}
		if abs, aerr := filepath.Abs(d); aerr == nil {
			d = abs
		}
		if seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// Look ищет исполняемый файл name: сначала в PATH процесса, затем в
// дополнительных каталогах Dirs(). Возвращает абсолютный путь и true, либо
// пустую строку и false. Имена с разделителем пути трактуются как путь.
func Look(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	if strings.Contains(name, string(filepath.Separator)) || strings.Contains(name, "/") {
		if IsExecutable(name) {
			return name, true
		}
		return "", false
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, true
	}
	for _, d := range Dirs() {
		p := filepath.Join(d, name)
		if IsExecutable(p) {
			return p, true
		}
	}
	return "", false
}

// Available сообщает, можно ли запустить бинарник name (в PATH или в
// дополнительных каталогах).
func Available(name string) bool {
	_, ok := Look(name)
	return ok
}

// IsExecutable проверяет, что путь — существующий исполняемый обычный файл.
func IsExecutable(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	return st.Mode().Perm()&0o111 != 0
}

// PathEnv возвращает значение PATH, дополненное каталогами Dirs() (без
// дубликатов, исходный порядок сохранён).
func PathEnv() string {
	return joinPath(mergePath(os.Getenv("PATH"), Dirs()))
}

// Env возвращает окружение base с расширенным PATH и без дублей переменных
// (при повторе ключа wins последнее значение — как в shell). Используется при
// запуске языковых серверов и команд инструмента Run: дочерний процесс
// (gopls → go, typescript-language-server → tsc) находит свои зависимости.
func Env(base []string) []string {
	idx := make(map[string]int, len(base))
	order := make([]string, 0, len(base))
	vals := make(map[string]string, len(base))
	for _, kv := range base {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, seen := idx[k]; !seen {
			idx[k] = len(order)
			order = append(order, k)
		}
		vals[k] = v
	}

	pathKey := "PATH"
	cur, has := vals[pathKey]
	if !has {
		order = append(order, pathKey)
	}
	vals[pathKey] = joinPath(mergePath(cur, Dirs()))

	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+vals[k])
	}
	return out
}

// mergePath добавляет к значению PATH каталоги extra, пропуская дубликаты.
func mergePath(pathVal string, extra []string) []string {
	var parts []string
	seen := make(map[string]bool)
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		parts = append(parts, p)
	}
	for _, p := range filepath.SplitList(pathVal) {
		add(p)
	}
	for _, p := range extra {
		add(p)
	}
	return parts
}

// joinPath склеивает список каталогов в значение PATH.
func joinPath(parts []string) string {
	return strings.Join(parts, string(os.PathListSeparator))
}
