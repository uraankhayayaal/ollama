package tools

import (
	"ai/forges"
	"ai/logging"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	logging.Detailf("[WriteFiles] записано %q -> %q", name, full)
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
	logging.Detailf("[AppendFile] дополнен %q -> %q", name, full)
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

// parseFileItems нормализует поле "files" инструмента WriteFiles в массив
// FileItem. Модели отправляют его по-разному: напрямую массивом, либо
// JSON-строкой (или []byte). Аналог parseComments в textreview.go.
func parseFileItems(raw any) []FileItem {
	var items []FileItem

	switch v := raw.(type) {
	case string:
		if unmarshalLikeModel(v, &items) {
			return items
		}
		items = parseDecodedFiles(v)
	case []byte:
		if unmarshalLikeModel(string(v), &items) {
			return items
		}
		items = parseDecodedFiles(string(v))
	case nil:
		return nil
	default:
		// Резервный путь — сериализуем обратно и разбираем.
		if b, err := json.Marshal(v); err == nil {
			_ = json.Unmarshal(b, &items)
		}
	}

	return items
}

// parseDecodedFiles разбирает «расшифрованную» JSON-строку поля files: модели
// (например, qwen) сериализуют массив файлов в JSON-строку, а после разбора
// аргументов (Ollama structpb + ParseArguments) экранирование внутри содержимого
// уже снято — в код попадают реальные переводы строк, табы и кавычки, и текст
// перестаёт быть валидным JSON. Структура при этом сохраняется: массив объектов
// с ключами "filename"/"content". Разбор идёт по структуре: объекты выделяются
// сбалансированными фигурными скобками, ключи фиксированы, значения читаются
// между кавычками.
func parseDecodedFiles(raw string) []FileItem {
	body := strings.TrimSpace(raw)
	if !strings.HasPrefix(body, "[") || !strings.HasSuffix(body, "]") {
		return nil
	}
	body = strings.TrimSpace(body[1 : len(body)-1])

	var items []FileItem
	for {
		body = strings.TrimSpace(body)
		if body == "" {
			break
		}
		if !strings.HasPrefix(body, "{") {
			return nil
		}
		end := findMatchingBrace(body, 0)
		if end < 0 {
			return nil
		}
		obj := body[:end+1]
		body = strings.TrimSpace(body[end+1:])
		if strings.HasPrefix(body, ",") {
			body = body[1:]
		} else if body != "" {
			return nil
		}

		it := FileItem{}
		found := false
		if f, ok := decodedFieldValue(obj, "filename"); ok {
			it.Filename = f
			found = true
		}
		if c, ok := decodedFieldValue(obj, "content"); ok {
			it.Content = c
			found = true
		}
		if found {
			items = append(items, it)
		}
	}
	return items
}

// findMatchingBrace возвращает индекс парной закрывающей скобки для "{" на
// позиции start. Скобки внутри содержимого (код) считаются сбалансированными —
// это типично для корректного исходного кода.
func findMatchingBrace(s string, start int) int {
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// decodedFieldValue извлекает значение поля key из объекта без экранирования.
// Значения читаются между кавычками; для поля content берётся текст до закрывающей
// кавычки перед "," + следующим ключом либо перед "}" объекта.
func decodedFieldValue(obj, key string) (string, bool) {
	needle := `"` + key + `"`
	idx := strings.Index(obj, needle)
	if idx < 0 {
		return "", false
	}
	rest := obj[idx+len(needle):]
	rest = strings.TrimLeft(rest, " \t\r\n")
	if !strings.HasPrefix(rest, ":") {
		return "", false
	}
	rest = strings.TrimLeft(rest[1:], " \t\r\n")
	if !strings.HasPrefix(rest, `"`) {
		return "", false
	}
	rest = rest[1:]

	// Граница значения: "," + следующий ключ ("filename"/"content"), либо "}" —
	// закрывающая скобка текущего объекта (возможные "}" внутри кода уже
	// сбалансированы, поэтому это всегда последняя "}" в объекте).
	boundary := len(rest)
	if m := nextKeyBoundary(rest); m >= 0 {
		boundary = m
	} else if c := strings.LastIndexByte(rest, '}'); c >= 0 {
		boundary = c
	}
	val := rest[:boundary]
	val = strings.TrimRight(val, " \t\r\n")
	val = strings.TrimSuffix(val, `"`)
	return val, true
}

// nextKeyBoundary находит позицию, на которой значение поля заканчивается: это
// индекс "," перед началом следующего ключа ("filename"/"content"), либо -1.
func nextKeyBoundary(rest string) int {
	best := -1
	for _, nk := range []string{"filename", "content"} {
		anchor := "," + `"` + nk + `"`
		if i := strings.Index(rest, anchor); i >= 0 && (best < 0 || i < best) {
			best = i
		}
		// Допускаем пробелы между запятой и ключом: обычно ","кey, но бывает
		// "," <пробелы> "key", если между полями добавлено форматирование.
		for j := 1; j < len(rest); j++ {
			if rest[j-1] == ',' {
				k := j
				for k < len(rest) && (rest[k] == ' ' || rest[k] == '\t' || rest[k] == '\n' || rest[k] == '\r') {
					k++
				}
				if strings.HasPrefix(rest[k:], `"`+nk+`"`) && (best < 0 || j-1 < best) {
					best = j - 1
				}
			}
		}
	}
	return best
}

// parsePathList нормализует аргумент-список путей (filenames/paths) инструментов
// ReadFiles/DeleteFiles. Модели передают его либо массивом строк, либо JSON-строкой
// (или []byte) — обе формы приводятся к []string.
func parsePathList(raw any) []string {
	switch v := raw.(type) {
	case string:
		var out []string
		if unmarshalLikeModel(v, &out) {
			return out
		}
	case []byte:
		var out []string
		if unmarshalLikeModel(string(v), &out) {
			return out
		}
	default:
		if b, err := json.Marshal(raw); err == nil {
			var out []string
			_ = json.Unmarshal(b, &out)
			return out
		}
	}
	return nil
}

// WriteFiles обрабатывает пакетную запись файлов, вызванную ИИ-агентом.
// Поле "files" может прийти в двух формах — как массив объектов (типично для
// OpenAI/Ollama) или как JSON-строка (некоторые модели, напр. qwen, склонны
// сериализовать массив в строку). Обе формы нормализуются, чтобы агент не
// тратил раунды на повторные попытки из-за неверного парсинга.
func (ops *FileOps) WriteFiles(args map[string]any) ([]byte, error) {
	var params BulkParams

	bytes, err := json.Marshal(args)
	if err == nil {
		// Пробуем стандартное разложение «files» как массива.
		if unmErr := json.Unmarshal(bytes, &params); unmErr != nil || len(params.Files) == 0 {
			// Не вышло напрямую — пробуем через поле "files" как JSON-строку
			// (или иной строковый вид), следуя общему паттерну parseComments.
			params = BulkParams{Files: parseFileItems(args["files"])}
		}
	}

	if len(params.Files) == 0 {
		logging.Warnf("[WriteFiles] ВНИМАНИЕ: список файлов пуст. Полученные аргументы: %s", string(bytes))
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
	logging.Detailf("[WriteFiles] результат: %v", result)
	return resultJSON, nil
}

// ReadParams соответствует JSON-параметрам инструмента ReadFiles
type ReadParams struct {
	Filenames []string `json:"filenames"`
}

// Лимиты чтения защищают контекст модели от переполнения на больших
// проектах: один файл — не более readMaxFileSize, суммарно за один вызов —
// не более readMaxTotalSize; избыток содержания обрезается с пометкой.
// Настраиваются CODEGEN_READ_MAX_FILE и CODEGEN_READ_MAX_TOTAL.
const (
	readMaxFileSizeDefault  = 100_000
	readMaxTotalSizeDefault = 800_000
)

func readLimit() (perFile, total int) {
	n := readMaxFileSizeDefault
	if v := os.Getenv("CODEGEN_READ_MAX_FILE"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 {
			n = x
		}
	}
	m := readMaxTotalSizeDefault
	if v := os.Getenv("CODEGEN_READ_MAX_TOTAL"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 {
			m = x
		}
	}
	return n, m
}

// ReadFiles читает содержимое указанных файлов и возвращает их контент ИИ-агенту.
// Размер каждого файла и общий объём за вызов ограничены (см. readLimit),
// чтобы инструмент не переполнил контекст модели на большом проекте.
func (ops *FileOps) ReadFiles(args map[string]any) ([]byte, error) {
	var params ReadParams

	bytes, err := json.Marshal(args)
	if err == nil {
		// Пробуем стандартное разложение "filenames" как массива; если модель
		// передала его JSON-строкой — нормализуем через parsePathList.
		if unmErr := json.Unmarshal(bytes, &params); unmErr != nil || len(params.Filenames) == 0 {
			params.Filenames = parsePathList(args["filenames"])
		}
	}

	if len(params.Filenames) == 0 {
		resultJSON, _ := json.Marshal(map[string]string{
			"status":  "error",
			"message": "Список файлов для чтения пуст",
		})
		return resultJSON, nil
	}

	maxFile, maxTotal := readLimit()
	result := []map[string]string{}
	total := 0

	for _, filename := range params.Filenames {
		// Если общий бюджет уже исчерпан — остальные файлы пропускаем,
		// чтобы не раздувать сообщение инструмента.
		if total >= maxTotal {
			result = append(result, map[string]string{
				"filename": filename,
				"status":   "skipped",
				"message":  "лимит суммарного объёма чтения исчерпан, содержимое не получено",
			})
			continue
		}

		r := ops.ReadResult(filename)
		if r["status"] != "success" {
			result = append(result, r)
			continue
		}

		content := r["content"]
		if len(content) > maxFile {
			r["content"] = content[:maxFile] +
				fmt.Sprintf("\n\n[... содержание обрезано, файл %d байт, лимит %d байт ...]",
					len(content), maxFile)
			content = r["content"]
		}
		total += len(content)
		result = append(result, r)
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
