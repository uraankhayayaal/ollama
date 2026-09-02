package codegenerator

import (
	"ai/agents"
	"ai/agents/codereviewer"
	"ai/forges"
	"ai/tools"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ollama/ollama/api"
)

type Codegenerator struct {
	OutputDir string
	Prompt    string
	Config    Config
	written   int
}

var genSeq int64

// NewCodegenerator создаёт генератор с уникальной папкой в temp/.
// Имя папки строится из времени и атомарного счётчика, что исключает
// коллизии даже при создании нескольких генераторов в одну микросекунду.
// prompt — текст задания для модели, передаётся как аргумент командной строки.
func NewCodegenerator(prompt string) *Codegenerator {
	seq := atomic.AddInt64(&genSeq, 1)
	dir := filepath.Join("temp", fmt.Sprintf("gen_%s_%04d", time.Now().Format("20060102_150405"), seq))
	os.MkdirAll(dir, 0755)
	return &Codegenerator{OutputDir: dir, Prompt: prompt, Config: LoadConfig()}
}

func (cg Codegenerator) GetMessages() []agents.Message {
	return []agents.Message{
		{
			Type:    agents.MessageTypeHuman,
			Message: cg.Prompt,
		},
	}
}

// RequiredToolFirstRound требует, чтобы в первом раунде модель обязательно
// вызвала WriteFiles (создание файлов проекта), а не ответила текстом.
// Раннер при необходимости подскажет модели и повторит запрос.
func (cg Codegenerator) RequiredToolFirstRound() (string, bool) {
	return "WriteFiles", true
}

func (cg Codegenerator) GetAgentMemoryMessages(text []agents.Message) []agents.Message {
	lang := cg.Config.Language
	if lang == "" {
		lang = "Go"
	}

	var moduleInstruction string
	if cg.Config.Module != "" {
		moduleInstruction = fmt.Sprintf("Используй имя модуля Go %q в файле go.mod.", cg.Config.Module)
	}

	overwriteRule := "Ты можешь перезаписывать файлы инструментами WriteFiles/WriteFile."
	if cg.Config.NoOverwrite {
		overwriteRule = "Включена защита от перезаписи: инструменты вернут ошибку, если ты попытаешься перезаписать уже существующий файл. Перезаписывай файлы только при необходимости."
	}

	return []agents.Message{
		{
			Type: agents.MessageTypeSystem,
			Message: fmt.Sprintf(`Ты - опытный разработчик на языке %s и архитектор, который пишет аккуратный, рабочий код и соблюдает архитектурные слои и обязанности каждого участка кода.
Ты работаешь только внутри выходной директории проекта (OutputDir).
%s
%s

Твой план работы:
1. Создай все нужные файлы (включая go.mod, если требуется) инструментами WriteFiles/WriteFile.
2. Проверь, что проект компилируется и проходит проверки, запустив инструмент Run с командами: "go build ./...", "go vet ./..." и (если есть тесты) "go test ./...".
3. Если компиляция или проверки падают, исправь код (WriteFiles перезапишет файл, а для точечных добавлений используй AppendFile) и запускай Run снова, пока всё не станет зелёным.
4. После успешного build продумай краткую проверку главного сценария работы (вызов программы).
5. Перед чтением или правкой файлов узнай, что уже создано, инструментом List.

Правила:
- Нельзя отвечать текстом-рассуждением вместо действий. Используй инструменты.
- Передавай все файлы списком в свойстве files в одном вызове инструмента WriteFiles.
- В конце приведи файл readme.md с инструкцией запуска и использования.
- Нельзя писать файлы вне OutputDir (инструменты сами это заблокируют).`, lang, moduleInstruction, overwriteRule),
		},
	}
}

