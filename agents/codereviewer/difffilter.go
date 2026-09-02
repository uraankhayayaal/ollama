package codereviewer

import (
	"regexp"
	"strings"
)

// fileHeaderRe извлекает путь из заголовка изменённого файла "+ + +/<path>".
var fileHeaderRe = regexp.MustCompile(`^\+\+\+ (?:b/)?(\S+)`)

// generatedFileRe — признаки сгенерированного/бинарного файла по пути.
var generatedFileRe = regexp.MustCompile(`(?i)((^|/)(dist|build|vendor|node_modules|__pycache__)/|package-lock\.json|\.(lock|min\.js|min\.css|map|png|jpe?g|gif|svg|ico|woff2?|ttf|eot|pdf|zip|gz|wasm|pb|cross)$)`)

// pbGeneratedRe — сгенерированные из протобафов/кодовые генераторы.
var pbGeneratedRe = regexp.MustCompile(`(?i)\.(pb|gen|generated)\.`)

// skipFile решает, нужно ли исключить файл из ревью.
func skipFile(path string) bool {
	return generatedFileRe.MatchString(path) || pbGeneratedRe.MatchString(path)
}

// filterGeneratedDiff удаляет из diff ханки сгенерированных/бинарных файлов.
// Заголовок файла начинается строкой "+++ b/<path>"; все строки до следующего
// такого заголовка принадлежат этому файлу и удаляются, если он сгенерирован.
func filterGeneratedDiff(diff string) string {
	lines := strings.Split(diff, "\n")
	var out []string

	skip := false
	for _, line := range lines {
		if m := fileHeaderRe.FindStringSubmatch(line); m != nil {
			// Новый заголовок файла — решаем по пути, пропускать ли его.
			skip = skipFile(m[1])
			if !skip {
				out = append(out, line)
			}
			continue
		}
		if skip {
			continue
		}
		out = append(out, line)
	}

	return strings.Join(out, "\n")
}
