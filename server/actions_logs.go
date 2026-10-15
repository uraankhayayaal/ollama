package server

import (
	"context"
	"fmt"
	"strings"

	applogs "ai/application/logs"
	"ai/shared/logging"
	"ai/tools"
)

// Мост ReadProjectLogs (Ф-5 «разобрать логи»): даёт ассистенту СЫРЫЕ строки
// логов проекта.
//
// Зачем он нужен, раз у ассистента есть файловые инструменты: логи лежат в
// logs/<проект>.log, то есть ВНЕ корня рабочей папки проекта, а
// tools/fileops.go:ResolvePath запрещает ассистенту выход за него. Поэтому
// прочитать лог штатным Read нельзя — нужен серверный мост, пропускающий
// ровно те файлы, которые показывает Logboard (тот же сборщик
// collectProjectLogs), и ничего больше.
//
// Инструмент не заменяет дайджест: дайджест отдаётся в промпте (см.
// domain/log.BuildLogDigest), а инструмент нужен для докапывания — увидеть
// реальные строки конкретной находки, её окружение и соседние записи.

// actionReadLogs — имя инструмента-моста.
const actionReadLogs = "ReadProjectLogs"

const (
	// logToolDefaultLines — сколько строк отдаём по умолчанию.
	logToolDefaultLines = 200
	// logToolMaxLines — потолок по строкам на файл. Модель всё равно не
	// перечитает тысячи строк: полезнее короткий хвост с фильтром.
	logToolMaxLines = 1000
	// logToolMaxFiles — сколько файлов за один вызов. Иначе модель одной
	// пачкой вытащит весь лог-каталог и зальёт контекст.
	logToolMaxFiles = 3
	// logToolMaxBytes — потолок содержимого одного файла в ответе.
	logToolMaxBytes = 24 << 10
)

// logToolArgs — разобранные аргументы ReadProjectLogs.
type logToolArgs struct {
	file  string
	grep  string
	lines int
}

// logToolFile — один файл в ответе инструмента.
type logToolFile struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	Lines    int    `json:"lines"`
	// Matched — сколько строк содержали grep (0, если фильтр не задан).
	Matched int `json:"matched"`
	// Total — строк в файле всего, до отсечения по limit.
	Total int `json:"total"`
	// Truncated — содержимое в ответе обрезано (по строкам или по байтам).
	Truncated bool   `json:"truncated"`
	Content   string `json:"content,omitempty"`
	Note      string `json:"note,omitempty"`
}

// newLogReadTool возвращает мост-инструмент чтения логов проекта.
func newLogReadTool(sess *Session) tools.Tool {
	return &actionTool{
		name: actionReadLogs, b: sess,
		description: "Прочитать СЫРЫЕ строки логов проекта (те же файлы, что показывает панель логов: logs/<проект>.log и logs/ внутри папки проекта). " +
			"Логи лежат вне рабочей папки проекта, поэтому обычный Read их не видит — только этот инструмент. " +
			"Используй для докапывания находки из дайджеста: увидеть реальные строки, найти все вхождения ошибки (grep) или соседние записи контекста. " +
			"Возвращает последние строки файла (по умолчанию 200, максимум 1000), а не весь лог.",
		args: map[string]any{
			"file": map[string]any{
				"type": "string",
				"description": "Имя файла лога (например calc.log). Пусто или \"all\" — самые свежие файлы проекта. " +
					"Список доступных файлов смотри в поле files ответа.",
			},
			"grep": map[string]any{
				"type":        "string",
				"description": "Фильтр: оставить только строки, содержащие эту подстроку (без регулярных выражений). Для поиска причины находки.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Сколько последних строк вернуть (по умолчанию %d, максимум %d)", logToolDefaultLines, logToolMaxLines),
			},
		},
		run: func(_ context.Context, args map[string]any) (map[string]any, error) {
			return sess.readProjectLogs(logToolArgs{
				file:  actionArg(args, "file"),
				grep:  actionArg(args, "grep"),
				lines: actionArgInt(args, "limit"),
			})
		},
	}
}

// readProjectLogs — реализация моста. Возвращает список доступных файлов и
// содержимое выбранных.
func (sess *Session) readProjectLogs(a logToolArgs) (map[string]any, error) {
	inf, err := sess.srv.reg.Get(sess.project)
	if err != nil {
		return nil, fmt.Errorf("проект %s не найден", sess.project)
	}
	globalDir := sess.srv.logsDir()
	own := logging.ProjectLogName(sess.project) + ".log"
	files, _ := collectProjectLogs(sess.srv.logDirs(inf.Root), globalDir, own)

	// Список всегда полный: модель должна видеть имена файлов, даже если
	// запросила несуществующий (иначе она станет угадывать имена вслепую).
	list := make([]map[string]any, 0, len(files))
	for _, f := range files {
		list = append(list, map[string]any{
			"name": f.Name, "size": f.Size, "modified": f.Modified,
		})
	}
	out := map[string]any{"files": list, "dir": strings.Join(sess.srv.logDirs(inf.Root), " · ")}
	if len(files) == 0 {
		out["note"] = "у проекта нет файлов логов"
		return out, nil
	}

	sel := pickLogFiles(files, a.file, own)
	if len(sel) == 0 {
		out["note"] = fmt.Sprintf("файл %q не найден; доступные файлы перечислены выше", a.file)
		return out, nil
	}
	lines := a.lines
	if lines <= 0 {
		lines = logToolDefaultLines
	}
	if lines > logToolMaxLines {
		lines = logToolMaxLines
	}

	outFiles := make([]logToolFile, 0, len(sel))
	for _, f := range sel {
		lf, err := readLogSlice(f.Path, f.Size, a.grep, lines)
		if err != nil {
			lf = logToolFile{Name: f.Name, Note: "не удалось прочитать: " + err.Error()}
		}
		outFiles = append(outFiles, lf)
	}
	out["content"] = outFiles
	return out, nil
}

// pickLogFiles выбирает файлы для чтения: по имени, либо (пусто/all) самые
// свежие. Сначала — файл проекта: он почти всегда и есть нужный.
func pickLogFiles(files []logFileEntry, want, own string) []logFileEntry {
	return applogs.PickFiles(files, want, own, logToolMaxFiles)
}

// readLogSlice читает хвост файла (последние limit строк), при grep — только
// совпавшие строки. Логика — в application/logs.ReadLogSlice.
func readLogSlice(path string, size int64, grep string, limit int) (logToolFile, error) {
	s, err := applogs.ReadLogSlice(path, grep, limit, logMaxBytes, logToolMaxBytes)
	if err != nil {
		return logToolFile{}, err
	}
	return logToolFile{
		Name:      s.Name,
		Size:      s.Size,
		Modified:  s.Modified,
		Lines:     s.Lines,
		Matched:   s.Matched,
		Total:     s.Total,
		Truncated: s.Truncated,
		Content:   s.Content,
		Note:      s.Note,
	}, nil
}
