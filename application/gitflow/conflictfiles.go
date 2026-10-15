package gitflow

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Ограничения выдачи конфликтов модели: слишком большие файлы отдаём зоной
// конфликта (файл помечается в truncated — править его нужно через edits).
const (
	// MaxConflictFileBytes — предел размера файла, который целиком уходит модели.
	MaxConflictFileBytes = 48 << 10
	// MaxConflictFiles — предел числа конфликтных файлов в ответе.
	MaxConflictFiles = 20

	conflictRegionLines = 80
)

// ConflictEdit — точечная замена фрагмента в конфликтном файле (для файлов,
// которые не влезли в ответ целиком). Old обязан встречаться в файле ровно
// один раз, иначе запись отклоняется целиком (никаких «угадываний» правок).
type ConflictEdit struct {
	File string `json:"file"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

// ConflictView — что модели нужно знать о конфликте: файлы, где ещё есть
// маркеры, и их содержимое (для больших — только зона конфликта).
type ConflictView struct {
	// Files — конфликтные файлы, которые ещё содержат маркеры.
	Files []string `json:"files"`
	// Content — содержимое каждого файла из Files.
	Content map[string]string `json:"content"`
	// Truncated — файлы, отданные фрагментом (править только через edits).
	Truncated []string `json:"truncated,omitempty"`
	// Branch — релизная ветка эпика, в которую идёт резолв.
	Branch string `json:"branch,omitempty"`
	// Hint — что делать модели с этими данными.
	Hint string `json:"hint,omitempty"`
}

// NewConflictView собирает список оставшихся конфликтных файлов и их
// содержимое (для файлов больше MaxConflictFileBytes — только зона конфликта).
// Вынесено из HTTP-слоя (`server/actions_resolve.go`, Этап 6): правило отбора
// и формат подсказки модели — не задача сервера.
func NewConflictView(wt string, files []string) *ConflictView {
	v := &ConflictView{Content: map[string]string{}}
	if len(files) > MaxConflictFiles {
		files = files[:MaxConflictFiles]
	}
	pending := pendingConflictFiles(wt, files)
	v.Files = pending
	for _, p := range pending {
		data, err := os.ReadFile(filepath.Join(wt, filepath.FromSlash(p)))
		if err != nil {
			continue
		}
		if len(data) <= MaxConflictFileBytes {
			v.Content[p] = string(data)
			continue
		}
		v.Truncated = append(v.Truncated, p)
		v.Content[p] = ConflictRegion(string(data))
	}
	switch {
	case len(v.Files) == 0:
		v.Hint = "конфликтных файлов не осталось"
	case len(v.Truncated) > 0:
		v.Hint = "файлы из truncated показаны фрагментом — правь их через edits (file+old+new), остальные можно целиком через files"
	default:
		v.Hint = "выбери корректное содержимое и запиши его через action=apply (files: путь → полный текст без маркеров)"
	}
	return v
}

// pendingConflictFiles — пути из paths, где на диске (dir) ещё остались маркеры
// конфликта git. Локальная копия git.HasConflictMarkers: application-слой не
// импортирует infrastructure (Этап 8, срез H3c). Отсутствующие файлы
// пропускаются; небезопасные пути игнорируются.
func pendingConflictFiles(dir string, paths []string) []string {
	var out []string
	for _, p := range paths {
		rel, err := ConflictSafeRel(p)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		for _, ln := range strings.Split(string(data), "\n") {
			if isConflictMarkerLine(ln) {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// isConflictMarkerLine — маркер блока конфликта git (наша/разделитель/чужая
// сторона) без учёта метки после маркера. Разделитель матчится точным
// равенством, как в infrastructure/git.
func isConflictMarkerLine(ln string) bool {
	switch {
	case strings.HasPrefix(ln, "<<<<<<< "):
		return true
	case ln == "=======":
		return true
	case strings.HasPrefix(ln, ">>>>>>> "):
		return true
	}
	return false
}

// ConflictRegion вырезает строки рядом с маркерами конфликта — для файлов,
// которые целиком в ответ модели не влезают.
func ConflictRegion(content string) string {
	lines := strings.Split(content, "\n")
	keep := make([]bool, len(lines))
	half := conflictRegionLines / 2
	for i, ln := range lines {
		if !IsConflictMarkerLine(ln) {
			continue
		}
		lo, hi := i-half, i+half
		if lo < 0 {
			lo = 0
		}
		if hi >= len(lines) {
			hi = len(lines) - 1
		}
		for j := lo; j <= hi; j++ {
			keep[j] = true
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "… [файл показан фрагментом вокруг конфликтов, всего строк %d] …\n", len(lines))
	for i, ln := range lines {
		if !keep[i] {
			continue
		}
		fmt.Fprintf(&b, "%d\t%s\n", i+1, ln)
	}
	return b.String()
}

// IsConflictMarkerLine — строка с маркером конфликта git (<<<<<<< / ======= /
// >>>>>>> возможно с префиксом-меткой).
func IsConflictMarkerLine(ln string) bool {
	t := strings.TrimSpace(ln)
	switch {
	case strings.HasPrefix(t, "<<<<<<<"), strings.HasPrefix(t, "|||||||"),
		strings.HasPrefix(t, "======="), strings.HasPrefix(t, ">>>>>>>"):
		return true
	}
	return false
}

// ApplyConflictFiles — запись правок модели в конфликтный worktree: files
// (полное содержимое) затем edits (точечные замены, по порядку). Возвращает
// список изменённых файлов (rel-пути, через «/»).
func ApplyConflictFiles(wt string, allowed map[string]bool, pending []string, files map[string]string, edits []ConflictEdit) ([]string, error) {
	touched := map[string]bool{}
	names := sortedKeys(files)
	for _, p := range names {
		rel, err := checkConflictFile(wt, allowed, p, pending)
		if err != nil {
			return nil, err
		}
		full := filepath.Join(wt, filepath.FromSlash(rel))
		fi, err := os.Lstat(full)
		if err != nil {
			return nil, fmt.Errorf("файла %s нет в конфликтном worktree: %v", p, err)
		}
		if fi.Size() > MaxConflictFileBytes {
			return nil, fmt.Errorf("%s слишком большой (%d байт) — передай правку через edits (file+old+new)", p, fi.Size())
		}
		if err := os.WriteFile(full, []byte(files[p]), fi.Mode().Perm()); err != nil {
			return nil, fmt.Errorf("запись %s: %v", p, err)
		}
		touched[rel] = true
	}
	for i, e := range edits {
		rel, err := checkConflictFile(wt, allowed, e.File, pending)
		if err != nil {
			return nil, err
		}
		full := filepath.Join(wt, filepath.FromSlash(rel))
		cur, err := os.ReadFile(full)
		if err != nil {
			return nil, fmt.Errorf("чтение %s: %v", e.File, err)
		}
		if e.Old == "" {
			return nil, fmt.Errorf("edits[%d] (%s): old пустой — укажи уникальный фрагмент конфликта", i, e.File)
		}
		if n := strings.Count(string(cur), e.Old); n != 1 {
			return nil, fmt.Errorf("edits[%d] (%s): фрагмент old встречается %d раз (нужен ровно 1) — пришли уникальный фрагмент конфликта", i, e.File, n)
		}
		out := strings.Replace(string(cur), e.Old, e.New, 1)
		fi, err := os.Lstat(full)
		if err != nil {
			return nil, fmt.Errorf("файла %s нет: %v", e.File, err)
		}
		if err := os.WriteFile(full, []byte(out), fi.Mode().Perm()); err != nil {
			return nil, fmt.Errorf("запись %s: %v", e.File, err)
		}
		touched[rel] = true
	}
	out := make([]string, 0, len(touched))
	for p := range touched {
		out = append(out, p)
	}
	return out, nil
}

// checkConflictFile проверяет, что путь из аргумента модели — конфликтный файл
// текущего резолва, лежащий внутри worktree (без выхода по «..» и без symlink).
func checkConflictFile(wt string, allowed map[string]bool, p string, pending []string) (string, error) {
	rel, err := ConflictSafeRel(p)
	if err != nil {
		return "", err
	}
	if !allowed[rel] {
		return "", fmt.Errorf("файл %s не конфликтный — править можно: %s (остальные файлы ассистент не трогает)", p, strings.Join(pending, ", "))
	}
	full := filepath.Join(wt, filepath.FromSlash(rel))
	if !WithinDir(wt, full) {
		return "", fmt.Errorf("путь %s выходит за пределы конфликтного worktree", p)
	}
	fi, err := os.Lstat(full)
	if err != nil {
		return "", fmt.Errorf("файла %s нет в конфликтном worktree: %v", p, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s — не обычный файл (%s), правка запрещена", p, fi.Mode())
	}
	return rel, nil
}

// ConflictSafeRel — безопасный относительный путь внутри каталога (аналог
// git.safePathRel, который неэкспортируемый).
func ConflictSafeRel(p string) (string, error) {
	q := filepath.ToSlash(filepath.Clean(strings.TrimSpace(p)))
	if q == "" || q == "." || q == ".." || filepath.IsAbs(filepath.FromSlash(p)) {
		return "", fmt.Errorf("недопустимый путь %q: нужен путь файла относительно корня проекта (как в conflicts.files)", p)
	}
	if strings.HasPrefix(q, "../") || strings.Contains(q, "/../") {
		return "", fmt.Errorf("недопустимый путь %q: выход за пределы каталога запрещён", p)
	}
	return q, nil
}

// WithinDir — лежит ли p внутри root (после Clean).
func WithinDir(root, p string) bool {
	r := filepath.Clean(root)
	c := filepath.Clean(p)
	return c == r || strings.HasPrefix(c, r+string(filepath.Separator))
}

// sortedKeys — ключи map в стабильном порядке (детерминированный apply).
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
