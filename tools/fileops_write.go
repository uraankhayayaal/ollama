package tools

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"

	"ai/infrastructure/forges"
)

// DeleteParams соответствует JSON-параметрам инструмента DeleteFiles
type DeleteParams struct {
	Paths []string `json:"paths"` // Может принимать как файлы, так и папки
}

// DeleteFiles удаляет указанные файлы или папки с диска
func (ops *FileOps) DeleteFiles(args map[string]any) ([]byte, error) {
	var params DeleteParams

	bytes, err := json.Marshal(args)
	if err == nil {
		// Нормализуем "paths": массив или JSON-строка.
		if unmErr := json.Unmarshal(bytes, &params); unmErr != nil || len(params.Paths) == 0 {
			params.Paths = parsePathList(args["paths"])
		}
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