func (cg Codegenerator) GetTools() []tools.ToolDefinition {
	return []tools.ToolDefinition{
		{
			Name:        "WriteFile",
			Description: "Используй этот инструмент для сохранения одного файла с кодом.",
			Parameters: map[string]any{
				"type":                 "object", // Корень параметров ВСЕГДА object
				"properties":           singleFileProps(),
				"required":             []string{"filename", "content"},
				"additionalProperties": false,
			},
		},
		{
			Name:        "WriteFiles",
			Description: "Используй этот инструмент для сохранения множества файлов.",
			Parameters: map[string]any{
				"type": "object", // Корень параметров ВСЕГДА object
				"properties": map[string]any{
					"files": map[string]any{
						"type":        "array",
						"description": "Список файлов для записи",
						"items": map[string]any{
							"type":                 "object",
							"properties":           singleFileProps(),
							"required":             []string{"filename", "content"},
							"additionalProperties": false,
						},
					},
				},
				"required":             []string{"files"}, // Массив файлов обязателен для вызова инструмента
				"additionalProperties": false,
			},
		},
		{
			Name:        "ReadFiles",
			Description: "Используй этот инструмент для чтения содержимого одного или нескольких файлов проекта.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filenames": map[string]any{
						"type":        "array",
						"description": "Список путей к файлам, которые нужно прочитать, например ['main.go', 'utils/math.go']",
						"items": map[string]any{
							"type": "string",
						},
					},
				},
				"required":             []string{"filenames"},
				"additionalProperties": false,
			},
		},
		{
			Name:        "DeleteFiles",
			Description: "Используй этот инструмент для удаления ненужных файлов или папок.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"paths": map[string]any{
						"type":        "array",
						"description": "Список путей к файлам или папкам для удаления, например ['old_code.go', 'temp_dir']",
						"items": map[string]any{
							"type": "string",
						},
					},
				},
				"required":             []string{"paths"},
				"additionalProperties": false,
			},
		},
		{
			Name:        "Run",
			Description: "Используй этот инструмент для запуска команд (например, go build, go vet) в выходной директории сгенерированного проекта, чтобы проверить, что код компилируется и проходит проверки. Возвращает stdout+stderr.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{"type": "string", "description": "Команда для запуска, например 'go build ./...'"},
				},
				"required":             []string{"command"},
				"additionalProperties": false,
			},
		},
		{
			Name:        "List",
			Description: "Используй этот инструмент для получения списка (дерева) файлов в проекте, чтобы узнать, что уже создано, прежде чем читать или изменять.",
			Parameters: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"required":             []string{},
				"additionalProperties": false,
			},
		},
		{
			Name:        "AppendFile",
			Description: "Используй этот инструмент для добавления текста в конец существующего файла (например, новой функции или реализации). Для больших правок лучше перезаписать файл через WriteFile/WriteFiles.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filename": map[string]any{"type": "string", "description": "Путь к файлу для дополнения, например 'main.go'"},
					"content":  map[string]any{"type": "string", "description": "Текст, добавляемый в конец файла"},
				},
				"required":             []string{"filename", "content"},
				"additionalProperties": false,
			},
		},
	}
}

// singleFileProps возвращает общую схему свойств для одного файла (filename + content).
func singleFileProps() map[string]any {
	return map[string]any{
		"filename": map[string]any{"type": "string", "description": "Название файла с путем, например 'utils/math.go'"},
		"content":  map[string]any{"type": "string", "description": "Полный исходный код файла"},
	}
}

