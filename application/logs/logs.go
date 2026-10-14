// Пакет logs — серверная логика панели «Логи» (Logboard): сбор *.log файлов
// проекта из общего каталога сервера и подкаталога logs рабочего каталога,
// дедупликация, отбор только «своих» файлов из общего каталога и чтение хвоста
// с ограничением объёма.
//
// Пакет не зависит от HTTP и от пакета server: каталоги передаются аргументом,
// содержимое файлов читается напрямую (stdlib-only).
package logs

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MaxBytes — максимальный объём содержимого одного файла, отдаваемый Logboard
// (хвост лога — самые свежие записи). 0 — без ограничения.
const MaxBytes = 1 << 20 // 1 МБ

// ClearMarker — заголовок, который очистка оставляет вместо прежнего
// содержимого. Пустой файл не отличить от «логов ещё никогда не было», а
// содержимое лога — это улики агентского прогона, поэтому момент стирания
// должен остаться в самом файле.
const ClearMarker = "=== Логи очищены %s ==="

// Entry — один *.log файл: метаданные + содержимое.
type Entry struct {
	Name     string `json:"name"`
	Path     string `json:"path,omitempty"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	Content  string `json:"content"`
	// Cut — содержимое обрезано до MaxBytes. В JSON не отдаётся: итог виден в
	// поле Truncated ответа.
	Cut bool `json:"-"`
}

// Response — ответ панели логов.
type Response struct {
	Dir       string  `json:"dir"`
	Files     []Entry `json:"files"`
	Selected  string  `json:"selected"`
	Truncated bool    `json:"truncated"`
}

// CollectProject собирает лог-файлы проекта по dirs с дедупликацией по имени.
// Факт обрезки хвоста — во втором возвращаемом значении.
//
// В общем каталоге сервера лежат логи ВСЕХ проектов и самого процесса
// (server.log) — оттуда берётся только файл этого проекта (own), иначе панель
// показывала бы чужие логи, а очистка стирала бы их. В локальном logs/
// проекта берутся все.
func CollectProject(dirs []string, globalDir, own string) (out []Entry, cut bool) {
	seen := map[string]bool{}
	for _, dir := range dirs {
		entries := CollectFiles(dir)
		if dir == globalDir {
			entries = Filter(entries, own)
		}
		for _, e := range entries {
			if seen[e.Name] {
				continue
			}
			seen[e.Name] = true
			cut = cut || e.Cut
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, cut
}

// Filter оставляет только файл с указанным именем.
func Filter(in []Entry, name string) []Entry {
	var out []Entry
	for _, e := range in {
		if e.Name == name {
			out = append(out, e)
		}
	}
	return out
}

// CollectFiles собирает *.log файлы каталога dir: метаданные + содержимое
// (последние MaxBytes байт; факт обрезки — в поле Cut записи).
func CollectFiles(dir string) []Entry {
	infos, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Entry
	for _, de := range infos {
		if de.IsDir() || !strings.HasSuffix(strings.ToLower(de.Name()), ".log") {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		content, cut := ReadTail(filepath.Join(dir, de.Name()))
		out = append(out, Entry{
			Name:     de.Name(),
			Path:     filepath.Join(dir, de.Name()),
			Size:     info.Size(),
			Modified: info.ModTime().Format("2006-01-02 15:04:05"),
			Content:  content,
			Cut:      cut,
		})
	}
	return out
}

// ReadTail читает хвост файла: если размер больше MaxBytes, берёт последние
// MaxBytes байт и помечает обрезку.
func ReadTail(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", false
	}
	if MaxBytes <= 0 || info.Size() <= MaxBytes {
		buf := make([]byte, info.Size())
		if _, err := f.Read(buf); err != nil {
			return "", false
		}
		return string(buf), false
	}

	offset := info.Size() - MaxBytes
	if _, err := f.Seek(offset, 0); err != nil {
		return "", false
	}
	buf := make([]byte, MaxBytes)
	n, _ := f.Read(buf)
	return "(лог обрезан до последних записей)\n" + string(buf[:n]), true
}

// FileExists проверяет существование пути.
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
