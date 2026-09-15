package tools

// Семантическое патчение Go-кода через go/ast. Механизм изолирует правку
// субагента-разработчика в рамках ОДНОЙ функции: агент возвращает в JSON
// только тело конкретной функции (полный func-decl), а backend находит её
// узел в AST, заменяет именно его и форматирует файл через go/format.
// Всё остальное — другие функции, импорты, структура файла — остаётся
// нетронутым, поэтому субагент физически не может переписать/удалить
// функционал, реализованный другими задачами.
//
// Параллельно поддерживается опциональное ДОБАВЛЕНИЕ импортов (поле
// imports): новые пакеты, нужные новому телу функции, добавляются, если их
// ещё нет. Удалять или переставлять существующие импорты механизм не умеет.

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
)

// GoFuncPatchParams — JSON-параметры инструмента PatchGoFunction.
// Агент возвращает только поле тела функции; target_file/function_name
// адресуют узел в существующем файле.
type GoFuncPatchParams struct {
	// TargetFile — относительный путь к .go-файлу внутри OutputDir.
	TargetFile string `json:"target_file"`
	// FunctionName — имя функции/метода, узел которой заменяется.
	FunctionName string `json:"function_name"`
	// Receiver — имя типа ресивера метода (например "Service"). Обязателен,
	// если в файле несколько функций с одинаковым FunctionName (методы разных
	// типов). Пустой — подходит любая функция с этим именем.
	Receiver string `json:"receiver"`
	// Body — ПОЛНЫЙ исходник заменяющей функции, включая "func":
	// "func (s *Service) CreateUser(ctx context.Context, u *User) error {...}".
	Body string `json:"body"`
	// Imports — опциональные импорт-пути, которые нужно ДОБАВИТЬ в файл,
	// если их там ещё нет ("errors", "alias \"path\", "\"golang.org/x/...\"").
	Imports []string `json:"imports"`
}

// PatchGoFuncSource применяет семантический патч к исходному коду Go-файла:
// разбирает src в AST, находит функцию с именем FunctionName (с учётом
// Receiver), заменяет только её узел на новый из Body, добавляет недостающие
// импорты и форматирует файл через go/format. Возвращает итоговый исходник.
//
// Путь filePath используется только в сообщениях об ошибках.
func PatchGoFuncSource(filePath string, src []byte, p GoFuncPatchParams) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("файл %s не является валидным Go-кодом: %v", filePath, err)
	}

	newDecl, err := parseFuncDecl(fset, filePath, p.Body, p.FunctionName)
	if err != nil {
		return nil, err
	}

	idx, old, err := findFuncDecl(file, p)
	if err != nil {
		return nil, err
	}

	if err := validateRecvReplace(old, newDecl); err != nil {
		return nil, err
	}

	// Сохраняем документацию старой функции, если в новом теле её нет, —
	// чтобы замена не съедала полезный doc-комментарий.
	if old.Doc != nil && newDecl.Doc == nil {
		newDecl.Doc = old.Doc
	}

	file.Decls[idx] = newDecl

	if len(p.Imports) > 0 {
		if err := addGoImports(file, p.Imports); err != nil {
			return nil, err
		}
	}

	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return nil, fmt.Errorf("не удалось отформатировать результат: %v", err)
	}
	out := buf.Bytes()

	// Контрольная пересборка и второй проход форматирования: ре-парсинг
	// результата с чистым FileSet нормализует позиции синтетического узла
	// функции из body (они были привязаны к другому файлу), убирает артефакты
	// пустых строк/отступов и гарантирует, что вывод — валидный Go.
	fset2 := token.NewFileSet()
	file2, err := parser.ParseFile(fset2, filePath, out, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("результат патча не является валидным Go-кодом: %v", err)
	}
	var buf2 bytes.Buffer
	if err := format.Node(&buf2, fset2, file2); err != nil {
		return nil, fmt.Errorf("не удалось отформатировать результат: %v", err)
	}
	return buf2.Bytes(), nil
}

