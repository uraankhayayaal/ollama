// Package injections реализует безопасный рендеринг шаблонов и вычисление
// условий when для промпт-инъекций. Пакет независим от models — чтобы
// не тянуть цикл импортов runner → models → runner.
package injections

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// envAllowlist — единственные env-префиксы, из которых инъекция может читать
// значение. Всё остальное окружение (домашние каталоги, PATH, чужие токены)
// шаблону недоступно: инъекция — это содержимое промпта, и подстановка
// произвольной переменной окружения утекала бы секреты в модель и в лог
// запроса. Список расширяется переменной AI_INJECTION_ENV_ALLOW (через запятую).
var envAllowlist = []string{"AI_", "CODEGEN_", "OLLAMA_", "KANBAN_"}

// envAllowlistExtra — дополнительные префиксы из AI_INJECTION_ENV_ALLOW.
func envAllowlistExtra() []string {
	raw := strings.TrimSpace(os.Getenv("AI_INJECTION_ENV_ALLOW"))
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// sensitiveEnvPatterns — переменные, которые запрещены ВСЕГДА, даже если
// попали в allowlist по имени ( *_API_KEY, *_TOKEN и т.п.).
var sensitiveEnvPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)_API_KEY$`),
	regexp.MustCompile(`(?i)_TOKEN$`),
	regexp.MustCompile(`(?i)_SECRET$`),
	regexp.MustCompile(`(?i)_PASSWORD$`),
	regexp.MustCompile(`(?i)_CREDENTIAL$`),
	regexp.MustCompile(`(?i)^OPENAI_API_KEY$`),
	regexp.MustCompile(`(?i)^ANTHROPIC_API_KEY$`),
	regexp.MustCompile(`(?i)^OLLAMA_API_KEY$`),
}

// IsSensitiveEnv проверяет, является ли env-переменная чувствительной.
func IsSensitiveEnv(name string) bool {
	for _, re := range sensitiveEnvPatterns {
		if re.MatchString(name) {
			return true
		}
	}
	return false
}

// EnvAllowed сообщает, разрешено ли инъекции читать эту переменную окружения.
func EnvAllowed(name string) bool {
	if name == "" || IsSensitiveEnv(name) {
		return false
	}
	for _, p := range envAllowlist {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	for _, p := range envAllowlistExtra() {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// templateVarPattern — шаблоны {{vars.*}}, {{env.*}}, {{session.*}}, {{assistant.*}}.
var templateVarPattern = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s*\}\}`)

// RenderFnDefault — встроенный рендеринг шаблонов инъекций.
// Неизвестное или запрещённое значение оставляется в виде исходного
// {{...}}: молча подставлять пустую строку опаснее (в промпте появляется
// обрывок фразы), а молча отдавать секрет — тем более.
var RenderFnDefault = func(content string, ctx MergeContext) string {
	result := content
	result = templateVarPattern.ReplaceAllStringFunc(result, func(match string) string {
		sub := templateVarPattern.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		parts := strings.SplitN(sub[1], ".", 2)
		if len(parts) < 2 {
			return match
		}
		ns, key := parts[0], parts[1]
		switch ns {
		case "vars":
			if v, ok := ctx.Vars[key]; ok {
				return fmt.Sprintf("%v", v)
			}
		case "env":
			if IsSensitiveEnv(key) {
				return "[REDACTED]"
			}
			if !EnvAllowed(key) {
				return match
			}
			if v, ok := ctx.Env[key]; ok {
				return v
			}
			if v, ok := os.LookupEnv(key); ok {
				return v
			}
		case "session":
			if v := ctx.Session[key]; v != nil {
				return fmt.Sprintf("%v", v)
			}
		case "assistant":
			if v := ctx.Assistant[key]; v != nil {
				return fmt.Sprintf("%v", v)
			}
		}
		return match
	})
	return result
}

// EvalFnDefault — встроенное вычисление условий when. Поддерживаются ==, !=,
// <, <=, >, >= (последние — только для чисел, т.е. практично для turn),
// &&, ||, ! и скобки. Неразобранное условие считается ложным (инъекция
// пропускается), но причина видна в MergeResult.Skipped.
var EvalFnDefault = func(when string, ctx MergeContext) bool {
	when = strings.TrimSpace(when)
	if when == "" {
		return true
	}
	lookup := map[string]string{
		"model":     ctx.Model,
		"provider":  ctx.Provider,
		"role":      ctx.Role,
		"project":   ctx.Project,
		"task_id":   ctx.TaskID,
		"user":      ctx.User,
		"has_files": strconv.FormatBool(ctx.HasFiles),
		"turn":      strconv.Itoa(ctx.Turn),
		"tools":     strings.Join(ctx.Tools, ","),
	}
	// Безопасный eval: только булевы выражения (==, !=, &&, ||, !, скобки).
	resolved, err := evalExpr(when, lookup)
	if err != nil {
		return false
	}
	return resolved
}

