package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const Structure = "Structure"

type structEntry struct {
	Name   string      `json:"name"`
	Type   string      `json:"type"`
	Indent int         `json:"-"`
	Children []structEntry `json:"children,omitempty"`
}

type structureResult struct {
	Status string          `json:"status"`
	Tree   structEntry     `json:"tree,omitempty"`
	Path   string          `json:"path"`
	MaxDepth int          `json:"max_depth"`
	Error  string          `json:"error,omitempty"`
}

type structureTool struct {
	ops *FileOps
}

func (t *structureTool) Name() string { return Structure }

func (t *structureTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        Structure,
		Description: "Получить рекурсивную структуру директории проекта. Параметр path — опциональная директория (по умолчанию OutputDir); maxDepth — максимум глубины вложенности (по умолчанию 5, 0 — без ограничений); exclude — опциональный список имен директорий для исключения (.git, node_modules, vendor, dist и т.д.).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":     map[string]any{"type": "string", "description": "Директория для анализа (по умолчанию OutputDir)"},
				"maxDepth": map[string]any{"type": "integer", "description": "Максимум глубины вложенности (по умолчанию 5, 0 — без ограничений)"},
				"exclude":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Исключить директории (.git, node_modules и т.д.)"},
			},
			"required":             []string{},
			"additionalProperties": false,
		},
	}
}

func (t *structureTool) Execute(args map[string]any) ([]byte, error) { return t.exec(args) }

func (t *structureTool) exec(args map[string]any) ([]byte, error) {
	searchDir := ""
	if t.ops != nil {
		searchDir = t.ops.OutputDir
	}
	if p, ok := args["path"].(string); ok && p != "" {
		searchDir = p
	}
	if searchDir == "" {
		searchDir, _ = os.Getwd()
	}

	maxDepth := 5
	if d, ok := args["maxDepth"].(float64); ok {
		maxDepth = int(d)
	}

	exclude := map[string]bool{".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true, "__pycache__": true, ".next": true, ".cache": true, "temp": true}
	if ex, ok := args["exclude"].([]any); ok {
		for _, e := range ex {
			if es, ok := e.(string); ok {
				exclude[es] = true
			}
		}
	}

	info, err := os.Stat(searchDir)
	if err != nil {
		res := structureResult{Status: "error", Error: err.Error()}
		return json.Marshal(res)
	}
	if !info.IsDir() {
		res := structureResult{Status: "error", Error: fmt.Sprintf("не директория: %s", searchDir)}
		return json.Marshal(res)
	}

	rootName := filepath.Base(searchDir)
	root := structEntry{Name: rootName, Type: "dir"}
	root.Children = buildTree(searchDir, searchDir, 0, maxDepth, exclude)

	res := structureResult{
		Status:   "ok",
		Tree:     root,
		Path:     searchDir,
		MaxDepth: maxDepth,
	}
	return json.Marshal(res)
}

func buildTree(baseDir, dir string, depth int, maxDepth int, exclude map[string]bool) []structEntry {
	if maxDepth > 0 && depth >= maxDepth {
		return nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var result []structEntry
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && !exclude[name] {
			continue
		}
		if exclude[name] {
			continue
		}

		entry := structEntry{Name: name}

		if e.IsDir() {
			entry.Type = "dir"
			entry.Children = buildTree(baseDir, filepath.Join(dir, name), depth+1, maxDepth, exclude)
		} else {
			entry.Type = "file"
		}
		result = append(result, entry)
	}
	return result
}
