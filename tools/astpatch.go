package tools

// astpatch.go — единая точечная правка функции для нескольких языков (Ф-5).
//
// Смысл инструмента тот же, что у PatchGoFuncSource: субагент возвращает в JSON
// ТОЛЬКО одну функцию, а backend находит её узел в исходнике и заменяет ровно
// его. Всё остальное — соседние функции, импорты, порядок объявлений,
// форматирование — остаётся нетронутым, поэтому субагент физически не может
// переписать функционал, реализованный другими задачами.
//
// Разница с Go в том, что для Go мы разбираем AST и печатаем файл через
// go/format (там есть канонический формат), а для TS/Python границы берём из
// лексера (langsyntax.go) и делаем splice байтов. Отсюда важное свойство:
// для TS/Python соседние байты файла гарантированно не меняются, а для Go
// меняется только эквивалентная gofmt-форма текста.
//
// Диспетчер языка определяется по расширению target_file: язык из CODEGEN_LANG не
// участвует. Раньше подсказка «для Go — PatchGoFunction» в промпте разработчика
// фактически закрывала путь к правке TS/Python, хотя правки там требовались
// постоянно.

import (
	"fmt"
	"strings"
)

// PatchFunctionParams — JSON-параметры инструмента PatchFunction. Совместимы
// с GoFuncPatchParams: те же поля, те же имена, тот же смысл.
type PatchFunctionParams struct {
	// TargetFile — относительный путь к файлу внутри OutputDir. Расширение
	// (.go/.ts/.tsx/.js/.jsx/.py) определяет язык обработки.
	TargetFile string `json:"target_file"`
	// FunctionName — имя заменяемой функции/метода.
	FunctionName string `json:"function_name"`
	// Receiver — владелец метода: имя типа для Go, имя класса для TS/JS/Python.
	// Обязателен, если в файле несколько функций с одинаковым именем.
	Receiver string `json:"receiver"`
	// Body — ПОЛНЫЙ исходник заменяющей функции, включая сигнатуру и тело:
	// Go — "func (s *Service) CreateUser(...) error {...}",
	// TS — "export async function createUser(id: string): Promise<User> {...}",
	// Python — "def create_user(self, id: str) -> User: ...".
	// Имя/сигнатура в body обязаны совпадать с function_name.
	Body string `json:"body"`
	// Imports — опциональные импорт-пути для Go (добавление недостающих,
	// существующие не трогаются). Для TS/Python игнорируются: там добавление
	// импорта безопаснее делать через SearchReplace/WriteFiles.
	Imports []string `json:"imports"`
}

// PatchFunctionSource — единая точечная правка функции. Возвращает итоговый
// исходник целиком.
//
// filePath используется только для выбора языка и в сообщениях об ошибках.
func PatchFunctionSource(filePath string, src []byte, p PatchFunctionParams) ([]byte, error) {
	if p.FunctionName == "" {
		return nil, fmt.Errorf("function_name обязателен")
	}
	if strings.TrimSpace(p.Body) == "" {
		return nil, fmt.Errorf("body обязателен: передай ПОЛНЫЙ исходник заменяющей функции")
	}
	lang := langOfFile(filePath)
	switch lang {
	case langGo:
		// Без регрессии: Go-путь остаётся на go/ast + go/format.
		return PatchGoFuncSource(filePath, src, GoFuncPatchParams{
			TargetFile:   p.TargetFile,
			FunctionName: p.FunctionName,
			Receiver:     p.Receiver,
			Body:         p.Body,
			Imports:      p.Imports,
		})
	case langTS, langPy:
		return patchFuncBySpan(filePath, lang, string(src), p)
	}
	return nil, fmt.Errorf("точечная правка функций не поддерживает %q: поддерживаются .go, .ts, .tsx, .js, .jsx, .py", filePath)
}

// patchFuncBySpan — замена функции по найденным границам с проверками
// безопасности: имя в body должно совпадать с адресуемой функцией, а после
// splice файл обязан остаться синтаксически разбираемым.
func patchFuncBySpan(filePath, lang, src string, p PatchFunctionParams) ([]byte, error) {
	f, err := functionFinder(lang, src)
	if err != nil {
		return nil, fmt.Errorf("файл %s: %w", filePath, err)
	}
	sp, err := f.find(p.FunctionName, p.Receiver)
	if err != nil {
		return nil, fmt.Errorf("файл %s: %w", filePath, err)
	}
	if err := checkBodyMatches(src, lang, sp, p.Body); err != nil {
		return nil, fmt.Errorf("файл %s, %s: %w", filePath, describeSpan(sp), err)
	}

	body := p.Body
	if lang == langPy {
		body = pyIndentedBody(src, sp.Start, body)
	}

	// Хвостовой перевод строки тела не входит в границы (одно-выражное тело
	// стрелки), поэтому сохраняем его после splice.
	out := make([]byte, 0, len(src)+len(body))
	out = append(out, src[:sp.Start]...)
	out = append(out, body...)
	out = append(out, src[sp.End:]...)

	if err := verifyReplaced(lang, string(out), p.FunctionName, p.Receiver); err != nil {
		return nil, fmt.Errorf("файл %s, %s: результат патча не проходит проверку: %w", filePath, describeSpan(sp), err)
	}
	return out, nil
}

// pyIndentedBody — границы Python-функции включают отступ строки (ln.start), а
// body от модели приходит без него. Без добавления отступа splice заменит
// «    def method(self)» на «def method(self)» — метод класса молча станет
// функцией верхнего уровня. Отступ добавляется только к первой строке:
// остальные строки body модель возвращает уже с отступами тела.
func pyIndentedBody(src string, start int, body string) string {
	indent := ""
	for i := start; i < len(src); i++ {
		if src[i] == ' ' || src[i] == '\t' {
			indent += string(src[i])
		} else {
			break
		}
	}
	if indent == "" || strings.HasPrefix(body, " ") || strings.HasPrefix(body, "\t") {
		return body
	}
	return indent + body
}

// checkBodyMatches — body должен объявлять именно ту функцию, что адресована.
// Иначе модель «переименует» функцию вместо правки, и вызывающий код молча
// отвалится: самая частая и самая дорогая ошибка точечной правки.
func checkBodyMatches(src, lang string, sp funcSpan, body string) error {
	bf, err := functionFinder(lang, body)
	if err != nil {
		return fmt.Errorf("body не разбирается: %w", err)
	}
	names := bf.names()
	for _, n := range names {
		if n == sp.Name {
			return nil
		}
	}
	if len(names) == 0 {
		return fmt.Errorf("в body не найдено ни одной функции — передай полный исходник функции, а не только её тело")
	}
	return fmt.Errorf("в body объявлена функция %q, а правится %q", names[0], sp.Name)
}

// verifyReplaced — контрольный re-parse результата: после правки функция должна
// находиться ровно один раз, а синтаксис — разбираться. Ловит обрезанное тело
// и потерянную скобку до записи на диск.
func verifyReplaced(lang, out, name, owner string) error {
	f, err := functionFinder(lang, out)
	if err != nil {
		return err
	}
	if _, err := f.find(name, owner); err != nil {
		return fmt.Errorf("после замены %s не находится: %w", quoteFunc(name, owner), err)
	}
	return nil
}

func describeSpan(sp funcSpan) string {
	desc := fmt.Sprintf("строка %d", sp.Line)
	if sp.Owner != "" {
		desc = sp.Owner + "." + sp.Name + " (" + desc + ")"
	}
	return desc
}