// Метод возвращает слайс официальных инструментов Ollama
func (cr Codegenerator) GetToolsForOllama() []api.Tool {
	// --- Инструмент 1: WriteFile ---
	singleProps := api.NewToolPropertiesMap()
	singleProps.Set("filename", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Название файла, например 'main.go'",
	})
	singleProps.Set("content", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Код файла",
	})

	// --- Инструмент 2: WriteFiles (Вложенная схема) ---
	// Сначала создаем свойства для внутренних объектов массива (для каждого файла)
	itemProps := api.NewToolPropertiesMap()
	itemProps.Set("filename", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Название файла с путем, например 'utils/math.go'",
	})
	itemProps.Set("content", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Полный исходный код файла",
	})

	// 1. Схема для ReadFiles
	readProps := api.NewToolPropertiesMap()
	readProps.Set("filenames", api.ToolProperty{
		Type:        api.PropertyType{"array"},
		Description: "Список имен файлов для чтения, например ['main.go', 'calc/roots.go']",
		Items: &api.ToolProperty{
			Type: api.PropertyType{"string"},
		},
	})

	// 2. Схема для DeleteFiles
	deleteProps := api.NewToolPropertiesMap()
	deleteProps.Set("paths", api.ToolProperty{
		Type:        api.PropertyType{"array"},
		Description: "Список путей к файлам или папкам для удаления, например ['old_code.go', 'temp_dir']",
		Items: &api.ToolProperty{
			Type: api.PropertyType{"string"},
		},
	})

	// Теперь создаем корневые свойства для WriteFiles, куда помещаем массив с itemProps
	bulkProps := api.NewToolPropertiesMap()
	bulkProps.Set("files", api.ToolProperty{
		Type:        api.PropertyType{"array"},
		Description: "Список файлов для записи",
		Items: &api.ToolProperty{
			Type:       api.PropertyType{"object"},
			Properties: itemProps, // Передаем упорядоченную мапу для внутренней схемы
			Required:   []string{"filename", "content"},
		},
	})

	// Схема для AppendFile
	appendProps := api.NewToolPropertiesMap()
	appendProps.Set("filename", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Путь к файлу для дополнения, например 'main.go'",
	})
	appendProps.Set("content", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Текст, добавляемый в конец файла",
	})

	return []api.Tool{
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "WriteFile",
				Description: "Используй этот инструмент для сохранения одного файла с кодом.",
				Parameters: api.ToolFunctionParameters{
					Type:       "object",
					Properties: singleProps, // Передаем *api.ToolPropertiesMap
					Required:   []string{"filename", "content"},
				},
			},
		},
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "WriteFiles",
				Description: "Используй этот инструмент для одновременного сохранения множества файлов (например, структуры всего проекта).",
				Parameters: api.ToolFunctionParameters{
					Type:       "object",
					Properties: bulkProps, // Передаем *api.ToolPropertiesMap
					Required:   []string{"files"},
				},
			},
		},
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "ReadFiles",
				Description: "Используй этот инструмент для чтения содержимого одного или нескольких файлов проекта.",
				Parameters: api.ToolFunctionParameters{
					Type:       "object",
					Properties: readProps,
					Required:   []string{"filenames"},
				},
			},
		},
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "DeleteFiles",
				Description: "Используй этот инструмент для удаления ненужных файлов или папок.",
				Parameters: api.ToolFunctionParameters{
					Type:       "object",
					Properties: deleteProps,
					Required:   []string{"paths"},
				},
			},
		},
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "Run",
				Description: "Используй этот инструмент для запуска команд (например, go build, go vet) в выходной директории сгенерированного проекта.",
				Parameters: api.ToolFunctionParameters{
					Type: "object",
					Properties: func() *api.ToolPropertiesMap {
						p := api.NewToolPropertiesMap()
						p.Set("command", api.ToolProperty{
							Type:        api.PropertyType{"string"},
							Description: "Команда для запуска, например 'go build ./...'",
						})
						return p
					}(),
					Required: []string{"command"},
				},
			},
		},
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "List",
				Description: "Используй этот инструмент для получения списка (дерева) файлов в проекте, чтобы узнать, что уже создано.",
				Parameters: api.ToolFunctionParameters{
					Type:       "object",
					Properties: api.NewToolPropertiesMap(),
				},
			},
		},
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "AppendFile",
				Description: "Используй этот инструмент для добавления текста в конец существующего файла.",
				Parameters: api.ToolFunctionParameters{
					Type:       "object",
					Properties: appendProps,
					Required:   []string{"filename", "content"},
				},
			},
		},
	}
}

func (cg *Codegenerator) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	switch functionName {

	case "WriteFiles":
		return cg.WriteFiles(functionArgs)
	case "WriteFile":
		return cg.WriteFile(functionArgs)
	case "ReadFiles":
		return cg.ReadFiles(functionArgs)
	case "DeleteFiles":
		return cg.DeleteFiles(functionArgs)
	case "Run":
		return cg.Run(functionArgs)
	case "List":
		return cg.List(functionArgs)
	case "AppendFile":
		return cg.AppendFile(functionArgs)
	default:
		return nil, fmt.Errorf("function %s not implemented in Codegenerator", functionName)
	}
}

func (cg *Codegenerator) WriteFile(args map[string]any) ([]byte, error) {
	var params map[string]string

	bytes, err := json.Marshal(args)
	if err == nil {
		_ = json.Unmarshal(bytes, &params)
	}

	filename := params["filename"]
	content := params["content"]

	result := []map[string]string{}
	if err := cg.write(filename, content); err != nil {
		result = append(result, map[string]string{
			"filename": filename,
			"status":   "error",
			"message":  err.Error(),
		})
	} else {
		result = append(result, map[string]string{
			"filename": filename,
			"status":   "success",
		})
	}

	resultJSON, _ := json.Marshal(result)
	return resultJSON, nil
}

