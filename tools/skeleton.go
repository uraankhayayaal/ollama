package tools

// Компактная «карта кода»: скелеты файлов без тел функций. Лидам и
// разработчикам нужны интерфейсы, структуры и сигнатуры, а не внутренности
// функций — чтение полного текста большого файла переполняет контекст модели
// («model context fatigue») и мешает декомпозиции. Инструмент ReadMap отдаёт
// карту файла (декларации с номерами строк), а по маркеру дозаправки
// FetchContext подтягивает либо карту, либо точный диапазон строк
// («хирургическое окно»), не читая файл целиком.
//
//	Go:  разбор через go/ast — сигнатуры функций/методов, поля структур,
//	     методы интерфейсов, const/var с типами.
//	TS/JS: построчный сканер — interface/type (полными телами как контракты),
//	     function/class/const — только открывающая строка.
//	прочее: первые строки файла с нумерацией (fallback).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// skeletonMaxChars — предел объёма карты одного файла, чтобы даже скелет
// гигантского файла не раздувал контекст модели.
const skeletonMaxChars = 24_000

// skeletonFallbackLines — сколько строк исходника показываем в карте файлов
// без специального разбора (LangUnknown).
const skeletonFallbackLines = 40

// skeletonLineMax — предел длины одной строки карты (длинные строки режутся).
const skeletonLineMax = 200

// Кеш карт кода (Скорость 4): ReadMap и FetchContext в течение одного шага
// читают одни и те же файлы (в т.ч. параллельные шаги волны), а построение
// карты через go/ast — дорого. Ключ — путь+размер+mtime файла: контент
// изменился — размер/время меняются, кеш корректно инвалидируется.
// Отключается переменной окружения CODEGEN_SKELETON_CACHE ("0"/"false"/"off").
const (
	skeletonCacheCap   = 512
	skeletonCacheOnEnv = "CODEGEN_SKELETON_CACHE"
)

var (
	skeletonCacheMu sync.Mutex
	skeletonCache   = make(map[string]string)
)

// skeletonCacheEnabled определяет, включён ли кеш карт кода.
func skeletonCacheEnabled() bool {
	if v := strings.TrimSpace(os.Getenv(skeletonCacheOnEnv)); v != "" {
		switch strings.ToLower(v) {
		case "0", "false", "off", "no":
			return false
		}
	}
	return true
}

// skeletonizeCached возвращает карту кода файла full, беря её из кеша, если
// файл не менялся (ключ path|size|mtime). При отключённом кеше — прямой
// построение карты.
func skeletonizeCached(full string, src []byte) string {
	if !skeletonCacheEnabled() {
		return SkeletonizeFile(full, src)
	}
	key := ""
	if info, err := os.Stat(full); err == nil {
		key = fmt.Sprintf("%s|%d|%d", full, info.Size(), info.ModTime().UnixNano())
	}
	if key != "" {
		skeletonCacheMu.Lock()
		if v, ok := skeletonCache[key]; ok {
			skeletonCacheMu.Unlock()
			return v
		}
		// Переполнение: очищаем кеш целиком (простое вытеснение — карты
		// строятся заново на следующем запросе, без потери корректности).
		if len(skeletonCache) >= skeletonCacheCap {
			clear(skeletonCache)
		}
		skeletonCacheMu.Unlock()
	}

	s := SkeletonizeFile(full, src)
	if key != "" {
		skeletonCacheMu.Lock()
		skeletonCache[key] = s
		skeletonCacheMu.Unlock()
	}
	return s
}

// SkeletonizeFile возвращает компактную карту исходника src (принадлежащего
// файлу filename): декларации с номерами строк, без тел функций. По языку
// файла выбирается разборщик; для незнакомых расширений — fallback.
func SkeletonizeFile(filename string, src []byte) string {
	switch {
	case strings.HasSuffix(strings.ToLower(filename), ".go"):
		return skeletonGo(src)
	case isTSLike(filename):
		return skeletonScript(linesOf(src))
	default:
		return skeletonFallback(linesOf(src))
	}
}

