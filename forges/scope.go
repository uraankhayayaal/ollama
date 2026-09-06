package forges

import (
	"path/filepath"
	"strings"
)

// ScopeMatcher — скомпилированный список областей работы (файлы/директории
// проекта). Позволяет проверить, попадает ли файл в область (Allow) и есть
// ли вообще что-то из области внутри директории (HasInside).
//
// Живёт в пакете forges (а не tools), потому что tools зависит от forges,
// а forges не может импортировать tools (цикл импортов). Используется и
// forges.LocalForge, и tools.FileOps.
//
// Семантика записи:
//   - "src/main.go" — только конкретный файл;
//   - "internal/order/" — вся директория (все файлы внутри неё);
//   - "" или пустой срез — без ограничений.
//
// Пути нормализуются: отбрасываются "/./", ведущие/хвостовые слэши,
// "." и пустые записи игнорируются.
type ScopeMatcher struct {
	entries []string
}

// CompileScope нормализует список областей в ScopeMatcher.
// Пустой/мусорный срез даёт matcher, разрешающий всё.
func CompileScope(entries []string) *ScopeMatcher {
	norm := make([]string, 0, len(entries))
	for _, e := range entries {
		e = strings.TrimSpace(e)
		e = strings.Trim(e, "/")
		e = filepath.ToSlash(filepath.Clean(e))
		if e == "" || e == "." {
			continue
		}
		norm = append(norm, e)
	}
	return &ScopeMatcher{entries: norm}
}

// Empty сообщает, что областей нет (всё разрешено).
func (m *ScopeMatcher) Empty() bool {
	return m == nil || len(m.entries) == 0
}

// Allow возвращает true, если относительный slash-путь файла/директории
// лежит внутри хотя бы одной области: точное совпадение с записью или
// нахождение внутри записи-директории.
func (m *ScopeMatcher) Allow(rel string) bool {
	if m.Empty() {
		return true
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." || rel == "" {
		return true
	}
	for _, e := range m.entries {
		if rel == e || strings.HasPrefix(rel, e+"/") {
			return true
		}
	}
	return false
}

// HasInside возвращает true, если директория rel (slash-путь) сама является
// областью работы или внутри неё (в листьях) есть область — т.е. при обходе
// в неё стоит заходить, даже если сама директория формально вне области.
func (m *ScopeMatcher) HasInside(dir string) bool {
	if m.Empty() {
		return false
	}
	dir = filepath.ToSlash(filepath.Clean(dir))
	if dir == "." || dir == "" {
		return true
	}
	for _, e := range m.entries {
		if e == dir || strings.HasPrefix(e, dir+"/") {
			return true
		}
	}
	return false
}