package tools

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// Snap — снимок состояния директории проекта перед работой субагента.
// Это основа «механизма отката»: если шаг выполнился с ошибкой, Restore
// возвращает директорию к состоянию снимка — созданные субагентом файлы
// удаляются, а изменённые/удалённые им файлы восстанавливаются. Так упавший
// субагент не оставляет в общем репозитории половину переписанного кода:
// следующий шаг/параллельный агент стартует с заведомо консистентного
// состояния, а не с обломков чужой неудачной правки.
//
// Снимок хранит содержимое и режим каждого файла. Все операции файловые;
// при ПАРАЛЛЕЛЬНОМ выполнении шагов одной волны снимок каждого шага снимается
// по его scope (NewSnapScoped) — области шагов волны не пересекаются, поэтому
// откат одного шага не трогает файлы соседних. Исключённые пути (например,
// PLAN.md, которым управляет исполнитель) не восстанавливаются и не удаляются.
type Snap struct {
	root    string
	files   map[string]snapFile
	exclude map[string]bool
	// scoped — снимок снят по области (NewSnapScoped): файлы ВНЕ области могут
	// принадлежать соседним шагам волны, их откат не должен трогать.
	// scopeFiles — точные файлы области, scopeDirs — каталоги области.
	scoped     bool
	scopeFiles map[string]bool
	scopeDirs  []string
}

// inScope проверяет, входит ли относительный путь файла в область снимка.
func (s *Snap) inScope(rel string) bool {
	if s.scopeFiles[rel] {
		return true
	}
	for _, d := range s.scopeDirs {
		if rel == d || strings.HasPrefix(rel, d+"/") {
			return true
		}
	}
	return false
}

// snapFile — содержимое и режим одного файла в снимке.
type snapFile struct {
	data []byte
	mode fs.FileMode
}

// NewSnap снимает текущее состояние всех файлов внутри root. Ошибки обхода
// отдельных файлов не фатальны: недоступный файл просто не попадёт в снимок
// (при откате он будет удалён как «созданный после снимка»).
func NewSnap(root string) (*Snap, error) {
	st, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("не удаётся снять снимок %q: %v", root, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%q — не директория, снимок невозможен", root)
	}
	s := &Snap{root: filepath.Clean(root), files: map[string]snapFile{}}
	_ = filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		s.addFile(path)
		return nil
	})
	return s, nil
}

// NewSnapScoped снимает состояние ТОЛЬКО файлов, попадающих в scope — тех же
// записей, что использует FileOps.SetScope: файл — конкретный файл, директория
// с завершающим слэшем — весь её обход. Записи, указывающие на несуществующие
// файлы (шаги создания), в снимок не попадают.
//
// exclude — относительные slash-пути, которые снимок не должен ни фиксировать,
// ни откатывать (например "PLAN.md": файл, которым управляет исполнитель,
// и параллельный шаг не должен удалять его при откате своего соседа).
//
// Пустой scope — то же, что NewSnap (весь root). Такой снимок используется в
// последовательном режиме и для шагов без области, покрывающих весь проект.
func NewSnapScoped(root string, scope []string, exclude ...string) (*Snap, error) {
	clean := filepath.Clean(root)
	if st, err := os.Stat(clean); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("не удаётся снять снимок %q: %v", clean, err)
	}
	if len(scope) == 0 {
		return NewSnap(clean)
	}
	excl := map[string]bool{}
	for _, e := range exclude {
		e = filepath.ToSlash(filepath.Clean(e))
		if e != "." {
			excl[e] = true
		}
	}
	s := &Snap{root: clean, files: map[string]snapFile{}, exclude: excl, scoped: true, scopeFiles: map[string]bool{}}
	for _, entry := range scope {
		shot := strings.TrimSpace(strings.ReplaceAll(entry, "\\", "/"))
		shot = strings.TrimPrefix(shot, "./")
		if shot == "" {
			continue
		}
		base := filepath.Join(clean, filepath.FromSlash(strings.TrimSuffix(shot, "/")))
		if info, err := os.Stat(base); err != nil || !info.IsDir() {
			// Файл (или отсутствующая цель): снимаем конкретный файл.
			if err == nil {
				s.addFile(base)
				if rel, rerr := filepath.Rel(clean, base); rerr == nil {
					s.scopeFiles[filepath.ToSlash(rel)] = true
				}
			}
			continue
		}
		if rel, rerr := filepath.Rel(clean, base); rerr == nil {
			s.scopeDirs = append(s.scopeDirs, filepath.ToSlash(rel))
		}
		_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			s.addFile(path)
			return nil
		})
	}
	return s, nil
}

// addFile добавляет файл в снимок (пропускает исключённые и уже снятые).
func (s *Snap) addFile(path string) {
	rel, err := filepath.Rel(s.root, path)
	if err != nil {
		return
	}
	rel = filepath.ToSlash(rel)
	if s.exclude[rel] {
		return
	}
	if _, ok := s.files[rel]; ok {
		return
	}
	info, ierr := os.Stat(path)
	if ierr != nil {
		return
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		return
	}
	s.files[rel] = snapFile{data: data, mode: info.Mode()}
}

// Restore возвращает директорию к состоянию снимка:
//   - файлы снимка, изменённые или удалённые после снимка, — восстанавливаются
//     (содержимое и режим, файловые права родительских директорий создаются);
//   - файлы, созданные после снимка, — удаляются.
//
// Возвращает списки восстановленных и удалённых относительных (slash) путей.
// На пустом снимке (пустая директория) — no-op.
func (s *Snap) Restore() (restored, removed []string, err error) {
	if s == nil {
		// Nil-снимок — безопасный no-op (см. TestSnapRestoreNil).
		return nil, nil, nil
	}
	err = withProjectLock(s.root, func() error {
		var rerr error
		restored, removed, rerr = s.restoreLocked()
		return rerr
	})
	return restored, removed, err
}

