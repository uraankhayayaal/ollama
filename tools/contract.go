package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// APIEntry — одна публичная декларация пакета (функция, тип, константа,
// переменная). Sign — детерминированно нормализованная сигнатура: из
// параметров/полей убираются имена, остаются только типы, поэтому переименование
// аргументов не даёт ложного «изменения контракта».
type APIEntry struct {
	Kind string // "func" | "type" | "const" | "var"
	Name string
	Sign string // нормализованная сигнатура
}

// PublicAPISnapshot — снимок публичной поверхности проекта (Go-пакеты):
// детерминированно отсортированный список деклараций. Используется для
// сравнения API до и после шага разработчика (см. ComparePublicAPI).
type PublicAPISnapshot struct {
	Entries []APIEntry
}

// BuildPublicAPISnapshot собирает публичные декларации всех Go-файлов проекта
// в dir (рекурсивно, кроме .git/vendor/cache). Тест-файлы не учитываются.
func BuildPublicAPISnapshot(dir string) (*PublicAPISnapshot, error) {
	s := &PublicAPISnapshot{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "vendor" || name == "node_modules" || name == "cache" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			// Служебные/сгенерированные файлы, которые не собираются, не роняем.
			return nil
		}
		if f.Name == nil {
			return nil
		}
		pkg := f.Name.Name
		scanDecls(fset, path, f, pkg, s)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(s.Entries, func(i, j int) bool {
		a, b := s.Entries[i], s.Entries[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Sign < b.Sign
	})
	return s, nil
}

func scanDecls(fset *token.FileSet, path string, f *ast.File, pkg string, s *PublicAPISnapshot) {
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv != nil {
				// Методы не входят в публичный контракт пакета — только функции.
				continue
			}
			if !unicode.IsUpper(rune(d.Name.Name[0])) {
				continue
			}
			s.Entries = append(s.Entries, APIEntry{
				Kind: "func",
				Name: d.Name.Name,
				Sign: typeList(d.Type.Params) + " -> " + typeList(d.Type.Results),
			})
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch ts := spec.(type) {
				case *ast.TypeSpec:
					if !unicode.IsUpper(rune(ts.Name.Name[0])) {
						continue
					}
					// Знак типа — «скелет» декларации (поля/методы) без имён:
					// показываем изменение формы, а не косметику.
					s.Entries = append(s.Entries, APIEntry{
						Kind: "type",
						Name: ts.Name.Name,
						Sign: compact(nodeText(fset, path, ts)),
					})
				case *ast.ValueSpec:
					kind := "var"
					if d.Tok == token.CONST {
						kind = "const"
					}
					for _, n := range ts.Names {
						if !unicode.IsUpper(rune(n.Name[0])) {
							continue
						}
						s.Entries = append(s.Entries, APIEntry{
							Kind: kind,
							Name: n.Name,
							Sign: compact(nodeText(fset, path, ts)),
						})
					}
				}
			}
		}
	}
}

// typeList нормализует список полей до строки типов (имена отбрасываются).
func typeList(fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	var parts []string
	for _, f := range fl.List {
		t := typeExpr(f.Type)
		if f.Names == nil && strings.Contains(t, "(") {
			// Одиночный тип без имён — уже «чистый» тип.
			parts = append(parts, strings.Trim(t, "()"))
			continue
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, ", ")
}

func typeExpr(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return typeExpr(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + typeExpr(t.X)
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + typeExpr(t.Elt)
		}
		return "[" + strings.Trim(nodeText(nil, "", t.Len), " \t") + "]" + typeExpr(t.Elt)
	case *ast.Ellipsis:
		return "..." + typeExpr(t.Elt)
	case *ast.FuncType:
		return "func(" + typeList(t.Params) + ")" + resultsSuffix(t.Results)
	case *ast.InterfaceType:
		return "interface{...}"
	case *ast.StructType:
		return "struct{...}"
	case *ast.MapType:
		return "map[" + typeExpr(t.Key) + "]" + typeExpr(t.Value)
	case *ast.ParenExpr:
		return "(" + typeExpr(t.X) + ")"
	case *ast.ChanType:
		dir := ""
		if t.Dir&ast.SEND != 0 {
			dir = "<-"
		}
		return "chan " + dir + typeExpr(t.Value)
	default:
		return compact(nodeText(nil, "", e))
	}
}

func resultsSuffix(fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	if len(fl.List) == 1 && fl.List[0].Names == nil {
		return " " + typeExpr(fl.List[0].Type)
	}
	return " (" + typeList(fl) + ")"
}

// nodeText возвращает исходный фрагмент узла; nodeText(nil, "", e) без fset/path
// используется только для выражений, не требующих текста исходника
// (вариант для простых узлов возвращается через compact внутреннего описания).
func nodeText(fset *token.FileSet, path string, n ast.Node) string {
	if fset == nil {
		return ""
	}
	start := fset.Position(n.Pos()).Offset
	end := fset.Position(n.End()).Offset
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if start < 0 || end > len(data) || start > end {
		return ""
	}
	return string(data[start:end])
}

// compact схлопывает пробелы/переводы строк в один пробел и обрезает строку
// до разумной длины (детерминированный «скелет» сигнатуры для сравнения).
func compact(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// ComparePublicAPI сравнивает два снимка публичной поверхности проекта.
// Возвращает детерминированно отсортированный список строк: добавленные
// декларации ("+"), удалённые ("-") и изменённые сигнатуры ("~").
func ComparePublicAPI(before, after *PublicAPISnapshot) []string {
	if before == nil || after == nil {
		return nil
	}
	afterSet := map[string]bool{}
	for _, e := range after.Entries {
		afterSet[e.Kind + " " + e.Name + " " + e.Sign] = true
	}
	beforeSet := map[string]bool{}
	for _, e := range before.Entries {
		beforeSet[e.Kind + " " + e.Name + " " + e.Sign] = true
	}
	signByKey := map[string]string{}
	for _, e := range before.Entries {
		signByKey[e.Kind+" "+e.Name] = e.Sign
	}

	var out []string
	seen := map[string]bool{}
	for _, e := range after.Entries {
		key := e.Kind + " " + e.Name
		combo := e.Kind + " " + e.Name + " " + e.Sign
		if !beforeSet[combo] {
			line := "+ " + entryString(e)
			if seen[line] {
				continue
			}
			seen[line] = true
			if beforeSign, ok := signByKey[key]; ok && beforeSign != e.Sign {
				line = "~ " + entryString(e) + " (было: " + beforeSign + ")"
			}
			out = append(out, line)
		}
	}
	for _, e := range before.Entries {
		combo := e.Kind + " " + e.Name + " " + e.Sign
		if !afterSet[combo] {
			line := "- " + entryString(e)
			if seen[line] {
				continue
			}
			seen[line] = true
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return out
}

func entryString(e APIEntry) string {
	return e.Kind + " " + e.Name + " " + e.Sign
}