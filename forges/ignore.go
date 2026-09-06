package forges

import "strings"

// ignoredDirNames — каталоги, которые исключаются из диффа ревью, дерева
// файлов List и отчётов: зависимости (node_modules, vendor), результаты
// сборки (dist, build, target), кеши и временные файлы (__pycache__, .cache,
// tmp) и служебные каталоги (.git, .idea). Без этого проект с node_modules
// сжигал бы весь контекст модели на содержимом библиотек.
var ignoredDirNames = map[string]bool{
	// Зависимости и библиотеки.
	"node_modules":     true,
	"bower_components": true,
	"vendor":           true,
	"Pods":             true,
	"Carthage":         true,
	// Результаты сборки.
	"dist":        true,
	"build":       true,
	"out":         true,
	"target":      true,
	".next":       true,
	".nuxt":       true,
	".output":     true,
	".turbo":      true,
	"DerivedData": true,
	// Метрики/покрытие.
	"coverage":    true,
	".nyc_output": true,
	"htmlcov":     true,
	// Кеши и временные файлы.
	"__pycache__":   true,
	".pytest_cache": true,
	".mypy_cache":   true,
	".ruff_cache":   true,
	".tox":          true,
	".venv":         true,
	"venv":          true,
	".cache":        true,
	"tmp":           true,
	"temp":          true,
	// Служебные каталоги.
	".git":    true,
	".hg":     true,
	".svn":    true,
	".idea":   true,
	".vscode": true,
}

// IsIgnoredDir сообщает, нужно ли исключить директорию с данным именем
// из контекста ревью/списков (имя проверяется без учёта регистра).
func IsIgnoredDir(name string) bool {
	return ignoredDirNames[strings.ToLower(name)]
}
