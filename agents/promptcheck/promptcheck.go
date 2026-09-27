// Package promptcheck содержит проверку согласованности промпта агента и его
// набора инструментов (Ф-2 PLAN-2026-09-19-todo-owerview-for-prom.md).
//
// Проблема, которую ловит Check: промпт советует вызвать инструмент, которого
// в наборе агента нет. Модель получает «not in tool set» и либо выдумывает
// аргументы, либо пропускает проверку целиком — и декларация «проверь в
// рантайме» превращается в пустые слова. Именно так молча пропала инструкция
// про Makefile, когда цель была в промпте, а инструмента в наборе не было.
//
// Пакет отдельный от agents, потому что агенты-роли импортируют agents (цикл
// запрещён), а проверять нужно именно их наборы. Обратная сторона: набор
// инструментов в проверку нельзя вытащить извне по имени переменной — поэтому
// роли передают его сами, а промпт сдаётся готовым текстом.
package promptcheck

import "strings"

// KnownTools — инструменты этого репозитория, которые промпты вправе называть
// прямо по имени. Список закрытый: новое упоминание добавляется сюда руками,
// иначе проверка его пропустит (это осознанный компромисс — вылавливать имена
// регуляркой по всему тексту нельзя, слова в комментариях дают ложные
// срабатывания).
var KnownTools = []string{
	"WriteFiles", "ReadFiles", "ReadMap", "DeleteFiles", "Run", "List", "AppendFile",
	"SearchReplace", "PatchFunction", "PatchGoFunction", "DetectStack", "ReadAppLogs",
	"LspCheck", "LspDefinition", "LspReferences", "LspHover", "CodeSearch",
	"RagIndexStatus", "ResolveGitConflicts",
}

// KnownTypos — близкие к реальным имена, которые регулярно всплывают в
// промптах при правках руками. Их наличие — всегда опечатка.
var KnownTypos = []string{
	"ReadAppLog ", "ReadAppLogsLog", "AppLogsRead", "PatchGoFunc", "CodeSerach", "ReadFile ",
}

// MentionedTools возвращает инструменты из KnownTools, названные в тексте как
// отдельные слова. Границы слова обязательны: «Run» внутри RunTokenEconomy
// инструментом не является.
func MentionedTools(prompt string) []string {
	var out []string
	for _, name := range KnownTools {
		if MentionsWord(prompt, name) {
			out = append(out, name)
		}
	}
	return out
}

// CheckToolSet сверяет промпт с набором инструментов и возвращает список
// расхождений (пустой — всё в порядке). Расхождение одно: промпт называет
// инструмент, которого в наборе нет.
func CheckToolSet(role, prompt string, toolNames []string) []string {
	var problems []string
	for _, want := range MentionedTools(prompt) {
		if !Contains(toolNames, want) {
			problems = append(problems, role+": промпт называет "+want+", но инструмента нет в наборе")
		}
	}
	return problems
}

// CheckTypos ищет в промпте имена, похожие на инструменты, но не являющиеся
// ими: модель получит «not in tool set» на первом же раунде.
func CheckTypos(role, prompt string) []string {
	var problems []string
	for _, bad := range KnownTypos {
		if MentionsWord(prompt, bad) {
			problems = append(problems, role+": в промпте опечатка в имени инструмента: "+bad)
		}
	}
	return problems
}

// MentionsWord ищет word как отдельное слово (границы по символам-идентификаторам).
func MentionsWord(text, word string) bool {
	word = strings.TrimSpace(word)
	if word == "" {
		return false
	}
	rest := text
	for {
		i := strings.Index(rest, word)
		if i < 0 {
			return false
		}
		beforeOK := i == 0 || !isIdentByte(rest[i-1])
		after := i + len(word)
		afterOK := after >= len(rest) || !isIdentByte(rest[after])
		if beforeOK && afterOK {
			return true
		}
		rest = rest[i+1:]
	}
}

// Contains ищет значение в срезе строк.
func Contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func isIdentByte(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}