// FileItem описывает структуру одного файла, приходящего из аргументов ИИ
type FileItem struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

// BulkParams соответствует корневому JSON-объекту параметров инструмента WriteFiles
type BulkParams struct {
	Files []FileItem `json:"files"`
}

// WriteFiles обрабатывает пакетную запись файлов, вызванную ИИ-агентом
func (cg *Codegenerator) WriteFiles(args map[string]any) ([]byte, error) {
	var params BulkParams

	bytes, err := json.Marshal(args)
	if err == nil {
		_ = json.Unmarshal(bytes, &params)
	}

	if len(params.Files) == 0 {
		fmt.Printf("[WriteFiles] ВНИМАНИЕ: список файлов пуст. Полученные аргументы: %s\n", string(bytes))
		resultJSON, _ := json.Marshal(map[string]string{
			"status":     "error",
			"message":    "Список файлов пуст или неверный формат аргументов",
			"raw_args":   string(bytes),
			"suggestion": "Аргументы должны быть в формате: {\"files\": [{\"filename\": \"путь\", \"content\": \"код\"}]}",
		})
		return resultJSON, nil
	}

	result := []map[string]string{}
	for _, file := range params.Files {
		if err := cg.write(file.Filename, file.Content); err != nil {
			result = append(result, map[string]string{
				"filename": file.Filename,
				"status":   "error",
				"message":  err.Error(),
			})
		} else {
			result = append(result, map[string]string{
				"filename": file.Filename,
				"status":   "success",
			})
		}
	}

	resultJSON, _ := json.Marshal(result)
	fmt.Println("[WriteFiles] результат:", result)
	return resultJSON, nil
}

// ReadParams соответствует JSON-параметрам инструмента ReadFiles
type ReadParams struct {
	Filenames []string `json:"filenames"`
}

// ReadFiles читает содержимое указанных файлов и возвращает их контент ИИ-агенту
func (cg Codegenerator) ReadFiles(args map[string]any) ([]byte, error) {
	var params ReadParams

	bytes, err := json.Marshal(args)
	if err == nil {
		_ = json.Unmarshal(bytes, &params)
	}

	if len(params.Filenames) == 0 {
		resultJSON, _ := json.Marshal(map[string]string{
			"status":  "error",
			"message": "Список файлов для чтения пуст",
		})
		return resultJSON, nil
	}

	result := []map[string]string{}

	for _, filename := range params.Filenames {
		result = append(result, cg.readResult(filename))
	}

	resultJSON, _ := json.Marshal(result)
	return resultJSON, nil
}

// readResult читает файл и возвращает результат-статус.
func (cg Codegenerator) readResult(name string) map[string]string {
	full, err := cg.resolvePath(name)
	if err != nil {
		return map[string]string{"filename": name, "status": "error", "message": err.Error()}
	}
	content, err := os.ReadFile(full)
	if err != nil {
		return map[string]string{"filename": name, "status": "error", "message": err.Error()}
	}
	return map[string]string{"filename": name, "status": "success", "content": string(content)}
}

// DeleteParams соответствует JSON-параметрам инструмента DeleteFiles
type DeleteParams struct {
	Paths []string `json:"paths"` // Может принимать как файлы, так и папки
}

// DeleteFiles удаляет указанные файлы или папки с диска
func (cg Codegenerator) DeleteFiles(args map[string]any) ([]byte, error) {
	var params DeleteParams

	bytes, err := json.Marshal(args)
	if err == nil {
		_ = json.Unmarshal(bytes, &params)
	}

	if len(params.Paths) == 0 {
		resultJSON, _ := json.Marshal(map[string]string{
			"status":  "error",
			"message": "Список путей для удаления пуст",
		})
		return resultJSON, nil
	}

	result := []map[string]string{}

	for _, path := range params.Paths {
		res, err := cg.remove(path)
		if err != nil {
			result = append(result, map[string]string{
				"path":    path,
				"status":  "error",
				"message": err.Error(),
			})
			continue
		}
		result = append(result, res)
	}

	resultJSON, _ := json.Marshal(result)
	return resultJSON, nil
}

