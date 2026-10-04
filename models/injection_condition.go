package models

import (
	"fmt"
	"strings"
)

// ConditionContext — контекст для вычисления условий when.
type ConditionContext struct {
	Model    string
	Provider string
	Role     string
	Project  string
	Turn     int
	User     string
	Tools    []string
	HasFiles bool
}

// ConditionEvaluator — безопасный evaluator условий when.
// Поддерживает простые boolean-выражения: ==, !=, &&, ||, !, скобки.
// Переменные: model, provider, role, project, turn, user, has_files, tools.
type ConditionEvaluator struct {
	ctx ConditionContext
}

// NewConditionEvaluator создаёт evaluator с заданным контекстом.
func NewConditionEvaluator(ctx ConditionContext) *ConditionEvaluator {
	return &ConditionEvaluator{ctx: ctx}
}

// Evaluate вычисляет условие when. Пустое условие = true.
func (e *ConditionEvaluator) Evaluate(expr string) (bool, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true, nil
	}
	result, err := e.parseOr(expr)
	if err != nil {
		return false, fmt.Errorf("injection condition: %w", err)
	}
	return result, nil
}

func (e *ConditionEvaluator) parseOr(input string) (bool, error) {
	parts := e.splitTopLevel(input, "||")
	if len(parts) == 1 {
		return e.parseAnd(input)
	}
	for _, part := range parts {
		val, err := e.parseAnd(strings.TrimSpace(part))
		if err != nil {
			return false, err
		}
		if val {
			return true, nil
		}
	}
	return false, nil
}

func (e *ConditionEvaluator) parseAnd(input string) (bool, error) {
	parts := e.splitTopLevel(input, "&&")
	if len(parts) == 1 {
		return e.parseNot(input)
	}
	for _, part := range parts {
		val, err := e.parseNot(strings.TrimSpace(part))
		if err != nil {
			return false, err
		}
		if !val {
			return false, nil
		}
	}
	return true, nil
}

func (e *ConditionEvaluator) parseNot(input string) (bool, error) {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "!") {
		val, err := e.parseNot(input[1:])
		if err != nil {
			return false, err
		}
		return !val, nil
	}
	return e.parseAtom(input)
}

func (e *ConditionEvaluator) parseAtom(input string) (bool, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return false, fmt.Errorf("пустое выражение")
	}
	if strings.HasPrefix(input, "(") && strings.HasSuffix(input, ")") {
		return e.parseOr(input[1 : len(input)-1])
	}
	if strings.HasPrefix(input, "(") {
		return false, fmt.Errorf("несбалансированные скобки в %q", input)
	}
	return e.parseComparison(input)
}

func (e *ConditionEvaluator) parseComparison(input string) (bool, error) {
	input = strings.TrimSpace(input)
	operators := []string{"==", "!="}
	for _, op := range operators {
		idx := strings.Index(input, op)
		if idx < 0 {
			continue
		}
		left := strings.TrimSpace(input[:idx])
		right := strings.TrimSpace(input[idx+len(op):])
		leftVal, err := e.resolveVar(left)
		if err != nil {
			return false, err
		}
		rightVal, err := e.resolveVar(right)
		if err != nil {
			return false, err
		}
		if op == "==" {
			return leftVal == rightVal, nil
		}
		return leftVal != rightVal, nil
	}
	return false, fmt.Errorf("не удалось разобрать условие %q", input)
}

func (e *ConditionEvaluator) resolveVar(name string) (string, error) {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, "\"") && strings.HasSuffix(name, "\"") && len(name) >= 2 {
		return name[1 : len(name)-1], nil
	}
	if strings.HasPrefix(name, "'") && strings.HasSuffix(name, "'") && len(name) >= 2 {
		return name[1 : len(name)-1], nil
	}
	switch name {
	case "model":
		return e.ctx.Model, nil
	case "provider":
		return e.ctx.Provider, nil
	case "role":
		return e.ctx.Role, nil
	case "project":
		return e.ctx.Project, nil
	case "user":
		return e.ctx.User, nil
	case "has_files":
		if e.ctx.HasFiles {
			return "true", nil
		}
		return "false", nil
	case "turn":
		return fmt.Sprintf("%d", e.ctx.Turn), nil
	case "tools":
		return strings.Join(e.ctx.Tools, ","), nil
	}
	return "", fmt.Errorf("неизвестная переменная %q", name)
}

func (e *ConditionEvaluator) splitTopLevel(input, sep string) []string {
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
