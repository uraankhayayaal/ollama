package tools

// langsyntax.go — поиск границ функций в TS/JS и Python без cgo (Ф-5).
//
// Почему не tree-sitter сразу: привязки требуют cgo и ведут себя по-разному на
// разных платформах, а агентский цикл должен работать без компилятора. Задача
// Ф-5 узкая — найти ОДНУ функцию и заменить ровно её. Для этого хватает лексера,
// который знает строки, комментарии и вложенность скобок; tree-sitter остаётся
// возможным pluggable backend (интерфейс finder это позволяет), но hermetic-путь
// по умолчанию — здесь.
//
// Главное свойство: сканер НЕ переформатирует файл, а возвращает байтовые
// границы [Start, End) функции. Замена идёт splice-ом, поэтому соседний код,
// отступы, порядок импортов и форматирование остаются байт в байт — это и есть
// требование «правка функции без повреждения соседнего кода».
//
// Осознанные границы применимости:
//   - регулярные литералы в JS определяются правилом «предыдущий значимый
//     токен» (после `)`, `]`, идентификатора — деление, иначе литерал). Ошибка
//     здесь сдвинула бы подсчёт скобок, поэтому тесты бьют по `/[{]/` и `/\d{2}/`;
//   - шаблонные строки учитывают вложенность `${...}`;
//   - вложенные функции не мешают: тело ищется балансировкой до нулевой глубины.

import (
	"fmt"
	"strings"
)

// Языки, которые понимает точечная правка функций.
const (
	langGo = "go"
	langTS = "ts"
	langPy = "py"
)

// funcSpan — границы функции в исходнике.
type funcSpan struct {
	Name  string
	Start int    // начало объявления (с учётом декораторов)
	End   int    // позиция сразу за телом
	Owner string // класс-владелец для метода; "" — функция верхнего уровня
	Line  int    // 1-based номер первой строки, для сообщений модели
}

// finder — поиск функций конкретного языка.
type finder interface {
	find(name, owner string) (funcSpan, error)
	names() []string
}

// functionFinder — выбор поисковика по языку. Сканирование выполняется сразу,
// чтобы синтаксическая ошибка в исходнике не маскировалась под «функция не
// найдена».
func functionFinder(lang, src string) (finder, error) {
	switch lang {
	case langTS:
		spans, err := scanTS(src)
		if err != nil {
			return nil, err
		}
		return &tsFinder{spans: spans}, nil
	case langPy:
		spans, err := scanPy(src)
		if err != nil {
			return nil, err
		}
		return &pyFinder{spans: spans}, nil
	}
	return nil, fmt.Errorf("язык %q не поддерживается точечной правкой функций", lang)
}

// langOfFile — язык по расширению файла: единственное место, где решается,
// чем заниматься (Go уходит в gopatch.go).
func langOfFile(path string) string {
	switch {
	case strings.HasSuffix(path, ".go"):
		return langGo
	case strings.HasSuffix(path, ".ts"), strings.HasSuffix(path, ".tsx"),
		strings.HasSuffix(path, ".js"), strings.HasSuffix(path, ".jsx"),
		strings.HasSuffix(path, ".mjs"), strings.HasSuffix(path, ".cjs"):
		return langTS
	case strings.HasSuffix(path, ".py"), strings.HasSuffix(path, ".pyi"):
		return langPy
	}
	return ""
}

// tsFinder — поиск функций в TS/JS.
type tsFinder struct {
	spans []funcSpan
}

// tsNotFunction — идентификаторы, после которых `(` — конструкция языка, а не
// объявление функции. Объявить функцию с таким именем нельзя.
var tsNotFunction = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "catch": true,
	"return": true, "function": true, "do": true, "else": true, "with": true,
	"typeof": true, "new": true, "delete": true, "void": true, "in": true,
	"of": true, "case": true, "await": true, "yield": true, "import": true,
	"export": true, "throw": true,
}

