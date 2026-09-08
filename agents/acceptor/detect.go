package acceptor

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Kind — тип проекта, определяемый по маркерам в корне директории.
type Kind string

const (
	KindGo      Kind = "go"
	KindNode    Kind = "node"
	KindPython  Kind = "python"
	KindUnknown Kind = "unknown"
)

// DetectKind определяет тип проекта по маркерам в корне директории.
// Приоритет: go.mod → package.json → требовательные питон-маркеры.
func DetectKind(dir string) Kind {
	switch {
	case hasFile(dir, "go.mod"):
		return KindGo
	case hasFile(dir, "package.json"):
		return KindNode
	case hasFile(dir, "requirements.txt"),
		hasFile(dir, "pyproject.toml"),
		hasFile(dir, "setup.py"),
		hasFile(dir, "main.py"),
		hasFile(dir, "app.py"):
		return KindPython
	default:
		return KindUnknown
	}
}

// ProjectRoot — один обнаруживаемый проект (под)приёмки со своим типом.
type ProjectRoot struct {
	// Dir — абсолютный путь к проекту.
	Dir string
	// Rel — относительный путь от корня приёмки ("" для самого корня,
	// "frontend", "server" — для подпроектов монорепозитория).
	Rel string
	// Kind — тип проекта (go/node/python).
	Kind Kind
}

// DetectProjects находит проекты в директории приёмки:
//   - если в корне есть маркер проекта (go.mod/package.json/…) — это один
//     проект в корне (Rel == "");
//   - иначе просматриваем прямые подкаталоги и возвращаем те, в которых
//     есть собственные маркеры (фронтенд и бэкенд монорепозитория);
//   - каталоги без маркеров и служебные (node_modules, dist, .git, …)
//     игнорируются.
//
// Результат детерминирован: сортировка по имени подкаталога.
func DetectProjects(dir string) []ProjectRoot {
	if kind := DetectKind(dir); kind != KindUnknown {
		return []ProjectRoot{{Dir: dir, Rel: "", Kind: kind}}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var roots []ProjectRoot
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || isIgnoredDir(name) {
			continue
		}
		sub := filepath.Join(dir, name)
		if kind := DetectKind(sub); kind != KindUnknown {
			roots = append(roots, ProjectRoot{Dir: sub, Rel: name, Kind: kind})
		}
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].Rel < roots[j].Rel })
	return roots
}

func hasFile(dir, name string) bool {
	info, err := os.Stat(filepath.Join(dir, name))
	return err == nil && !info.IsDir()
}

// buildCommand возвращает команду сборки проекта, определённую по типу.
// Пустая строка означает, что сборка для данного типа не требуется.
func (k Kind) buildCommand(dir string) string {
	switch k {
	case KindGo:
		return "go build ./..."
	case KindNode:
		// Если есть npm-скрипт build — запускаем его, есть tsconfig без
		// скрипта — проверяем типы через tsc --noEmit. Иначе сборки нет.
		if nodeHasScript(dir, "build") {
			return "npm run build"
		}
		if hasFile(dir, "tsconfig.json") {
			return "npx tsc --noEmit"
		}
		return ""
	case KindPython:
		// Питон не компилируется, но syntax-check всех модулей ловит
		// синтаксические ошибки до запуска (дёшево и быстро).
		return pythonCmd() + " -m compileall -q ."
	default:
		return ""
	}
}

// runCommand возвращает команду запуска приложения, определённую по типу.
// Пустая строка означает, что точку входа определить не удалось.
func (k Kind) runCommand(dir string) string {
	switch k {
	case KindGo:
		if hasFile(dir, "main.go") {
			return "go run ."
		}
		if mainPkg := findGoMainPkg(dir); mainPkg != "" {
			return "go run " + mainPkg
		}
		return ""
	case KindNode:
		if sc := nodeStartScript(dir); sc != "" {
			return sc
		}
		if m := nodeMain(dir); m != "" {
			return "node " + m
		}
		if nodeHasEntryFile(dir) {
			return "node ."
		}
		// Статический фронтенд (Vite/React/Vue и т.п.): есть build-скрипт,
		// но нет серверной точки входа для запуска — приёмка по сборке
		// достаточна, запуск не требуется.
		return ""
	case KindPython:
		if hasFile(dir, "main.py") {
			return pythonCmd() + " main.py"
		}
		if hasFile(dir, "app.py") {
			return pythonCmd() + " app.py"
		}
		return ""
	default:
		return ""
	}
}

// formatCommand возвращает команду проверки стилизатора и название
// инструмента. Пустая команда — стилизатор для проекта не найден/не настроен.
// Команда не должна МЕНЯТЬ файлы — только сообщать о нарушениях.
func (k Kind) formatCommand(dir string) (cmd, tool string) {
	switch k {
	case KindGo:
		// gofmt -l печатает список неотформатированных файлов и завершается 0.
		return "find . -name '*.go' -not -path './vendor/*' -print0 | xargs -0 -r gofmt -l", "gofmt"
	case KindNode:
		// Только локальные бинари (npx умеет качать из сети — не для приёмки).
		if hasNodeBin(dir, "prettier") {
			return "./node_modules/.bin/prettier --check .", "prettier"
		}
		return "", ""
	case KindPython:
		if cmdAvailable("black") {
			return "black --check .", "black"
		}
		return "", ""
	default:
		return "", ""
	}
}

