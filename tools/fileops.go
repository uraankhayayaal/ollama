package tools

import (
	"ai/forges"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FileOps — разделяемое состояние файловых инструментов генератора кода
// (WriteFiles, ReadFiles, DeleteFiles, Run, List, AppendFile).
// Держит рабочую директорию (OutputDir) и политики записи, общие для всех
// выбранных агентом инструментов.
type FileOps struct {
	// OutputDir — единственная директория, внутри которой разрешена работа
	// инструментов (защита от выхода за пределы через ".." или абсолютные пути).
	OutputDir string
	// MaxFiles — максимальное число файлов, которое можно записать за запуск.
	// 0 или отрицательное — без лимита.
	MaxFiles int
	// NoOverwrite — запрещает перезаписывать уже существующие файлы.
	NoOverwrite bool
	// Scope — области работы (файлы/директории проекта), в рамках которых
	// разрешены операции. Пустой — без ограничений (весь OutputDir).
	Scope []string
	// scopeMatch — скомпилированный matcher областей; nil — без ограничений.
	scopeMatch *forges.ScopeMatcher
	// written — счётчик записанных файлов (разделяется инструментами).
	written int
}

// SetScope задаёт области работы для инструментов (нормализует записи через
// forges.CompileScope). Вызов с пустым/nil-слайсом снимает ограничения.
func (ops *FileOps) SetScope(scope []string) {
	ops.Scope = scope
	ops.scopeMatch = forges.CompileScope(scope)
}

// allowed проверяет, разрешён ли файл (относительный slash-путь) областью
// работы. Без установленного scope разрешено всё.
func (ops *FileOps) allowed(rel string) bool {
	if ops.scopeMatch == nil || ops.scopeMatch.Empty() {
		return true
	}
	return ops.scopeMatch.Allow(rel)
}

// dirHasScope сообщает, стоит ли заходить в директорию при обходе
// (внутри неё есть файлы из области работы), даже если сама директория
// в область не входит.
func (ops *FileOps) dirHasScope(rel string) bool {
	if ops.scopeMatch == nil {
		return true
	}
	return ops.scopeMatch.HasInside(rel)
}

// relPath возвращает относительный slash-путь файла внутри OutputDir.
func (ops *FileOps) relPath(full string) string {
	rel, err := filepath.Rel(ops.OutputDir, full)
	if err != nil {
		return filepath.ToSlash(full)
	}
	return filepath.ToSlash(rel)
}

// ResolvePath приводит относительный путь к абсолютному в пределах OutputDir
// и защищает от выхода за границу через ".." или абсолютные пути.
func (ops *FileOps) ResolvePath(name string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("абсолютный путь запрещён: %s", name)
	}
	full := filepath.Join(ops.OutputDir, cleaned)
	root := filepath.Clean(ops.OutputDir)
	if !strings.HasPrefix(full, root+string(filepath.Separator)) && full != root {
		return "", fmt.Errorf("путь выходит за пределы OutputDir: %s", name)
	}
	return full, nil
}

// Write создаёт директории при необходимости и записывает файл.
func (ops *FileOps) Write(name, content string) error {
	full, err := ops.ResolvePath(name)
	if err != nil {
		return err
	}
	if !ops.allowed(ops.relPath(full)) {
		return fmt.Errorf("файл %q вне области работы (scope: %v)", name, ops.Scope)
	}

	if ops.MaxFiles > 0 && ops.written >= ops.MaxFiles {
		return fmt.Errorf("превышен лимит записанных файлов (%d)", ops.MaxFiles)
	}
	if ops.NoOverwrite {
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
	ops.written++
	fmt.Printf("[WriteFiles] записано %q -> %q\n", name, full)
	return nil
}

// AppendTo прибавляет текст в конец существующего файла (без полной
// перезаписи). Используется инструментом AppendFile для точечных правок.
func (ops *FileOps) AppendTo(name, content string) error {
	full, err := ops.ResolvePath(name)
	if err != nil {
		return err
	}
	if !ops.allowed(ops.relPath(full)) {
		return fmt.Errorf("файл %q вне области работы (scope: %v)", name, ops.Scope)
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

// ReadResult читает файл и возвращает результат-статус.
func (ops *FileOps) ReadResult(name string) map[string]string {
	full, err := ops.ResolvePath(name)
	if err != nil {
		return map[string]string{"filename": name, "status": "error", "message": err.Error()}
	}
	if !ops.allowed(ops.relPath(full)) {
		return map[string]string{"filename": name, "status": "error", "message": "файл вне области работы (scope)"}
	}
	content, err := os.ReadFile(full)
	if err != nil {
		return map[string]string{"filename": name, "status": "error", "message": err.Error()}
	}
	return map[string]string{"filename": name, "status": "success", "content": string(content)}
}

// Remove удаляет файл или папку и возвращает (результат-статус, ошибку).
func (ops *FileOps) Remove(name string) (map[string]string, error) {
	full, err := ops.ResolvePath(name)
	if err != nil {
		return nil, err
	}
	if !ops.allowed(ops.relPath(full)) {
		return map[string]string{"path": name, "status": "error", "message": "файл вне области работы (scope)"}, nil
	}
	if _, err := os.Stat(full); os.IsNotExist(err) {
		return map[string]string{"path": name, "status": "error", "message": "файл или папка не существует"}, nil
	}
	if err := os.RemoveAll(full); err != nil {
		return nil, err
	}
	return map[string]string{"path": name, "status": "success"}, nil
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
func (ops *FileOps) WriteFiles(args map[string]any) ([]byte, error) {
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
		if err := ops.Write(file.Filename, file.Content); err != nil {
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
func (ops *FileOps) ReadFiles(args map[string]any) ([]byte, error) {
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
		result = append(result, ops.ReadResult(filename))
	}

	resultJSON, _ := json.Marshal(result)
	return resultJSON, nil
}

// DeleteParams соответствует JSON-параметрам инструмента DeleteFiles
type DeleteParams struct {
	Paths []string `json:"paths"` // Может принимать как файлы, так и папки
}

// DeleteFiles удаляет указанные файлы или папки с диска
func (ops *FileOps) DeleteFiles(args map[string]any) ([]byte, error) {
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
		res, err := ops.Remove(path)
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

// AppendFile добавляет текст в конец указанного файла.
func (ops *FileOps) AppendFile(args map[string]any) ([]byte, error) {
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
	} else if err := ops.AppendTo(filename, content); err != nil {
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
// что уже создано, прежде чем читать или править код. При заданной области
// работы (Scope) возвращаются только файлы внутри неё.
func (ops *FileOps) List(args map[string]any) ([]byte, error) {
	var entries []string
	err := filepath.WalkDir(ops.OutputDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == ops.OutputDir {
			return nil
		}
		rel, rerr := filepath.Rel(ops.OutputDir, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			// Не опускаемся в зависимости/билды/кеши (node_modules и т.п.),
			// чтобы не жечь контекст модели на мусорных файлах.
			if forges.IsIgnoredDir(d.Name()) {
				return filepath.SkipDir
			}
			// Показываем директорию, если она в области работы; заходим в неё,
			// даже если она вне области, но внутри есть файлы из области.
			if ops.dirHasScope(rel) {
				if ops.allowed(rel) {
					entries = append(entries, rel+"/")
				}
				return nil
			}
			return filepath.SkipDir
		}
		if !ops.allowed(rel) {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			entries = append(entries, fmt.Sprintf("%s (%d B)", rel, info.Size()))
		} else {
			entries = append(entries, rel)
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
func (ops *FileOps) Run(args map[string]any) ([]byte, error) {
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

	workdir := ops.OutputDir
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