// find — функция по имени и (необязательному) владельцу.
func (f *tsFinder) find(name, owner string) (funcSpan, error) {
	var hits []funcSpan
	for _, sp := range f.spans {
		if sp.Name == name && (owner == "" || sp.Owner == owner) {
			hits = append(hits, sp)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	if len(hits) > 1 {
		return funcSpan{}, fmt.Errorf("функций %s найдено %d (%s) — уточни receiver или переименуй",
			quoteFunc(name, owner), len(hits), strings.Join(ownerNames(hits), ", "))
	}
	return funcSpan{}, notFoundError(name, owner, f.names())
}

func (f *tsFinder) names() []string {
	out := make([]string, 0, len(f.spans))
	for _, sp := range f.spans {
		out = append(out, sp.Name)
	}
	return out
}

// tsClass — открытый класс: имя и позиция сразу за его телом.
type tsClass struct {
	name   string
	closer int
}

// scanTS — обход TS/JS-исходника и сбор границ всех функций.
func scanTS(src string) ([]funcSpan, error) {
	s := &tsScanner{src: src}
	var spans []funcSpan
	var classes []tsClass

	for !s.eof() {
		s.skipSpace()
		if s.eof() {
			break
		}
		if c := s.peek(); c == '"' || c == '\'' || c == '`' {
			if err := s.skipString(); err != nil {
				return nil, err
			}
			continue
		}
		if s.peekIs("/") && (s.peekAt(1) == '/' || s.peekAt(1) == '*') {
			s.skipComment()
			continue
		}

		if s.peekIs("class") && !isIdentPart(s.peekAt(len("class"))) {
			name, closer, err := s.readClass()
			if err != nil {
				return nil, err
			}
			classes = append(classes, tsClass{name: name, closer: closer})
			continue
		}

		if !isIdentStart(s.peek()) {
			s.i++
			continue
		}
		nameStart := s.i
		name := s.readIdent()
		if tsNotFunction[name] {
			continue
		}
		owner := enclosingClass(classes, nameStart)
		// Объектный литерал: const api = { ... } — собираем методы объекта
		// (owner пустой), само объявление как span не нужно.
		saveEq := s.i
		s.skipSpace()
		if s.peek() == '=' {
			s.i++
			s.skipSpace()
			if s.peek() == '{' {
				objSpans, end, err := s.readObjectMethods()
				if err == nil && len(objSpans) > 0 {
					spans = append(spans, objSpans...)
					s.i = end
					continue
				}
			}
		}
		s.i = saveEq
		sp, ok := s.readSignatureBody(name, nameStart, owner)
		if ok {
			spans = append(spans, sp)
			s.i = sp.End
		}
	}
	return spans, nil
}

// readObjectMethods — содержимое объектного литерала: s.i обязан указывать на
// '{'. Собирает методы (name(...) {...} и name = ... => ...) с пустым owner,
// поля и геттеры без сигнатуры пропускает. Возвращает spans и позицию за
// закрывающей '}' литерала; s.i остаётся на '{' (как у readClass).
func (s *tsScanner) readObjectMethods() ([]funcSpan, int, error) {
	save := s.i
	end, err := s.matchBraceBlock()
	if err != nil {
		return nil, 0, err
	}
	s.i = save + 1
	var objSpans []funcSpan
	for !s.eof() {
		s.skipSpace()
		if s.eof() || s.peek() == '}' {
			break
		}
		if !isIdentStart(s.peek()) {
			s.i++
			continue
		}
		nameStart := s.i
		name := s.readIdent()
		if tsNotFunction[name] {
			continue
		}
		sp, ok := s.readSignatureBody(name, nameStart, "")
		if ok {
			objSpans = append(objSpans, sp)
			s.i = sp.End
		}
	}
	return objSpans, end, nil
}

// enclosingClass — ближайший класс, тело которого ещё не закончилось на позиции
// pos. Анонимные объявления (типы, литералы) владельца не дают: иначе сигнатура
// анонимной функции внутри метода приписывалась бы классу, которого там нет.
func enclosingClass(classes []tsClass, pos int) string {
	for i := len(classes) - 1; i >= 0; i-- {
		if pos < classes[i].closer {
			return classes[i].name
		}
	}
	return ""
}

// readSignatureBody — после уже прочитанного имени читает сигнатуру и тело.
// Обрабатывает объявления `name(...)`, методы, поля-стрелки `name = (...) =>`
// и `name: (...) =>`.
func (s *tsScanner) readSignatureBody(name string, nameStart int, owner string) (funcSpan, bool) {
	save := s.i
	s.skipSpace()

	// Стрелочное присваивание: `name = ...` / `name: (...) =>`.
	if s.peek() == '=' || (s.peek() == ':' && s.arrowFollows()) {
		s.i++
		s.skipSpace()
		for _, kw := range []string{"async", "function"} {
			if s.peekIs(kw) && !isIdentPart(s.peekAt(len(kw))) {
				s.i += len(kw)
				s.skipSpace()
			}
		}
		// Стрелка с сигнатурой: const f = (params): RetType =>
		if s.peek() == '(' {
			if _, _, err := s.matchPair('(', ')'); err != nil {
				s.i = save
				return funcSpan{}, false
			}
			s.skipSpace()
			if s.peek() == ':' {
				for !s.eof() && s.peek() != '{' && !s.peekIs("=>") && s.peek() != ';' {
					s.i++
				}
				s.skipSpace()
			}
		}
		if sp, ok := s.readBody(name, nameStart, owner); ok {
			return sp, true
		}
		s.i = save
		return funcSpan{}, false
	}

	// Сигнатура с параметрами: `name(...) {` — функция, метод, getter.
	if s.peek() == '(' {
		if _, _, err := s.matchPair('(', ')'); err != nil {
			s.i = save
			return funcSpan{}, false
		}
		s.skipSpace()
		// Тип возврата TS: до `{` или `=>`.
		if s.peek() == ':' {
			for !s.eof() && s.peek() != '{' && !s.peekIs("=>") && s.peek() != ';' {
				s.i++
			}
			s.skipSpace()
		}
		if sp, ok := s.readBody(name, nameStart, owner); ok {
			return sp, true
		}
	}
	s.i = save
	return funcSpan{}, false
}

// arrowFollows — после `:` действительно идёт стрелка (тип свойства), а не
// объектный литерал или `case x:`.
func (s *tsScanner) arrowFollows() bool {
	rest := s.src[s.i+1:]
	trimmed := strings.TrimLeft(rest, " \t\r\n")
	return strings.HasPrefix(trimmed, "(") || strings.HasPrefix(trimmed, "async") ||
		strings.HasPrefix(trimmed, "function")
}

// readBody — читает тело: `{...}` либо одно-выражное тело стрелки.
func (s *tsScanner) readBody(name string, nameStart int, owner string) (funcSpan, bool) {
	if s.peekIs("=>") {
		s.i += 2
		s.skipSpace()
		if s.peek() == '{' {
			end, err := s.matchBraceBlock()
			if err != nil {
				return funcSpan{}, false
			}
			return s.spanOf(name, nameStart, end, owner), true
		}
		// Одно-выражное тело: до `;`/`,`/переноса строки на нулевой глубине.
		start := s.i
		for !s.eof() {
			c := s.peek()
			if c == '{' || c == '(' || c == '[' {
				if _, _, err := s.matchPair(c, s.closerFor(c)); err != nil {
					return funcSpan{}, false
				}
				continue
			}
			if c == ';' || c == ',' || c == '}' || c == '\n' {
				break
			}
			s.i++
		}
		end := s.trimTrailingSpace(s.i)
		if end <= start {
			return funcSpan{}, false
		}
		s.i = end
		return s.spanOf(name, nameStart, end, owner), true
	}
	if s.peek() == '{' {
		end, err := s.matchBraceBlock()
		if err != nil {
			return funcSpan{}, false
		}
		return s.spanOf(name, nameStart, end, owner), true
	}
	return funcSpan{}, false
}

// readClass — читает заголовок `class Name ... {` и возвращает имя и позицию
// за закрывающей `}` класса. s.i возвращается на '{': тело класса дальше
// обрабатывается обычным циклом scanTS, поэтому методы собираются как spans
// с owner=класс (enclosingClass ограничивает их closer класса).
func (s *tsScanner) readClass() (string, int, error) {
	s.i += len("class")
	s.skipSpace()
	name := ""
	if isIdentStart(s.peek()) {
		name = s.readIdent()
	}
	for !s.eof() && s.peek() != '{' {
		s.i++
	}
	if s.eof() {
		return "", 0, fmt.Errorf("не найдено тело класса %q", name)
	}
	save := s.i
	end, err := s.matchBraceBlock()
	if err != nil {
		return "", 0, err
	}
	s.i = save
	return name, end, nil
}

// spanOf — границы объявления с поправкой на префиксы и декораторы над ним.
func (s *tsScanner) spanOf(name string, nameStart, end int, owner string) funcSpan {
	start := s.declPrefixStart(nameStart)
	return funcSpan{
		Name:  name,
		Start: start,
		End:   end,
		Owner: owner,
		Line:  strings.Count(s.src[:start], "\n") + 1,
	}
}

// declPrefixStart — расширяет начало объявления вверх, включая префиксы
// (export, export default, async, function, const/let/var) и строки-декораторы
// (@Component({...}), @Override): иначе префикс остался бы над чужой функцией
// или отрезал бы часть объявления при замене.
func (s *tsScanner) declPrefixStart(nameStart int) int {
	best := nameStart
	for nameStart > 0 {
		lineStart := strings.LastIndex(s.src[:nameStart], "\n") + 1
		if lineStart == nameStart {
			nameStart--
			continue
		}
		prefix := strings.TrimSpace(s.src[lineStart:nameStart])
		if !isDeclPrefix(prefix) {
			break
		}
		best = lineStart
		nameStart = lineStart
	}
	return best
}

// isDeclPrefix — строка является префиксом объявления функции: модификаторы
// (export, default, async, function), объявление переменной (const/let/var)
// или декоратор (@...). Пустая строка тоже префикс: объявление может быть
// многострочным (export\nfunction f() {}).
func isDeclPrefix(line string) bool {
	if line == "" {
		return true
	}
	if strings.HasPrefix(line, "@") {
		return true
	}
	switch line {
	case "export", "export default", "export async", "export function",
		"export default async", "export default function",
		"export async function", "export default async function",
		"async", "async function", "function",
		"const", "let", "var":
		return true
	}
	return false
}

func (s *tsScanner) trimTrailingSpace(i int) int {
	for i > 0 {
		c := s.src[i-1]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			i--
			continue
		}
		break
	}
	return i
}

// tsScanner — обход TS/JS с пропуском литералов.
type tsScanner struct {
	src string
	i   int
}

func (s *tsScanner) eof() bool { return s.i >= len(s.src) }

func (s *tsScanner) peek() byte {
	if s.eof() {
		return 0
	}
	return s.src[s.i]
}

func (s *tsScanner) peekAt(off int) byte {
	if s.i+off >= len(s.src) || s.i+off < 0 {
		return 0
	}
	return s.src[s.i+off]
}

func (s *tsScanner) peekIs(tok string) bool { return strings.HasPrefix(s.src[s.i:], tok) }

func (s *tsScanner) readIdent() string {
	start := s.i
	for !s.eof() && isIdentPart(s.peek()) {
		s.i++
	}
	return s.src[start:s.i]
}

func (s *tsScanner) closerFor(open byte) byte {
	switch open {
	case '(':
		return ')'
	case '[':
		return ']'
	case '{':
		return '}'
	}
	return 0
}

// skipSpace — пробелы, переводы строк и комментарии.
func (s *tsScanner) skipSpace() {
	for !s.eof() {
		c := s.peek()
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			s.i++
		case c == '/' && s.peekAt(1) == '/':
			s.skipComment()
		case c == '/' && s.peekAt(1) == '*':
			s.skipComment()
		default:
			return
		}
	}
}