// resolvePath приводит относительный путь к абсолютному в пределах OutputDir
// и защищает от выхода за границу через ".." или абсолютные пути.
func (cg Codegenerator) resolvePath(name string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("абсолютный путь запрещён: %s", name)
	}
	full := filepath.Join(cg.OutputDir, cleaned)
	root := filepath.Clean(cg.OutputDir)
	if !strings.HasPrefix(full, root+string(filepath.Separator)) && full != root {
		return "", fmt.Errorf("путь выходит за пределы OutputDir: %s", name)
	}
	return full, nil
}

// write создаёт директории при необходимости и записывает файл.
func (cg *Codegenerator) write(name, content string) error {
	full, err := cg.resolvePath(name)
	if err != nil {
		return err
	}

	if cg.Config.MaxFiles > 0 && cg.written >= cg.Config.MaxFiles {
		return fmt.Errorf("превышен лимит записанных файлов (%d)", cg.Config.MaxFiles)
	}
	if cg.Config.NoOverwrite {
		if _, err := os.Stat(full); err == nil {
			return fmt.Errorf("файл уже существует (%s), перезапись запрещена (CODEGEN_NO_OVERWRITE=true)", name)
		}
	}

	if dir := filepath.Dir(full); dir != "." && dir != "/" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("не удалось создать директорию %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		return err
	}
	cg.written++
	fmt.Printf("[WriteFiles] записано %q -> %q\n", name, full)
	return nil
}

// appendTo прибавляет текст в конец существующего файла (без полной
// перезаписи). Используется инструментом AppendFile для точечных правок.
func (cg *Codegenerator) appendTo(name, content string) error {
	full, err := cg.resolvePath(name)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(full, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return err
	}
	fmt.Printf("[AppendFile] дополнен %q -> %q\n", name, full)
	return nil
}

// AppendFile добавляет текст в конец указанного файла.
func (cg *Codegenerator) AppendFile(args map[string]any) ([]byte, error) {
	var params map[string]string
	bytes, err := json.Marshal(args)
	if err == nil {
		_ = json.Unmarshal(bytes, &params)
	}
	filename := params["filename"]
	content := params["content"]

	var res map[string]string
	if filename == "" || content == "" {
		res = map[string]string{
			"filename": filename,
			"status":   "error",
			"message":  "filename и content обязательны",
		}
	} else if err := cg.appendTo(filename, content); err != nil {
		res = map[string]string{
			"filename": filename,
			"status":   "error",
			"message":  err.Error(),
		}
	} else {
		res = map[string]string{
			"filename": filename,
			"status":   "success",
		}
	}
	return json.Marshal(res)
}

// List возвращает дерево файлов/папок внутри OutputDir, чтобы модель знала,
// что уже создано, прежде чем читать или править код.
func (cg *Codegenerator) List(args map[string]any) ([]byte, error) {
	var entries []string
	err := filepath.WalkDir(cg.OutputDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == cg.OutputDir {
			return nil
		}
		rel, rerr := filepath.Rel(cg.OutputDir, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			entries = append(entries, rel+"/")
		} else {
			if info, ierr := d.Info(); ierr == nil {
				entries = append(entries, fmt.Sprintf("%s (%d B)", rel, info.Size()))
			} else {
				entries = append(entries, rel)
			}
		}
		return nil
	})
	if err != nil {
		return json.Marshal(map[string]string{"status": "error", "message": err.Error()})
	}
	if len(entries) == 0 {
		return json.Marshal(map[string]string{"status": "empty", "message": "В каталоге пока нет файлов."})
	}
	return json.Marshal(map[string]any{"status": "success", "files": entries})
}

// RunParams соответствует JSON-параметрам инструмента Run
type RunParams struct {
	Command string `json:"command"`
}

// Run запускает команду в OutputDir (например, go build) и возвращает вывод ИИ-агенту.
func (cg Codegenerator) Run(args map[string]any) ([]byte, error) {
	var params RunParams

	raw, err := json.Marshal(args)
	if err == nil {
		_ = json.Unmarshal(raw, &params)
	}

	if params.Command == "" {
		resultJSON, _ := json.Marshal(map[string]string{
			"status":  "error",
			"message": "команда не указана",
		})
		return resultJSON, nil
	}

	workdir := cg.OutputDir
	cmd := exec.Command("sh", "-c", params.Command)
	cmd.Dir = workdir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	result := map[string]string{
		"command":    params.Command,
		"workdir":    workdir,
		"exit_error": "",
		"stdout":     stdout.String(),
		"stderr":     stderr.String(),
	}
	if runErr != nil {
		result["exit_error"] = runErr.Error()
		result["status"] = "error"
	} else {
		result["status"] = "success"
	}

	resultJSON, _ := json.Marshal(result)
	return resultJSON, nil
}