// findFuncDecl ищет индекс заменяемой функции в Decls файла. При нескольких
// кандидатах с одним именем (методы разных типов) требует Receiver.
func findFuncDecl(file *ast.File, p GoFuncPatchParams) (int, *ast.FuncDecl, error) {
	var matches []int
	for i, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != p.FunctionName {
			continue
		}
		if p.Receiver != "" && funcRecvTypeName(fd) != p.Receiver {
			continue
		}
		matches = append(matches, i)
	}

	switch len(matches) {
	case 0:
		// Подсказываем модели, что именно не найдено и что есть в файле.
		if names := funcNamesOf(file, p.Receiver, p.FunctionName); len(names) > 0 {
			return 0, nil, fmt.Errorf(
				"функция %q не найдена в %s; %s. Уточни function_name и receiver", p.FunctionName, p.TargetFile, names)
		}
		return 0, nil, fmt.Errorf("функция %q не найдена в %s", p.FunctionName, p.TargetFile)
	case 1:
		return matches[0], file.Decls[matches[0]].(*ast.FuncDecl), nil
	default:
		return 0, nil, fmt.Errorf(
			"в %s несколько функций с именем %q (методы разных типов). Укажи receiver, например %q, чтобы выбрать нужную",
			p.TargetFile, p.FunctionName, funcRecvTypeName(file.Decls[matches[0]].(*ast.FuncDecl)))
	}
}

// parseFuncDecl разбирает поле Body в *ast.FuncDecl и проверяет, что агент
// вернул именно функцию с запрошенным именем (защита от галлюцинаций).
func parseFuncDecl(fset *token.FileSet, filePath, body, wantName string) (*ast.FuncDecl, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, errors.New("body пуст: агент должен вернуть полный исходник заменяющей функции (начиная с \"func\")")
	}
	// parser.ParseFile требует package-клаузу — оборачиваем тело функции в
	// синтетический файл. Модель возвращает ТОЛЬКО func-decl, поэтому
	// package-клауза в body не ожидается.
	bodySrc := "package patch\n\n" + body
	pf, err := parser.ParseFile(fset, filePath+" [patch]", []byte(bodySrc), parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("body не является валидным Go-кодом: %v", err)
	}
	var decl *ast.FuncDecl
	for _, d := range pf.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			decl = fd
			break
		}
	}
	if decl == nil {
		return nil, errors.New("body не содержит функции (func): агент должен вернуть полный func-decl")
	}
	if decl.Name == nil || decl.Name.Name != wantName {
		return nil, fmt.Errorf("в body функция называется %q, а в function_name запрошено %q — патч отклонён",
			funcDeclName(decl), wantName)
	}
	return decl, nil
}

// validateRecvReplace проверяет, что тип ресивера не меняется патчем:
// нельзя тихо превратить метод в функцию или перенести метод на другой тип —
// это сломало бы соседний код. Имя ресивера (s, t) менять разрешено.
func validateRecvReplace(old, nw *ast.FuncDecl) error {
	oldT, newT := funcRecvTypeName(old), funcRecvTypeName(nw)
	switch {
	case oldT == "" && newT == "":
		return nil
	case oldT != "" && newT != "" && oldT == newT:
		return nil
	case oldT != "" && newT == "":
		return fmt.Errorf("меняется метод %q в функцию без ресивера — так нельзя, ресивер обязателен", old.Name.Name)
	case oldT == "" && newT != "":
		return fmt.Errorf("функция %q не была методом, а в body объявлен ресивер %q — так нельзя", old.Name.Name, newT)
	default:
		return fmt.Errorf("ресивер меняется с %q на %q — так нельзя, метод должен остаться у того же типа", oldT, newT)
	}
}

// funcDeclName возвращает имя функции (метода) из декларации.
func funcDeclName(fd *ast.FuncDecl) string {
	if fd.Name == nil {
		return ""
	}
	return fd.Name.Name
}

// funcRecvTypeName возвращает имя типа ресивера метода без указателей и
// дженерик-параметров: (*Service, Service, Service[T]) -> "Service".
// Для обычной функции возвращает "".
func funcRecvTypeName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	t := fd.Recv.List[0].Type
	for {
		switch tt := t.(type) {
		case *ast.StarExpr:
			t = tt.X
		case *ast.IndexExpr:
			t = tt.X
		case *ast.IndexListExpr:
			t = tt.X
		default:
			if id, ok := t.(*ast.Ident); ok {
				return id.Name
			}
			return ""
		}
	}
}