// matchPair — парная скобка с учётом вложенных литералов и комментариев.
// Возвращает границы пары [Start, End).
func (s *tsScanner) matchPair(open, close byte) (int, int, error) {
	start := s.i
	depth := 0
	for !s.eof() {
		c := s.peek()
		switch {
		case c == '"' || c == '\'' || c == '`':
			if err := s.skipString(); err != nil {
				return 0, 0, err
			}
			continue
		case c == '/' && (s.peekAt(1) == '/' || s.peekAt(1) == '*'):
			s.skipComment()
			continue
		case c == '/' && s.regexAllowedHere():
			if err := s.skipRegex(); err != nil {
				return 0, 0, err
			}
			continue
		case c == open:
			depth++
		case c == close:
			depth--
			if depth == 0 {
				s.i++
				return start, s.i, nil
			}
		}
		s.i++
	}
	return 0, 0, fmt.Errorf("не найдена парная %q начиная с позиции %d", string(close), start)
}

func (s *tsScanner) matchBraceBlock() (int, error) {
	_, end, err := s.matchPair('{', '}')
	return end, err
}

// tsRegexAllowedAfter — значимые токены, после которых `/` открывает регулярное
// выражение (классическое правило лексера JS).
var tsRegexAllowedAfter = map[string]bool{
	"return": true, "typeof": true, "case": true, "in": true, "of": true,
	"new": true, "delete": true, "void": true, "instanceof": true,
	"do": true, "else": true, "yield": true, "await": true, "throw": true,
}

