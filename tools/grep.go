package tools

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const Grep = "Grep"

type grepResult struct {
	Status   string    `json:"status"`
	Matches  []match   `json:"matches,omitempty"`
	Total    int       `json:"total"`
	Files    int       `json:"files"`
	Pattern  string    `json:"pattern"`
	FilesSearched int `json:"files_searched"`
	Error    string    `json:"error,omitempty"`
}

type match struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type grepTool struct {
	ops *FileOps
}

func (t *grepTool) Name() string { return Grep }

func (t *grepTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        Grep,
		Description: "Поиск по паттерну (строка или регулярное выражение) во всех файлах проекта. Возвращает совпадения с номерами строк и содержимым строки. Параметр pattern — обязательный; path — опциональная директория (по умолчанию OutputDir); type — тип поиска (literal для точного совпадения, regex для регулярки, по умолчанию literal); include — опциональный glob-паттерн файлов (*.go, *.ts); maxResults — максимум совпадений (по умолчанию 50).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern":    map[string]any{"type": "string", "description": "Искомый паттерн (строка или регулярное выражение)"},
				"path":       map[string]any{"type": "string", "description": "Директория для поиска (по умолчанию OutputDir)"},
				"type":       map[string]any{"type": "string", "enum": []string{"literal", "regex"}, "description": "Тип поиска: literal (точное совпадение) или regex (регулярное выражение)"},
				"include":    map[string]any{"type": "string", "description": "Glob-паттерн файлов для фильтрации (*.go, *.ts и т.д.)"},
				"maxResults": map[string]any{"type": "integer", "description": "Максимум совпадений (по умолчанию 50)"},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
	}
}

func (t *grepTool) Execute(args map[string]any) ([]byte, error) { return t.exec(args) }

func (t *grepTool) exec(args map[string]any) ([]byte, error) {
	pattern, ok := args["pattern"].(string)
	if !ok || pattern == "" {
		res := grepResult{Status: "error", Error: "pattern требуется"}
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

	searchType := "literal"
	if st, ok := args["type"].(string); ok && st != "" {
		searchType = st
	}

	include := ""
	if inc, ok := args["include"].(string); ok {
		include = inc
	}

	maxResults := 50
	if mr, ok := args["maxResults"].(float64); ok && mr > 0 {
		maxResults = int(mr)
	} else if mr, ok := args["maxResults"].(int); ok && mr > 0 {
		maxResults = mr
	}

	var re *regexp.Regexp
	var err error
	if searchType == "regex" {
		re, err = regexp.Compile(pattern)
		if err != nil {
			res := grepResult{Status: "error", Error: fmt.Sprintf("невалидная регулярка: %v", err)}
			return json.Marshal(res)
		}
	}

	var matches []match
	filesSearched := 0
	folderAdded := 0

	err = filepath.Walk(searchDir, func(fp string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if folderAdded >= maxResults {
			return filepath.SkipDir
		}

		name := info.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "__") {
			return nil
		}
		if include != "" {
			matched, merr := filepath.Match(include, name)
			if merr != nil || !matched {
				return nil
			}
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		filesSearched++
		f, ferr := os.Open(fp)
		if ferr != nil {
			return nil
		}
		defer f.Close()

		relPath, rerr := filepath.Rel(searchDir, fp)
		if rerr != nil {
			relPath = fp
		}

		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			var found bool
			if searchType == "regex" {
				found = re.MatchString(line)
			} else {
				found = strings.Contains(line, pattern)
			}
			if found {
				text := line
				if len(text) > 200 {
					text = text[:200]
				}
				matches = append(matches, match{
					File: relPath,
					Line: lineNum,
					Text: text,
				})
				folderAdded++
				if folderAdded >= maxResults {
					return nil
				}
			}
		}
		return nil
	})

	res := grepResult{
		Status:        "ok",
		Matches:       matches,
		Total:         len(matches),
		Files:         filesSearched,
		Pattern:       pattern,
		FilesSearched: filesSearched,
	}
	if err != nil {
		res.Status = "error"
		res.Error = err.Error()
	}
	return json.Marshal(res)
}
