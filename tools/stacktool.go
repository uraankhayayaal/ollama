package tools

// Инструмент DetectStack — определение типа проекта и состава ролей по
// маркерам в корне директории (Ф-2 PLAN-architect-intelligence.md).
//
// Использует stackdetect для базового Kind (go/php/node/python/unknown) и
// эвристику состава (директории/маркеры frontend/backend/infra/tests), чтобы
// архитектор назначал эпики только реально нужным лидам. Никакой сети —
// детерминированный (hermetic).

import (
	"ai/stackdetect"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const DetectStack = "DetectStack"

// StackRoles — состав ролей проекта, выявленный эвристикой по файлам/директориям.
type StackRoles struct {
	Frontend bool `json:"frontend"`
	Backend  bool `json:"backend"`
	DevOps   bool `json:"devops"`
	QA       bool `json:"qa"`
}

// StackInfo — результат детекта стека и состава проекта.
type StackInfo struct {
	Status  string     `json:"status"`
	Kind    string     `json:"kind"`
	Stack   string     `json:"stack,omitempty"`
	Roles   StackRoles `json:"roles"`
	Summary string     `json:"summary,omitempty"`
	Markers []string   `json:"markers,omitempty"`
}

type stackTool struct {
	ops *FileOps
}

func (t *stackTool) Name() string { return DetectStack }

func (t *stackTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name: DetectStack,
		Description: "Определить фактический тип проекта (стек) и состав направлений по маркерам корня проекта: go.mod/composer.json/package.json, директории server/frontend/backend/web/infra/deploy/tests, Docker Compose и др. Возвращает kind, roles (frontend/backend/devops/qa) и markers. Используй перед публикацией эпиков, чтобы назначать эпики только реально нужным лидам.",
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"required":             []string{},
			"additionalProperties": false,
		},
	}
}

func (t *stackTool) Execute(args map[string]any) ([]byte, error) { return t.exec() }

func (t *stackTool) exec() ([]byte, error) {
	dir := ""
	if t.ops != nil {
		dir = t.ops.OutputDir
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}
	info := detectStackAt(dir)
	b, _ := json.Marshal(info)
	return b, nil
}

func detectStackAt(dir string) StackInfo {
	kind := stackdetect.DetectKind(dir)
	kstr := string(kind)
	if kstr == "" {
		kstr = "unknown"
	}

	roles := StackRoles{}
	markers := []string{}

	switch kind {
	case stackdetect.KindGo:
		roles.Backend = true
		markers = append(markers, "go.mod")
	case stackdetect.KindPhp:
		roles.Backend = true
		markers = append(markers, "composer.json")
	case stackdetect.KindNode:
		// Node может быть frontend-only или fullstack; эвристика ниже
		roles.Backend = roles.Backend || hasDir(dir, "server") || hasDir(dir, "backend") || hasDir(dir, "api")
		markers = append(markers, "package.json")
	case stackdetect.KindPython:
		roles.Backend = true
		if hasFile(dir, "requirements.txt") {
			markers = append(markers, "requirements.txt")
		}
		if hasFile(dir, "pyproject.toml") {
			markers = append(markers, "pyproject.toml")
		}
		if hasFile(dir, "setup.py") {
			markers = append(markers, "setup.py")
		}
		if hasFile(dir, "main.py") || hasFile(dir, "app.py") {
			markers = append(markers, "main.py/app.py")
		}
	default:
		// unknown — эвристика по директориям
	}

	// Frontend
	if hasDir(dir, "frontend") || hasDir(dir, "web") || hasDir(dir, "app") || hasDir(dir, "client") || hasFile(dir, "index.html") {
		roles.Frontend = true
	}
	if hasFile(dir, "package.json") && !containsMarker(markers, "package.json") {
		markers = append(markers, "package.json")
	}

	// DevOps / infra
	if hasDir(dir, "infra") || hasDir(dir, "deploy") || hasDir(dir, "ops") || hasFile(dir, "docker-compose.yml") || hasFile(dir, "docker-compose.yaml") || hasFile(dir, "Dockerfile") || hasFile(dir, "Makefile") || hasDir(dir, ".github/workflows") || hasK8s(dir) {
		roles.DevOps = true
	}
	if hasFile(dir, "docker-compose.yml") || hasFile(dir, "docker-compose.yaml") {
		markers = append(markers, "docker-compose")
	}
	if hasDir(dir, ".github/workflows") {
		markers = append(markers, "ci(.github/workflows)")
	}

	// QA/tests
	if hasDir(dir, "tests") || hasDir(dir, "test") || hasDir(dir, "__tests__") || hasDir(dir, "spec") || hasDir(dir, "e2e") {
		roles.QA = true
		markers = append(markers, "tests/")
	}

	// Если Node и есть frontend/web — считаем frontend активным
	if kind == stackdetect.KindNode && (hasDir(dir, "frontend") || hasDir(dir, "web") || hasDir(dir, "client")) {
		roles.Frontend = true
	}

	// Backend эвристика для неизвестного
	if !roles.Backend {
		if hasDir(dir, "server") || hasDir(dir, "backend") || hasDir(dir, "api") || hasDir(dir, "internal") || hasDir(dir, "cmd") {
			roles.Backend = true
		}
	}

	summary := buildSummary(kind, roles, markers)

	return StackInfo{
		Status:  "ok",
		Kind:    kstr,
		Roles:   roles,
		Summary: summary,
		Markers: markers,
	}
}

func hasDir(dir, name string) bool {
	info, err := os.Stat(filepath.Join(dir, name))
	return err == nil && info.IsDir()
}

func hasFile(dir, name string) bool {
	info, err := os.Stat(filepath.Join(dir, name))
	return err == nil && !info.IsDir()
}

func hasGlob(dir, pattern string) bool {
	m, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return false
	}
	return len(m) > 0
}

// hasK8s проверяет наличие k8s-манифестов в корне (файлы с "k8s" в имени
// либо *.k8s.* / *.yaml в casing). Deterministic, без сети.
func hasK8s(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if strings.Contains(name, "k8s") || strings.Contains(name, "kubernetes") || strings.Contains(name, "deploy") {
			return true
		}
	}
	return false
}

func containsMarker(markers []string, m string) bool {
	for _, x := range markers {
		if x == m {
			return true
		}
	}
	return false
}

func buildSummary(kind stackdetect.Kind, r StackRoles, m []string) string {
	parts := []string{string(kind)}
	if r.Frontend {
		parts = append(parts, "frontend")
	}
	if r.Backend {
		parts = append(parts, "backend")
	}
	if r.DevOps {
		parts = append(parts, "devops")
	}
	if r.QA {
		parts = append(parts, "qa")
	}
	s := strings.Join(parts, "+")
	if len(m) > 0 {
		s += " (" + strings.Join(m, ", ") + ")"
	}
	return s
}