var tsRegexAllowedPunct = map[byte]bool{
	'(': true, ',': true, '=': true, ':': true, '[': true, '!': true,
	'&': true, '|': true, '?': true, '{': true, '}': true, ';': true,
	'+': true, '-': true, '*': true, '%': true, '<': true, '>': true, '~': true,
}

// regexAllowedHere — открывает ли текущий `/` регулярный литерал. Решение по
// ПРЕДЫДУЩЕМУ значимому токену: смотрим назад от позиции, пропуская пробелы и
// комментарии, поэтому состояние сканера не может «протухнуть».
func (s *tsScanner) regexAllowedHere() bool {
	i := s.i - 1
	for i >= 0 {
		c := s.src[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			i--
			continue
		}
		if isIdentPart(c) {
			start := i
			for i >= 0 && isIdentPart(s.src[i]) {
				i--
			}
			return tsRegexAllowedAfter[s.src[i+1:start+1]]
		}
		return tsRegexAllowedPunct[c]
	}
	// Начало файла: литерал допустим.
	return true
}

// skipString — строковый литерал, включая шаблонные строки с вложенными
// интерполяциями `${...}`.
func (s *tsScanner) skipString() error {
	quote := s.peek()
	s.i++
	if quote == '`' {
		depth := 0
		for !s.eof() {
			c := s.peek()
			switch {
			case c == '\\':
				s.i += 2
				continue
			case depth == 0 && c == '`':
				s.i++
				return nil
			case depth == 0 && c == '$' && s.peekAt(1) == '{':
				depth++
				s.i += 2
				continue
			case depth > 0 && c == '`':
				if err := s.skipString(); err != nil {
					return err
				}
				continue
			case depth > 0 && c == '{':
				depth++
			case depth > 0 && c == '}':
				depth--
			}
			s.i++
		}
		return fmt.Errorf("незакрытая шаблонная строка")
	}
	for !s.eof() {
		c := s.peek()
		if c == '\\' {
			s.i += 2
			continue
		}
		if c == quote {
			s.i++
			return nil
		}
		if c == '\n' {
			return fmt.Errorf("незакрытая строковая константа")
		}
		s.i++
	}
	return fmt.Errorf("незакрытая строковая константа")
}