// remove удаляет файл или папку и возвращает (результат-статус, ошибку).
func (cg Codegenerator) remove(name string) (map[string]string, error) {
	full, err := cg.resolvePath(name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(full); os.IsNotExist(err) {
		return map[string]string{"path": name, "status": "error", "message": "файл или папка не существует"}, nil
	}
	if err := os.RemoveAll(full); err != nil {
		return nil, err
	}
	return map[string]string{"path": name, "status": "success"}, nil
}

// SelfReviewDir возвращает путь к директории сгенерированного кода.
// Используется main.go для цикла self-repair: локальное ревью + исправление.
func (cg Codegenerator) SelfReviewDir() string {
	return cg.OutputDir
}

// NewReviewAgentFor создаёт агента код-ревью, привязанного к локальной
// директории с кодом (через forges.LocalForge). Позволяет прогнать написанный
// код через логику codereviewer без сети. focus — необязательная цель ревью
// (например "безопасность"). Возвращает агента и его forge, чтобы вызывающий
// мог извлечь собранные замечания (lf.Published).
func (cg Codegenerator) NewReviewAgentFor(dir string, focus string) (agents.Agent, forges.Forge, error) {
	lf, err := forges.NewLocalForge(dir)
	if err != nil {
		return nil, nil, err
	}
	return codereviewer.NewCodereviewerWithForge(lf, focus), lf, nil
}

// FixPromptFor формирует новое задание для исправления сгенерированного кода:
// берет исходное задание и дополняет его найденными при ревью замечаниями.
func (cg Codegenerator) FixPromptFor(original string, comments []forges.ReviewComment) string {
	if len(comments) == 0 {
		return original + "\n\nКод прошёл локальное ревью без замечаний — финальный прогон: убедись, что всё компилируется и работает."
	}

	var b strings.Builder
	b.WriteString(original)
	b.WriteString("\n\nИсправь уже сгенерированный код в OutputDir по следующим замечаниям ревью (не ломай остальной функционал, затем снова загони go build и go vet через Run):\n")
	for _, c := range comments {
		fmt.Fprintf(&b, "- %s:%d %s\n", c.FilePath, c.Line, c.Text)
	}
	return b.String()
}

// NewCodegeneratorInDir создаёт генератор в заданной директории (а не в новой
// temp/). Используется для повторных раундов self-repair: модель исправляет
// уже существующий вывод на основе замечаний ревью.
func NewCodegeneratorInDir(prompt, dir string) (*Codegenerator, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		return nil, err
	}
	return &Codegenerator{OutputDir: abs, Prompt: prompt, Config: LoadConfig()}, nil
}

// Finalize пишет в OutputDir файл-отчёт SUMMARY.md (если включено конфигом)
// со структурой сгенерированного проекта. Вызывается из main.go после цикла
// генерации, заполняя отчёт реально созданными файлами.
func (cg Codegenerator) Finalize() {
	if cg.Config.SummaryFile == "" {
		return
	}

	full, err := cg.resolvePath(cg.Config.SummaryFile)
	if err != nil {
		return
	}

	lines := []string{
		"# Сгенерированный проект",
		"",
		"- Язык: " + cg.Config.Language,
		"- Задание: " + cg.Prompt,
		"",
		"## Файлы",
		"",
	}

	_ = filepath.Walk(cg.OutputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(cg.OutputDir, path)
		if rerr != nil {
			return nil
		}
		if rel == cg.Config.SummaryFile {
			return nil
		}
		lines = append(lines, "- "+filepath.ToSlash(rel))
		return nil
	})

	content := strings.Join(lines, "\n") + "\n"
	_ = os.WriteFile(full, []byte(content), 0644)
	fmt.Printf("[Finalize] написан отчёт: %s\n", full)

	// Гарантируем наличие README.md с инструкциями по установке, запуску и
	// использованию. Если модель уже создала его — не трогаем (не перезапишем).
	cg.EnsureREADME()
}

