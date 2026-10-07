package tools

import (
	"ai/forges"
	"ai/logging"
	"ai/sandbox"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// FileOps — разделяемое состояние файловых инструментов генератора кода
// (WriteFiles, ReadFiles, DeleteFiles, Run, List, AppendFile).
// Держит рабочую директорию (OutputDir) и политики записи, общие для всех
// выбранных агентом инструментов.
type FileOps struct {
	// OutputDir — единственная директория, внутри которой разрешена работа
	// инструментов (защита от выхода за пределы через ".." или абсолютные пути).
	OutputDir string
	// Project — ИМЯ проекта для RAG (payload project_name) и логов. Задаётся
	// оркестрацией явно: OutputDir у специалиста — worktree задачи
	// (temp/.wt-task-<проект>-<id>), и имя из его basename указывало бы на
	// несуществующий проект (пустая выдача CodeSearch, чанки в чужой индекс).
	// Пусто — фолбэк на basename OutputDir (консольные/простые проекты).
	Project string
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
	// WriteAllowlist — allowlist ЗАПИСИ (отдельно от Scope): разрешённые пути
	// для мутаций (Write/Append/Delete/Patch). Scope ограничивает и чтение,
	// а архитектору (этап 4.2 плана) нужен свободный доступ к чтению кода при
	// жёстко сжатой области записи — поэтому проверка раздельная. Пустой —
	// без ограничений (запись определяется Scope, как раньше).
	WriteAllowlist []string
	// writeAllowMatch — скомпилированный allowlist записи; nil/пусто — без
	// ограничений.
	writeAllowMatch *forges.ScopeMatcher
	// WriteReadmeOnly — true: записывающие инструменты (Write/Append/Delete)
	// разрешены ТОЛЬКО для файлов readme* в корне OutputDir. Чтение (ReadFiles,
	// List) не ограничивается. Ставится лидам, чтобы они могли вести план
	// проекта в readme, но не могли писать/удалять код.
	WriteReadmeOnly bool
	// written — счётчик записанных файлов (разделяется инструментами).
	written int
	// touched — относительные (slash) пути файлов, затронутых мутацией с
	// момента последнего вызова LspAutoFix. Используется Ф-2 (авто-
	// самоисправление): после раунда с записями раннер проверяет эти файлы
	// LspCheck и подмешивает модели скрытый промпт при ошибках. Ведётся под
	// touchedMu; сама мутация сериализуется пер-проектной блокировкой.
	touched   []string
	touchedMu sync.Mutex
}

// SetScope задаёт области работы для инструментов (нормализует записи через
// forges.CompileScope). Вызов с пустым/nil-слайсом снимает ограничения.
func (ops *FileOps) SetScope(scope []string) {
	ops.Scope = scope
	ops.scopeMatch = forges.CompileScope(scope)
}

// SetWriteAllowlist задаёт allowlist записи: разрешённые пути мутаций
// (Write/Append/Delete/Patch), независимых от Scope. Пустой/nil-слайс снимает
// ограничение (запись определяется Scope). Гард области (этап 4.2 плана) живёт
// в инструменте, а не только в промпте: модель, выйдшая за allowlist, получает
// ошибку, а не молчаливое отклонение.
func (ops *FileOps) SetWriteAllowlist(paths []string) {
	ops.WriteAllowlist = paths
	ops.writeAllowMatch = forges.CompileScope(paths)
}

// SetOutputDir переключает рабочую директорию инструментов на лету. Используется
// оркестрацией (Ф-3): у git-проектов специалист работает в отдельном worktree
// ветки задачи, а не в общей проектной копии. Раннер зовёт метод через интерфейс
// { SetOutputDir(string) } у агента (developer/devops/qaengineer встраивают
// *FileOps), поэтому сигнатура — часть публичного контракта.
// При смене директории сбрасывает touched: файлы из предыдущей ветки могут
// отсутствовать в новой, а переиндексация несуществующих файлов — ошибка.
func (ops *FileOps) SetOutputDir(dir string) {
	ops.OutputDir = dir
	ops.touchedMu.Lock()
	ops.touched = nil
	ops.touchedMu.Unlock()
}

// SetProjectName задаёт имя проекта для RAG-инструментов и переиндексации.
// Оркестрация зовёт метод через интерфейс { SetProjectName(string) } у агента
// вместе с SetOutputDir: у git-проектов специалист работает в worktree задачи,
// имя которого не совпадает с именем проекта в индексе. Пустое имя снимает
// явное значение (фолбэк — basename OutputDir).
func (ops *FileOps) SetProjectName(name string) {
	ops.Project = strings.TrimSpace(name)
}

// ProjectName — имя проекта для RAG: явное Project, иначе basename OutputDir
// (temp/<имя> → "<имя>"; пустой OutputDir — "").
func (ops *FileOps) ProjectName() string {
	if name := strings.TrimSpace(ops.Project); name != "" {
		return name
	}
	if ops == nil || ops.OutputDir == "" {
		return ""
	}
	return filepath.Base(filepath.Clean(ops.OutputDir))
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

// writeAllowed проверяет, разрешена ли ЗАПИСЬ файла (относительный
// slash-путь). В режиме «только readme» (WriteReadmeOnly) разрешены ТОЛЬКО
// файлы readme* в корне OutputDir независимо от Scope (чтение при этом
// продолжает ограничиваться Scope). Иначе — allowlist записи (WriteAllowlist,
// если задан) И область работы: обе проверки должны пройти.
func (ops *FileOps) writeAllowed(rel string) bool {
	if ops.WriteReadmeOnly {
		// Разрешён только файл readme* в корне проекта (без вложенных
		// каталогов), независимо от регистра (README.md, readme.md, ...).
		slug := filepath.ToSlash(filepath.Clean(rel))
		if slug == "." || slug == "" || strings.Contains(slug, "/") {
			return false
		}
		return strings.HasPrefix(strings.ToLower(slug), "readme")
	}
	if ops.writeAllowMatch != nil && !ops.writeAllowMatch.Empty() && !ops.writeAllowMatch.Allow(rel) {
		return false
	}
	return ops.allowed(rel)
}

// writeDenyMsg — точная причина отказа записи (для модели): при гарде allowlist
// (этап 4.2 плана) перечисляет разрешённые пути — модель видит границы и может
// исправиться, а не наступать на них вслепую. Без allowlist — прежнее сообщение
// про scope (обратная совместимость).
func (ops *FileOps) writeDenyMsg(name string) string {
	if ops.writeAllowMatch != nil && !ops.writeAllowMatch.Empty() {
		return fmt.Sprintf("файл %q вне allowlist записи (разрешено: %s)", name, strings.Join(ops.WriteAllowlist, ", "))
	}
	return fmt.Sprintf("файл %q вне области работы (scope: %v)", name, ops.Scope)
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
// Мутация сериализуется пер-проектной блокировкой (см. filelock.go): любые две
// записи в каталог проекта выполняются строго последовательно, исключая гонки
// между параллельными шагами волны плана.
func (ops *FileOps) Write(name, content string) error {
	return withProjectLock(ops.OutputDir, func() error {
		return ops.writeLocked(name, content)
	})
}

// writeLocked — тело Write без пер-проектной блокировки. Вызывается только из
// Write (под блокировкой) и тестами напрямую не предполагается.
func (ops *FileOps) writeLocked(name, content string) error {
	full, err := ops.ResolvePath(name)
	if err != nil {
		return err
	}
	if !ops.writeAllowed(ops.relPath(full)) {
		return fmt.Errorf("%s", ops.writeDenyMsg(name))
	}

	if ops.MaxFiles > 0 && ops.written >= ops.MaxFiles {
		return fmt.Errorf("превышен лимит записанных файлов (%d)", ops.MaxFiles)
	}
	if ops.NoOverwrite {
		if _, err := os.Stat(full); err == nil {
			return fmt.Errorf("файл уже существует (%s), перезапись запрещена (CODEGEN_NO_OVERWRITE=true)", name)
		}
	}

	if err := ensureParentDirs(full); err != nil {
		return err
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		return err
	}
	ops.written++
	ops.recordTouched(full)
	logging.Detailf("[WriteFiles] записано %q -> %q", name, full)
	return nil
}

// AppendTo прибавляет текст в конец существующего файла (без полной
// перезаписи). Используется инструментом AppendFile для точечных правок.
// Сериализуется той же пер-проектной блокировкой, что и Write.
func (ops *FileOps) AppendTo(name, content string) error {
	return withProjectLock(ops.OutputDir, func() error {
		return ops.appendToLocked(name, content)
	})
}

// appendToLocked — тело AppendTo без пер-проектной блокировки.
func (ops *FileOps) appendToLocked(name, content string) error {
	full, err := ops.ResolvePath(name)
	if err != nil {
		return err
	}
	if !ops.writeAllowed(ops.relPath(full)) {
		return fmt.Errorf("%s", ops.writeDenyMsg(name))
	}
	if err := ensureParentDirs(full); err != nil {
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
	ops.recordTouched(full)
	logging.Detailf("[AppendFile] дополнен %q -> %q", name, full)
	return nil
}

// ensureParentDirs создаёт родительские каталоги для будущей записи файла
// full и «лечит» файлы-заглушки, блокирующие эти каталоги.
//
// У модели нет инструмента создания директорий: чтобы «создать структуру»,
// она часто вызывает WriteFiles с путём каталога и пустым содержимым. На диске
// тогда появляется ПУСТОЙ ФАЙЛ с именем каталога (например server/internal),
// занимающий имя будущей папки. Любая последующая запись внутрь падает с
// ENOTDIR: «файлы в папке не меняются», шаги циклически повторяются. Эта
// функция устраняет такое состояние: пустая заглушка удаляется, вместо неё
// создаются каталоги. Непустой файл НЕ трогается — это реальный код, а не
// заглушка.
func ensureParentDirs(full string) error {
	dir := filepath.Dir(full)
	for {
		err := os.MkdirAll(dir, 0755)
		if err == nil {
			return nil
		}
		blocker := deepestExistingPath(dir)
		if blocker == "" {
			return fmt.Errorf("не удалось создать директорию %s: %v", dir, err)
		}
		st, serr := os.Lstat(blocker)
		if serr != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("не удалось создать директорию %s: путь %s занят", dir, blocker)
		}
		if st.Size() > 0 {
			return fmt.Errorf("не удалось создать директорию %s: путь %s занят непустым файлом", dir, blocker)
		}
		logging.Warnf("[tools] устранена пустая файловая заглушка %q, блокировавшая запись (директория была «создана» пустым файлом)", blocker)
		if err := os.Remove(blocker); err != nil {
			return fmt.Errorf("не удалось создать директорию %s: %v", dir, err)
		}
		// Продолжаем цикл: MkdirAll(dir) повторяется уже без блокера.
	}
}

// deepestExistingPath возвращает самый глубокий существующий путь на оси
// dir .. корня файловой системы. Если это регулярный файл на месте каталога —
// это и есть предполагаемый блокер (пустая заглушка).
func deepestExistingPath(dir string) string {
	p := dir
	for {
		if _, err := os.Lstat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return ""
		}
		p = parent
	}
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
		if os.IsNotExist(err) {
			return map[string]string{"filename": name, "status": "error", "message": "файл не существует в текущей ветке (возможно, он был в предыдущей ветке или ещё не создан)"}
		}
		return map[string]string{"filename": name, "status": "error", "message": err.Error()}
	}
	return map[string]string{"filename": name, "status": "success", "content": string(content)}
}

// Remove удаляет файл или папку и возвращает (результат-статус, ошибку).
// Сериализуется пер-проектной блокировкой: удаление не должно пересекаться
// с параллельной записью в тот же каталог проекта.
func (ops *FileOps) Remove(name string) (map[string]string, error) {
	var res map[string]string
	err := withProjectLock(ops.OutputDir, func() error {
		var rerr error
		res, rerr = ops.removeLocked(name)
		return rerr
	})
	return res, err
}

// removeLocked — тело Remove без пер-проектной блокировки.
func (ops *FileOps) removeLocked(name string) (map[string]string, error) {
	full, err := ops.ResolvePath(name)
	if err != nil {
		return nil, err
	}
	if !ops.writeAllowed(ops.relPath(full)) {
		return map[string]string{"path": name, "status": "error", "message": ops.writeDenyMsg(name)}, nil
	}
	if _, err := os.Stat(full); os.IsNotExist(err) {
		return map[string]string{"path": name, "status": "error", "message": "файл или папка не существует"}, nil
	}
	if err := os.RemoveAll(full); err != nil {
		return nil, err
	}
	ops.recordTouched(full)
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

// ReadParams соответствует JSON-параметрам инструмента ReadFiles
type ReadParams struct {
	Filenames []string `json:"filenames"`
	// Lines — опциональный интервал строк «хирургического окна»: "20-45",
	// "40" или "90-" (1-based, включительно). Применяется ко всем файлам
	// вызова. Пусто — файл читается целиком. Имеет приоритет над Offset.
	Lines string `json:"lines"`
	// Offset — смещение в байтах для дочитки тяжёлого файла. Модель передаёт
	// сюда next_offset из предыдущего ответа, когда файл не поместился в
	// порцию (см. FileContentResult.HasMore). Применяется ко всем файлам
	// вызова; игнорируется, если задан Lines.
	Offset int `json:"offset,omitempty"`
}

// FileContentResult — ответ ReadFiles по одному файлу. В отличие от прежней
// пары map[string]string несёт метаданные пагинации: HasMore сообщает, что
// файл не прочитан целиком, а NextOffset — с какого смещения продолжать.
type FileContentResult struct {
	Filename   string `json:"filename"`
	Content    string `json:"content,omitempty"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	HasMore    bool   `json:"has_more"`
	NextOffset int    `json:"next_offset,omitempty"`
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

const runMaxOutputDefault = 20_000

// Порция чтения на файл (readChunk) — сколько байт одного файла попадает в
// ответ за один вызов. Файл крупнее порции НЕ обрезается молча: в ответе
// выставляется has_more и next_offset, и модель дочитывает остаток явным
// вторым вызовом с offset. Это устраняет «слепую» обрезку, из-за которой
// агент видел только начало файла и зацикливался, не понимая причину.
const readChunkDefault = 16_384

// readChunk — размер порции чтения на файл (CODEGEN_READ_CHUNK).
func readChunk() int {
	n := readChunkDefault
	if v := os.Getenv("CODEGEN_READ_CHUNK"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 {
			n = x
		}
	}
	return n
}

// readMaxFilesDefault — сколько файлов харнес читает за один раунд. Модели
// свойственно заказывать 8-14 файлов за раз; в ответ попадали только первые,
// а модель снова перечитывала те же файлы (зацикливание). Лишние имена НЕ
// отбрасываются молча и НЕ роняют вызов: файлы свыше лимита получают
// отдельную запись со status "skipped" и подсказкой дочитать их следующим
// вызовом.
const readMaxFilesDefault = 3

func readMaxFiles() int {
	n := readMaxFilesDefault
	if v := os.Getenv("CODEGEN_READ_MAX_FILES"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 {
			n = x
		}
	}
	return n
}

// runOutputLimit — предел объёма одного потока вывода (stdout или stderr) в
// символах. Вывод длиннее лимита обрезается с маркером (см. truncateRunOutput),
// чтобы один шумный прогон (go test -v на весь модуль, npm с логами) не
// съедал контекст модели целиком. Экономия не зависит от послушания модели.
// readFileChunk читает порцию файла начиная со смещения offset. Проверки
// области работы те же, что в ReadResult (ResolvePath запрещает абсолютные
// пути и выход через "..", allowed ограничивает scope), поэтому инструмент
// не даёт агенту читать файлы за пределами проекта.
//
// Возвращает запись ответа: контент порции, признак неполного чтения и
// смещение для продолжения.
func (ops *FileOps) readFileChunk(name string, offset, chunk int) FileContentResult {
	fail := func(msg string) FileContentResult {
		return FileContentResult{Filename: name, Status: "error", Message: msg}
	}

	full, err := ops.ResolvePath(name)
	if err != nil {
		return fail(err.Error())
	}
	if !ops.allowed(ops.relPath(full)) {
		return fail("файл вне области работы (scope)")
	}

	file, err := os.Open(full)
	if err != nil {
		if os.IsNotExist(err) {
			return fail("файл не существует в текущей ветке ...")
		}
		return fail(err.Error())
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return fail(err.Error())
	}
	size := stat.Size()
	if offset < 0 {
		offset = 0
	}
	if int64(offset) >= size {
		return fail(fmt.Sprintf("offset %d выходит за пределы размера файла (%d байт)", offset, size))
	}

	// NUL-байт в порции — признак бинарника. Выкладывать такие байты в
	// контекст бессмысленно (и они ломают разбор), поэтому отвечаем отказом.
	n := size - int64(offset)
	if n > int64(chunk) {
		n = int64(chunk)
	}
	buf := make([]byte, n)
	if _, err := file.ReadAt(buf, int64(offset)); err != nil && err != io.EOF {
		return fail(fmt.Sprintf("ошибка чтения: %v", err))
	}
	if bytes.IndexByte(buf, 0) >= 0 {
		return fail(fmt.Sprintf("файл выглядит бинарным (%d байт), текстовое содержимое не возвращается", size))
	}

	// Смещение задаёт модель, поэтому offset может попасть внутрь UTF-8
	// символа, а граница порции — разрезать его. Подрезаем порцию с обоих
	// концов по границам символов: иначе json.Marshal заменит каждый
	// невалидный байт на U+FFFD (три байта вместо одного) и модель получит
	// искажённое содержимое.
	buf, cutHead := trimPartialRune(buf)
	if len(buf) == 0 {
		// Остаток короче одного символа — это оборванный хвост файла, а не
		// ошибка: дочитывать больше нечего, и has_more=false честнее отказа.
		if size-int64(offset) <= utf8.UTFMax {
			return FileContentResult{
				Filename: name, Status: "success",
				NextOffset: int(size), Message: "success",
			}
		}
		return fail(fmt.Sprintf("offset %d не попадает на границу UTF-8 символа (порция из %d байт не содержит валидного текста)", offset, n))
	}
	if cutHead {
		return fail(fmt.Sprintf("offset %d попадает внутрь UTF-8 символа — дочитать с него нельзя. Используй offset из next_offset предыдущего ответа", offset))
	}

	// has_more и next_offset считаем по фактически возвращённым байтам:
	// иначе страницы теряли бы обрезанный символ и не восстанавливали файл.
	next := offset + len(buf)
	if int(next) < int(size) {
		return FileContentResult{
			Filename:   name,
			Content:    string(buf),
			Status:     "success",
			HasMore:    true,
			NextOffset: next,
			Message: fmt.Sprintf(
				"ВНИМАНИЕ: файл больше порции чтения, показаны байты [%d, %d) из %d. Остальное НЕ потеряно: вызови ReadFiles снова с теми же filenames и offset=%d, чтобы дочитать продолжение.",
				offset, next, size, next),
		}
	}
	return FileContentResult{
		Filename:   name,
		Content:    string(buf),
		Status:     "success",
		NextOffset: next,
		Message:    "success",
	}
}

// trimPartialRune подрезает порцию по границам UTF-8 с обоих концов: с хвоста
// убирает символ, разрезанный границей порции, с начала — символ, разрезанный
// смещением, заданным моделью. Второй результат сообщает, что начало порции
// пришлось подрезать (offset попал внутрь символа) — такие байты вернуть
// нельзя, и вызывающий обязан сообщить об этом, а не потерять их молча.
func trimPartialRune(b []byte) ([]byte, bool) {
	for i := 0; i < utf8.UTFMax && len(b) > 0; i++ {
		r, size := utf8.DecodeLastRune(b)
		if r != utf8.RuneError || size > 1 {
			break
		}
		b = b[:len(b)-1]
	}
	cutHead := false
	for i := 0; i < utf8.UTFMax && len(b) > 0; i++ {
		r, size := utf8.DecodeRune(b)
		if r != utf8.RuneError || size > 1 {
			break
		}
		b = b[1:]
		cutHead = true
	}
	return b, cutHead
}

func runOutputLimit() int {
	n := runMaxOutputDefault
	if v := os.Getenv("CODEGEN_RUN_MAX_OUTPUT"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 {
			n = x
		}
	}
	return n
}

// truncateRunOutput обрезает поток вывода до лимита, добавляя маркер обрезки
// (как в ReadFiles). Возвращает итог и признак обрезки: вызывающий ставит
// флаг truncated в результат инструмента — модель видит, что хвост вывода
// отброшен, и может перезапустить команду с фильтром (grep/tail), а не
// искать ошибку в обрывке.
func truncateRunOutput(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	return s[:limit] + fmt.Sprintf("\n\n[... вывод обрезан, показаны первые %d символов из %d; повтори команду с фильтром (grep/tail), если нужен хвост ...]",
		limit, len(s)), true
}

// ReadFiles читает содержимое указанных файлов и возвращает их контент ИИ-агенту.
//
// Защита контекста работает по трём рубежам:
//   - не более readMaxFiles() файлов за вызов; остальные получают запись
//     "skipped" с подсказкой дочитать их следующим вызовом (мягкий лимит:
//     вызов не падает, модель не теряет запрос);
//   - не более readChunk() байт на файл; остаток НЕ обрезается молча, а
//     возвращается как has_more + next_offset для дочитки через offset;
//   - не более readLimit().total байт суммарно за вызов.
//
// Хирургическое окно Lines и смещение Offset взаимно исключают: при заданном
// Lines файл режется по строкам, offset игнорируется.
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
	// Порция на файл = min(CODEGEN_READ_CHUNK, CODEGEN_READ_MAX_FILE): оба
	// лимита остаются осмысленными, но явная пониженная порция всегда
	// выигрывает — именно она даёт предсказуемый размер сообщения раунда.
	chunk := readChunk()
	if maxFile > 0 && chunk > maxFile {
		chunk = maxFile
	}

	// Мягкий лимит на число файлов: читаем первые maxFiles, лишние имена
	// возвращаем как "skipped", чтобы модель знала, что их надо заказать
	// отдельным вызовом, и не считала задачу выполненной.
	names := params.Filenames
	var deferred []string
	if max := readMaxFiles(); len(names) > max {
		deferred = names[max:]
		names = names[:max]
	}

	// Разбор опционального интервала строк («хирургическое окно»): модель
	// может прочитать не весь файл, а только нужный диапазон — это режет
	// контекст и помогает находить точный фрагмент SEARCH/REPLACE.
	var lineStart, lineEnd int
	hasLines := false
	if strings.TrimSpace(params.Lines) != "" {
		if s, e, ok := parseRange(params.Lines); ok {
			lineStart, lineEnd, hasLines = s, e, true
		}
	}

	result := make([]FileContentResult, 0, len(names)+len(deferred))
	total := 0

	for _, filename := range names {
		// Если общий бюджет уже исчерпан — остальные файлы пропускаем,
		// чтобы не раздувать сообщение инструмента.
		if total >= maxTotal {
			result = append(result, FileContentResult{
				Filename: filename,
				Status:   "skipped",
				Message:  "лимит суммарного объёма чтения исчерпан, содержимое не получено",
			})
			continue
		}

		var res FileContentResult
		if hasLines {
			// Окно по строкам читает файл целиком и режет его: offset при этом
			// не применяется (взаимоисключающие способы дочитать файл).
			res = ops.readFileLines(filename, params.Lines, lineStart, lineEnd, maxFile)
		} else {
			res = ops.readFileChunk(filename, params.Offset, chunk)
		}
		total += len(res.Content)
		result = append(result, res)
	}

	for _, filename := range deferred {
		result = append(result, FileContentResult{
			Filename: filename,
			Status:   "skipped",
			Message: fmt.Sprintf("не прочитан: за один вызов харнес возвращает не более %d файлов. Вызови ReadFiles снова с этим файлом (и offset, если нужен хвост)",
				readMaxFiles()),
		})
	}

	resultJSON, _ := json.Marshal(result)
	return resultJSON, nil
}

// readFileLines реализует «хирургическое окно»: читает файл и оставляет
// только строки [lineStart, lineEnd]. Обрезка по строкам, а не по байтам,
// поэтому граница не разрезает UTF-8 символ. Если файл больше maxFile, он
// предварительно урезается до maxFile с явной пометкой о потере хвоста
// (окно строк за пределами прочитанного недоступно).
func (ops *FileOps) readFileLines(name, spec string, lineStart, lineEnd, maxFile int) FileContentResult {
	full, err := ops.ResolvePath(name)
	if err != nil {
		return FileContentResult{Filename: name, Status: "error", Message: err.Error()}
	}
	if !ops.allowed(ops.relPath(full)) {
		return FileContentResult{Filename: name, Status: "error", Message: "файл вне области работы (scope)"}
	}
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return FileContentResult{Filename: name, Status: "error", Message: "файл не существует в текущей ветке ..."}
		}
		return FileContentResult{Filename: name, Status: "error", Message: err.Error()}
	}

	content := string(data)
	truncated := false
	if len(content) > maxFile {
		content = content[:maxFile]
		truncated = true
	}

	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	end := lineEnd
	if end == 0 || end > len(lines) {
		end = len(lines)
	}
	var sliced []string
	if lineStart <= end {
		sliced = make([]string, 0, end-lineStart+1)
		for i := lineStart; i <= end; i++ {
			sliced = append(sliced, lines[i-1])
		}
	}
	if len(sliced) == 0 {
		return FileContentResult{
			Filename: name,
			Status:   "error",
			Message:  fmt.Sprintf("интервал строк %q выходит за пределы файла (%d строк)", spec, len(lines)),
		}
	}

	msg := "success"
	if truncated {
		msg = fmt.Sprintf(
			"ВНИМАНИЕ: файл больше порции чтения (%d байт прочитано), интервал строк применим только к прочитанной части. Остальное НЕ потеряно: вызови ReadFiles без 'lines', чтобы дочитать файл.",
			maxFile)
	}
	return FileContentResult{
		Filename: name,
		Content:  "[строки " + strconv.Itoa(lineStart) + "-" + strconv.Itoa(end) + " файла]\n" + strings.Join(sliced, "\n"),
		Status:   "success",
		Message:  msg,
	}
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
// runTimeout возвращает таймаут исполнения команды инструмента Run.
// Значение берётся из CODEGEN_RUN_TIMEOUT (например "90s"), по умолчанию 60s.
// Таймаут важен: агент может запустить долгоживущую команду (npm run dev,
// сервер, интерактивная утилита) — без ограничения шаг завис бы навсегда,
// а у модели не было бы информации, что команда не завершается.
func runTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("CODEGEN_RUN_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 60 * time.Second
}

// runCommand запускает команду и возвращает её результат выполнения.
// Команда ограничена по времени runTimeout: если она не завершается (например,
// агент запустил дев-сервер), процесс и его группа убиваются, а в результате
// появляется понятное сообщение о таймауте — шаг продолжается, а не виснет.
//
// Исполнитель выбирается в три шага (Ф-4 + Этап 1):
//  1. активный сессионный контейнер (sandbox.Lookup): проектная сессия
//     поднимается сервером до старта runner'а, и тогда команда идёт в него;
//  2. при CODEGEN_SANDBOX=session без активной сессии — строгая ошибка, НЕ
//     тихий запуск на хосте: изоляция либо есть, либо об этом сказано прямо;
//  3. иначе прежние режимы: ephemeral docker run (container) или хост.
//
// project — имя проекта (ops.ProjectName), когда оно известно: точное имя
// важнее совпадения по пути. Пустое имя (ЛСП-чекер) работает как раньше.
func runCommand(command, workdir, project string) (map[string]string, error) {
	if ws := sandbox.Lookup(project, workdir); ws != nil {
		return runCommandWorkspace(command, workdir, ws)
	}
	cfg := loadSandboxConfig()
	if cfg.Mode == SandboxModeSession {
		// Сессионный режим объявлен, но контейнера нет: сервер не поднял
		// сессию (старый процесс, ошибка старта). Молчаливый хост здесь
		// означал бы «песочница есть» там, где её нет.
		return map[string]string{
			"command":    command,
			"workdir":    workdir,
			"exit_error": "песочница не активна",
			"status":     "error",
			"sandbox":    string(SandboxModeSession),
			"message": "CODEGEN_SANDBOX=session задан, но сессионный контейнер проекта не активен: " +
				"команда НЕ выполнена (без тихого запуска на хосте). " +
				"Перезапусти сессию проекта или выполни задачу в новой сессии сервера.",
		}, nil
	}
	return runCommandSandbox(command, workdir, cfg)
}

// runCommandWorkspace — выполнение команды через активный сессионный
// контейнер (sandbox.Workspace). Контракт результата неотличим от
// runCommandSandbox: те же поля, санитайзинг, подсказки и обрезка вывода.
func runCommandWorkspace(command, workdir string, ws sandbox.Workspace) (map[string]string, error) {
	// Отбраковка разрушительных команд ДО запуска — общий гейт со всеми
	// путями (в контейнере rm -rf уничтожит файлы проекта на хосте: том
	// смонтирован без копии).
	if reason, unsafe := destructiveCommandReason(command); unsafe {
		return map[string]string{
			"command":    command,
			"workdir":    workdir,
			"exit_error": "заблокировано политикой безопасности",
			"status":     "error",
			"message":    reason,
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), runTimeout())
	defer cancel()

	res, err := ws.Run(ctx, workdir, command)
	if err != nil {
		// Инфраструктурный сбой (нет демона, каталог вне монтирования) —
		// как и падение cmd.Start в host-пути: ошибка вызывающего, а не
		// результат команды.
		return nil, err
	}

	result := map[string]string{
		"command":    command,
		"workdir":    workdir,
		"exit_error": "",
		"stdout":     SanitizeToolOutput(res.Stdout),
		"stderr":     SanitizeToolOutput(res.Stderr),
		// Явно сообщаем модели, где выполнялась команда.
		"sandbox": string(SandboxModeSession),
	}
	if nws, ok := ws.(interface{ SandboxNetwork() string }); ok && nws.SandboxNetwork() == "none" {
		result["hint"] = sandboxNetworkHelp()
	}
	finishRunResult(result, command, res.TimedOut, res.ExitErr)
	return result, nil
}

// finishRunResult — общий хвост упаковки результата: статус, текст ошибки,
// подсказки (недостающий тулчейн) и обрезка вывода до runOutputLimit.
// timedOut=true приоритетнее runErr: таймаут — это своя история для модели
// («команда виснет», а не «упала»), и ExitErr там — ctx.Err().
func finishRunResult(result map[string]string, command string, timedOut bool, runErr error) {
	if timedOut {
		result["exit_error"] = "signal: killed (timeout)"
		result["status"] = "error"
		result["message"] = fmt.Sprintf("команда не завершилась за %s и была остановлена (timeout)", runTimeout())
	} else if runErr != nil {
		result["exit_error"] = runErr.Error()
		result["status"] = "error"
		if h := missingToolHint(command, result["stdout"]+"\n"+result["stderr"], runErr); h != "" {
			result["hint"] = h
		}
	} else {
		result["status"] = "success"
	}
	// Обрезка потоков вывода до лимита (см. runOutputLimit): маркер в тексте
	// и флаг truncated — модель понимает, что хвост отброшен. Статус и код
	// выхода обрезка не трогает: гейты остаются честными.
	limit := runOutputLimit()
	if out, trunc := truncateRunOutput(result["stdout"], limit); trunc {
		result["stdout"] = out
		result["stdout_truncated"] = "true"
	}
	if out, trunc := truncateRunOutput(result["stderr"], limit); trunc {
		result["stderr"] = out
		result["stderr_truncated"] = "true"
	}
}

// runCommandSandbox — реализация запуска с явной конфигурацией песочницы.
// LocalCommand подменяется в hermetic-тестах, поэтому тесты не запускают
// shell, docker и тем более проекты агентов.
func runCommandSandbox(command, workdir string, sb sandboxConfig) (map[string]string, error) {
	// Отбраковка разрушительных команд ДО любого запуска: в контейнере
	// `rm -rf /workspace` уничтожит файлы проекта на хосте (том смонтирован
	// без копии), а на хосте тем более.
	if reason, unsafe := destructiveCommandReason(command); unsafe {
		return map[string]string{
			"command":    command,
			"workdir":    workdir,
			"exit_error": "заблокировано политикой безопасности",
			"status":     "error",
			"message":    reason,
		}, nil
	}

	mode := sb.Mode
	if mode == SandboxModeUnset {
		// Конфигурация не определилась (сбой env-разбора) — сознательно
		// возвращаемся к хосту, но помечаем это в результате.
		mode = SandboxModeLocal
	}
	sandboxed := mode == SandboxModeContainer
	if sandboxed {
		// Образ/каталог проверяем ДО запуска: нечитаемый путь или неизвестный
		// стек — это ошибка конфигурации, а не падение команды.
		if _, err := sandboxSpecFor(command, workdir, sb); err != nil {
			return map[string]string{
				"command":    command,
				"workdir":    workdir,
				"exit_error": "песочница не настроена",
				"status":     "error",
				"sandbox":    "container",
				"message":    err.Error(),
			}, nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), runTimeout())
	defer cancel()

	var cmd *exec.Cmd
	if sandboxed {
		path, args, err := sandboxCommand(command, workdir, sb)
		if err != nil {
			return nil, err
		}
		cmd = exec.CommandContext(ctx, path, args...)
		// Docker-демон живёт по сокету, а переменные вроде DOCKER_HOST нужно
		// передать дочернему процессу иначе, чем основному окружению агента.
		cmd.Env = dockerEnv()
	} else {
		var err error
		if sb.LocalCommand != nil {
			cmd, err = sb.LocalCommand(command, workdir)
		} else {
			cmd = exec.CommandContext(ctx, "sh", "-c", command)
		}
		if err != nil {
			return nil, err
		}
		cmd.Env = os.Environ()
	}
	cmd.Dir = workdir
	// День: команда может порождать детей (серверы, npm). Отдельная группа
	// процессов позволяет при таймауте убить их всех, а не только шелл.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Ждём завершения в отдельной горутине: при таймауте необходимо убить
	// ВСЮ группу процессов — выживший потомок (go run → бинарь, npm → сервер,
	// sleep) держит каналы stdout/stderr открытыми, и прямой cmd.Wait()
	// блокируется навсегда, игнорируя таймаут.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var runErr error
	timedOut := false
	select {
	case <-ctx.Done():
		// Превышен таймаут: процесс (и его потомки) ещё живы — принудительно
		// завершаем всю группу, чтобы закрылись унаследованные каналы вывода
		// и cmd.Wait() (в done) разблокировался.
		if cmd.Process != nil && cmd.Process.Pid > 0 {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		<-done
		timedOut = true
		runErr = ctx.Err()
	case werr := <-done:
		runErr = werr
	}

	// Вывод команды нормализуется ДО упаковки в JSON: цвета терминала (ANSI),
	// управляющие и NUL-байты в строке ломают JSON-пакет и разбор аргументов
	// следующих вызовов модели («invalid character ']' looking for beginning of
	// value»), из-за чего падал сам харнес.
	result := map[string]string{
		"command":    command,
		"workdir":    workdir,
		"exit_error": "",
		"stdout":     SanitizeToolOutput(stdout.String()),
		"stderr":     SanitizeToolOutput(stderr.String()),
		// Явно сообщаем модели, где выполнялась команда. Без этого «песочница»
		// существует только в коде, а агент (и человек в логе) считает все
		// запуски изолированными — включая те, что ушли на хост по фолбэку.
		"sandbox": string(mode),
	}
	if !sandboxed {
		result["sandbox_note"] = sandboxFallbackReason
	} else if sb.Network == "none" {
		result["hint"] = sandboxNetworkHelp()
	}
	finishRunResult(result, command, timedOut, runErr)
	return result, nil
}

// missingToolHint распознаёт «инструмент/модуль недоступен в окружении хоста»
// (sh: cargo: command not found, exit 127, ModuleNotFoundError) и возвращает
// подсказку модели. Без неё агент начинает перебирать окружение — which/find по
// всей файловой системе (минуты до таймаута), pip install, повторные запуски
// той же команды — и сжигает раунды, хотя тулчейн проекта живёт в контейнере.
func missingToolHint(command, output string, runErr error) string {
	lower := strings.ToLower(output)
	missing := strings.Contains(lower, "command not found") ||
		strings.Contains(lower, "modulenotfounderror") ||
		strings.Contains(lower, "no module named") ||
		strings.Contains(lower, "not recognized as an internal or external command")
	if !missing {
		// Любой носитель кода выхода: *exec.ExitError (хост/эфемерный
		// контейнер) и sandbox.exitStatusError (сессионный контейнер) —
		// 127 значит «команда не найдена» в обоих.
		if ec, ok := runErr.(interface{ ExitCode() int }); ok && ec.ExitCode() == 127 {
			missing = true
		}
	}
	if !missing {
		return ""
	}
	tool := strings.TrimSpace(command)
	if f := strings.Fields(command); len(f) > 0 {
		tool = f[0]
	}
	return fmt.Sprintf("Инструмент/модуль для %q отсутствует в окружении хоста — НЕ ищи его (which/find), НЕ устанавливай (pip/brew/apt) и НЕ повторяй команду. "+
		"Сборку и тесты проекта выполняй в его собственном окружении: docker compose run --rm <сервис> <команда> (сервис и команды см. в docker-compose.yml и README проекта). "+
		"Если у проекта есть корневой Makefile — используй его инфра-цели: 'make infra.<цель>' = 'docker compose run -it --rm <сервис> <исходная команда>' (см. Makefile, цель help). "+
		"Если контейнерного стека нет — проверь результат статически (чтение файлов, LSP-диагностика) и честно укажи в отчёте, что проверка на хосте недоступна.", tool)
}

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

	// Защита от зависания: долгоживущие серверные команды (go run server/main.go,
	// npm run dev и т.п.) не завершаются сами — вместо ожидания таймаута сразу
	// возвращаем ошибку с инструкцией верифицировать через сборку и автотесты.
	if hint, ok := longRunningHint(params.Command); ok {
		resultJSON, _ := json.Marshal(map[string]string{
			"status":  "error",
			"message": hint,
		})
		return resultJSON, nil
	}

	result, err := runCommand(params.Command, ops.OutputDir, ops.ProjectName())
	if err != nil {
		return nil, err
	}

	resultJSON, _ := json.Marshal(result)
	return resultJSON, nil
}

// longRunningHint определяет, похожа ли команда на запуск долгоживущего
// процесса (сервер/дев-режим), и возвращает пояснение для модели. Длинные
// команды «запуска приложения» не завершаются до таймаута, и агент виснет на
// 60+ секунд в ожидании вывода; для проверки правильности кода достаточно
// сборки и автотестов.
func longRunningHint(command string) (string, bool) {
	cmd := strings.TrimSpace(command)
	lower := strings.ToLower(cmd)
	// Команды для фонового запуска/быстрой проверки (с sleep-овым прогревом и
	// последующим curl) не блокируют шаг — пропускаем хинт.
	if strings.Contains(lower, "&") || strings.Contains(lower, "sleep") && strings.Contains(lower, "curl") {
		return "", false
	}
	// Запуск приложения/сервера через go run (server/main.go, ./, cmd/server
	// и т.п.) не завершается — блокируем, оставляя go run только для быстрых
	// утилит/скриптов, которые не ведут себя как серверы.
	if idx := strings.Index(lower, "go run"); idx >= 0 {
		rest := strings.TrimSpace(lower[idx+len("go run"):])
		target := rest
		if i := strings.IndexAny(rest, " \t\n"); i >= 0 {
			target = rest[:i]
		}
		blocked := target == "." || target == "./" || target == "./main" || target == "./app"
		for _, marker := range []string{"server", "cmd/", "main.go", "/app", "app/"} {
			if strings.Contains(target, marker) {
				blocked = true
				break
			}
		}
		if blocked {
			return "Команда похожа на запуск приложения/сервера через go run (например, go run server/main.go) — процесс не завершается сам, и шаг завис бы на таймауте. Не запускай приложение: вместо этого проверь код через сборку и автотесты (go build ./... и go test ./...), а поднятие сервисов опиши в README.", true
		}
		return "", false
	}
	fragments := []string{
		"npm run dev",
		"npm start",
		"npm run start",
		"npm run serve",
		"yarn start",
		"yarn run start",
		"yarn dev",
		"yarn run dev",
		"pnpm dev",
		"pnpm start",
		"bun dev",
		"bun run dev",
		"bun start",
		"npx dev",
		"npx run dev",
		"vite dev",
		"next dev",
		"nuxt dev",
		"ng serve",
		"quasar dev",
		"uvicorn",
		"gunicorn",
		"flask run",
		"fastapi dev",
		"python app",
		"python3 app",
		"python main",
		"node server",
		"node index",
		"deno run",
		"docker compose up",
		"docker-compose up",
		"docker-compose -f up",
		"kubectl port-forward",
		"helm install",
		"dotnet run",
		"java -jar",
		"rails server",
		"python manage.py runserver",
		"python manage.py migrate",
		"celery worker",
	}
	for _, f := range fragments {
		if strings.Contains(lower, f) {
			return "Команда похожа на запуск долгоживущего фронтенд-процесса/сервера в дев-режиме (npm/pnpm/yarn run dev|start и аналоги), который не завершается сам — шаг завис бы на таймауте. Не запускай приложение: для фронтенда проверь через сборку и тесты (npm run build / pnpm build / yarn build и npm test / pnpm test / yarn test по аналогии), для бэкенда — go build ./... и go test ./....", true
		}
	}
	return "", false
}
