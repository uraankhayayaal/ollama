package tools

import (
	"ai/infrastructure/forges"
	"path/filepath"
	"strings"
)

// testDirNames — имена каталогов, содержимое которых считается тестовым:
// любой файл внутри них — тест (или тестовая поддержка/фикстура), поэтому в
// режиме «только тесты» запись туда разрешена.
var testDirNames = map[string]bool{
	"test": true, "tests": true, "__tests__": true,
	"e2e": true, "spec": true, "specs": true,
	"testdata": true, "test-data": true,
}

// testConfigFiles — имена конфигов тест-раннеров: это не сами тесты, но
// QA-инженер проектирует под них окружение, поэтому запись разрешена.
var testConfigFiles = map[string]bool{
	"playwright.config.ts": true, "playwright.config.js": true,
	"playwright.config.mts": true, "playwright.config.mjs": true,
	"cypress.config.ts": true, "cypress.config.js": true,
	"jest.config.ts": true, "jest.config.js": true,
	"jest.config.mjs": true, "jest.config.cjs": true,
	"vitest.config.ts": true, "vitest.config.js": true,
	"vitest.config.mts": true, "vitest.config.mjs": true,
	"mocha.opts": true, "karma.conf.js": true,
	"pytest.ini": true, "tox.ini": true, "conftest.py": true,
	"phpunit.xml": true, "phpunit.xml.dist": true,
	"phpspec.yml": true, "codeception.yml": true,
}

// testFileSuffixes — суффиксы имён файлов автотестов по распространённым
// стекам. Проверяются по концу имени (basename), без учёта регистра.
var testFileSuffixes = []string{
	"_test.go",             // Go
	"_test.py", "_spec.py", // Python (test_*.py проверяется отдельно)
	"_test.rb", "_spec.rb", // Ruby
	"_test.exs", "_spec.exs", // Elixir
	"_test.rs",    // Rust
	"_test.scala", // Scala
	"_test.dart",  // Dart
	"_test.clj",   // Clojure
}

// testCamelSuffixes — конвенции «CamelCase + Test(s)» (Java/Kotlin/C#/PHP):
// проверяются по исходному имени с сохранением регистра, иначе «contest.java»
// или «unit.php» ложно считались бы тестами.
var testCamelSuffixes = []string{
	"Test.java", "Tests.java", "TestCase.java",
	"Test.kt", "Tests.kt",
	"Test.cs", "Tests.cs",
	"Test.php", "Tests.php",
}

// isTestPath сообщает, является ли относительный slash-путь файлом
// автотестов по конвенциям распространённых стеков (Go, JS/TS, Python, Ruby
// и др.). Используется режимом WriteTestsOnly: тестировщик пишет тесты, но
// не код продукта. Проверка чисто именная (без обращения к диску), чтобы
// гард работал и до создания файла.
func isTestPath(rel string) bool {
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "" || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return false
	}
	parts := strings.Split(rel, "/")
	for _, seg := range parts[:len(parts)-1] {
		if testDirNames[strings.ToLower(seg)] {
			return true
		}
	}
	name := parts[len(parts)-1]
	base := strings.ToLower(name)
	if testConfigFiles[base] {
		return true
	}
	// Python: test_*.py.
	if strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py") {
		return true
	}
	// JS/TS: foo.test.ts, foo.spec.tsx и прочие расширения.
	for _, ext := range []string{".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".mts", ".cts"} {
		if strings.HasSuffix(base, ".test"+ext) || strings.HasSuffix(base, ".spec"+ext) {
			return true
		}
	}
	for _, suf := range testFileSuffixes {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	for _, suf := range testCamelSuffixes {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}
	return false
}

// SetWriteTestsOnly включает режим «только тесты»: записывающие инструменты
// разрешены лишь для тестовых файлов (isTestPath) и дополнительных точных
// путей extra (например, файл-отчёт QA). Пустой extra снимает дополнительные
// пути. Ставится QA-инженеру.
func (ops *FileOps) SetWriteTestsOnly(extra []string) {
	ops.WriteTestsOnly = true
	ops.WriteTestsExtra = extra
	ops.writeTestsExtraMatch = forges.CompileScope(extra)
}
