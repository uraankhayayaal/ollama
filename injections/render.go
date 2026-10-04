// Package injections реализует безопасный рендеринг and evaluation для инъекций.
// Независимый от models — avoids cycles in the build graph.
package injections

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// sensitiveEnvPatterns — внутренние предположения, отдорные шаблонному
// рендерингу. Избегая зависимости от models — avoiding cycles.
var sensitiveEnvPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)_API_KEY$`),
	regexp.MustCompile(`(?i)_TOKEN$`),
	regexp.MustCompile(`(?i)_SECRET$`),
	regexp.MustCompile(`(?i)_PASSWORD$`),
	regexp.MustCompile(`(?i)_CREDENTIAL$`),
	regexp.MustCompile(`^OPENAI_API_KEY$`),
	regexp.MustCompile(`^ANTHROPIC_API_KEY$`),
	regexp.MustCompile(`^OLLAMA_API_KEY$`),
}

// IsSensitiveEnv проверяет, является ли env-переменная чувствительной
// по локальным правилам — без зависимости от models.
func IsSensitiveEnv(name string) bool {
	for _, re := range sensitiveEnvPatterns {
		if re.MatchString(name) {
			return true
		}
	}
	return false
}

// templateVarPattern — шаблон {{vars.*}}, {{env.*}}, {{session.*}}, {{assistant.*}}.
var templateVarPattern = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s*\}\}`)

// RenderFnDefault — встроенный рендеринг шаблонов инъекций.
// Без зависимости от models — avoids cycles in runner -> models -> runner.
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
		}
		return match
	})
	return result
}

// EvalFnDefault — встроенный evaluation для условий when.
// Без зависимости от models — avoids cycles in runner -> models -> runner.
var EvalFnDefault = func(when string, ctx MergeContext) bool {
	when = strings.TrimSpace(when)
	if when == "" {
		return true
	}
	// Building a condition context for env-lookup only.
	lookup := make(map[string]string, len(ctx.Tools)+5)
	lookup["model"] = ctx.Model
	lookup["provider"] = ctx.Provider
	lookup["role"] = ctx.Role
	lookup["project"] = ctx.Project
	lookup["user"] = ctx.User
	if ctx.HasFiles {
		lookup["has_files"] = "true"
	} else {
		lookup["has_files"] = "false"
	}
	lookup["turn"] = fmt.Sprintf("%d", ctx.Turn)
	lookup["tools"] = strings.Join(ctx.Tools, ",")

	// Secure eval — boolean expressions only (==, !=, &&, ||, !, parens).
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
		return evalNot(expr[1:], ctx)
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
	if strings.HasPrefix(expr, "(") {
		return false, fmt.Errorf("несбалансированные скобки в %q", expr)
	}
	return evalComparison(expr, ctx)
}

func evalComparison(expr string, ctx map[string]string) (bool, error) {
	opIdx := -1
	op := ""

	if idx := strings.Index(expr, "!="); idx >= 0 {
		opIdx = idx
		op = "!="
	}
	if idx := strings.Index(expr, "=="); idx >= 0 {
		if opIdx < 0 || idx < opIdx {
			opIdx = idx
			op = "=="
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
	if op == "==" {
		return left == right, nil
	}
	return left != right, nil
}

func resolveEnvOrConst(val string, ctx map[string]string) (string, error) {
	val = strings.TrimSpace(val)
	// String literal
	if (strings.HasPrefix(val, "\"") && strings.HasSuffix(val, "\"")) ||
		(strings.HasPrefix(val, "'") && strings.HasSuffix(val, "'")) {
		return val[1 : len(val)-1], nil
	}
	// Boolean literals
	switch val {
	case "true":
		return "true", nil
	case "false":
		return "false", nil
	default:
		// Lookup in context
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