// skipRegex — регулярный литерал с классами символов и экранированием.
func (s *tsScanner) skipRegex() error {
	s.i++
	inClass := false
	for !s.eof() {
		c := s.peek()
		switch {
		case c == '\\':
			s.i += 2
			continue
		case c == '[':
			inClass = true
		case c == ']':
			inClass = false
		case c == '/' && !inClass:
			s.i++
			for !s.eof() && isIdentPart(s.peek()) {
				s.i++
			}
			return nil
		case c == '\n':
			return fmt.Errorf("незакрытый регулярный литерал")
		}
		s.i++
	}
	return fmt.Errorf("незакрытый регулярный литерал")
}

func (s *tsScanner) skipComment() {
	if s.peekAt(1) == '/' {
		for !s.eof() && s.peek() != '\n' {
			s.i++
		}
		return
	}
	s.i += 2
	for !s.eof() && !(s.peek() == '*' && s.peekAt(1) == '/') {
		s.i++
	}
	if !s.eof() {
		s.i += 2
	}
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// pyFinder — поиск функций в Python по отступам.
type pyFinder struct {
	spans []funcSpan
}

// pyLine — строка с отступом и границами.
type pyLine struct {
	start  int
	end    int
	text   string
	indent int
	blank  bool
}

func (f *pyFinder) find(name, owner string) (funcSpan, error) {
	var hits []funcSpan
	for _, sp := range f.spans {
		if sp.Name == name && (owner == "" || sp.Owner == owner) {
			hits = append(hits, sp)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	if len(hits) > 1 {
		return funcSpan{}, fmt.Errorf("функций %s найдено %d (%s) — уточни receiver или переименуй",
			quoteFunc(name, owner), len(hits), strings.Join(ownerNames(hits), ", "))
	}
	return funcSpan{}, notFoundError(name, owner, f.names())
}

func (f *pyFinder) names() []string {
	out := make([]string, 0, len(f.spans))
	for _, sp := range f.spans {
		out = append(out, sp.Name)
	}
	return out
}

// scanPy — обход Python по отступам. Границы функции: от строки `def` (или
// декораторов над ней) до первой непустой строки с отступом <= отступа `def`.
// Так тело не может «съесть» соседний код, а хвостовые пустые строки в замену
// не попадают.
func scanPy(src string) ([]funcSpan, error) {
	lines := splitPyLines(src)
	var spans []funcSpan
	// Классы-владельцы: отступ класса и позиция конца тела.
	type pyClass struct {
		name   string
		indent int
		end    int
	}
	var classes []pyClass

	for idx := 0; idx < len(lines); idx++ {
		ln := lines[idx]
		if ln.blank {
			continue
		}
		trimmed := strings.TrimSpace(ln.text)
		kind, name, ok := pyDeclKind(trimmed)
		if !ok {
			continue
		}
		// Конец тела — первая непустая строка с отступом <= отступа объявления.
		end := len(src)
		for j := idx + 1; j < len(lines); j++ {
			next := lines[j]
			if next.blank {
				continue
			}
			if next.indent <= ln.indent {
				end = next.start
				break
			}
		}
		if end == len(src) {
			// Тело до конца файла: обрезаем хвостовые пустые строки.
			end = trimEndWS(src, end)
		} else {
			// Хвостовые пустые строки перед следующим объявлением — не часть
			// функции: заканчиваем на последней непустой строке тела.
			for j := idx + 1; j < len(lines); j++ {
				if lines[j].start >= end {
					break
				}
				if !lines[j].blank {
					end = lines[j].end
				}
			}
		}
		start := ln.start
		// Декораторы над объявлением входят в границы: иначе `@decorator`
		// остался бы над чужой функцией.
		for k := idx - 1; k >= 0; k-- {
			if lines[k].blank {
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(lines[k].text), "@") && lines[k].indent == ln.indent {
				start = lines[k].start
				continue
			}
			break
		}
		owner := ""
		for i := len(classes) - 1; i >= 0; i-- {
			if classes[i].indent < ln.indent && start < classes[i].end {
				owner = classes[i].name
				break
			}
		}
		if kind == "class" {
			classes = append(classes, pyClass{name: name, indent: ln.indent, end: end})
			continue
		}
		spans = append(spans, funcSpan{
			Name:  name,
			Start: start,
			End:   end,
			Owner: owner,
			Line:  strings.Count(src[:start], "\n") + 1,
		})
	}
	return spans, nil
}

// pyDeclKind — вид объявления в строке: "def" или "class".
func pyDeclKind(trimmed string) (kind, name string, ok bool) {
	rest := trimmed
	if strings.HasPrefix(rest, "async ") {
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "async "))
	}
	for _, kw := range []string{"def ", "class "} {
		if strings.HasPrefix(rest, kw) {
			rest = strings.TrimSpace(rest[len(kw):])
			name = pyDeclName(rest)
			if name == "" {
				return "", "", false
			}
			return strings.TrimSpace(kw), name, true
		}
	}
	return "", "", false
}

// pyDeclName — имя из заголовка объявления: до `(` или `:`.
func pyDeclName(rest string) string {
	end := len(rest)
	if i := strings.IndexByte(rest, '('); i >= 0 && i < end {
		end = i
	}
	if i := strings.IndexByte(rest, ':'); i >= 0 && i < end {
		end = i
	}
	name := strings.TrimSpace(rest[:end])
	if name == "" || !isPyIdent(name) {
		return ""
	}
	return name
}

func isPyIdent(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (i > 0 && c >= '0' && c <= '9')
		if !ok {
			return false
		}
	}
	return len(s) > 0
}

