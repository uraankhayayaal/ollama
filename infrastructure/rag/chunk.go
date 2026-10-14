// Модуль структурной нарезки кода RAG (см. PLAN-2026-09-19-done-qdrant.md, Ф-2).
//
// Решение пользователя №3: чанки — законченные функции/методы/структуры со
// всеми сопутствующими комментариями (лимит ~50-100 строк), а не «слепая»
// резка по символам. Go разбирается через go/ast, TS/JS и Python — по
// маркерам определений, всё остальное откатывается на линейную нарезку.

package rag

import (
	"go/ast"
	"go/parser"
	"go/token"
	"hash/fnv"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Chunk — непрерывный фрагмент кода файла с координатами в исходнике.
type Chunk struct {
	Content   string
	StartLine int // 1-based, включительно
	EndLine   int // 1-based, включительно
	// Symbol — имя определения, которому принадлежит фрагмент (функция,
	// метод, тип, класс); пусто, если определение не распознано (линейная
	// нарезка, обычный текст). Используется для стабильного chunk_id чанка
	// между коммитами (см. ChunkID и PLAN-2026-09-27-done-branch-aware-rag.md, Р-2).
	Symbol string
}

// maxChunkLines — предельное число строк в одном чанке. Длинные функции/
// методы режутся на части по строкам (границы без разрыва середины строки).
const maxChunkLines = 100

// ChunkFile нарезает содержимое файла на структурные куски по расширению:
//   - Go — через go/ast (функции/методы/структуры/интерфейсы с doc-комментариями);
//   - TS/JS — по маркерам определений и балансу фигурных скобок;
//   - Python — по def/class и отступам;
//   - прочее — линейная нарезка с лимитом maxChunkLines.
func ChunkFile(filename, content string) []Chunk {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".go":
		return chunkGoFile(filename, content)
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		return chunkBracedFile(content)
	case ".py", ".pyi":
		return chunkPythonFile(content)
	default:
		return chunkLines(content)
	}
}

// chunkGoFile нарезает Go-файл по объявлениям: функции/методы (FuncDecl),
// типы (type — структуры/интерфейсы/алиасы) и верхнеуровневые var/const.
// Import-блоки не индексируются (шум без кода). Каждый чанк включает
// сопутствующие doc-комментарии.
func chunkGoFile(filename, content string) []Chunk {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, content, parser.ParseComments)
	if err != nil {
		// Какой-то код не парсится (частичные куски, битые билд-теги):
		// фолбэк на линейную нарезку, чтобы файл не выпадал из индекса.
		return chunkLines(content)
	}

	lines := strings.Split(content, "\n")
	var chunks []Chunk
	add := func(startL, endL int, symbol string) {
		if startL < 1 {
			startL = 1
		}
		if endL > len(lines) {
			endL = len(lines)
		}
		if startL > endL {
			return
		}
		text := strings.Join(lines[startL-1:endL], "\n")
		if strings.TrimSpace(text) == "" {
			return
		}
		chunks = append(chunks, splitChunk(text, startL, symbol)...)
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			startL := fset.Position(d.Pos()).Line
			if d.Doc != nil {
				startL = fset.Position(d.Doc.Pos()).Line
			}
			add(startL, fset.Position(d.End()).Line, goFuncSymbol(d))
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				continue
			}
			startL := fset.Position(d.Pos()).Line
			if d.Doc != nil {
				startL = fset.Position(d.Doc.Pos()).Line
			}
			add(startL, fset.Position(d.End()).Line, goDeclSymbol(d))
		}
	}
	return chunks
}

// goFuncSymbol — имя функции/метода Go: "Sum" для функции, "(*Server).Handle"
// для метода (тип receivers даёт уникальность имён методов). Функции-литералы
// без имени вклада в символ не дают — пусто.
func goFuncSymbol(d *ast.FuncDecl) string {
	if d == nil || d.Name == nil {
		return ""
	}
	name := d.Name.Name
	if d.Recv != nil && len(d.Recv.List) > 0 {
		recv := goRecvType(d.Recv.List[0].Type)
		if recv != "" {
			return recv + "." + name
		}
	}
	return name
}

// goDeclSymbol — имя первого объявления в group-декларации (type/var/const).
func goDeclSymbol(d *ast.GenDecl) string {
	if d == nil {
		return ""
	}
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if s.Name != nil {
				return s.Name.Name
			}
		case *ast.ValueSpec:
			if len(s.Names) > 0 {
				return s.Names[0].Name
			}
		}
	}
	return ""
}

// goRecvType — текстовый вид типа receivers метода ("Server" для значений,
// "*Server" для указателей).
func goRecvType(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		if inner := goRecvType(t.X); inner != "" {
			return "*" + inner
		}
	case *ast.IndexExpr: // обобщённый получатель T[P]
		return goRecvType(t.X)
	case *ast.IndexListExpr:
		return goRecvType(t.X)
	}
	return ""
}

