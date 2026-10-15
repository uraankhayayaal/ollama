package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"ai/shared/logging"
)

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

// parseFileMap нормализует объектную форму поля files {"путь": "контент"},
// которую модели иногда используют вместо массива объектов с filename/content.
// Принимает map, JSON-строку объекта или []byte.
func parseFileMap(raw any) []FileItem {
	switch v := raw.(type) {
	case map[string]string:
		return mapToFileItems(v)
	case []byte:
		return parseFileMap(string(v))
	case string:
		var m map[string]string
		if json.Unmarshal([]byte(v), &m) == nil && len(m) > 0 {
			return mapToFileItems(m)
		}
		return nil
	default:
		if b, err := json.Marshal(v); err == nil {
			var m map[string]string
			if json.Unmarshal(b, &m) == nil && len(m) > 0 {
				return mapToFileItems(m)
			}
		}
		return nil
	}
}

// repairEscapedQuotes убирает лишнее экранирование кавычек из содержимого
// файла: qwen при сериализации массива в JSON-строку пишет \" вместо ".
// В корректно сформированном коде последовательность \" в исходниках
// встречается редко (только внутри строковых литералов/регулярок), поэтому
// замена безопасна для сгенерированного кода.
func repairEscapedQuotes(s string) string {
	return strings.ReplaceAll(s, `\"`, `"`)
}

func mapToFileItems(m map[string]string) []FileItem {
	items := make([]FileItem, 0, len(m))
	for path, content := range m {
		items = append(items, FileItem{Filename: path, Content: content})
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
			// Модели (qwen и др.) при сериализации массива файлов в JSON-строку
			// иногда двойжды экранируют содержимое: после разбора аргументов в
			// коде остаются лишние символы \" вместо ". Убираем их только у
			// содержимого (не у имени файла), чтобы Go/TS-код не содержал
			// битых экранированных кавычек.
			it.Content = repairEscapedQuotes(it.Content)
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
		// Объектная форма {"путь": "контент"} — модели записывают файлы и так.
		if items := parseFileMap(args["files"]); len(items) > 0 {
			params.Files = items
		}
	}
	if len(params.Files) == 0 {
		// Одиночный объект {"filename": "путь", "content": "код"} без "files".
		if fn, ok := args["filename"]; ok {
			params.Files = []FileItem{{Filename: fmt.Sprint(fn), Content: fmt.Sprint(args["content"])}}
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