// EnsureREADME создаёт README.md в OutputDir, если он ещё не существует,
// с инструкцией по установке, запуску и использованию, собранной из
// реально сгенерированных файлов. Если README уже есть (его написала
// модель) — файл не перезаписывается, чтобы не портить авторский текст.
func (cg Codegenerator) EnsureREADME() {
	const name = "README.md"

	full, err := cg.resolvePath(name)
	if err != nil {
		return
	}
	if _, err := os.Stat(full); err == nil {
		// README уже есть (например, его создала модель) — не трогаем.
		fmt.Printf("[EnsureREADME] %s уже существует, пропускаю\n", full)
		return
	}

	files := cg.listProjectFiles()
	content := buildReadme(cg, files)
	if content == "" {
		return
	}
	_ = os.WriteFile(full, []byte(content), 0644)
	fmt.Printf("[EnsureREADME] создан: %s\n", full)
}

// listProjectFiles возвращает относительные пути всех файлов OutputDir
// (рекурсивно), кроме собственного README/SUMMARY, отсортированные.
func (cg Codegenerator) listProjectFiles() []string {
	var out []string
	_ = filepath.Walk(cg.OutputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(cg.OutputDir, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "README.md" || rel == cg.Config.SummaryFile {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out
}

// buildReadme собирает текст README.md из имеющихся файлов: определяет
// модуль и точку входа (main.go, cmd/), Go-версию, наличие тестов и
// формирует разделы "Установка", "Запуск", "Использование".
func buildReadme(cg Codegenerator, files []string) string {
	if len(files) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("# Сгенерированный проект\n\n")
	b.WriteString("Проект создан агентом-генератором кода.\n\n")

	module := cg.Config.Module
	goVersion := goVersionFromFiles(cg.OutputDir, files)
	hasTests := hasGoTests(files)

	if module != "" {
		fmt.Fprintf(&b, "- Модуль: `%s`\n", module)
	}
	if goVersion != "" {
		fmt.Fprintf(&b, "- Go: `%s`\n", goVersion)
	}
	if hasTests {
		b.WriteString("- Тесты: есть\n")
	}

	b.WriteString("\n## Установка\n\n")
	if module != "" {
		fmt.Fprintf(&b, "```bash\ngo mod download\n```\n\n")
	}
	if goVersion != "" {
		fmt.Fprintf(&b, "Требуется Go %s или новее.\n\n", goVersion)
	}

	b.WriteString("## Запуск\n\n")
	if mainPath := findMainGo(files); mainPath != "" {
		fmt.Fprintf(&b, "```bash\ngo run %s\n```\n\n", mainPath)
	} else {
		b.WriteString("```bash\ngo run .\n```\n\n")
	}

	if hasTests {
		b.WriteString("## Тесты\n\n```bash\ngo test ./...\n```\n\n")
	}

	b.WriteString("## Использование\n\n")
	if mainPath := findMainGo(files); mainPath != "" {
		fmt.Fprintf(&b, "После запуска (`go run %s`) программа выполнит основной сценарий из задания.\n", mainPath)
	} else {
		b.WriteString("Публичные пакеты/функции проекта используются через импорт: `import \"%s/…\"`.\n")
	}

	b.WriteString("\n## Структура\n\n```\n")
	for _, f := range files {
		fmt.Fprintf(&b, "%s\n", f)
	}
	b.WriteString("```\n")

	return b.String()
}

// findMainGo возвращает путь к файлу point-of-entry (main.go или первый
// файл под cmd/), подходящий для команды "go run". Иначе "".
func findMainGo(files []string) string {
	if len(files) == 0 {
		return ""
	}
	// Отдаём предпочтение корневому main.go.
	for _, f := range files {
		if f == "main.go" || f == "./main.go" {
			return "main.go"
		}
	}
	for _, f := range files {
		if filepath.Base(f) == "main.go" {
			return f
		}
	}
	return ""
}

// hasGoTests сообщает, содержит ли список файлов Go-тесты (*_test.go).
func hasGoTests(files []string) bool {
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			return true
		}
	}
	return false
}

// goVersionFromFiles ищет строку "go x.y.z" в go.mod и возвращает её,
// иначе "".
func goVersionFromFiles(dir string, files []string) string {
	for _, f := range files {
		if filepath.Base(f) != "go.mod" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "go ") {
				return strings.TrimPrefix(line, "go ")
			}
		}
	}
	return ""
}
