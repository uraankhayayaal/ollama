// Logboard (REST): чтение лог-файлов проекта для панели «Логи».
//
// У каждого проекта свой лог: пакет logging пишет его в logs/<проект>.log
// (каталог переопределяется LOG_DIR). Общий каталог сервера содержит файлы
// ВСЕХ проектов и служебный server.log, поэтому оттуда отдаётся только файл
// запрашиваемого проекта — чужие логи в панель не попадают. Дополнительно
// сканируется подкаталог logs корня рабочей папки проекта: там лежат логи
// самого приложения, и они отдаются все.
//
// Эндпоинт GET /api/projects/{id}/logs возвращает метаданные + содержимое и
// имя выделенного файла для показа в Logboard. DELETE с тем же путём обрезает
// эти файлы (кнопка «Очистить логи»).

package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ai/logging"
)

// logMaxBytes — максимальный объём содержимого одного файла, отдаваемый
// Logboard (хвост лога — самые свежие записи). 0 — без ограничения.
const logMaxBytes = 1 << 20 // 1 МБ

// logFileEntry — один *.log файл: метаданные + содержимое.
type logFileEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path,omitempty"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	Content  string `json:"content"`
	// cut — содержимое обрезано до logMaxBytes. В JSON не отдаётся: итог
	// виден в поле Truncated ответа.
	cut bool `json:"-"`
}

// logsResponse — ответ GET /api/projects/{id}/logs.
type logsResponse struct {
	Dir       string         `json:"dir"`
	Files     []logFileEntry `json:"files"`
	Selected  string         `json:"selected"`
	Truncated bool           `json:"truncated"`
}

// logDirs — каталоги, где могут лежать лог-файлы проекта: общий каталог
// логов сервера + подкаталог logs внутри рабочей папки проекта (если есть).
// Единая точка для чтения и очистки: обе операции обязаны видеть один и тот
// же набор файлов, иначе панель показывала бы то, что очистка не тронула.
func (s *Server) logDirs(root string) []string {
	globalDir := s.logsDir()
	dirs := []string{globalDir}
	if root != "" {
		if local := filepath.Join(root, "logs"); fileExists(local) {
			dirs = append(dirs, local)
		}
	}
	return dirs
}

