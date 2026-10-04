package models

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// TemplateContext — контекст для рендеринга шаблонов инъекций.
type TemplateContext struct {
	Vars     map[string]any
	Env      map[string]string
	Session  map[string]any
	Assistant map[string]any
}

// sensitiveEnvPatterns — паттерны для чувствительных env-переменных.
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

// IsSensitiveEnv проверяет, является ли env-переменная чувствительной.
func IsSensitiveEnv(name string) bool {
	for _, re := range sensitiveEnvPatterns {
		if re.MatchString(name) {
			return true
		}
	}
	return false
}

// templateVarPattern — паттерн для переменных шаблона {{...}}.
var templateVarPattern = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s*\}\}`)

// RenderTemplate рендерит шаблон инъекции с подстановкой переменных.
// Поддерживает: {{vars.*}}, {{env.*}}, {{session.*}}, {{assistant.*}}.
// Чувствительные env-переменные заменяются на [REDACTED].
func RenderTemplate(tmpl string, ctx TemplateContext) string {
	return templateVarPattern.ReplaceAllStringFunc(tmpl, func(match string) string {
		sub := templateVarPattern.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		path := sub[1]
		parts := strings.SplitN(path, ".", 2)
		if len(parts) < 2 {
			return match
		}
		namespace := parts[0]
		key := parts[1]

		switch namespace {
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
			if v, ok := ctx.Session[key]; ok {
				return fmt.Sprintf("%v", v)
			}
		case "assistant":
			if v, ok := ctx.Assistant[key]; ok {
				return fmt.Sprintf("%v", v)
			}
		}
		return match
	})
}

// MaxTemplatePasses — максимальное число проходов рендеринга (защита от циклов).
const MaxTemplatePasses = 2

// RenderTemplateSafe рендерит шаблон с ограничением числа проходов.
func RenderTemplateSafe(tmpl string, ctx TemplateContext) string {
	result := tmpl
	for i := 0; i < MaxTemplatePasses; i++ {
		next := RenderTemplate(result, ctx)
		if next == result {
			break
		}
		result = next
	}
	return result
}