// funcNamesOf описывает функции файла с заданным именем (или все функции,
// если имя пусто), помогая модели исправить запрос при промахе.
func funcNamesOf(file *ast.File, recvName, funcName string) string {
	var parts []string
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if funcName != "" && fd.Name != nil && fd.Name.Name == funcName {
			parts = append(parts, fmt.Sprintf("%q (ресивер %q)", fd.Name.Name, funcRecvTypeName(fd)))
			continue
		}
		if recvName != "" && funcRecvTypeName(fd) == recvName {
			parts = append(parts, fmt.Sprintf("%q (ресивер %q)", funcDeclName(fd), recvName))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "в файле есть: " + strings.Join(parts, ", ")
}

// addGoImports добавляет в файл импорты, которых ещё нет. Добавляются только
// новые пакеты; существующие импорты не трогаются и не удаляются.
func addGoImports(file *ast.File, imports []string) error {
	existing := map[string]bool{}
	var importDecl *ast.GenDecl
	for _, d := range file.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.IMPORT {
			continue
		}
		importDecl = gd
		for _, s := range gd.Specs {
			if is, ok := s.(*ast.ImportSpec); ok {
				if p, err := strconv.Unquote(is.Path.Value); err == nil {
					existing[p] = true
				}
			}
		}
		break
	}

	type add struct{ alias, path string }
	var toAdd []add
	for _, imp := range imports {
		alias, path, err := parseImportEntry(imp)
		if err != nil {
			return err
		}
		if existing[path] {
			continue
		}
		toAdd = append(toAdd, add{alias: alias, path: path})
	}
	if len(toAdd) == 0 {
		return nil
	}

	specs := make([]ast.Spec, 0, len(toAdd))
	for _, a := range toAdd {
		sp := &ast.ImportSpec{Path: &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(a.path)}}
		if a.alias != "" {
			sp.Name = &ast.Ident{Name: a.alias}
		}
		specs = append(specs, sp)
	}

	if importDecl != nil && importDecl.Lparen.IsValid() {
		// Дополняем обычный «блочный» import.
		importDecl.Specs = append(importDecl.Specs, specs...)
		return nil
	}
	if importDecl != nil {
		// "import fmt" без скобок — переводим в блочную форму и дополняем.
		existingSingle := importDecl.Specs
		importDecl.Specs = append(append([]ast.Spec{}, existingSingle...), specs...)
		importDecl.Lparen = token.Pos(1)
		importDecl.Rparen = token.Pos(1)
		return nil
	}
	// Файл без import-декларации: вставляем новый блочный import первой
	// декларацией (после package-клаузы его размещает go/format автоматически).
	newImport := &ast.GenDecl{
		Tok:    token.IMPORT,
		Lparen: token.Pos(1),
		Rparen: token.Pos(1),
		Specs:  specs,
	}
	file.Decls = append([]ast.Decl{newImport}, file.Decls...)
	return nil
}

// parseImportEntry разбирает элемент поля imports: путь в кавычках/без,
// либо "alias путь" (например `json "encoding/json"` или `. "github.com/x"`).
func parseImportEntry(imp string) (alias, path string, err error) {
	s := strings.TrimSpace(imp)
	if s == "" {
		return "", "", errors.New("пустой элемент imports")
	}
	if strings.HasPrefix(s, "\"") || strings.HasPrefix(s, "`") {
		q, e := strconv.Unquote(s)
		if e != nil {
			return "", "", fmt.Errorf("неверный импорт %q: %v", imp, e)
		}
		return "", q, nil
	}
	parts := strings.Fields(s)
	if len(parts) == 1 {
		// "errors"-стиль без кавычек: целый токен — путь.
		return "", parts[0], nil
	}
	pathPart := strings.Join(parts[1:], " ")
	if !strings.HasPrefix(pathPart, "\"") {
		pathPart = strconv.Quote(pathPart)
	}
	q, e := strconv.Unquote(pathPart)
	if e != nil {
		return "", "", fmt.Errorf("неверный импорт %q: %v", imp, e)
	}
	return parts[0], q, nil
}