// chunkBracedFile нарезает TS/JS/JSX-файл: границами служат определения
// (function/class/interface/type/enum и const-стрелочные функции) и методы
// в теле классов. Глубина скобок считается с учётом строковых литералов,
// чтобы `{` внутри строки/шаблона не ломали баланс.
func chunkBracedFile(content string) []Chunk {
	lines := strings.Split(content, "\n")

	var starts []int
	depth := 0
	for i, raw := range lines {
		t := strings.TrimSpace(raw)
		if t != "" && (tsDefRe.MatchString(t) || (depth > 0 && tsMethodRe.MatchString(t) && !tsControlRe.MatchString(t))) {
			starts = append(starts, i)
		}
		depth += braceDelta(raw)
		if depth < 0 {
			depth = 0
		}
	}
	if len(starts) == 0 {
		return chunkLines(content)
	}

	return segmentChunks(lines, starts, isTSCommentLine, func(i int) string { return tsSymbol(i, lines) })
}

// segmentChunks склеивает области между стартовыми строками в чанки:
// границы расширяются вверх по комментариям/декораторам (comment), между
// границами идёт линейная нарезка с лимитом maxChunkLines. Блок до первой
// границы (шапка файла, импорты) становится отдельным чанком. Символ чанка
// берётся из строки-границы (symbol по индексу строки).
func segmentChunks(lines []string, starts []int, comment func(string) bool, symbol func(int) string) []Chunk {
	bounds := append([]int{0}, starts...)
	bounds = append(bounds, len(lines))

	var chunks []Chunk
	for k := 0; k+1 < len(bounds); k++ {
		s := bounds[k]
		e := bounds[k+1]
		if k > 0 {
			s = extendUp(lines, s, bounds[k-1], comment)
		}
		if s >= e {
			continue
		}
		text := strings.Join(lines[s:e], "\n")
		if strings.TrimSpace(text) == "" {
			continue
		}
		sym := ""
		if symbol != nil {
			sym = symbol(bounds[k])
		}
		chunks = append(chunks, splitChunk(text, s+1, sym)...)
	}
	return chunks
}

// extendUp тянет границу вверх: поглощает подряд идущие строки-комментарии
// (и пустые строки между ними и кодом), не пересекая низ low.
func extendUp(lines []string, start, low int, comment func(string) bool) int {
	seen := false
	for i := start - 1; i > low; i-- {
		t := strings.TrimSpace(lines[i])
		switch {
		case t == "" && seen:
			start = i // пустая строка между комментарием и кодом
		case comment(t):
			start = i
			seen = true
		case t == "":
			continue
		default:
			return start
		}
	}
	return start
}

// chunkPythonFile нарезает Python-файл по def/class. Верхняя граница
// чанка расширяется на декораторы (@...) и комментарии над определением;
// область определения — до следующего def/class на том же уровне отступа.
func chunkPythonFile(content string) []Chunk {
	lines := strings.Split(content, "\n")

	var starts []int
	for i, raw := range lines {
		if pyDefRe.MatchString(strings.TrimSpace(raw)) {
			starts = append(starts, i)
		}
	}
	if len(starts) == 0 {
		return chunkLines(content)
	}
	return segmentChunks(lines, starts, isPyCommentLine, func(i int) string { return pySymbol(i, lines) })
}

// chunkLines нарезает произвольный текст на строки не длиннее maxChunkLines.
func chunkLines(content string) []Chunk {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return nil
	}
	var out []Chunk
	for s := 0; s < len(lines); s += maxChunkLines {
		e := s + maxChunkLines
		if e > len(lines) {
			e = len(lines)
		}
		out = append(out, Chunk{Content: strings.Join(lines[s:e], "\n"), StartLine: s + 1, EndLine: e})
	}
	return out
}

// splitChunk режет чанк на куски по maxChunkLines строк (без разрыва строки).
// Короткий текст возвращается одним чанком. Если чанк длиннее лимита, символ
// получают только части, кроме первой (у части есть собственный порядковый
// номер) — иначе части одного определения получили бы одинаковый chunk_id.
func splitChunk(text string, startL int, symbol string) []Chunk {
	lines := strings.Split(text, "\n")
	if len(lines) <= maxChunkLines {
		return []Chunk{{Content: text, StartLine: startL, EndLine: startL + len(lines) - 1, Symbol: symbol}}
	}
	var out []Chunk
	for s := 0; s < len(lines); s += maxChunkLines {
		e := s + maxChunkLines
		if e > len(lines) {
			e = len(lines)
		}
		sym := symbol
		if n := s / maxChunkLines; n > 0 {
			sym = symbol + "@" + strconv.Itoa(n)
		}
		out = append(out, Chunk{
			Content:   strings.Join(lines[s:e], "\n"),
			StartLine: startL + s,
			EndLine:   startL + e - 1,
			Symbol:    sym,
		})
	}
	return out
}