func evalExpr(expr string, ctx map[string]string) (bool, error) {
	if parts := splitTopLevel(expr, "||"); len(parts) > 0 {
		for _, part := range parts {
			if val, err := evalAnd(strings.TrimSpace(part), ctx); err == nil && val {
				return true, nil
			} else if err != nil {
				return false, err
			}
		}
		return false, nil
	}
	return evalAnd(expr, ctx)
}

func evalAnd(expr string, ctx map[string]string) (bool, error) {
	if parts := splitTopLevel(expr, "&&"); len(parts) > 0 {
		for _, part := range parts {
			if val, err := evalNot(strings.TrimSpace(part), ctx); err != nil {
				return false, err
			} else if !val {
				return false, nil
			}
		}
		return true, nil
	}
	return evalNot(expr, ctx)
}

func evalNot(expr string, ctx map[string]string) (bool, error) {
	expr = strings.TrimSpace(expr)
	if strings.HasPrefix(expr, "!") {
		val, err := evalNot(expr[1:], ctx)
		if err != nil {
			return false, err
		}
		return !val, nil
	}
	return evalAtom(expr, ctx)
}

func evalAtom(expr string, ctx map[string]string) (bool, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return false, fmt.Errorf("пустое выражение")
	}
	if strings.HasPrefix(expr, "(") && strings.HasSuffix(expr, ")") {
		return evalExpr(expr[1:len(expr)-1], ctx)
	}
	if strings.HasPrefix(expr, "(") || strings.HasSuffix(expr, ")") {
		return false, fmt.Errorf("несбалансированные скобки в %q", expr)
	}
	return evalComparison(expr, ctx)
}

// comparisonOps — операторы сравнения. Порядок важен: двухсимвольные проверяются
// раньше односимвольных, иначе «<=» распалось бы на «<» + «=».
var comparisonOps = []string{"!=", "==", "<=", ">=", "<", ">"}

func evalComparison(expr string, ctx map[string]string) (bool, error) {
	opIdx := -1
	op := ""
	for _, cand := range comparisonOps {
		if idx := strings.Index(expr, cand); idx >= 0 && (opIdx < 0 || idx < opIdx) {
			opIdx = idx
			op = cand
		}
	}

	if opIdx < 0 {
		return false, fmt.Errorf("не удалось разобрать условие %q", expr)
	}

	left, err := resolveEnvOrConst(strings.TrimSpace(expr[:opIdx]), ctx)
	if err != nil {
		return false, err
	}
	right, err := resolveEnvOrConst(strings.TrimSpace(expr[opIdx+len(op):]), ctx)
	if err != nil {
		return false, err
	}
	switch op {
	case "==":
		return left == right, nil
	case "!=":
		return left != right, nil
	}

	// Упорядоченные сравнения — только для чисел (прежде всего turn). Для
	// строк «model > "x"» смысла нет, поэтому такая попытка считается ошибкой
	// разбора, а не ложью: молчаливый false прятал бы опечатку.
	lf, err1 := strconv.ParseFloat(left, 64)
	rf, err2 := strconv.ParseFloat(right, 64)
	if err1 != nil || err2 != nil {
		return false, fmt.Errorf("оператор %q в условии %q работает только с числами", op, expr)
	}
	switch op {
	case "<":
		return lf < rf, nil
	case "<=":
		return lf <= rf, nil
	case ">":
		return lf > rf, nil
	case ">=":
		return lf >= rf, nil
	}
	return false, fmt.Errorf("неизвестный оператор %q", op)
}

func resolveEnvOrConst(val string, ctx map[string]string) (string, error) {
	val = strings.TrimSpace(val)
	// Строковый литерал.
	if (strings.HasPrefix(val, "\"") && strings.HasSuffix(val, "\"") && len(val) >= 2) ||
		(strings.HasPrefix(val, "'") && strings.HasSuffix(val, "'") && len(val) >= 2) {
		return val[1 : len(val)-1], nil
	}
	switch val {
	case "true":
		return "true", nil
	case "false":
		return "false", nil
	default:
		if v, ok := ctx[val]; ok {
			return v, nil
		}
		return "", fmt.Errorf("неизвестная переменная '%s'", val)
	}
}

func splitTopLevel(input, sep string) []string {
	var parts []string
	depth := 0
	start := 0
	for i := 0; i < len(input); i++ {
		switch input[i] {
		case '(':
			depth++
		case ')':
			depth--
		case '"', '\'':
			quote := input[i]
			i++
			for i < len(input) && input[i] != quote {
				if input[i] == '\\' {
					i++
				}
				i++
			}
		}
		if depth == 0 && i+len(sep) <= len(input) && input[i:i+len(sep)] == sep {
			parts = append(parts, input[start:i])
			i += len(sep) - 1
			start = i + 1
		}
	}
	parts = append(parts, input[start:])
	return parts
}