// splitPyLines — строки с отступом и границами. Табы считаем за ширину 1: у
// смешанных отступов Python всё равно не запустится, а наш подсчёт не должен
// молча расходиться с интерпретатором.
func splitPyLines(src string) []pyLine {
	var out []pyLine
	start := 0
	for i := 0; i <= len(src); i++ {
		if i == len(src) || src[i] == '\n' {
			text := src[start:i]
			trimmed := strings.TrimLeft(text, " \t")
			out = append(out, pyLine{
				start:  start,
				end:    i,
				text:   text,
				indent: len(text) - len(trimmed),
				blank:  strings.TrimSpace(text) == "",
			})
			start = i + 1
		}
	}
	// Хвостовой пустой элемент после последнего перевода строки не нужен.
	if n := len(out); n > 0 && out[n-1].text == "" {
		out = out[:n-1]
	}
	return out
}

func trimEndWS(src string, end int) int {
	for end > 0 {
		c := src[end-1]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			end--
			continue
		}
		break
	}
	return end
}

// notFoundError — сообщение с перечнем найденных функций: по нему модель сразу
// видит опечатку в имени или receiver.
func notFoundError(name, owner string, have []string) error {
	msg := fmt.Sprintf("функция %q не найдена", name)
	if owner != "" {
		msg += " у владельца " + quoteFunc(owner, "")
	}
	if len(have) > 0 {
		msg += "; найдены: " + strings.Join(have, ", ")
	}
	return fmt.Errorf("%s", msg)
}

func quoteFunc(name, owner string) string {
	if owner != "" {
		return owner + "." + name
	}
	return name
}

func ownerNames(spans []funcSpan) []string {
	out := make([]string, 0, len(spans))
	for _, sp := range spans {
		if sp.Owner != "" {
			out = append(out, sp.Owner+"."+sp.Name)
			continue
		}
		out = append(out, sp.Name)
	}
	return out
}