// isTSLike определяет файлы web-стека, для которых есть построчный сканер.
func isTSLike(filename string) bool {
	lower := strings.ToLower(filename)
	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".vue", ".svelte"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// linesOf разбивает исходник на строки и срезает висячий пустой последний
// элемент (результат Split по "\n", закончившийся переводом строки).
func linesOf(src []byte) []string {
	lines := strings.Split(string(src), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// capSkeleton ограничивает карту по размеру и длине строки, помечая обрезку.
func capSkeleton(s string) string {
	if len(s) > skeletonMaxChars {
		s = s[:skeletonMaxChars]
		s += "\n[... карта обрезана по лимиту ...]"
	}
	return s
}

// trimLong возвращает строку, укороченную до skeletonLineMax символов.
func trimLong(s string) string {
	if len(s) > skeletonLineMax {
		return s[:skeletonLineMax] + "..."
	}
	return s
}

// ------------- Go: разбор через go/ast -------------

// skeletonGo строит карту Go-файла: package, imports и все верхнеуровневые
// декларации с сигнатурами без тел. Каждая декларация помечается номером
// строки её начала (// L<N>), чтобы лид/разработчик мог передать точный
// диапазон «хирургического окна» последнему исполнителю.
func skeletonGo(src []byte) string {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "skeleton.go", src, parser.ParseComments)
	if err != nil {
		// Бинарник-parse не удался (незавершённый файл на диске): fallback.
		return skeletonFallback(linesOf(src))
	}

	var b strings.Builder
	if file.Name != nil {
		fmt.Fprintf(&b, "package %s\n", file.Name.Name)
	}

	// Импорты — одной строкой, это лишь навигация, а не контракт.
	b.WriteString(skeletonImports(file))

	lineOf := func(pos token.Pos) int {
		return fset.Position(pos).Line
	}

	for _, d := range file.Decls {
		switch decl := d.(type) {
		case *ast.GenDecl:
			switch decl.Tok {
			case token.IMPORT:
				// Уже выведены выше одной строкой.
			case token.TYPE:
				for _, sp := range decl.Specs {
					ts, ok := sp.(*ast.TypeSpec)
					if !ok {
						continue
					}
					fmt.Fprintf(&b, "%s // L%d\n", skeletonTypeSpec(fset, ts), lineOf(ts.Pos()))
				}
			case token.CONST, token.VAR:
				s := skeletonValueSpec(fset, decl.Tok, decl.Specs)
				if s != "" {
					fmt.Fprintf(&b, "%s // L%d\n", s, lineOf(decl.Pos()))
				}
			}
		case *ast.FuncDecl:
			fmt.Fprintf(&b, "%s // L%d\n", renderFuncSignature(fset, decl), lineOf(decl.Pos()))
		}
	}
	return capSkeleton(b.String())
}

// skeletonImports сводит блок import к однострочному списку путей.
func skeletonImports(file *ast.File) string {
	var paths []string
	for _, imp := range file.Imports {
		p := ""
		if imp.Path != nil {
			if u, err := strconv.Unquote(imp.Path.Value); err == nil {
				p = u
			} else {
				p = imp.Path.Value
			}
		}
		if imp.Name != nil {
			p = imp.Name.Name + " " + p
		}
		if p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return ""
	}
	return "import (" + strings.Join(paths, "; ") + ")\n"
}

// skeletonTypeSpec описывает type-декларацию: struct — поля, interface —
// методы, alias — целевую часть. Всё — компактной строкой.
func skeletonTypeSpec(fset *token.FileSet, ts *ast.TypeSpec) string {
	var buf bytes.Buffer
	buf.WriteString("type ")
	buf.WriteString(ts.Name.Name)
	if ts.Assign.IsValid() {
		buf.WriteString(" = ")
		buf.WriteString(renderType(fset, ts.Type))
		return buf.String()
	}
	switch t := ts.Type.(type) {
	case *ast.StructType:
		buf.WriteString(" struct { ")
		buf.WriteString(renderStructFields(fset, t.Fields))
		buf.WriteString(" }")
	case *ast.InterfaceType:
		buf.WriteString(" interface { ")
		buf.WriteString(renderIfaceMethods(fset, t.Methods))
		buf.WriteString(" }")
	default:
		buf.WriteString(" ")
		buf.WriteString(trimLong(renderType(fset, t)))
	}
	return buf.String()
}

// skeletonValueSpec описывает const/var: имена и (если есть) тип, без значений.
func skeletonValueSpec(fset *token.FileSet, tok token.Token, specs []ast.Spec) string {
	var parts []string
	for _, sp := range specs {
		vs, ok := sp.(*ast.ValueSpec)
		if !ok {
			continue
		}
		var names []string
		for _, n := range vs.Names {
			names = append(names, n.Name)
		}
		s := strings.Join(names, ", ")
		if vs.Type != nil {
			s += " " + trimLong(renderType(fset, vs.Type))
		}
		if s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.ToLower(tok.String()) + " " + strings.Join(parts, "; ")
}

// renderType печатает выражение типа (field.Type и т.п.).
func renderType(fset *token.FileSet, t ast.Expr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, t); err != nil {
		return "<?type>"
	}
	return buf.String()
}

// renderStructFields описывает поля struct компактной строкой через "; ".
func renderStructFields(fset *token.FileSet, fl *ast.FieldList) string {
	if fl == nil {
		return ""
	}
	var parts []string
	for _, f := range fl.List {
		typ := renderType(fset, f.Type)
		if len(f.Names) == 0 {
			parts = append(parts, typ)
			continue
		}
		var names []string
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
		parts = append(parts, strings.Join(names, ", ")+" "+typ)
	}
	return strings.Join(parts, "; ")
}

// renderIfaceMethods описывает методы интерфейса компактной строкой.
func renderIfaceMethods(fset *token.FileSet, fl *ast.FieldList) string {
	if fl == nil {
		return ""
	}
	var parts []string
	for _, f := range fl.List {
		typ := renderType(fset, f.Type)
		if len(f.Names) == 0 {
			parts = append(parts, typ)
			continue
		}
		var names []string
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
		parts = append(parts, strings.Join(names, ", ")+" "+typ)
	}
	return strings.Join(parts, "; ")
}

// renderFieldList печатает список параметров/результатов: "(a A, b B)".
func renderFieldList(fset *token.FileSet, fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return "()"
	}
	var parts []string
	for _, f := range fl.List {
		typ := renderType(fset, f.Type)
		if len(f.Names) == 0 {
			parts = append(parts, typ)
			continue
		}
		var names []string
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
		parts = append(parts, strings.Join(names, ", ")+" "+typ)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// renderFuncSignature печатает сигнатуру функции/метода без тела.
func renderFuncSignature(fset *token.FileSet, fd *ast.FuncDecl) string {
	var buf bytes.Buffer
	buf.WriteString("func ")
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		buf.WriteString("(" + renderType(fset, fd.Recv.List[0].Type) + ") ")
	}
	buf.WriteString(fd.Name.Name)
	buf.WriteString(renderFieldList(fset, fd.Type.Params))
	if fd.Type.Results != nil && len(fd.Type.Results.List) > 0 {
		buf.WriteString(" ")
		if len(fd.Type.Results.List) > 1 || len(fd.Type.Results.List[0].Names) > 0 {
			buf.WriteString(renderFieldList(fset, fd.Type.Results))
		} else {
			buf.WriteString(renderType(fset, fd.Type.Results.List[0].Type))
		}
	}
	return buf.String()
}

// ------------- TypeScript/JavaScript: построчный сканер -------------

// Классы открывающих строк для языка web-стека. Для interface/type/enum тело
// целиком считается контрактом и показывается; для function/class/const —
// только открывающая строка (сигнатура).
var (
	reIfType        = regexp.MustCompile(`^(?:export\s+)?(?:declare\s+)?(?:interface|type|enum)\s+\w+`)
	reFunc          = regexp.MustCompile(`^(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s+\*?[\w$]+`)
	reClass         = regexp.MustCompile(`^(?:export\s+)?(?:default\s+)?(?:abstract\s+)?class\s+[\w$]+`)
	reConstDecl     = regexp.MustCompile(`^(?:export\s+)?(?:const|let|var)\s+[\w$]+\s*(?:=|:)`)
	reArrowConst    = regexp.MustCompile(`^(?:export\s+)?const\s+[\w$]+\s*=\s*(?:async\s*)?\((?:[^()]|\([^()]*\))*\)\s*=>`)
	reMethod        = regexp.MustCompile(`^(?:readonly\s+|private\s+|public\s+|protected\s+|static\s+|async\s+)*\*?[\w$]+(?:\([^)]*\)(?:\s*:\s*[\w<>\[\]|., ]+)?|=\s*\([^)]*\)\s*=>)\s*[{:]$`)
	reProp          = regexp.MustCompile(`^(?:readonly\s+)?[\w$]+\??\s*:\s*[\w<>\[\]|.,'"]+[;,]?$`)
	reTSImport      = regexp.MustCompile(`^import\s|^export\s+\*\s+from|^export\s*\{|^import\s*\(`)
)

// skeletonScript строит карту TS/JS-файла: интерфейсы/типы/перечисления —
// полными телами (контракты), функции/классы/константы — только открывающими
// строками с номерами строк. Глубина фигурных скобок отслеживается кумулятивно
// (depth), поэтому работают и многострочные декларации, и вложенные тела.
func skeletonScript(lines []string) string {
	var b strings.Builder
	depth := 0
	// classStack — глубины, на которых находятся тела открытых class-блоков
	// (строки-члены класса определяются как depth == classStack[top]).
	var classStack []int

	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		lineNo := i + 1
		o, c := countBraces(line)
		db := o - c

		inClassBody := len(classStack) > 0 && depth == classStack[len(classStack)-1]

		switch {
		case reIfType.MatchString(line) && depth == 0:
			// interface/type/enum — контракты: показываем всё тело до
			// закрывающей скобки начального уровня вместе с номерами строк.
			n := writeContract(&b, lines, i, depth)
			depth = contractEndDepth(lines, i, depth)
			i = n - 1
			continue
		case reFunc.MatchString(line) && depth == 0,
			reArrowConst.MatchString(line) && depth == 0:
			b.WriteString(fmt.Sprintf("%d: %s // { ... }\n", lineNo, trimLong(strings.TrimSpace(raw))))
		case reClass.MatchString(line) && depth == 0:
			b.WriteString(fmt.Sprintf("%d: %s // { ... }\n", lineNo, trimLong(strings.TrimSpace(raw))))
			// Тело class-блока живёт на глубине depth+db (после его открывающей
			// скобки), там ищем члены класса.
			classStack = append(classStack, depth+db)
		case reConstDecl.MatchString(line) && depth == 0:
			b.WriteString(fmt.Sprintf("%d: %s\n", lineNo, trimLong(strings.TrimSpace(raw))))
		case reTSImport.MatchString(line) && depth == 0:
			// Импорты важны для типов (порой они же контракт) — показываем
			// целиком, но не даём раздуться.
			b.WriteString(fmt.Sprintf("%d: %s\n", lineNo, trimLong(strings.TrimSpace(raw))))
		case inClassBody:
			if reMethod.MatchString(line) {
				b.WriteString(fmt.Sprintf("%d: %s // { ... }\n", lineNo, trimLong(strings.TrimSpace(raw))))
			} else if reProp.MatchString(line) {
				b.WriteString(fmt.Sprintf("%d: %s\n", lineNo, trimLong(strings.TrimSpace(raw))))
			}
		}

		depth += db
		// Класс закрылся, если текущая глубина упала ниже его тела.
		for len(classStack) > 0 && depth < classStack[len(classStack)-1] {
			classStack = classStack[:len(classStack)-1]
		}
	}
	return capSkeleton(b.String())
}

// writeContract записывает в builder интерфейс/тип/enum вместе с телом:
// строки со start до закрывающей скобки (возврат к базовой глубине).
// Возвращает индекс первой строки ПОСЛЕ контракта.
func writeContract(b *strings.Builder, lines []string, start, baseDepth int) int {
	cur := baseDepth
	wrote := 0
	for j := start; j < len(lines); j++ {
		t := strings.TrimSpace(lines[j])
		b.WriteString(fmt.Sprintf("%d: %s\n", j+1, trimLong(t)))
		wrote++
		o, c := countBraces(lines[j])
		cur += o - c
		if j > start && cur <= baseDepth {
			break
		}
		if wrote >= 80 {
			b.WriteString(fmt.Sprintf("%d: ... (тело контракта длинное, продолжается)\n", start+wrote+1))
			break
		}
	}
	return start + wrote
}

// contractEndDepth возвращает глубину файла после закрытия контракта,
// начавшегося на строке start (базовая глубина baseDepth до открытия).
func contractEndDepth(lines []string, start, baseDepth int) int {
	cur := baseDepth
	for j := start; j < len(lines); j++ {
		o, c := countBraces(lines[j])
		cur += o - c
		if j > start && cur <= baseDepth {
			return cur
		}
	}
	return cur
}

// countBraces возвращает количество открывающих и закрывающих скобок строки,
// игнорируя строковые литералы, шаблонные строки и линейные комментарии.
func countBraces(s string) (open, close int) {
	inStr := byte(0) // 0 | '\'' | '"' | '`'
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if esc {
			esc = false
			continue
		}
		if c == '\\' && inStr != 0 && inStr != '`' {
			esc = true
			continue
		}
		switch {
		case inStr != 0:
			if c == inStr {
				inStr = 0
			}
		case c == '\'' || c == '"' || c == '`':
			inStr = c
		case i+1 < len(s) && c == '/' && s[i+1] == '/':
			// Линейный комментарий — до конца строки прохода не нужно.
			return open, close
		case c == '{':
			open++
		case c == '}':
			close++
		}
	}
	return open, close
}

