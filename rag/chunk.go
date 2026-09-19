// Модуль структурной нарезки кода RAG (см. PLAN-qdrant.md, Ф-2).
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
	"path/filepath"
	"regexp"
	"strings"
)

// Chunk — непрерывный фрагмент кода файла с координатами в исходнике.
type Chunk struct {
	Content   string
	StartLine int // 1-based, включительно
	EndLine   int // 1-based, включительно
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
	add := func(startL, endL int) {
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
		chunks = append(chunks, splitChunk(text, startL)...)
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			startL := fset.Position(d.Pos()).Line
			if d.Doc != nil {
				startL = fset.Position(d.Doc.Pos()).Line
			}
			add(startL, fset.Position(d.End()).Line)
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				continue
			}
			startL := fset.Position(d.Pos()).Line
			if d.Doc != nil {
				startL = fset.Position(d.Doc.Pos()).Line
			}
			add(startL, fset.Position(d.End()).Line)
		}
	}
	return chunks
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

	return segmentChunks(lines, starts, isTSCommentLine)
}

// segmentChunks склеивает области между стартовыми строками в чанки:
// границы расширяются вверх по комментариям/декораторам (comment), между
// границами идёт линейная нарезка с лимитом maxChunkLines. Блок до первой
// границы (шапка файла, импорты) становится отдельным чанком.
func segmentChunks(lines []string, starts []int, comment func(string) bool) []Chunk {
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
		chunks = append(chunks, splitChunk(text, s+1)...)
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
	return segmentChunks(lines, starts, isPyCommentLine)
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
// Короткий текст возвращается одним чанком.
func splitChunk(text string, startL int) []Chunk {
	lines := strings.Split(text, "\n")
	if len(lines) <= maxChunkLines {
		return []Chunk{{Content: text, StartLine: startL, EndLine: startL + len(lines) - 1}}
	}
	var out []Chunk
	for s := 0; s < len(lines); s += maxChunkLines {
		e := s + maxChunkLines
		if e > len(lines) {
			e = len(lines)
		}
		out = append(out, Chunk{Content: strings.Join(lines[s:e], "\n"), StartLine: startL + s, EndLine: startL + e - 1})
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
	// pyDefRe — определение Python: def/class (в т.ч. async def).
	pyDefRe = regexp.MustCompile(`^(?:async\s+)?(?:def|class)\s+\w`)
)

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