package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

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
		// Окно не попало в прочитанное содержимое. Это частый триггер петли
		// «читаю одно и то же окно»: модель повторяет вызов с теми же 'lines',
		// надеясь на другой результат. Отвечаем не отказом, а инструкцией —
		// что реально сделать вместо повтора (см. loop.go: loopBreakMessage).
		if truncated {
			// Файл больше лимита чтения: до строк просто «не дочитали», а не
			// «их нет». Без этого различия модель решила бы, что файла короче.
			return FileContentResult{
				Filename: name,
				Status:   "error",
				Message: fmt.Sprintf(
					"интервал строк %q за пределами прочитанной части: из-за лимита чтения (%d байт) доступны только первые %d строк файла. НЕ повторяй этот вызов с теми же 'lines': прочитай файл без 'lines', чтобы дочитать его целиком (при необходимости через offset).",
					spec, maxFile, len(lines)),
			}
		}
		return FileContentResult{
			Filename: name,
			Status:   "error",
			Message: fmt.Sprintf(
				"интервал строк %q выходит за пределы файла — файл содержит %d строк. НЕ повторяй вызов с теми же 'lines': прочитай файл целиком (без 'lines') или возьми существующий диапазон (1-%d).",
				spec, len(lines), len(lines)),
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