// ------------- Fallback: нумерация первых строк -------------

func skeletonFallback(lines []string) string {
	var b strings.Builder
	n := skeletonFallbackLines
	if len(lines) < n {
		n = len(lines)
	}
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%d: %s\n", i+1, trimLong(lines[i]))
	}
	if len(lines) > n {
		fmt.Fprintf(&b, "... (ещё %d строк файла, для фрагментов используй ReadFiles с параметром lines)\n", len(lines)-n)
	}
	return capSkeleton(b.String())
}

// ------------- parseRange: интервалы строк -------------

// parseRange разбирает интервал строк: "N", "N-M", "N-", "N:M" (1-based,
// включительно). Возвращает start/end; end == 0 означает «до конца файла».
// Неверная запись — ok == false.
func parseRange(spec string) (start, end int, ok bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0, 0, false
	}
	sep := strings.IndexAny(spec, ":-")
	if sep < 0 {
		n, err := strconv.Atoi(spec)
		if err != nil || n < 1 {
			return 0, 0, false
		}
		return n, n, true
	}
	var err error
	start, err = strconv.Atoi(strings.TrimSpace(spec[:sep]))
	if err != nil || start < 1 {
		return 0, 0, false
	}
	rest := strings.TrimSpace(spec[sep+1:])
	if rest == "" {
		return start, 0, true
	}
	end, err = strconv.Atoi(rest)
	if err != nil || end < start {
		return 0, 0, false
	}
	return start, end, true
}

