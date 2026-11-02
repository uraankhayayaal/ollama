package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const Glob = "Glob"

type globResult struct {
	Status  string   `json:"status"`
	Files   []string `json:"files,omitempty"`
	Total   int      `json:"total"`
	Pattern string   `json:"pattern"`
	Path    string   `json:"path"`
	Error   string   `json:"error,omitempty"`
}

type globTool struct {
	ops *FileOps
}

func (t *globTool) Name() string { return Glob }

func (t *globTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        Glob,
		Description: "Найти файлы по glob-паттерну. Параметр pattern — обязательный glob-паттерн (*.go, **/*.ts, src/**/*); path — опциональная директория (по умолчанию OutputDir). Возвращает список совпавших файлов с относительными путями.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Glob-паттерн для поиска (*.go, **/*.ts, src/**/*.js)"},
				"path":    map[string]any{"type": "string", "description": "Директория для поиска (по умолчанию OutputDir)"},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
	}
}

func (t *globTool) Execute(args map[string]any) ([]byte, error) { return t.exec(args) }

func (t *globTool) exec(args map[string]any) ([]byte, error) {
	pattern, ok := args["pattern"].(string)
	if !ok || pattern == "" {
		res := globResult{Status: "error", Error: "pattern требуется"}
		return json.Marshal(res)
	}

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

	fullPattern := filepath.Join(searchDir, pattern)
	matches, err := filepath.Glob(fullPattern)
	if err != nil {
		res := globResult{Status: "error", Error: err.Error()}
		return json.Marshal(res)
	}

	files := make([]string, 0, len(matches))
	for _, m := range matches {
		info, serr := os.Stat(m)
		if serr != nil || info.IsDir() {
			continue
		}
		relPath, rerr := filepath.Rel(searchDir, m)
		if rerr != nil {
			relPath = m
		}
		files = append(files, relPath)
	}

	return json.Marshal(globResult{
		Status:  "ok",
		Files:   files,
		Total:   len(files),
		Pattern: pattern,
		Path:    searchDir,
	})
}
