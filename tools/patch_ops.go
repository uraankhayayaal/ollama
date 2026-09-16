package tools

// Обработчики инструментов-патчей в *FileOps: PatchGoFunction (семантическая
// замена одной Go-функции через go/ast) и SearchReplace (универсальный
// текстовый SEARCH/REPLACE). Оба работают ТОЛЬКО с существующими файлами и
// применяют правку атомарно: файл не записывается, если хоть один фрагмент
// не найден (SEARCH) или функция/файл невалидны — модель не может скрытно
// удалить или переписать чужой функционал.

import (
	"ai/logging"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PatchGoFunction применяет семантический патч к Go-файлу: заменяет узел
// ОДНОЙ функции (известной по function_name/receiver) на код из body и
// форматирует файл через go/format. Остальные функции, импорты и структура
// файла остаются нетронутыми (см. PatchGoFuncSource).
// Read-modify-write сериализуется пер-проектной блокировкой: параллельные
// шаги волны не должны одновременно читать устаревшие версии одного файла
// и взаимно затирать патчи.
func (ops *FileOps) PatchGoFunction(args map[string]any) ([]byte, error) {
	var resp []byte
	err := withProjectLock(ops.OutputDir, func() error {
		var rerr error
		resp, rerr = ops.patchGoFunctionLocked(args)
		return rerr
	})
	return resp, err
}

// patchGoFunctionLocked — тело PatchGoFunction без пер-проектной блокировки.
func (ops *FileOps) patchGoFunctionLocked(args map[string]any) ([]byte, error) {
	var params GoFuncPatchParams
	raw, err := json.Marshal(args)
	if err == nil {
		_ = json.Unmarshal(raw, &params)
	}
	// Модели могут передать imports JSON-строкой (["errors","fmt"]) — нормализуем.
	if len(params.Imports) == 0 {
		if p, ok := args["imports"].(string); ok && strings.TrimSpace(p) != "" {
			params.Imports = parsePathList(p)
		}
	}

	if params.TargetFile == "" || params.FunctionName == "" || params.Body == "" {
		return patchStatusError("target_file, function_name и body обязательны. Возвращай ПОЛНЫЙ исходник заменяющей функции в body (начиная с \"func\")")
	}
	if !strings.HasSuffix(params.TargetFile, ".go") {
		return patchStatusError(fmt.Sprintf("target_file %q — не Go-файл: PatchGoFunction работает только с .go", params.TargetFile))
	}

	full, err := ops.ResolvePath(params.TargetFile)
	if err != nil {
		return patchStatusError(err.Error())
	}
	rel := ops.relPath(full)
	if !ops.allowed(rel) {
		return patchStatusError(fmt.Sprintf("файл %q вне области работы (scope: %v)", params.TargetFile, ops.Scope))
	}
	if !ops.writeAllowed(rel) {
		return patchStatusError(fmt.Sprintf("запись в файл %q запрещена (WriteReadmeOnly)", params.TargetFile))
	}

	info, err := os.Stat(full)
	if err != nil {
		return patchStatusError(fmt.Sprintf("файл %q не существует или недоступен: %v", params.TargetFile, err))
	}
	if info.IsDir() {
		return patchStatusError(fmt.Sprintf("%q — директория, а нужен Go-файл", params.TargetFile))
	}
	src, err := os.ReadFile(full)
	if err != nil {
		return patchStatusError(fmt.Sprintf("не удалось прочитать %q: %v", params.TargetFile, err))
	}

	out, err := PatchGoFuncSource(filepath.Base(params.TargetFile), src, params)
	if err != nil {
		return patchStatusError(err.Error())
	}

	if err := os.WriteFile(full, out, info.Mode()); err != nil {
		return patchStatusError(fmt.Sprintf("не удалось записать %q: %v", params.TargetFile, err))
	}
	logging.Detailf("[PatchGoFunction] функция %q в %q заменена (receiver %q)",
		params.FunctionName, params.TargetFile, params.Receiver)
	return json.Marshal(map[string]string{
		"status":        "success",
		"filename":      params.TargetFile,
		"function_name": params.FunctionName,
		"message":       fmt.Sprintf("функция %q заменена, файл отформатирован через go/format", params.FunctionName),
	})
}

// SearchReplace применяет к существующим файлам блоки SEARCH/REPLACE.
// Каждый SEARCH обязан совпасть с кодом файла дословно; при промахе хоть
// одного блока файл НЕ записывается и возвращается ошибка с фрагментом.
// Весь read-modify-write проходит под пер-проектной блокировкой (см. filelock.go).
func (ops *FileOps) SearchReplace(args map[string]any) ([]byte, error) {
	var resp []byte
	err := withProjectLock(ops.OutputDir, func() error {
		var rerr error
		resp, rerr = ops.searchReplaceLocked(args)
		return rerr
	})
	return resp, err
}

// searchReplaceLocked — тело SearchReplace без пер-проектной блокировки.
func (ops *FileOps) searchReplaceLocked(args map[string]any) ([]byte, error) {
	files := parseSearchReplaceFiles(args["files"])
	if len(files) == 0 {
		return patchStatusError("список files пуст или неверный формат. Ожидается: {\"files\": [{\"filename\": \"...\", \"patches\": [{\"search\": \"...\", \"replace\": \"...\"}]}]}. Вместо патчей можно передать raw-текст блоков <<<<<<< SEARCH ... ======= ... >>>>>>> REPLACE в поле content")
	}

	result := make([]map[string]string, 0, len(files))
	for _, f := range files {
		if f.Filename == "" {
			result = append(result, map[string]string{"status": "error", "message": "filename пуст"})
			continue
		}
		full, err := ops.ResolvePath(f.Filename)
		res := map[string]string{"filename": f.Filename}
		if err != nil {
			res["status"] = "error"
			res["message"] = err.Error()
			result = append(result, res)
			continue
		}
		rel := ops.relPath(full)
		if !ops.allowed(rel) {
			res["status"] = "error"
			res["message"] = fmt.Sprintf("файл вне области работы (scope: %v)", ops.Scope)
			result = append(result, res)
			continue
		}
		if !ops.writeAllowed(rel) {
			res["status"] = "error"
			res["message"] = fmt.Sprintf("запись в файл запрещена (WriteReadmeOnly): %s", f.Filename)
			result = append(result, res)
			continue
		}
		info, serr := os.Stat(full)
		if serr != nil {
			res["status"] = "error"
			res["message"] = fmt.Sprintf("файл не существует или недоступен: %v", serr)
			result = append(result, res)
			continue
		}
		if info.IsDir() {
			res["status"] = "error"
			res["message"] = "это директория, а нужен файл"
			result = append(result, res)
			continue
		}
		src, rerr := os.ReadFile(full)
		if rerr != nil {
			res["status"] = "error"
			res["message"] = fmt.Sprintf("не удалось прочитать: %v", rerr)
			result = append(result, res)
			continue
		}

		out, aerr := ApplySearchReplace(string(src), f.Patches)
		if aerr != nil {
			res["status"] = "error"
			res["message"] = aerr.Error() + " Файл НЕ изменён."
			result = append(result, res)
			continue
		}

		if werr := os.WriteFile(full, []byte(out), info.Mode()); werr != nil {
			res["status"] = "error"
			res["message"] = fmt.Sprintf("не удалось записать: %v", werr)
			result = append(result, res)
			continue
		}
		res["status"] = "success"
		res["patches"] = fmt.Sprintf("%d", len(f.Patches))
		res["message"] = fmt.Sprintf("применено блоков SEARCH/REPLACE: %d", len(f.Patches))
		result = append(result, res)
		logging.Detailf("[SearchReplace] %q: применено %d блоков", f.Filename, len(f.Patches))
	}

	return json.Marshal(result)
}

// patchStatusError формирует ответ инструмента-патча с ошибкой. Формат
// {"status":"error"} совпадает с остальными инструментами, чтобы runner
// распознавал неудачу и подсказывал модели повторить.
func patchStatusError(msg string) ([]byte, error) {
	out, _ := json.Marshal(map[string]string{"status": "error", "message": msg})
	return out, nil
}