// collectProjectLogs собирает лог-файлы проекта по dirs с дедупликацией по
// имени. Факт обрезки хвоста — в out.cut.
//
// В общем каталоге сервера лежат логи ВСЕХ проектов и самого процесса
// (server.log) — оттуда берётся только файл этого проекта, иначе панель
// показывала бы чужие логи, а очистка стирала бы их. В локальном logs/
// проекта берутся все.
func collectProjectLogs(dirs []string, globalDir, own string) (out []logFileEntry, cut bool) {
	seen := map[string]bool{}
	for _, dir := range dirs {
		entries := collectLogFiles(dir)
		if dir == globalDir {
			entries = filterLogEntries(entries, own)
		}
		for _, e := range entries {
			if seen[e.Name] {
				continue
			}
			seen[e.Name] = true
			cut = cut || e.cut
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, cut
}

// handleGetLogs возвращает логи проекта: файлы каталога logs/ (глобального
// и внутри каталога проекта) с содержимым. Подписывает проект на
// real-time обновления (строчки шлются по WS type="log").
func (s *Server) handleGetLogs(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	inf, err := s.reg.Get(project)
	if err != nil {
		writeErr(w, http.StatusNotFound, "проект не найден")
		return
	}

	globalDir := s.logsDir()
	dirs := s.logDirs(inf.Root)

	// У каждого проекта свой файл logs/<проект>.log. Создаём его сразу, чтобы
	// панель не показывала «файлов нет» до первого запуска задачи и чтобы
	// брокер мог подписаться на хвост.
	own := logging.ProjectLogName(project) + ".log"
	logging.Attach(project)

	// Подписываем проект на real-time (WS type="log"). Подписка идемпотентна и
	// нужна даже когда файлов ещё нет: брокер периодически пересканирует
	// каталоги и подхватит лог, созданный позже (первый запуск задачи).
	s.logBroker.Subscribe(project, dirs, listLogFiles(dirs))

	out := logsResponse{Dir: strings.Join(dirs, " · ")}
	out.Files, out.Truncated = collectProjectLogs(dirs, globalDir, own)

	// Выделяем файл проекта: logs/<проект>.log, иначе самый свежий.
	if out.Selected == "" {
		for _, f := range out.Files {
			if f.Name == own {
				out.Selected = own
				break
			}
		}
	}
	if out.Selected == "" && len(out.Files) > 0 {
		latest := out.Files[0]
		for _, f := range out.Files[1:] {
			if f.Modified >= latest.Modified {
				latest = f
			}
		}
		out.Selected = latest.Name
	}

	writeJSON(w, http.StatusOK, out)
}

// logsDir — каталог лог-файлов сервера (LOG_DIR или "logs" относительно cwd).
func (s *Server) logsDir() string {
	if d := strings.TrimSpace(os.Getenv("LOG_DIR")); d != "" {
		return d
	}
	return "logs"
}

// clearMarker — заголовок, который очистка оставляет вместо прежнего
// содержимого. Пустой файл не отличить от «логов ещё никогда не было», а
// содержимое лога — это улики агентского прогона, поэтому момент стирания
// должен остаться в самом файле.
const clearMarker = "=== Логи очищены %s ==="

// handleDeleteLogs обрезает лог-файлы проекта: DELETE /api/projects/{id}/logs.
// Обрезаются ровно те файлы, которые отдаёт GET (см. collectProjectLogs) —
// иначе очистка либо не тронула бы то, что панель показывает, либо стёрла бы
// чужие логи из общего каталога.
//
// Перезаписывание открытых дескрипторов безопасно: логгеры пишут с
// O_APPEND, поэтому следующая запись уходит в конец уже пустого файла. Брокер
// ловит усечение сам (размер меньше позиции чтения) и переоткрывает файл с
// начала — отдельного сброса его состояния не требуется.
func (s *Server) handleDeleteLogs(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	inf, err := s.reg.Get(project)
	if err != nil {
		writeErr(w, http.StatusNotFound, "проект не найден")
		return
	}

	own := logging.ProjectLogName(project) + ".log"
	logging.Attach(project)
	dirs := s.logDirs(inf.Root)
	files, _ := collectProjectLogs(dirs, s.logsDir(), own)

	marker := fmt.Sprintf(clearMarker, time.Now().Format("2006-01-02 15:04:05")) + "\n"
	cleared := make([]string, 0, len(files))
	for _, f := range files {
		if err := os.WriteFile(f.Path, []byte(marker), 0o644); err != nil {
			writeErr(w, http.StatusInternalServerError, "не удалось очистить лог "+f.Name+": "+err.Error())
			return
		}
		cleared = append(cleared, f.Name)
	}
	logging.Infof("логи очищены: проект=%s файлов=%d", project, len(cleared))

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"cleared": cleared,
		"count":   len(cleared),
	})
}

// fileExists проверяет существование пути.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// filterLogEntries оставляет только файл с указанным именем.
func filterLogEntries(in []logFileEntry, name string) []logFileEntry {
	var out []logFileEntry
	for _, e := range in {
		if e.Name == name {
			out = append(out, e)
		}
	}
	return out
}

// collectLogFiles собирает *.log файлы каталога dir: метаданные + содержимое
// (последние logMaxBytes байт; факт обрезки — в поле cut записи).
func collectLogFiles(dir string) []logFileEntry {
	infos, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []logFileEntry
	for _, de := range infos {
		if de.IsDir() || !strings.HasSuffix(strings.ToLower(de.Name()), ".log") {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		content, cut := readLogTail(filepath.Join(dir, de.Name()))
		out = append(out, logFileEntry{
			Name:     de.Name(),
			Path:     filepath.Join(dir, de.Name()),
			Size:     info.Size(),
			Modified: info.ModTime().Format("2006-01-02 15:04:05"),
			Content:  content,
			cut:      cut,
		})
	}
	return out
}

// readLogTail читает хвост файла: если размер больше logMaxBytes, берёт
// последние logMaxBytes байт и помечает обрезку.
func readLogTail(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", false
	}
	if logMaxBytes <= 0 || info.Size() <= logMaxBytes {
		buf := make([]byte, info.Size())
		if _, err := f.Read(buf); err != nil {
			return "", false
		}
		return string(buf), false
	}

	offset := info.Size() - logMaxBytes
	if _, err := f.Seek(offset, 0); err != nil {
		return "", false
	}
	buf := make([]byte, logMaxBytes)
	n, _ := f.Read(buf)
	return "(лог обрезан до последних записей)\n" + string(buf[:n]), true
}
