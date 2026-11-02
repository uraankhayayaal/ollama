package tools

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const Git = "Git"
const Diff = "Diff"
const GitCommit = "GitCommit"

type gitResult struct {
	Status    string `json:"status"`
	Output    string `json:"output,omitempty"`
	ExitCode  int    `json:"exit_code"`
	Error     string `json:"error,omitempty"`
}

type gitTool struct {
	ops *FileOps
}

func (t *gitTool) Name() string { return Git }

func (t *gitTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        Git,
		Description: "Выполнить git-команду в директории проекта. Параметр command — обязательная git-команда (git status, git log, git diff, git branch и т.д.); path — опциональная директория (по умолчанию OutputDir).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "Git-команда (например 'status', 'log -3', 'diff --name-only')"},
				"path":    map[string]any{"type": "string", "description": "Директория репозитория (по умолчанию OutputDir)"},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
	}
}

type diffResult struct {
	Status    string `json:"status"`
	Diff      string `json:"diff,omitempty"`
	Truncated bool   `json:"truncated"`
	Error     string `json:"error,omitempty"`
}

type diffTool struct {
	ops *FileOps
}

func (t *diffTool) Name() string { return Diff }

func (t *diffTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        Diff,
		Description: "Показать diff между двумя путями или версиями. Параметр file — обязательный путь к файлу; base — опциональный путь к базовой версии (если не указан, показывает diff по сравнению с git HEAD); path — опциональная директория (по умолчанию OutputDir).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file": map[string]any{"type": "string", "description": "Путь к файлу для сравнения"},
				"base": map[string]any{"type": "string", "description": "Путь к базовой версии (если не указан — git HEAD)"},
				"path": map[string]any{"type": "string", "description": "Директория (по умолчанию OutputDir)"},
			},
			"required":             []string{"file"},
			"additionalProperties": false,
		},
	}
}

type commitTool struct {
	ops *FileOps
}

func (t *commitTool) Name() string { return GitCommit }

func (t *commitTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        GitCommit,
		Description: "Создать git-коммит с изменёнными файлами. Параметр message — обязательное сообщение коммита; files — опциональный список файлов для добавления (если не указан — добавляются все изменения); path — опциональная директория (по умолчанию OutputDir).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string", "description": "Сообщение коммита"},
				"files":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Список файлов для add (если пусто — все изменения)"},
				"path":    map[string]any{"type": "string", "description": "Директория репозитория (по умолчанию OutputDir)"},
			},
			"required":             []string{"message"},
			"additionalProperties": false,
		},
	}
}

func (t *gitTool) Execute(args map[string]any) ([]byte, error) { return t.exec(args) }

func (t *gitTool) exec(args map[string]any) ([]byte, error) {
	command, ok := args["command"].(string)
	if !ok || command == "" {
		res := gitResult{Status: "error", Error: "command требуется"}
		return json.Marshal(res)
	}

	dir := ""
	if t.ops != nil {
		dir = t.ops.OutputDir
	}
	if p, ok := args["path"].(string); ok && p != "" {
		dir = p
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}

	cmd := exec.Command("git", strings.Fields(command)...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	result := gitResult{
		Output: strings.TrimSpace(string(out)),
	}
	if err != nil {
		result.Status = "error"
		result.ExitCode = cmd.ProcessState.ExitCode()
		result.Error = err.Error()
	} else {
		result.Status = "ok"
	}
	return json.Marshal(result)
}

func (t *diffTool) Execute(args map[string]any) ([]byte, error) { return t.exec(args) }

func (t *diffTool) exec(args map[string]any) ([]byte, error) {
	file, ok := args["file"].(string)
	if !ok || file == "" {
		res := diffResult{Status: "error", Error: "file требуется"}
		return json.Marshal(res)
	}

	dir := ""
	if t.ops != nil {
		dir = t.ops.OutputDir
	}
	if p, ok := args["path"].(string); ok && p != "" {
		dir = p
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}

	fullFile := filepath.Join(dir, file)
	base := ""
	if b, ok := args["base"].(string); ok && b != "" {
		base = filepath.Join(dir, b)
	}

	var out string
	if base != "" {
		cmd := exec.Command("diff", base, fullFile)
		data, err := cmd.CombinedOutput()
		if err != nil {
			if strings.Contains(err.Error(), "No such file") {
				out = ""
			} else {
				res := diffResult{Status: "error", Error: err.Error()}
				return json.Marshal(res)
			}
		} else {
			out = string(data)
		}
	} else {
		cmd := exec.Command("git", "diff", file)
		cmd.Dir = dir
		data, err := cmd.CombinedOutput()
		if err != nil {
			res := diffResult{Status: "error", Error: err.Error()}
			return json.Marshal(res)
		}
		out = string(data)
	}

	truncated := false
	if len(out) > 10000 {
		out = out[:10000] + "\n... (вывод обрезан до 10000 символов)"
		truncated = true
	}

	return json.Marshal(diffResult{
		Status:    "ok",
		Diff:      out,
		Truncated: truncated,
	})
}

func (t *commitTool) Execute(args map[string]any) ([]byte, error) { return t.exec(args) }

func (t *commitTool) exec(args map[string]any) ([]byte, error) {
	message, ok := args["message"].(string)
	if !ok || message == "" {
		res := gitResult{Status: "error", Error: "message требуется"}
		return json.Marshal(res)
	}

	dir := ""
	if t.ops != nil {
		dir = t.ops.OutputDir
	}
	if p, ok := args["path"].(string); ok && p != "" {
		dir = p
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}

	if files, ok := args["files"].([]any); ok && len(files) > 0 {
		for _, f := range files {
			if fs, ok := f.(string); ok && fs != "" {
				cmd := exec.Command("git", "add", fs)
				cmd.Dir = dir
				if _, err := cmd.CombinedOutput(); err != nil {
					res := gitResult{Status: "error", Error: "не удалось добавить файл: " + fs}
					return json.Marshal(res)
				}
			}
		}
	} else {
		cmd := exec.Command("git", "add", ".")
		cmd.Dir = dir
		if _, err := cmd.CombinedOutput(); err != nil {
			res := gitResult{Status: "error", Error: "git add: " + err.Error()}
			return json.Marshal(res)
		}
	}

	cmd := exec.Command("git", "commit", "-m", message)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	result := gitResult{
		Output: strings.TrimSpace(string(out)),
	}
	if err != nil {
		result.Status = "error"
		result.Error = err.Error()
		if strings.TrimSpace(string(out)) != "" {
			result.Output = strings.TrimSpace(string(out))
		}
	} else {
		result.Status = "ok"
	}
	return json.Marshal(result)
}