// ------------- FileOps: инструмент ReadMap -------------

// ReadMapParams — параметры инструмента ReadMap (список имён файлов).
type ReadMapParams struct {
	Filenames []string `json:"filenames"`
}

// ReadMap читает карты (скелеты) указанных файлов: декларации без тел с
// номерами строк. Компактнее ReadFiles в разы для больших файлов и
// предназначен для быстрой ориентации (лид: декомпозиция; разработчик:
// поиск контрактов перед точечным чтением).
func (ops *FileOps) ReadMap(args map[string]any) ([]byte, error) {
	var params ReadMapParams
	raw, err := json.Marshal(args)
	if err == nil {
		if unmErr := json.Unmarshal(raw, &params); unmErr != nil || len(params.Filenames) == 0 {
			params.Filenames = parsePathList(args["filenames"])
		}
	}
	if len(params.Filenames) == 0 {
		out, _ := json.Marshal(map[string]string{
			"status":  "error",
			"message": "список файлов для чтения карты пуст",
		})
		return out, nil
	}

	result := make([]map[string]string, 0, len(params.Filenames))
	for _, name := range params.Filenames {
		full, rerr := ops.ResolvePath(name)
		if rerr != nil {
			result = append(result, map[string]string{"filename": name, "status": "error", "message": rerr.Error()})
			continue
		}
		if !ops.allowed(ops.relPath(full)) {
			result = append(result, map[string]string{"filename": name, "status": "error", "message": "файл вне области работы (scope)"})
			continue
		}
		src, rerr := os.ReadFile(full)
		if rerr != nil {
			result = append(result, map[string]string{"filename": name, "status": "error", "message": rerr.Error()})
			continue
		}
		result = append(result, map[string]string{
			"filename": name,
			"status":   "success",
			"map":      skeletonizeCached(full, src),
		})
	}
	return json.Marshal(result)
}

