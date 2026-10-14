package logs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	logpkg "ai/domain/log"
)

// Slice — результат чтения хвоста лог-файла для ответа модели.
type Slice struct {
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

// ReadLogSlice читает хвост файла (последние limit строк), при grep — только
// совпавшие строки. Читает с конца файла целиком: логи проекта уже
// ограничены панелью, а полный файл может быть больше — тогда берём хвост
// размера readMaxBytes, как это делает Logboard.
//
// contentMaxBytes — потолок содержимого в ответе (обрезается с конца).
func ReadLogSlice(path string, grep string, limit int, readMaxBytes int, contentMaxBytes int) (Slice, error) {
	f, err := os.Open(path)
	if err != nil {
		return Slice{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Slice{}, err
	}
	res := Slice{
		Name:     filepath.Base(path),
		Size:     info.Size(),
		Modified: info.ModTime().Format("2006-01-02 15:04:05"),
	}
	start := int64(0)
	if readMaxBytes > 0 && info.Size() > int64(readMaxBytes) {
		start = info.Size() - int64(readMaxBytes)
		res.Truncated = true
		res.Note = "файл больше 1 МБ, прочитан хвост"
	}
	if _, err := f.Seek(start, 0); err != nil {
		return res, err
	}
	buf := make([]byte, info.Size()-start)
	if _, err := f.Read(buf); err != nil && len(buf) > 0 {
		return res, err
	}

	all := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if len(all) == 1 && all[0] == "" {
		all = nil
	}
	res.Total = len(all)
	picked := all
	if grep != "" {
		picked = nil
		for _, l := range all {
			if strings.Contains(strings.ToLower(l), strings.ToLower(grep)) {
				picked = append(picked, l)
			}
		}
		res.Matched = len(picked)
	}
	if len(picked) > limit {
		picked = picked[len(picked)-limit:]
		res.Truncated = true
		if res.Note == "" {
			res.Note = fmt.Sprintf("показаны последние %d из %d строк", limit, res.Total)
		}
	}
	res.Lines = len(picked)
	content := strings.Join(picked, "\n")
	if len(content) > contentMaxBytes {
		content = content[len(content)-contentMaxBytes:]
		res.Truncated = true
	}
	res.Content = logpkg.TruncateLogText(content, contentMaxBytes)
	if len(res.Content) == 0 && grep != "" {
		res.Note = strings.TrimSpace(res.Note + " подстрока не найдена")
	}
	return res, nil
}