// analyzeCommand возвращает команду проверки анализатора и название
// инструмента. Пустая команда — анализатор не найден/не настроен.
func (k Kind) analyzeCommand(dir string) (cmd, tool string) {
	switch k {
	case KindGo:
		return "go vet ./...", "go vet"
	case KindNode:
		// Приоритет: скрипт lint в package.json, затем локальный eslint.
		if pj := readPackageJSON(dir); pj != nil && pj.Scripts["lint"] != "" && hasFile(dir, "node_modules") {
			return "npm run lint", "npm run lint"
		}
		if hasNodeBin(dir, "eslint") {
			return "./node_modules/.bin/eslint .", "eslint"
		}
		return "", ""
	case KindPython:
		if cmdAvailable("ruff") {
			return "ruff check .", "ruff"
		}
		if cmdAvailable("flake8") {
			return "flake8 .", "flake8"
		}
		return "", ""
	default:
		return "", ""
	}
}

// installCommand возвращает команду установки зависимостей и название
// инструмента. Пустая команда — устанавливать нечего (стандартная библиотека,
// нет манифеста зависимостей и т.п.).
func (k Kind) installCommand(dir string) (cmd, tool string) {
	switch k {
	case KindGo:
		// go mod download качает модули из go.sum/go.mod в модульный кеш.
		// Идемпотентен и безвреден для проектов только со стандартной
		// библиотекой.
		return "go mod download", "go mod download"
	case KindNode:
		if hasFile(dir, "package-lock.json") || hasFile(dir, "npm-shrinkwrap.json") {
			return "npm ci", "npm ci"
		}
		if hasFile(dir, "package.json") {
			return "npm install", "npm install"
		}
		return "", ""
	case KindPython:
		if hasFile(dir, "requirements.txt") {
			return pythonCmd() + " -m pip install -r requirements.txt", "pip install"
		}
		return "", ""
	default:
		return "", ""
	}
}

// packageJSON — минимальная структура package.json, нужная приёмщику.
type packageJSON struct {
	Main    string            `json:"main"`
	Scripts map[string]string `json:"scripts"`
}

func readPackageJSON(dir string) *packageJSON {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil
	}
	var pj packageJSON
	if err := json.Unmarshal(data, &pj); err != nil {
		return nil
	}
	return &pj
}

func nodeHasScript(dir, name string) bool {
	pj := readPackageJSON(dir)
	if pj == nil {
		return false
	}
	_, ok := pj.Scripts[name]
	return ok && pj.Scripts[name] != ""
}

// nodeStartScript возвращает npm-скрипт запуска: "node <main>", либо
// "npm run start", если он задан. Сначала пытаемся запустить напрямую
// через node, иначе через npm-скрипт.
func nodeStartScript(dir string) string {
	pj := readPackageJSON(dir)
	if pj == nil {
		return ""
	}
	if sc := pj.Scripts["start"]; sc != "" {
		// Скрипт уже исполняет node (например "node server.js") — используем
		// его как есть. Управляемые командные скрипты сложно валидировать,
		// поэтому полагаемся на стандартное значение.
		return "npm run start"
	}
	return ""
}

func nodeMain(dir string) string {
	pj := readPackageJSON(dir)
	if pj != nil && pj.Main != "" {
		main := filepath.ToSlash(pj.Main)
		if strings.HasSuffix(main, ".js") || strings.HasSuffix(main, ".mjs") {
			return main
		}
	}
	return ""
}

// nodeHasEntryFile проверяет наличие исполнимой точки входа (файла, который
// запускает сервер/приложение) в корне Node-проекта. Используется, чтобы
// отличить серверное приложение от статического фронтенда, у которого есть
// только build-скрипт (Vite/React/Vue): фронтенд принимать по сборке.
func nodeHasEntryFile(dir string) bool {
	for _, f := range []string{
		"index.js", "index.mjs", "index.cjs",
		"server.js", "server.mjs", "server.cjs",
		"app.js", "app.mjs", "main.js", "main.mjs",
	} {
		if hasFile(dir, f) {
			return true
		}
	}
	return false
}

// findGoMainPkg ищет пакет с функцией main ниже корня (например, cmd/server/)
// и возвращает путь для «go run <path>». Корневой main.go уже обработан
// вызывающим кодом. Возвращает "" если точка входа не найдена.
func findGoMainPkg(dir string) string {
	var candidates []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == dir {
			return nil
		}
		if d.IsDir() {
			if isIgnoredDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "main.go" {
			rel, rerr := filepath.Rel(dir, path)
			if rerr != nil {
				return nil
			}
			pkgDir := filepath.ToSlash(filepath.Dir(rel))
			if pkgDir != "." && pkgDir != "" {
				candidates = append(candidates, pkgDir)
				return filepath.SkipDir
			}
		}
		return nil
	})
	sort.Strings(candidates)
	if len(candidates) == 0 {
		return ""
	}
	return "." + "/" + candidates[0]
}

// hasNodeBin проверяет наличие локального бинаря в node_modules/.bin.
func hasNodeBin(dir, bin string) bool {
	return hasFile(filepath.Join(dir, "node_modules/.bin"), bin)
}

// pythonCmd выбирает исполняемый интерпретатор: на многих системах только
// python3. Полагаемся на PATH (обнаруживаемые команды не ищем в фиксированных
// путях, чтобы не зависеть от окружения).
func pythonCmd() string {
	if cmdAvailable("python3") {
		return "python3"
	}
	return "python"
}

// cmdAvailable проверяет наличие команды в PATH через exec.LookPath.
func cmdAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// isIgnoredDir — директории, в которые не спускаемся при поиске точки входа
// (зависимости, билды, кеши).
func isIgnoredDir(name string) bool {
	switch name {
	case "vendor", "node_modules", "dist", "build", ".git", "target", "__pycache__", ".venv", "venv":
		return true
	}
	return strings.HasPrefix(name, ".")
}
