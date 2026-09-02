package langdetect

import (
	"fmt"
	"regexp"
	"strings"
)

// Language — поддерживаемый язык для код-ревью.
type Language string

// Известные языки. Значения используются как ключи в базе промптов.
const (
	PHP        Language = "php"
	Go         Language = "go"
	Python     Language = "python"
	JavaScript Language = "javascript"
	TypeScript Language = "typescript"
	Java       Language = "java"
	Kotlin     Language = "kotlin"
	CSharp     Language = "csharp"
	Cpp        Language = "cpp"
	Ruby       Language = "ruby"
	Rust       Language = "rust"
	Swift      Language = "swift"
	SQL        Language = "sql"
	GraphQL    Language = "graphql"
	Shell      Language = "shell"
	HTML       Language = "html"
	CSS        Language = "css"
	Dockerfile Language = "dockerfile"
	YAML       Language = "yaml"
	JSON       Language = "json"
	Unknown    Language = ""
)

// extensionLang — карта расширений файлов на язык.
var extensionLang = map[string]Language{
	".php":        PHP,
	".phtml":      PHP,
	".go":         Go,
	".py":         Python,
	".pyw":        Python,
	".js":         JavaScript,
	".mjs":        JavaScript,
	".cjs":        JavaScript,
	".ts":         TypeScript,
	".tsx":        TypeScript,
	".mts":        TypeScript,
	".jsx":        JavaScript,
	".java":       Java,
	".kt":         Kotlin,
	".kts":        Kotlin,
	".cs":         CSharp,
	".cpp":        Cpp,
	".cc":         Cpp,
	".cxx":        Cpp,
	".hpp":        Cpp,
	".h":          Cpp,
	".rb":         Ruby,
	".ru":         Ruby,
	".rs":         Rust,
	".swift":      Swift,
	".sql":        SQL,
	".graphql":    GraphQL,
	".gql":        GraphQL,
	".sh":         Shell,
	".bash":       Shell,
	".zsh":        Shell,
	".html":       HTML,
	".htm":        HTML,
	".css":        CSS,
	".scss":       CSS,
	".less":       CSS,
	".dockerfile": Dockerfile,
	".yml":        YAML,
	".yaml":       YAML,
	".json":       JSON,
}

// filePathRe — извлекает путь из заголовка изменённого файла diff,
// вида "+++ b/src/app.php" или "+++ src/app.php".
var filePathRe = regexp.MustCompile(`(?:\+\+\+ b/|\+\+\+ )([\w./\-]+)`)

// Detect определяет преобладающий язык по тексту diff.
// Анализируются заголовки изменённых файлов (строки "+++ b/<path>").
// Возвращает Language; если ничего не распознано — Unknown.
func Detect(diff string) Language {
	counts := map[Language]int{}

	for _, line := range strings.Split(diff, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "+++") {
			continue
		}

		// Пропускаем /dev/null, нестадийные пути и индексные строки.
		if strings.Contains(line, "/dev/null") {
			continue
		}

		m := filePathRe.FindStringSubmatch(line)
		if len(m) < 2 {
			continue
		}
		path := m[1]
		if i := strings.Index(path, "\t"); i >= 0 {
			path = path[:i]
		}

		ext := extOf(path)
		lang, ok := extensionLang[ext]
		if ok {
			counts[lang]++
		}
	}

	return dominant(counts)
}

// extOf возвращает расширение файла (включая точку, в нижнем регистре)
// или "" если его нет.
func extOf(path string) string {
	base := path
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndex(base, "."); i >= 0 {
		return strings.ToLower(base[i:])
	}
	return ""
}

// dominant возвращает язык с наибольшим числом вхождений.
// При равенстве — any consistent; здесь берём первый максимальный.
func dominant(counts map[Language]int) Language {
	var best Language
	bestCount := 0
	for lang, n := range counts {
		if n > bestCount {
			best = lang
			bestCount = n
		}
	}
	return best
}

// String возвращает читаемое имя языка (для логов), или "".
func (l Language) String() string {
	if l == Unknown {
		return ""
	}
	if name, ok := languageNames[l]; ok {
		return name
	}
	return fmt.Sprintf("%q", string(l))
}

// languageNames — человекочитаемые названия языков.
var languageNames = map[Language]string{
	PHP:        "PHP",
	Go:         "Go",
	Python:     "Python",
	JavaScript: "JavaScript",
	TypeScript: "TypeScript",
	Java:       "Java",
	Kotlin:     "Kotlin",
	CSharp:     "C#",
	Cpp:        "C++",
	Ruby:       "Ruby",
	Rust:       "Rust",
	Swift:      "Swift",
	SQL:        "SQL",
	GraphQL:    "GraphQL",
	Shell:      "Shell",
	HTML:       "HTML",
	CSS:        "CSS",
	Dockerfile: "Dockerfile",
	YAML:       "YAML",
	JSON:       "JSON",
}