// restoreLocked — тело Restore без пер-проектной блокировки. Откат выполняется
// под той же блокировкой, что и обычные записи: параллельные шаги волны в тот же
// момент могут писать код, и откат не должен «проглатывать» их файлы.
func (s *Snap) restoreLocked() (restored, removed []string, err error) {
	if s == nil || s.root == "" {
		return nil, nil, nil
	}

	// Текущее содержимое директории на момент отката.
	current := map[string]bool{}
	_ = filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(s.root, path)
		if rerr == nil {
			current[filepath.ToSlash(rel)] = true
		}
		return nil
	})

	// 1) Файлы снимка, которые пропали или изменились, — восстанавливаем.
	for rel := range s.files {
		if !current[rel] {
			restored = append(restored, rel)
			continue
		}
		full := filepath.Join(s.root, filepath.FromSlash(rel))
		data, rerr := os.ReadFile(full)
		if rerr != nil || !bytes.Equal(data, s.files[rel].data) {
			restored = append(restored, rel)
		}
	}
	sort.Strings(restored)
	for _, rel := range restored {
		entry := s.files[rel]
		full := filepath.Join(s.root, filepath.FromSlash(rel))
		if err := ensureParentDirs(full); err != nil {
			return restored, removed, fmt.Errorf("откат: %v", err)
		}
		if err := os.WriteFile(full, entry.data, entry.mode); err != nil {
			return restored, removed, fmt.Errorf("откат: не удалось восстановить %s: %v", rel, err)
		}
	}

	// 2) Файлы, созданные после снимка, — удаляем (кроме исключённых и
	// находящихся вне области scoped-снимка).
	for rel := range current {
		if _, ok := s.files[rel]; !ok {
			if s.exclude[rel] {
				continue
			}
			// Scoped-снимок: файлы вне области не наши — их не удаляем.
			if s.scoped && !s.inScope(rel) {
				continue
			}
			if err := os.Remove(filepath.Join(s.root, filepath.FromSlash(rel))); err != nil {
				return restored, removed, fmt.Errorf("откат: не удалось удалить %s: %v", rel, err)
			}
			removed = append(removed, rel)
		}
	}
	sort.Strings(removed)
	return restored, removed, nil
}

// FileCount возвращает число файлов в снимке (для тестов и логов).
func (s *Snap) FileCount() int {
	if s == nil {
		return 0
	}
	return len(s.files)
}

// Diff возвращает относительные (slash) пути, изменившиеся после снимка,
// без каких-либо побочных эффектов (только чтение):
//   - added — файлы, созданные после снимка (в области scoped-снимка и вне exclude);
//   - modified — файлы снимка, содержимое которых изменилось;
//   - removed — файлы снимка, отсутствующие на диске (удалённые).
//
// Используется для аудита шага («что субагент сделал»): те же правила области,
// что у Restore, чтобы параллельный залог другого шага не шумел в diff.
func (s *Snap) Diff() (added, modified, removed []string, err error) {
	if s == nil || s.root == "" {
		return nil, nil, nil, nil
	}

	current := map[string]bool{}
	_ = filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(s.root, path)
		if rerr == nil {
			current[filepath.ToSlash(rel)] = true
		}
		return nil
	})

	for rel := range current {
		if _, ok := s.files[rel]; !ok {
			if s.exclude[rel] {
				continue
			}
			if s.scoped && !s.inScope(rel) {
				continue
			}
			added = append(added, rel)
		}
	}
	sort.Strings(added)

	for rel := range s.files {
		if !current[rel] {
			removed = append(removed, rel)
			continue
		}
		if s.exclude[rel] {
			continue
		}
		if s.scoped && !s.inScope(rel) {
			continue
		}
		full := filepath.Join(s.root, filepath.FromSlash(rel))
		data, rerr := os.ReadFile(full)
		if rerr != nil || !bytes.Equal(data, s.files[rel].data) {
			modified = append(modified, rel)
		}
	}
	sort.Strings(modified)
	sort.Strings(removed)
	return added, modified, removed, nil
}

// DiffTextOld возвращает содержимое файла по относительному пути из снимка
// (baseline). Возвращает ("", false), если файла не было в снимке.
func (s *Snap) DiffTextOld(rel string) (string, bool) {
	f, ok := s.files[rel]
	if !ok {
		return "", false
	}
	return string(f.data), true
}

// DiffTextNew возвращает текущее содержимое файла с диска.
func (s *Snap) DiffTextNew(root, rel string) string {
	full := filepath.Join(root, filepath.FromSlash(rel))
	data, err := os.ReadFile(full)
	if err != nil {
		return ""
	}
	return string(data)
}

// DiffText генерирует unified-дифф для одного файла: baseline vs текущее
// содержимое. Возвращает ("", false) и ошибку, если файла не было в снимке.
func (s *Snap) DiffText(filepath string) (string, error) {
	old, ok := s.DiffTextOld(filepath)
	if !ok {
		return "", fmt.Errorf("файл не найден в снимке: %s", filepath)
	}
	newContent := s.DiffTextNew(s.root, filepath)

	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		FromFile: filepath,
		ToFile:   filepath,
		Context:  3,
		A:        difflib.SplitLines(old),
		B:        difflib.SplitLines(newContent),
	})
	if err != nil {
		return "", fmt.Errorf("diff %s: %v", filepath, err)
	}
	// Добавляем заголовок diff --git, чтобы sidebyside.ts правильно
	// парсил пути файлов.
	return "--- a/" + filepath + "\n+++ b/" + filepath + "\n" + diff, nil
}