// readMapTool — регистрация инструмента ReadMap в реестре.
type readMapTool struct{ ops *FileOps }

func (t *readMapTool) Name() string { return "ReadMap" }

func (t *readMapTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        "ReadMap",
		Description: "Компактная «карта кода» файла: декларации без тел функций (Go: сигнатуры функций/методов, поля структур, методы интерфейсов; TS/JS: interface/type/enum целиком, function/class — только сигнатуры) с номерами строк. Для быстрой ориентации и поиска контрактов ВМЕСТО чтения файлов целиком; точка фрагмента ищется по номерам строк из карты.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"filenames": map[string]any{
					"type":        "array",
					"description": "Список путей к файлам, карты которых нужны, например ['server/internal/domain/user.go', 'frontend/src/api/client.ts']",
					"items":       map[string]any{"type": "string"},
				},
			},
			"required":             []string{"filenames"},
			"additionalProperties": false,
		},
	}
}

func (t *readMapTool) Execute(args map[string]any) ([]byte, error) { return t.ops.ReadMap(args) }

// ------------- FetchContext: дозаправка контекста (on-demand) -------------

// FetchContext подтягивает компактный контекст по цели формата:
//
//	"path/to/file.go"            → карта файла (скелет)
//	"path/to/file.go:20-45"      → строки 20..45 («хирургическое окно»)
//	"path/to/file.go:40"         → строка 40
//	"path/to/file.go 20-45"      → то же через пробел (некоторые модели
//	                               разделяют путь и интервал пробелом)
//
// Используется runner'ом при маркере NEED_CONTEXT/NEED_SIGNATURE/NEED_FILE в
// финальном ответе модели (см. runner.NeedContextTargets). Возвращает false,
// если цель не распознана, файл вне области работы или не читается.
func (ops *FileOps) FetchContext(target string) (string, bool) {
	path, rng := splitRangeTarget(strings.TrimSpace(target))
	if path == "" {
		return "", false
	}

	full, err := ops.ResolvePath(path)
	if err != nil {
		return "", false
	}
	if !ops.allowed(ops.relPath(full)) {
		return "", false
	}
	src, err := os.ReadFile(full)
	if err != nil {
		return "", false
	}

	if rng != "" {
		start, end, ok := parseRange(rng)
		if !ok {
			return "", false
		}
		lines := linesOf(src)
		if start > len(lines) {
			return "", false
		}
		if end == 0 || end > len(lines) {
			end = len(lines)
		}
		var b strings.Builder
		for i := start; i <= end; i++ {
			b.WriteString(fmt.Sprintf("%d: %s\n", i, trimLong(lines[i-1])))
		}
		return b.String(), true
	}

	return skeletonizeCached(full, src), true
}

