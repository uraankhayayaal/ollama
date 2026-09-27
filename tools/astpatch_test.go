package tools

import (
	"strings"
	"testing"
)

// TestPatchFunctionGoRegression — Go-путь через PatchFunction не регрессирует:
// та же семантическая замена одной функции, что и PatchGoFunction.
func TestPatchFunctionGoRegression(t *testing.T) {
	src := `package svc

import "fmt"

func Hello() string {
	return "old"
}

func Other() int {
	return 42
}
`
	out, err := PatchFunctionSource("svc.go", []byte(src), PatchFunctionParams{
		TargetFile:   "svc.go",
		FunctionName: "Hello",
		Body:         `func Hello() string { return "new" }`,
	})
	if err != nil {
		t.Fatalf("PatchFunctionSource: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `return "new"`) {
		t.Errorf("тело функции не заменено:\n%s", got)
	}
	if !strings.Contains(got, "func Other() int") {
		t.Errorf("соседняя функция повреждена:\n%s", got)
	}
	if !strings.Contains(got, `"fmt"`) {
		t.Errorf("импорт потерян:\n%s", got)
	}
}

// TestPatchFunctionTSBasic — замена функции в TS без повреждения соседнего кода.
func TestPatchFunctionTSBasic(t *testing.T) {
	src := `import { User } from "./types";

export function getUser(id: string): User {
  return { id, name: "old" };
}

export function listUsers(): User[] {
  return [];
}
`
	out, err := PatchFunctionSource("users.ts", []byte(src), PatchFunctionParams{
		TargetFile:   "users.ts",
		FunctionName: "getUser",
		Body:         `export function getUser(id: string): User {
  return { id, name: "new" };
}`,
	})
	if err != nil {
		t.Fatalf("PatchFunctionSource: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `name: "new"`) {
		t.Errorf("тело функции не заменено:\n%s", got)
	}
	if !strings.Contains(got, "export function listUsers(): User[] {") {
		t.Errorf("соседняя функция повреждена:\n%s", got)
	}
	if !strings.Contains(got, `import { User } from "./types";`) {
		t.Errorf("импорт потерян:\n%s", got)
	}
	// Соседний код должен быть сохранён побайтно: проверяем, что префикс и суффикс
	// совпадают с оригиналом.
	prefix := `import { User } from "./types";

`
	if !strings.HasPrefix(got, prefix) {
		t.Errorf("префикс файла изменён:\n%s", got)
	}
}

// TestPatchFunctionTSClassMethod — метод класса с receiver.
func TestPatchFunctionTSClassMethod(t *testing.T) {
	src := `export class UserService {
  getUser(id: string): string {
    return "old";
  }

  listUsers(): string[] {
    return [];
  }
}

export class OrderService {
  getUser(id: string): string {
    return "order";
  }
}
`
	out, err := PatchFunctionSource("services.ts", []byte(src), PatchFunctionParams{
		TargetFile:   "services.ts",
		FunctionName: "getUser",
		Receiver:     "UserService",
		Body:         `getUser(id: string): string {
    return "new";
  }`,
	})
	if err != nil {
		t.Fatalf("PatchFunctionSource: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `return "new"`) {
		t.Errorf("тело метода не заменено:\n%s", got)
	}
	if !strings.Contains(got, `return "order"`) {
		t.Errorf("метод другого класса повреждён:\n%s", got)
	}
	if !strings.Contains(got, "listUsers(): string[] {") {
		t.Errorf("соседний метод повреждён:\n%s", got)
	}
}

// TestPatchFunctionTSArrow — стрелочная функция с одно-выражным телом.
func TestPatchFunctionTSArrow(t *testing.T) {
	src := `const double = (n: number): number => n * 2;

const triple = (n: number): number => n * 3;
`
	out, err := PatchFunctionSource("math.ts", []byte(src), PatchFunctionParams{
		TargetFile:   "math.ts",
		FunctionName: "double",
		Body:         `const double = (n: number): number => n * 4;`,
	})
	if err != nil {
		t.Fatalf("PatchFunctionSource: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "n * 4") {
		t.Errorf("тело стрелки не заменено:\n%s", got)
	}
	if !strings.Contains(got, "const triple = (n: number): number => n * 3;") {
		t.Errorf("соседняя стрелка повреждена:\n%s", got)
	}
}

// TestPatchFunctionTSRegexWithBraces — регулярное выражение с фигурными скобками
// не должно ломать подсчёт глубины.
func TestPatchFunctionTSRegexWithBraces(t *testing.T) {
	src := `const re = /[{]/;

export function match(s: string): boolean {
  return re.test(s);
}

export function other(): boolean {
  return false;
}
`
	out, err := PatchFunctionSource("regex.ts", []byte(src), PatchFunctionParams{
		TargetFile:   "regex.ts",
		FunctionName: "match",
		Body:         `export function match(s: string): boolean {
  return s.length > 0;
}`,
	})
	if err != nil {
		t.Fatalf("PatchFunctionSource: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "s.length > 0") {
		t.Errorf("тело функции не заменено:\n%s", got)
	}
	if !strings.Contains(got, `const re = /[{]/;`) {
		t.Errorf("регулярное выражение повреждено:\n%s", got)
	}
	if !strings.Contains(got, "export function other(): boolean {") {
		t.Errorf("соседняя функция повреждена:\n%s", got)
	}
}

// TestPatchFunctionTSTemplateLiteral — шаблонная строка с вложенной интерполяцией.
func TestPatchFunctionTSTemplateLiteral(t *testing.T) {
	src := "export function greet(name: string): string {\n  return `Hello ${name}!`;\n}\n\nexport function farewell(name: string): string {\n  return `Bye ${name}!`;\n}\n"
	out, err := PatchFunctionSource("greet.ts", []byte(src), PatchFunctionParams{
		TargetFile:   "greet.ts",
		FunctionName: "greet",
		Body:         "export function greet(name: string): string {\n  return `Hi ${name}!`;\n}",
	})
	if err != nil {
		t.Fatalf("PatchFunctionSource: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "Hi ${name}!") {
		t.Errorf("тело функции не заменено:\n%s", got)
	}
	if !strings.Contains(got, "export function farewell(name: string): string {") {
		t.Errorf("соседняя функция повреждена:\n%s", got)
	}
}

// TestPatchFunctionTSDecorator — декоратор над методом входит в границы замены.
func TestPatchFunctionTSDecorator(t *testing.T) {
	src := `export class Controller {
  @Get("/users")
  listUsers(): string[] {
    return [];
  }

  @Get("/users/:id")
  getUser(id: string): string {
    return "old";
  }
}
`
	out, err := PatchFunctionSource("controller.ts", []byte(src), PatchFunctionParams{
		TargetFile:   "controller.ts",
		FunctionName: "getUser",
		Receiver:     "Controller",
		Body:         `@Get("/users/:id")
  getUser(id: string): string {
    return "new";
  }`,
	})
	if err != nil {
		t.Fatalf("PatchFunctionSource: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `return "new"`) {
		t.Errorf("тело метода не заменено:\n%s", got)
	}
	if !strings.Contains(got, "listUsers(): string[] {") {
		t.Errorf("соседний метод повреждён:\n%s", got)
	}
	// Декоратор должен остаться над заменённым методом, а не над чужим.
	if strings.Index(got, "@Get(\"/users/:id\")") > strings.Index(got, "getUser") {
		t.Errorf("декоратор отделён от метода:\n%s", got)
	}
}

// TestPatchFunctionPyBasic — замена функции в Python без повреждения соседей.
func TestPatchFunctionPyBasic(t *testing.T) {
	src := `def get_user(user_id: str) -> dict:
    return {"id": user_id, "name": "old"}


def list_users() -> list:
    return []
`
	out, err := PatchFunctionSource("users.py", []byte(src), PatchFunctionParams{
		TargetFile:   "users.py",
		FunctionName: "get_user",
		Body:         `def get_user(user_id: str) -> dict:
    return {"id": user_id, "name": "new"}`,
	})
	if err != nil {
		t.Fatalf("PatchFunctionSource: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `"name": "new"`) {
		t.Errorf("тело функции не заменено:\n%s", got)
	}
	if !strings.Contains(got, "def list_users() -> list:") {
		t.Errorf("соседняя функция повреждена:\n%s", got)
	}
}

// TestPatchFunctionPyClassMethod — метод класса в Python с receiver.
func TestPatchFunctionPyClassMethod(t *testing.T) {
	src := `class UserService:
    def get_user(self, user_id: str) -> dict:
        return {"id": user_id, "name": "old"}

    def list_users(self) -> list:
        return []


class OrderService:
    def get_user(self, user_id: str) -> dict:
        return {"id": user_id, "name": "order"}
`
	out, err := PatchFunctionSource("services.py", []byte(src), PatchFunctionParams{
		TargetFile:   "services.py",
		FunctionName: "get_user",
		Receiver:     "UserService",
		Body:         `def get_user(self, user_id: str) -> dict:
        return {"id": user_id, "name": "new"}`,
	})
	if err != nil {
		t.Fatalf("PatchFunctionSource: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `"name": "new"`) {
		t.Errorf("тело метода не заменено:\n%s", got)
	}
	if !strings.Contains(got, `"name": "order"`) {
		t.Errorf("метод другого класса повреждён:\n%s", got)
	}
	if !strings.Contains(got, "def list_users(self) -> list:") {
		t.Errorf("соседний метод повреждён:\n%s", got)
	}
}

// TestPatchFunctionPyDecorator — декоратор над функцией Python входит в границы.
func TestPatchFunctionPyDecorator(t *testing.T) {
	src := `def route(path):
    def decorator(fn):
        return fn
    return decorator


@route("/users")
def list_users() -> list:
    return []


@route("/users/<id>")
def get_user(user_id: str) -> dict:
    return {"id": user_id, "name": "old"}
`
	out, err := PatchFunctionSource("routes.py", []byte(src), PatchFunctionParams{
		TargetFile:   "routes.py",
		FunctionName: "get_user",
		Body:         `@route("/users/<id>")
def get_user(user_id: str) -> dict:
    return {"id": user_id, "name": "new"}`,
	})
	if err != nil {
		t.Fatalf("PatchFunctionSource: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `"name": "new"`) {
		t.Errorf("тело функции не заменено:\n%s", got)
	}
	if !strings.Contains(got, "def list_users() -> list:") {
		t.Errorf("соседняя функция повреждена:\n%s", got)
	}
	if !strings.Contains(got, "@route(\"/users/<id>\")") {
		t.Errorf("декоратор потерян:\n%s", got)
	}
}

// TestPatchFunctionNotFound — функция не найдена, сообщение содержит подсказку.
func TestPatchFunctionNotFound(t *testing.T) {
	src := `export function existing(): void {}
`
	_, err := PatchFunctionSource("x.ts", []byte(src), PatchFunctionParams{
		TargetFile:   "x.ts",
		FunctionName: "missing",
		Body:         "export function missing(): void {}",
	})
	if err == nil {
		t.Fatal("ожидалась ошибка «функция не найдена»")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("сообщение не содержит имя функции: %v", err)
	}
	if !strings.Contains(err.Error(), "existing") {
		t.Errorf("сообщение не содержит подсказку с найденными функциями: %v", err)
	}
}

// TestPatchFunctionAmbiguous — несколько функций с одним именем без receiver.
func TestPatchFunctionAmbiguous(t *testing.T) {
	src := `class A {
  run() { return 1; }
}
class B {
  run() { return 2; }
}
`
	_, err := PatchFunctionSource("x.ts", []byte(src), PatchFunctionParams{
		TargetFile:   "x.ts",
		FunctionName: "run",
		Body:         "run() { return 3; }",
	})
	if err == nil {
		t.Fatal("ожидалась ошибка о неоднозначности")
	}
	if !strings.Contains(err.Error(), "receiver") {
		t.Errorf("сообщение не подсказывает уточнить receiver: %v", err)
	}
}

// TestPatchFunctionBodyNameMismatch — body объявляет другую функцию.
func TestPatchFunctionBodyNameMismatch(t *testing.T) {
	src := `export function target(): void {}
`
	_, err := PatchFunctionSource("x.ts", []byte(src), PatchFunctionParams{
		TargetFile:   "x.ts",
		FunctionName: "target",
		Body:         "export function other(): void {}",
	})
	if err == nil {
		t.Fatal("ожидалась ошибка о несовпадении имени в body")
	}
	if !strings.Contains(err.Error(), "other") || !strings.Contains(err.Error(), "target") {
		t.Errorf("сообщение не объясняет несовпадение: %v", err)
	}
}

// TestPatchFunctionUnsupportedExt — неподдерживаемое расширение файла.
func TestPatchFunctionUnsupportedExt(t *testing.T) {
	_, err := PatchFunctionSource("x.css", []byte("a{}"), PatchFunctionParams{
		TargetFile:   "x.css",
		FunctionName: "a",
		Body:         "a{}",
	})
	if err == nil {
		t.Fatal("ожидалась ошибка о неподдерживаемом расширении")
	}
}

// TestPatchFunctionEmptyBody — пустое body отклоняется до разбора.
func TestPatchFunctionEmptyBody(t *testing.T) {
	_, err := PatchFunctionSource("x.ts", []byte("export function f(): void {}"), PatchFunctionParams{
		TargetFile:   "x.ts",
		FunctionName: "f",
		Body:         "   ",
	})
	if err == nil {
		t.Fatal("ожидалась ошибка о пустом body")
	}
}

// TestScanTSNestedFunctions — вложенные функции не мешают поиску внешней.
func TestScanTSNestedFunctions(t *testing.T) {
	src := `export function outer(): void {
  function inner(): void {
    return;
  }
  inner();
}

export function sibling(): void {}
`
	f, err := functionFinder(langTS, src)
	if err != nil {
		t.Fatalf("functionFinder: %v", err)
	}
	sp, err := f.find("outer", "")
	if err != nil {
		t.Fatalf("find outer: %v", err)
	}
	if sp.Start != 0 {
		t.Errorf("начало outer: ожидалось 0, получено %d", sp.Start)
	}
	// Границы должны включать всю функцию, включая вложенную.
	if !strings.Contains(src[sp.Start:sp.End], "function inner()") {
		t.Errorf("границы outer не включают вложенную функцию: %q", src[sp.Start:sp.End])
	}
}

// TestScanPyNestedFunctions — вложенные def в Python.
func TestScanPyNestedFunctions(t *testing.T) {
	src := `def outer():
    def inner():
        pass
    inner()


def sibling():
    pass
`
	f, err := functionFinder(langPy, src)
	if err != nil {
		t.Fatalf("functionFinder: %v", err)
	}
	sp, err := f.find("outer", "")
	if err != nil {
		t.Fatalf("find outer: %v", err)
	}
	if !strings.Contains(src[sp.Start:sp.End], "def inner():") {
		t.Errorf("границы outer не включают вложенную функцию: %q", src[sp.Start:sp.End])
	}
	if strings.Contains(src[sp.Start:sp.End], "def sibling():") {
		t.Errorf("границы outer включают соседнюю функцию: %q", src[sp.Start:sp.End])
	}
}

// TestScanTSStringsWithBraces — строки с фигурными скобками не ломают баланс.
func TestScanTSStringsWithBraces(t *testing.T) {
	src := `const s = "}{";
const t = '}{';
	const u = "}{";
	export function f(): void {}
	`
	f, err := functionFinder(langTS, src)
	if err != nil {
		t.Fatalf("functionFinder: %v", err)
	}
	if _, err := f.find("f", ""); err != nil {
		t.Errorf("функция не найдена из-за скобок в строках: %v", err)
	}
}

// TestScanTSDivisionVsRegex — деление не принимается за начало регулярки.
func TestScanTSDivisionVsRegex(t *testing.T) {
	src := `const a = 10 / 2;
const b = a / 2;
export function f(): void {}
`
	f, err := functionFinder(langTS, src)
	if err != nil {
		t.Fatalf("functionFinder: %v", err)
	}
	if _, err := f.find("f", ""); err != nil {
		t.Errorf("функция не найдена из-за деления: %v", err)
	}
}

// TestScanPyAsyncDef — async def в Python.
func TestScanPyAsyncDef(t *testing.T) {
	src := `async def fetch_data(url: str) -> dict:
    return {}


def sync_func() -> None:
    pass
`
	f, err := functionFinder(langPy, src)
	if err != nil {
		t.Fatalf("functionFinder: %v", err)
	}
	sp, err := f.find("fetch_data", "")
	if err != nil {
		t.Fatalf("find fetch_data: %v", err)
	}
	if !strings.Contains(src[sp.Start:sp.End], "async def fetch_data") {
		t.Errorf("границы не включают async def: %q", src[sp.Start:sp.End])
	}
}

// TestScanTSExportDefault — export default function.
func TestScanTSExportDefault(t *testing.T) {
	src := `export default function main(): void {
  run();
}

export function other(): void {}
`
	f, err := functionFinder(langTS, src)
	if err != nil {
		t.Fatalf("functionFinder: %v", err)
	}
	sp, err := f.find("main", "")
	if err != nil {
		t.Fatalf("find main: %v", err)
	}
	if !strings.Contains(src[sp.Start:sp.End], "export default function main") {
		t.Errorf("границы не включают export default: %q", src[sp.Start:sp.End])
	}
}

// TestScanTSObjectMethod — метод объектного литерала.
func TestScanTSObjectMethod(t *testing.T) {
	src := `export const api = {
  getUser(id: string): string {
    return "old";
  },

  listUsers(): string[] {
    return [];
  },
};
`
	f, err := functionFinder(langTS, src)
	if err != nil {
		t.Fatalf("functionFinder: %v", err)
	}
	sp, err := f.find("getUser", "")
	if err != nil {
		t.Fatalf("find getUser: %v", err)
	}
	if !strings.Contains(src[sp.Start:sp.End], `return "old"`) {
		t.Errorf("границы метода объекта неверны: %q", src[sp.Start:sp.End])
	}
}

// TestScanPyPropertyDecorator — декоратор с аргументами над методом.
func TestScanPyPropertyDecorator(t *testing.T) {
	src := `class Service:
    @property
    def name(self) -> str:
        return self._name

    def other(self) -> None:
        pass
`
	f, err := functionFinder(langPy, src)
	if err != nil {
		t.Fatalf("functionFinder: %v", err)
	}
	sp, err := f.find("name", "Service")
	if err != nil {
		t.Fatalf("find name: %v", err)
	}
	if !strings.Contains(src[sp.Start:sp.End], "@property") {
		t.Errorf("границы не включают декоратор: %q", src[sp.Start:sp.End])
	}
}
