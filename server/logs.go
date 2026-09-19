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
// имя выделенного файла для показа в Logboard.

package server

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

	// Каталоги, где могут лежать лог-файлы: общий каталог логов сервера +
	// подкаталог logs внутри рабочей папки проекта (если есть).
	globalDir := s.logsDir()
	dirs := []string{globalDir}
	if inf.Root != "" {
		if local := filepath.Join(inf.Root, "logs"); fileExists(local) {
			dirs = append(dirs, local)
		}
	}

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
	seen := map[string]bool{}
	for _, dir := range dirs {
		entries := collectLogFiles(dir)
		// В общем каталоге сервера лежат логи ВСЕХ проектов и самого процесса
		// (server.log) — оставляем только файл этого проекта, иначе панель
		// показывала бы чужие логи. В локальном logs/ проекта берутся все.
		if dir == globalDir {
			entries = filterLogEntries(entries, own)
		}
		for _, e := range entries {
			if seen[e.Name] {
				continue
			}
			seen[e.Name] = true
			out.Truncated = out.Truncated || e.cut
			out.Files = append(out.Files, e)
		}
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Name < out.Files[j].Name })

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