// splitRangeTarget разделяет цель дозаправки на путь и (опционально) интервал
// строк. Поддерживает "путь:20-45" и "путь 20-45".
func splitRangeTarget(target string) (path, rng string) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", ""
	}
	// Форма "путь<пробел>N[-M]": отделяем интервал пробелом.
	if i := indexRangeSuffix(target); i > 0 {
		return strings.TrimSpace(target[:i]), strings.TrimSpace(target[i:])
	}
	// Форма "путь:N-M" / "путь:N": последнее двоеточие — граница интервала.
	if idx := strings.LastIndexByte(target, ':'); idx > 0 {
		cand := target[idx+1:]
		if _, _, ok := parseRange(cand); ok {
			return target[:idx], cand
		}
	}
	return target, ""
}

// indexRangeSuffix ищет в конце целевой строки интервал вида "N", "N-M", "N-"
// (отделённый пробелом) и возвращает индекс его начала или -1.
func indexRangeSuffix(target string) int {
	lastSpace := strings.LastIndexByte(target, ' ')
	if lastSpace < 0 {
		return -1
	}
	cand := strings.TrimSpace(target[lastSpace+1:])
	if cand == "" {
		return -1
	}
	if _, _, ok := parseRange(cand); ok {
		return lastSpace + 1
	}
	return -1
}