// Маркеры определений скобочных языков.
var (
	// tsDefRe — начало определения: function/class/interface/type/enum либо
	// присваивание стрелочной функции (const f = (...) => …).
	tsDefRe = regexp.MustCompile(
		`^(?:export\s+|declare\s+|default\s+|abstract\s+)*(?:async\s+)?(?:function|class|interface|type|enum)\b` +
			`|^(?:export\s+)?(?:async\s+)?(?:const|let|var)\s+[A-Za-z_$][\w$]*\s*=\s*(?:\([^)]*\)[^=]*=>|[\w.]+=>)`,
	)
	// tsMethodRe — метод внутри класса: name(args): … {.
	tsMethodRe = regexp.MustCompile(`^(?:async\s+|get\s+|set\s+)*[A-Za-z_$][\w$]*\s*\(.*\)\s*\{`)
	// tsControlRe — конструкции вида name(...) {, которые не являются методами.
	tsControlRe = regexp.MustCompile(`^(?:if|for|while|switch|catch|try|return|typeof|instanceof)\b`)
	// tsSymbolRe — имя определения TS/JS в любой из поддерживаемых форм:
	// function/class/interface/type/enum, стрелочная константа, метод.
	tsSymbolRe = regexp.MustCompile(
		`^(?:export\s+|declare\s+|default\s+|abstract\s+)*(?:async\s+)?(?:function|class|interface|type|enum)\s+([A-Za-z_$][\w$]*)` +
			`|^(?:export\s+)?(?:async\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)` +
			`|^(?:async\s+|get\s+|set\s+)*([A-Za-z_$][\w$]*)\s*\(`,
	)
	// pyDefRe — определение Python: def/class (в т.ч. async def).
	pyDefRe = regexp.MustCompile(`^(?:async\s+)?(?:def|class)\s+\w`)
	// pySymbolRe — имя определения Python: def/class + имя.
	pySymbolRe = regexp.MustCompile(`^(?:async\s+)?(?:def|class)\s+([A-Za-z_][\w]*)`)
)

// tsSymbol — имя TS/JS-определения по строке-границе (пусто, если строка не
// распознана как определение или метод). Имя получает ТОЛЬКО определение или
// метод класса: вызов вида doWork() на верхнем уровне символом не становится —
// иначе два таких вызова делили бы один chunk_id и один вытеснил бы другой из
// индекса (чанк считается заменённым).
func tsSymbol(line int, lines []string) string {
	if line < 0 || line >= len(lines) {
		return ""
	}
	t := strings.TrimSpace(lines[line])
	if !tsDefRe.MatchString(t) && !(tsMethodRe.MatchString(t) && !tsControlRe.MatchString(t)) {
		return ""
	}
	m := tsSymbolRe.FindStringSubmatch(t)
	if m == nil {
		return ""
	}
	for _, g := range m[1:] {
		if g != "" {
			return g
		}
	}
	return ""
}

// pySymbol — имя Python-определения по строке-границе.
func pySymbol(line int, lines []string) string {
	if line < 0 || line >= len(lines) {
		return ""
	}
	if m := pySymbolRe.FindStringSubmatch(strings.TrimSpace(lines[line])); m != nil {
		return m[1]
	}
	return ""
}

// ChunkID — детерминированный идентификатор чанка (PLAN-2026-09-27-
// branch-aware-rag.md, Р-2): FNV-1a 64 по проекту, файлу и символу. Один и
// тот же символ даёт один chunk_id во всех коммитах и ветках — по нему
// индексатор помечает прежние версии чанка устаревшими (replaced_by), а поиск
// по ветке исключает перекрытые чанки main. Без символа (линейная нарезка)
// вызывающая сторона передаёт ключ по начальной строке (см. chunkKey).
func ChunkID(project, relPath, symbol string) string {
	h := fnv.New64a()
	h.Write([]byte(project))
	h.Write([]byte{0})
	h.Write([]byte(relPath))
	h.Write([]byte{0})
	h.Write([]byte(symbol))
	return strconv.FormatUint(h.Sum64(), 16)
}

// chunkKey — ключ идентичности чанка для ChunkID: символ определения, а без
// него (линейная нарезка/текст) — начальная строка файла.
func chunkKey(ch Chunk) string {
	if sym := strings.TrimSpace(ch.Symbol); sym != "" {
		return sym
	}
	return "L" + strconv.Itoa(ch.StartLine)
}

// braceDelta считает изменение глубины фигурных скобок в строке, игнорируя
// скобки внутри строковых литералов, шаблонов и строковых комментариев.
func braceDelta(line string) int {
	delta := 0
	inStr := byte(0)
	esc := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if esc {
			esc = false
			continue
		}
		if inStr != 0 {
			if c == '\\' && inStr == '"' {
				esc = true
			} else if c == inStr {
				inStr = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			inStr = c
		case '{':
			delta++
		case '}':
			delta--
		case '/':
			if i+1 < len(line) && line[i+1] == '/' {
				return delta
			}
		}
	}
	return delta
}

// isTSCommentLine — строка-комментарий или JSDoc в TS/JS.
func isTSCommentLine(t string) bool {
	return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "*")
}

// isPyCommentLine — строка-комментарий или декоратор в Python.
func isPyCommentLine(t string) bool {
	return strings.HasPrefix(t, "#") || strings.HasPrefix(t, "@")
}
